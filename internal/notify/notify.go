package notify

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	aggWindow       = 60 * time.Second // 聚合窗口:自最后一条失败事件起静默满 60s 发汇总
	sendTimeout     = 5 * time.Second
	sendRetries     = 2 // 发送失败重试 1 次(共 2 次)
	maxErrLen       = 200
	maxAggItems     = 20   // 汇总消息最多列出条数(企业微信 markdown 有长度限制)
	maxContentBytes = 4000 // 消息体字节上限(企业微信 markdown 限 4096 字节,留余量给截断标记)
)

type event struct {
	project  string
	task     string
	attempts int
	err      string
}

// Notifier 企业微信 webhook 通知器。
// 聚合规则:周期内首条失败立即发送;60s 内后续失败静默累计;
// 自最后一条事件起静默满 60s 发送汇总,之后周期重置(下一条再立即发送)。
// webhook 为空时为 no-op,完全不启动后台 goroutine。
type Notifier struct {
	ch  chan event
	wg  sync.WaitGroup
	log *slog.Logger
}

// New 创建通知器。webhook 为空返回 no-op。
func New(webhook string, log *slog.Logger) *Notifier {
	n := &Notifier{log: log}
	if webhook == "" {
		return n
	}
	n.ch = make(chan event, 64)
	n.wg.Add(1)
	go n.loop(webhook)
	return n
}

// Failure 上报一条失败事件(非阻塞,队列满时丢弃,绝不影响备份流程)。
func (n *Notifier) Failure(project, task string, attempts int, errStr string) {
	if n.ch == nil {
		return // webhook 未配置
	}
	ev := event{project: project, task: task, attempts: attempts, err: truncate(errStr)}
	select {
	case n.ch <- ev:
	default:
	}
}

// Close 停止后台 goroutine 并同步 flush 未发送的累计。
// 注意:调用方须保证 Close 与 Failure 不并发(先停止产生失败事件的调度,再 Close);
// 本类型不做内部加锁。
func (n *Notifier) Close() {
	if n.ch == nil {
		return
	}
	close(n.ch)
	n.wg.Wait()
	n.ch = nil // 重复 Close 安全
}

func (n *Notifier) loop(webhook string) {
	defer n.wg.Done()
	var buffer []event
	immediateSent := false // 本周期是否已发过立即通知
	var timerC <-chan time.Time
	var t *time.Timer
	for {
		select {
		case ev, ok := <-n.ch:
			if !ok { // Close:flush 后退出
				if len(buffer) > 0 {
					n.post(webhook, aggregateMsg(buffer))
				}
				return
			}
			if !immediateSent {
				n.post(webhook, singleMsg(ev))
				immediateSent = true
			} else {
				buffer = append(buffer, ev)
			}
			if t != nil {
				t.Stop()
			}
			t = time.NewTimer(aggWindow)
			timerC = t.C
		case <-timerC:
			if len(buffer) > 0 {
				n.post(webhook, aggregateMsg(buffer))
				buffer = nil
			}
			immediateSent = false // 周期重置:下一条失败再立即发送
			timerC = nil
		}
	}
}

// post 发送 markdown 消息,失败重试 1 次;仍失败仅记日志。
func (n *Notifier) post(webhook, content string) {
	content = limitContent(content)
	payload, _ := json.Marshal(map[string]any{
		"msgtype":  "markdown",
		"markdown": map[string]string{"content": content},
	})
	client := &http.Client{Timeout: sendTimeout}
	var lastErr error
	for attempt := 0; attempt < sendRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Second)
		}
		req, err := http.NewRequest(http.MethodPost, webhook, bytes.NewReader(payload))
		if err != nil {
			n.log.Warn("通知请求构造失败", "err", redact(err))
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			lastErr = redact(err)
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		resp.Body.Close()
		var out struct {
			ErrCode int    `json:"errcode"`
			ErrMsg  string `json:"errmsg"`
		}
		json.Unmarshal(body, &out)
		if resp.StatusCode == http.StatusOK && out.ErrCode == 0 {
			return
		}
		lastErr = fmt.Errorf("http=%d errcode=%d %s", resp.StatusCode, out.ErrCode, out.ErrMsg)
	}
	n.log.Warn("webhook 通知发送失败", "err", lastErr)
}

// redact 脱敏:*url.Error 文本含完整 webhook URL(含 key),只保留底层错误。
func redact(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

func hostName() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}

func singleMsg(ev event) string {
	return fmt.Sprintf("**bpxup 备份失败**\n> 主机:%s\n> 时间:%s\n> 项目:%s / 任务:%s\n> 尝试:%d 次\n> 错误:%s",
		hostName(), time.Now().Format("2006-01-02 15:04:05"), ev.project, ev.task, ev.attempts, ev.err)
}

func aggregateMsg(evs []event) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**bpxup 备份失败汇总(共 %d 条)**\n> 主机:%s\n> 时间:%s\n",
		len(evs), hostName(), time.Now().Format("2006-01-02 15:04:05"))
	for i, ev := range evs {
		if i >= maxAggItems {
			fmt.Fprintf(&b, "> ……其余 %d 条略\n", len(evs)-maxAggItems)
			break
		}
		fmt.Fprintf(&b, "> %d. %s / %s(%d 次):%s\n", i+1, ev.project, ev.task, ev.attempts, ev.err)
	}
	return b.String()
}

// limitContent 按 UTF-8 边界截断消息体,防止超过企业微信 markdown 字节上限。
func limitContent(content string) string {
	b := []byte(content)
	if len(b) <= maxContentBytes {
		return content
	}
	i := maxContentBytes
	for i > 0 && !utf8.RuneStart(b[i]) { // 回退到 rune 起始字节,避免截断半个字符
		i--
	}
	return string(b[:i]) + "\n> ……(消息过长已截断)"
}

// truncate 截断错误摘要,防止超长错误撑爆消息。
func truncate(s string) string {
	s = strings.ReplaceAll(s, "\n", " ") // 换行会跳出引用块,打乱排版
	r := []rune(s)
	if len(r) > maxErrLen {
		return string(r[:maxErrLen]) + "…"
	}
	return s
}

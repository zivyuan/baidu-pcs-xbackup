package baidupan

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const chunkSize = 4 << 20 // 4MB

// 分片失败重试:30s 起指数退避(30/60/120/240s,封顶 300s),共 6 次重试。
// 500/504 是百度上传网关拥塞,秒级退避大概率再撞,必须等拥塞窗口过去。
const (
	chunkRetries      = 6
	chunkBackoffBase  = 30 * time.Second
	chunkBackoffLimit = 5 * time.Minute
)

// UploadFile 官方三步上传:precreate → superfile2 分片 → create。
// 秒传命中直接返回;分片失败按 30s 起指数退避重试 6 次。
// 断点续传:uploadid+分片清单持久化到 uploads.json(与 token 同目录),
// 任务级重试复用同一 uploadid 只补缺失分片,不再全量重来。
func (c *Client) UploadFile(remotePath, localPath string) error {
	if err := c.EnsureToken(); err != nil {
		return err
	}
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	size := st.Size()

	// 1. 计算分片 md5
	var md5s []string
	buf := make([]byte, chunkSize)
	for {
		n, rerr := io.ReadFull(f, buf)
		if n > 0 {
			h := md5.Sum(buf[:n])
			md5s = append(md5s, hex.EncodeToString(h[:]))
		}
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	if size == 0 {
		md5s = []string{} // 空文件 block_list 为空数组
	}
	blockJSON, _ := json.Marshal(md5s)

	// 2. 断点续传:存有同 size/同分片 md5 的会话,查询服务端已传分片
	uploadID := ""
	var need []int
	if id, ok := c.loadUploadState(remotePath, size, md5s); ok {
		if parts, lerr := c.listUploadedParts(id, remotePath); lerr == nil {
			uploadID = id
			need = missingSeqs(len(md5s), parts)
			c.saveUploadState(remotePath, id, size, md5s) // 刷新保存时间
		}
		// 查询失败(会话过期等)→ 走全新 precreate
	}

	// 3. precreate(续传命中时跳过)
	if uploadID == "" {
		preForm := url.Values{
			"path":       {remotePath},
			"isdir":      {"0"},
			"size":       {fmt.Sprint(size)},
			"autoinit":   {"1"},
			"rtype":      {"3"}, // 同名覆盖
			"block_list": {string(blockJSON)},
		}
		var pre struct {
			Errno      int64  `json:"errno"`
			ErrorMsg   string `json:"error_msg"`
			UploadID   string `json:"uploadid"`
			ReturnType int    `json:"return_type"`
			BlockList  []int  `json:"block_list"`
		}
		if err := c.callPOST(&pre, "/rest/2.0/xpan/file",
			url.Values{"method": {"precreate"}}, preForm); err != nil {
			return fmt.Errorf("预上传 %s: %w", remotePath, err)
		}
		// 注意:实测部分应用 precreate 恒返回 return_type=1 但文件并未创建,
		// 不可据此判定秒传成功。仅当"无 uploadid 且无需上传分片"才视为真秒传。
		need = pre.BlockList // 需要实际上传的分片序号
		if len(need) == 0 && size > 0 && pre.UploadID == "" {
			return nil // 真秒传命中
		}
		if len(need) == 0 && size > 0 {
			for i := range md5s {
				need = append(need, i)
			}
		}
		uploadID = pre.UploadID
		c.saveUploadState(remotePath, uploadID, size, md5s)
	}

	// 4. superfile2 逐分片上传(带重试)
	for _, seq := range need {
		if err := c.uploadChunk(uploadID, remotePath, seq, localPath); err != nil {
			return fmt.Errorf("分片 %d 上传 %s: %w", seq, remotePath, err)
		}
	}

	// 5. create(失败保留断点,任务重试时只补缺)
	createForm := url.Values{
		"path":       {remotePath},
		"isdir":      {"0"},
		"size":       {fmt.Sprint(size)},
		"uploadid":   {uploadID},
		"rtype":      {"3"},
		"block_list": {string(blockJSON)},
	}
	var cr struct {
		Errno      int64  `json:"errno"`
		ErrorMsg   string `json:"error_msg"`
		ReturnType int    `json:"return_type"`
	}
	if err := c.callPOST(&cr, "/rest/2.0/xpan/file",
		url.Values{"method": {"create"}}, createForm); err != nil {
		return fmt.Errorf("创建文件 %s: %w", remotePath, err)
	}
	c.clearUploadState(remotePath)
	return nil
}

// uploadChunk 上传单个分片到 d.pcs.baidu.com,失败按 30s 起指数退避重试 6 次。
func (c *Client) uploadChunk(uploadID, remotePath string, seq int, localPath string) error {
	offset := int64(seq) * chunkSize
	chunk := make([]byte, chunkSize)
	var lastErr error
	for attempt := 0; attempt <= chunkRetries; attempt++ {
		if attempt > 0 {
			backoff := chunkBackoffBase << (attempt - 1)
			if backoff > chunkBackoffLimit {
				backoff = chunkBackoffLimit
			}
			time.Sleep(backoff)
		}
		f, err := os.Open(localPath)
		if err != nil {
			return err
		}
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			f.Close()
			return err
		}
		n, rerr := io.ReadFull(f, chunk)
		f.Close()
		if rerr != nil && rerr != io.EOF && rerr != io.ErrUnexpectedEOF {
			return rerr
		}

		params := url.Values{
			"access_token": {c.token.AccessToken},
			"method":       {"upload"},
			"type":         {"tmpfile"},
			"path":         {remotePath},
			"uploadid":     {uploadID},
			"partseq":      {fmt.Sprint(seq)},
		}
		req, err := http.NewRequest("PUT",
			"https://d.pcs.baidu.com/rest/2.0/pcs/superfile2?"+params.Encode(),
			bytes.NewReader(chunk[:n]))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/octet-stream")
		resp, err := c.HTTP.Do(req)
		if err != nil {
			lastErr = c.redact(err)
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		var out struct {
			Errno int64  `json:"errno"`
			Error string `json:"error_msg"`
		}
		json.Unmarshal(body, &out)
		if resp.StatusCode == 200 && out.Errno == 0 {
			return nil
		}
		lastErr = fmt.Errorf("superfile2 http=%d errno=%d body=%s", resp.StatusCode, out.Errno, bodySnippet(body))
	}
	return lastErr
}

// bodySnippet 响应体摘要:压缩空白为单空格、截断 100 字符。
// 服务端异常时常返回 HTML 错误页而非 JSON(errno 解不出来),
// 摘要随错误链透传到日志与通知,便于排查真实原因。
func bodySnippet(body []byte) string {
	s := strings.Join(strings.Fields(string(body)), " ")
	if s == "" {
		return "<空>" // 网关直接断开等场景,避免错误消息以 body= 结尾
	}
	r := []rune(s)
	if len(r) > 100 {
		s = string(r[:100]) + "..."
	}
	return s
}

// ---------- 分片断点续传 ----------

// uploadState 单个远端路径的上传断点(uploadid 服务端会话一般数天内有效)。
type uploadState struct {
	UploadID string   `json:"uploadid"`
	Size     int64    `json:"size"`
	Blocks   []string `json:"blocks"` // 分片 md5,用于确认是同一文件
	SavedAt  int64    `json:"saved_at"`
}

const uploadStateTTL = 7 * 24 * time.Hour

func (c *Client) statePath() string {
	return filepath.Join(filepath.Dir(c.TokenFile), "uploads.json")
}

// initUploadStates 惰性加载一次断点文件;损坏则当作空(最多少续传一次)。
func (c *Client) initUploadStates() {
	if c.statesLoaded {
		return
	}
	c.statesLoaded = true
	c.states = map[string]uploadState{}
	if b, err := os.ReadFile(c.statePath()); err == nil {
		json.Unmarshal(b, &c.states)
	}
}

// persistUploadStates 落盘,顺带清掉超过 TTL 的陈旧断点。
func (c *Client) persistUploadStates() {
	cutoff := time.Now().Add(-uploadStateTTL).Unix()
	for k, st := range c.states {
		if st.SavedAt < cutoff {
			delete(c.states, k)
		}
	}
	b, _ := json.MarshalIndent(c.states, "", "  ")
	os.WriteFile(c.statePath(), b, 0o600)
}

// loadUploadState 命中条件:存在断点、大小与分片 md5 完全一致、未过期。
func (c *Client) loadUploadState(remotePath string, size int64, blocks []string) (string, bool) {
	if c.TokenFile == "" {
		return "", false
	}
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.initUploadStates()
	st, ok := c.states[remotePath]
	if !ok || st.UploadID == "" || st.Size != size || !sameStrings(st.Blocks, blocks) {
		return "", false
	}
	if time.Since(time.Unix(st.SavedAt, 0)) > uploadStateTTL {
		return "", false
	}
	return st.UploadID, true
}

func (c *Client) saveUploadState(remotePath, uploadID string, size int64, blocks []string) {
	if c.TokenFile == "" || uploadID == "" {
		return
	}
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.initUploadStates()
	c.states[remotePath] = uploadState{UploadID: uploadID, Size: size, Blocks: blocks, SavedAt: time.Now().Unix()}
	c.persistUploadStates()
}

func (c *Client) clearUploadState(remotePath string) {
	if c.TokenFile == "" {
		return
	}
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.initUploadStates()
	if _, ok := c.states[remotePath]; !ok {
		return
	}
	delete(c.states, remotePath)
	c.persistUploadStates()
}

// listUploadedParts 查询 uploadid 已成功上传的分片序号(superfile2 method=list)。
func (c *Client) listUploadedParts(uploadID, remotePath string) ([]int, error) {
	if err := c.EnsureToken(); err != nil {
		return nil, err
	}
	params := url.Values{
		"access_token": {c.token.AccessToken},
		"method":       {"list"},
		"type":         {"tmpfile"},
		"path":         {remotePath},
		"uploadid":     {uploadID},
	}
	req, err := http.NewRequest("GET",
		"https://pan.baidu.com/rest/2.0/pcs/superfile2?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, c.redact(err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("superfile2 list http=%d body=%s", resp.StatusCode, bodySnippet(body))
	}
	return parsePartList(body)
}

// parsePartList 解析 method=list 响应,兼容两种已知形态:
// {"status":0,"list":[0,1,2]} 与 {"status":0,"list":[{"index":0}...]}。
func parsePartList(body []byte) ([]int, error) {
	var out struct {
		Status int64           `json:"status"`
		List   json.RawMessage `json:"list"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("list 响应解析失败: %s", bodySnippet(body))
	}
	if out.Status != 0 {
		return nil, fmt.Errorf("list status=%d", out.Status)
	}
	var ints []int
	if err := json.Unmarshal(out.List, &ints); err == nil {
		return ints, nil
	}
	var objs []struct {
		Index int `json:"index"`
	}
	if err := json.Unmarshal(out.List, &objs); err == nil {
		parts := make([]int, len(objs))
		for i, o := range objs {
			parts[i] = o.Index
		}
		return parts, nil
	}
	return nil, fmt.Errorf("list 格式无法解析: %s", bodySnippet(body))
}

// missingSeqs 从 0..total-1 中去掉 uploaded,返回仍需上传的序号(升序)。
func missingSeqs(total int, uploaded []int) []int {
	set := map[int]bool{}
	for _, p := range uploaded {
		if p >= 0 && p < total {
			set[p] = true
		}
	}
	var out []int
	for i := 0; i < total; i++ {
		if !set[i] {
			out = append(out, i)
		}
	}
	return out
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

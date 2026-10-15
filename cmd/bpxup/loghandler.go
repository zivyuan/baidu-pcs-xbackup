package main

import (
	"context"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
)

// conciseHandler 精简文本日志格式:
//
//	2026-01-02 15:04:05 开始备份 任务=xx 尝试=1/5
//	2026-01-02 15:04:05 [错误] 备份失败 任务=xx err="..."
//
// INFO 不显示级别标记,WARN/ERROR 加 [警告]/[错误];属性为 key=value,
// 值含空白/引号/等号时加引号(行为接近 slog TextHandler)。
type conciseHandler struct {
	mu    *sync.Mutex
	w     io.Writer
	attrs []slog.Attr // WithAttrs 累积的前置属性(项目暂未使用)
}

func newConciseHandler(w io.Writer) *conciseHandler {
	return &conciseHandler{mu: &sync.Mutex{}, w: w}
}

func (h *conciseHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= slog.LevelInfo
}

func (h *conciseHandler) Handle(_ context.Context, r slog.Record) error {
	var b []byte
	if r.Time.IsZero() {
		r.Time = time.Now() // 直接构造 Record 未盖章时兜底,避免打出 0001-01-01
	}
	b = r.Time.AppendFormat(b, "2006-01-02 15:04:05")
	switch {
	case r.Level >= slog.LevelError:
		b = append(b, " [错误]"...)
	case r.Level >= slog.LevelWarn:
		b = append(b, " [警告]"...)
	}
	b = append(b, ' ')
	b = append(b, r.Message...)
	for _, a := range h.attrs {
		b = appendAttrValue(b, a)
	}
	r.Attrs(func(a slog.Attr) bool {
		b = appendAttrValue(b, a)
		return true
	})
	b = append(b, '\n')
	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := h.w.Write(b)
	return err
}

// WithAttrs 返回带前置属性的新 handler,共用同一把锁保证写互斥。
func (h *conciseHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &conciseHandler{mu: h.mu, w: h.w, attrs: append(append([]slog.Attr{}, h.attrs...), attrs...)}
}

// WithGroup 项目未使用分组,原样返回即可。
func (h *conciseHandler) WithGroup(string) slog.Handler { return h }

func appendAttrValue(b []byte, a slog.Attr) []byte {
	if a.Key == "" {
		return b // 空 key 属性跳过,与 TextHandler 行为一致
	}
	b = append(b, ' ')
	b = append(b, a.Key...)
	b = append(b, '=')
	v := a.Value.String()
	if needsQuoting(v) {
		return strconv.AppendQuote(b, v)
	}
	return append(b, v...)
}

func needsQuoting(v string) bool {
	if v == "" {
		return true
	}
	return strings.ContainsAny(v, " \t\"\n\r=")
}

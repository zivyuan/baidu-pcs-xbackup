package baidupan

// 分片断点续传模块测试:method=list 响应解析、缺失分片计算、
// 断点状态文件 存/取/清/过期/不匹配。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------- parsePartList ----------

func TestParsePartList_IntArray(t *testing.T) {
	parts, err := parsePartList([]byte(`{"status":0,"list":[0,1,3]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 3 || parts[0] != 0 || parts[1] != 1 || parts[2] != 3 {
		t.Fatalf("parts = %v", parts)
	}
}

func TestParsePartList_ObjectArray(t *testing.T) {
	parts, err := parsePartList([]byte(`{"status":0,"list":[{"index":0},{"index":2}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 || parts[0] != 0 || parts[1] != 2 {
		t.Fatalf("parts = %v", parts)
	}
}

func TestParsePartList_Empty(t *testing.T) {
	parts, err := parsePartList([]byte(`{"status":0,"list":[]}`))
	if err != nil || len(parts) != 0 {
		t.Fatalf("parts = %v err = %v, want 空", parts, err)
	}
}

func TestParsePartList_BadStatus(t *testing.T) {
	if _, err := parsePartList([]byte(`{"status":-6,"list":[0]}`)); err == nil {
		t.Fatal("status!=0 应报错")
	}
}

func TestParsePartList_Garbage(t *testing.T) {
	if _, err := parsePartList([]byte(`<html>bad gateway</html>`)); err == nil {
		t.Fatal("非 JSON 应报错")
	}
}

// ---------- missingSeqs ----------

func TestMissingSeqs(t *testing.T) {
	got := missingSeqs(5, []int{0, 2, 7, -1}) // 越界序号忽略
	want := []int{1, 3, 4}
	if len(got) != len(want) {
		t.Fatalf("missing = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("missing = %v, want %v", got, want)
		}
	}
	if got := missingSeqs(3, []int{0, 1, 2}); len(got) != 0 {
		t.Fatalf("全传完仍报缺失: %v", got)
	}
}

// ---------- 断点状态文件 ----------

func newStateClient(t *testing.T) *Client {
	t.Helper()
	c := &Client{TokenFile: filepath.Join(t.TempDir(), "token.json")}
	return c
}

func TestUploadState_Roundtrip(t *testing.T) {
	c := newStateClient(t)
	blocks := []string{"aa", "bb"}

	c.saveUploadState("/apps/r/f.zip", "uid-1", 8<<20, blocks)
	// 命中:同 size 同分片
	id, ok := c.loadUploadState("/apps/r/f.zip", 8<<20, blocks)
	if !ok || id != "uid-1" {
		t.Fatalf("命中失败: ok=%v id=%q", ok, id)
	}
	// 换新 Client(模拟进程重启/任务级重试),从磁盘加载
	c2 := &Client{TokenFile: c.TokenFile}
	if id, ok = c2.loadUploadState("/apps/r/f.zip", 8<<20, blocks); !ok || id != "uid-1" {
		t.Fatalf("重载后命中失败: ok=%v id=%q", ok, id)
	}

	c.clearUploadState("/apps/r/f.zip")
	if _, ok = c.loadUploadState("/apps/r/f.zip", 8<<20, blocks); ok {
		t.Fatal("清除后不应命中")
	}
}

func TestUploadState_Mismatch(t *testing.T) {
	c := newStateClient(t)
	blocks := []string{"aa", "bb"}
	c.saveUploadState("/apps/r/f.zip", "uid-1", 8<<20, blocks)

	// 大小不同(文件变了)
	if _, ok := c.loadUploadState("/apps/r/f.zip", 9<<20, blocks); ok {
		t.Fatal("size 不一致不应命中")
	}
	// 分片 md5 不同(内容变了)
	if _, ok := c.loadUploadState("/apps/r/f.zip", 8<<20, []string{"aa", "cc"}); ok {
		t.Fatal("分片不一致不应命中")
	}
	// 路径不同
	if _, ok := c.loadUploadState("/apps/r/g.zip", 8<<20, blocks); ok {
		t.Fatal("路径不一致不应命中")
	}
}

func TestUploadState_TTL(t *testing.T) {
	c := newStateClient(t)
	blocks := []string{"aa"}
	c.saveUploadState("/apps/r/f.zip", "uid-1", 4<<20, blocks)

	// 直接改内部状态模拟过期(避免真等 7 天)
	c.stateMu.Lock()
	st := c.states["/apps/r/f.zip"]
	st.SavedAt = time.Now().Add(-8 * 24 * time.Hour).Unix()
	c.states["/apps/r/f.zip"] = st
	c.stateMu.Unlock()

	if _, ok := c.loadUploadState("/apps/r/f.zip", 4<<20, blocks); ok {
		t.Fatal("过期断点不应命中")
	}
}

func TestUploadState_PruneOnPersist(t *testing.T) {
	c := newStateClient(t)
	c.saveUploadState("/apps/r/new.zip", "uid-1", 4<<20, []string{"aa"})

	c.stateMu.Lock()
	st := c.states["/apps/r/new.zip"]
	st.SavedAt = time.Now().Add(-8 * 24 * time.Hour).Unix()
	c.states["/apps/r/new.zip"] = st
	c.states["/apps/r/old.zip"] = uploadState{UploadID: "uid-0", Size: 1, Blocks: []string{"x"}, SavedAt: 1}
	c.stateMu.Unlock()

	c.saveUploadState("/apps/r/keep.zip", "uid-2", 2<<20, []string{"bb"})

	b, err := os.ReadFile(c.statePath())
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if strings.Contains(s, "old.zip") || strings.Contains(s, "new.zip") {
		t.Fatalf("陈旧断点未清理: %s", s)
	}
	if !strings.Contains(s, "keep.zip") {
		t.Fatalf("有效断点丢失: %s", s)
	}
}

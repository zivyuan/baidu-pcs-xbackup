package backup

// 对账引擎模块测试:fakePan 模拟网盘(List/MkdirAll/UploadFile),
// 覆盖续传、假成功复核、verify 报告/修复等核心路径。

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/zivyuan/baidu-pcs-xbackup/internal/baidupan"
	"github.com/zivyuan/baidu-pcs-xbackup/internal/config"
)

// fakePan 内存假网盘:dirs 记录目录,files 记录 文件路径→大小。
// failUpload/onUpload 用于注入失败/模拟假成功(上传不落盘)。
type fakePan struct {
	dirs       map[string]bool
	files      map[string]int64
	failUpload map[string]error // 远端路径 → 上传即失败
	silent     map[string]bool  // 远端路径 → 假成功(不落盘)
	uploaded   []string         // 实际调用过 UploadFile 的远端路径
}

func newFakePan() *fakePan {
	return &fakePan{
		dirs:       map[string]bool{"/apps/root": true},
		files:      map[string]int64{},
		failUpload: map[string]error{},
		silent:     map[string]bool{},
	}
}

func (f *fakePan) List(dir string) ([]baidupan.Entry, error) {
	if !f.dirs[dir] {
		return nil, baidupan.NewErrno(-9, "文件或目录不存在")
	}
	seen := map[string]bool{}
	var out []baidupan.Entry
	for p := range f.files {
		if parent := p[:strings.LastIndex(p, "/")]; parent == dir {
			out = append(out, baidupan.Entry{Path: p, Name: p[strings.LastIndex(p, "/")+1:], Size: f.files[p]})
		} else if strings.HasPrefix(p, dir+"/") {
			rest := p[len(dir)+1:]
			sub := rest[:strings.Index(rest, "/")]
			if !seen[sub] {
				seen[sub] = true
				out = append(out, baidupan.Entry{Path: dir + "/" + sub, Name: sub, IsDir: true})
			}
		}
	}
	for p := range f.dirs {
		if p == dir || !strings.HasPrefix(p, dir+"/") {
			continue
		}
		rest := p[len(dir)+1:]
		if !strings.Contains(rest, "/") && !seen[rest] {
			seen[rest] = true
			out = append(out, baidupan.Entry{Path: p, Name: rest, IsDir: true})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (f *fakePan) MkdirAll(dir string) error {
	f.dirs[dir] = true
	return nil
}

func (f *fakePan) UploadFile(remotePath, localPath string) error {
	if err, ok := f.failUpload[remotePath]; ok {
		return err
	}
	f.uploaded = append(f.uploaded, remotePath)
	if f.silent[remotePath] {
		return nil // 假成功:create 返回 0 但文件未创建
	}
	st, err := os.Stat(localPath)
	if err != nil {
		return err
	}
	for d := remotePath[:strings.LastIndex(remotePath, "/")]; d != ""; d = d[:strings.LastIndex(d, "/")] {
		f.dirs[d] = true
	}
	f.files[remotePath] = st.Size()
	return nil
}

func (f *fakePan) Download(fsID int64) ([]byte, error) { return nil, errors.New("未实现") }
func (f *fakePan) Delete(paths []string) error         { return nil }

// mkLocal 在临时目录里造源文件树,返回源根路径。
func mkLocal(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func newTestEngine(pan PanClient) *Engine {
	return New(pan, "/apps/root", slog.New(slog.NewTextHandler(os.Stderr, nil)))
}

func task(name, source, mode string) config.Task {
	return config.Task{Name: name, Source: source, Mode: mode}
}

func proj(id string) config.Project { return config.Project{ID: id} }

func rels(fs []localFile) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = f.Rel
	}
	return out
}

// ---------- walkLocal ----------

func TestWalkLocal(t *testing.T) {
	root := mkLocal(t, map[string]string{
		"a.txt":          "hello",
		"sub/b.txt":      "world!",
		"sub/deep/c.txt": "x",
	})
	// 软链应被跳过
	if err := os.Symlink(filepath.Join(root, "a.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	got, _, err := walkLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{"a.txt": 5, "sub/b.txt": 6, "sub/deep/c.txt": 1}
	if len(got) != len(want) {
		t.Fatalf("文件数 = %d, want %d(got %v)", len(got), len(want), got)
	}
	for rel, size := range want {
		if got[rel].Size != size || got[rel].Path == "" {
			t.Errorf("%s: got size=%d path=%q, want size=%d 且有本地路径", rel, got[rel].Size, got[rel].Path, size)
		}
	}
}

// ---------- diff ----------

func TestDiff(t *testing.T) {
	expect := map[string]localFile{
		"ok.txt":   {Rel: "ok.txt", Size: 10},
		"miss.txt": {Rel: "miss.txt", Size: 5},
		"bad.txt":  {Rel: "bad.txt", Size: 7},
	}
	actual := map[string]int64{"ok.txt": 10, "bad.txt": 8, "extra.txt": 99}
	got := rels(diff(expect, actual))
	want := []string{"bad.txt", "miss.txt"} // 缺失 + 大小不符,路径排序;远端多余的忽略
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("diff = %v, want %v", got, want)
	}
}

// ---------- remoteSizes ----------

func TestRemoteSizes(t *testing.T) {
	pan := newFakePan()
	pan.dirs["/apps/root/p/20260101/t"] = true
	pan.dirs["/apps/root/p/20260101/t/sub"] = true
	pan.files["/apps/root/p/20260101/t/a.txt"] = 3
	pan.files["/apps/root/p/20260101/t/sub/b.txt"] = 4
	pan.files["/apps/root/p/20260101/t/_SUCCESS"] = 3

	got, err := newTestEngine(pan).remoteSizes("/apps/root/p/20260101/t")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{"a.txt": 3, "sub/b.txt": 4, "_SUCCESS": 3}
	if len(got) != len(want) {
		t.Fatalf("远端清单 = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %d, want %d", k, got[k], v)
		}
	}

	// 目录不存在 → 空清单不报错
	got, err = newTestEngine(pan).remoteSizes("/apps/root/p/20260101/none")
	if err != nil || len(got) != 0 {
		t.Fatalf("不存在目录: got %v, err %v, want 空清单无错", got, err)
	}
}

// ---------- syncRaw(续传 + 假成功复核) ----------

func TestSyncRaw_Resume(t *testing.T) {
	root := mkLocal(t, map[string]string{
		"a.txt":     "aaaa",
		"sub/b.txt": "bbbbbb",
		"c.txt":     "cc",
	})
	pan := newFakePan()
	pan.dirs["/apps/root/p/20260101/t"] = true
	pan.dirs["/apps/root/p/20260101/t/sub"] = true
	pan.files["/apps/root/p/20260101/t/a.txt"] = 4   // 已传好 → 跳过
	pan.files["/apps/root/p/20260101/t/sub/b.txt"] = 3 // 大小不符 → 重传
	// c.txt 缺失 → 补传

	uploaded, total, err := newTestEngine(pan).syncRaw("/apps/root/p/20260101/t", root)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || uploaded != 2 {
		t.Fatalf("uploaded=%d total=%d, want 2/3", uploaded, total)
	}
	if pan.files["/apps/root/p/20260101/t/sub/b.txt"] != 6 || pan.files["/apps/root/p/20260101/t/c.txt"] != 2 {
		t.Fatalf("补传结果不符: %v", pan.files)
	}
	if len(pan.uploaded) != 2 {
		t.Fatalf("UploadFile 调用 %v, want 只传 2 个", pan.uploaded)
	}
}

func TestSyncRaw_FakeSuccess(t *testing.T) {
	root := mkLocal(t, map[string]string{"a.txt": "aaaa"})
	pan := newFakePan()
	pan.silent["/apps/root/p/20260101/t/a.txt"] = true // 假成功:不落盘

	_, _, err := newTestEngine(pan).syncRaw("/apps/root/p/20260101/t", root)
	if err == nil || !strings.Contains(err.Error(), "复核") {
		t.Fatalf("假成功未被复核拦截: err = %v", err)
	}
}

func TestSyncRaw_UploadFail(t *testing.T) {
	root := mkLocal(t, map[string]string{"a.txt": "aaaa"})
	pan := newFakePan()
	pan.failUpload["/apps/root/p/20260101/t/a.txt"] = errors.New("网络错误")

	if _, _, err := newTestEngine(pan).syncRaw("/apps/root/p/20260101/t", root); err == nil {
		t.Fatal("上传失败应返回错误")
	}
}

// ---------- VerifyTask(raw) ----------

func TestVerifyTask_Raw(t *testing.T) {
	root := mkLocal(t, map[string]string{"a.txt": "aaaa", "sub/b.txt": "bb"})
	eng := newTestEngine(newFakePan())
	p, tk := proj("p"), task("t", root, "raw")

	// 无远端数据:报告模式应列出全部缺失
	got, err := eng.VerifyTask(p, tk, "20260101", false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "a.txt,sub/b.txt" {
		t.Fatalf("差异清单 = %v, want [a.txt sub/b.txt]", got)
	}

	// fix 模式:重传 + 复核,返回空
	if got, err = eng.VerifyTask(p, tk, "20260101", true); err != nil || len(got) != 0 {
		t.Fatalf("fix 后 got=%v err=%v, want 空清单无错", got, err)
	}

	// 再校验:一致
	if got, err = eng.VerifyTask(p, tk, "20260101", false); err != nil || len(got) != 0 {
		t.Fatalf("修复后再校验 got=%v err=%v, want 一致", got, err)
	}
}

// ---------- VerifyTask(archive) ----------

func TestVerifyTask_Archive(t *testing.T) {
	root := mkLocal(t, map[string]string{"a.txt": "aaaa"})
	eng := newTestEngine(newFakePan())
	p, tk := proj("p"), task("t", root, "archive")

	// 整体缺失(日期目录都不存在):报告模式不算错误
	got, err := eng.VerifyTask(p, tk, "20260101", false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "t.tar.gz" {
		t.Fatalf("差异清单 = %v, want [t.tar.gz]", got)
	}

	// fix:建目录 + 上传 + 复核
	if got, err = eng.VerifyTask(p, tk, "20260101", true); err != nil || len(got) != 0 {
		t.Fatalf("fix 后 got=%v err=%v, want 空清单无错", got, err)
	}
	if !eng.dirCache["/apps/root/p/20260101"] {
		t.Fatal("fix 未创建日期目录")
	}
}

// ---------- LatestDate ----------

func TestLatestDate(t *testing.T) {
	pan := newFakePan()
	pan.dirs["/apps/root/p"] = true
	pan.dirs["/apps/root/p/20260101"] = true
	pan.dirs["/apps/root/p/20260103"] = true
	pan.dirs["/apps/root/p/notdate"] = true
	pan.files["/apps/root/p/stray.txt"] = 1

	d, err := newTestEngine(pan).LatestDate(proj("p"))
	if err != nil || d != "20260103" {
		t.Fatalf("LatestDate = %q err=%v, want 20260103", d, err)
	}

	// 项目目录不存在 → 空串无错
	if d, err = newTestEngine(pan).LatestDate(proj("none")); err != nil || d != "" {
		t.Fatalf("无历史: got %q err=%v, want 空串无错", d, err)
	}
}

var _ PanClient = (*fakePan)(nil)

package backup

// 对账引擎:本地期望清单 vs 远端实际清单,按"存在 + 大小一致"判定完整性。
// 上传前对账实现续传(只补缺口),打标前复核防假成功。

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/zivyuan/baidu-pcs-xbackup/internal/baidupan"
	"github.com/zivyuan/baidu-pcs-xbackup/internal/config"
)

// localFile 本地期望文件:相对任务目录的斜杠路径 + 本地路径 + 大小。
type localFile struct {
	Rel  string
	Path string
	Size int64
}

// walkLocal 遍历本地源,收集常规文件(跳过软链等非常规文件,与上传规则一致)。
func walkLocal(root string) (map[string]localFile, error) {
	root = filepath.Clean(root)
	out := map[string]localFile{}
	err := filepath.Walk(root, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." || fi.IsDir() || !fi.Mode().IsRegular() {
			return nil
		}
		rel = filepath.ToSlash(rel)
		out[rel] = localFile{Rel: rel, Path: path, Size: fi.Size()}
		return nil
	})
	return out, err
}

// remoteSizes 递归列出远端目录,返回 文件相对路径→大小。
// 目录逐层串行 List(自动翻页),避免并发触发限流;根目录不存在返回空清单。
func (e *Engine) remoteSizes(dir string) (map[string]int64, error) {
	out := map[string]int64{}
	prefix := strings.TrimSuffix(dir, "/") + "/"
	var walk func(d string) error
	walk = func(d string) error {
		entries, err := e.Pan.List(d)
		if err != nil {
			return err
		}
		for _, en := range entries {
			if en.IsDir {
				if err := walk(en.Path); err != nil {
					return err
				}
				continue
			}
			out[strings.TrimPrefix(en.Path, prefix)] = en.Size
		}
		return nil
	}
	if err := walk(dir); err != nil {
		if baidupan.IsErrno(err, -9) {
			return out, nil // 目录不存在:全部视为缺失
		}
		return nil, err
	}
	return out, nil
}

// diff 找出期望中缺失或大小不符的文件(按路径排序,输出稳定)。
func diff(expect map[string]localFile, actual map[string]int64) []localFile {
	var out []localFile
	for _, f := range expect {
		if size, ok := actual[f.Rel]; !ok || size != f.Size {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rel < out[j].Rel })
	return out
}

// syncRaw 对账式同步 raw 任务:只上传缺失/大小不符的文件,传完复核,
// 仍不一致返回错误。返回本次实际上传数与期望文件总数。
func (e *Engine) syncRaw(taskDir, source string) (uploaded, total int, err error) {
	if err := e.ensureDir(taskDir); err != nil { // 源目录只有文件没有子目录时也要有任务目录
		return 0, 0, err
	}
	expect, err := walkLocal(source)
	if err != nil {
		return 0, 0, fmt.Errorf("遍历源 %s: %w", source, err)
	}
	actual, err := e.remoteSizes(taskDir)
	if err != nil {
		return 0, len(expect), err
	}
	for _, f := range diff(expect, actual) {
		if idx := strings.LastIndex(f.Rel, "/"); idx >= 0 {
			if err := e.ensureDir(taskDir + "/" + f.Rel[:idx]); err != nil {
				return uploaded, len(expect), err
			}
		}
		if err := e.Pan.UploadFile(taskDir+"/"+f.Rel, f.Path); err != nil {
			return uploaded, len(expect), fmt.Errorf("上传 %s: %w", f.Rel, err)
		}
		uploaded++
	}
	// 复核:防 create 假成功
	actual, err = e.remoteSizes(taskDir)
	if err != nil {
		return uploaded, len(expect), err
	}
	if rest := diff(expect, actual); len(rest) > 0 {
		return uploaded, len(expect), fmt.Errorf("复核仍不一致 %d 个文件(如 %s)", len(rest), rest[0].Rel)
	}
	return uploaded, len(expect), nil
}

// remoteSizeMatches 远端文件存在且大小与 size 一致时返回 true。
func (e *Engine) remoteSizeMatches(remotePath string, size int64) (bool, error) {
	idx := strings.LastIndex(remotePath, "/")
	entries, err := e.Pan.List(remotePath[:idx])
	if err != nil {
		return false, err
	}
	name := remotePath[idx+1:]
	for _, en := range entries {
		if !en.IsDir && en.Name == name {
			return en.Size == size, nil
		}
	}
	return false, nil
}

// VerifyTask 对账指定任务指定日期的远端备份,返回不一致清单
// (raw: 相对路径;archive: ["<任务名>.tar.gz"])。
// fix=true 时重传差异并复核(受调用方有界重试约束),修完返回空清单。
func (e *Engine) VerifyTask(p config.Project, t config.Task, date string, fix bool) ([]string, error) {
	dir := e.dateDir(p, date)
	switch t.Mode {
	case "archive":
		tmp, err := os.CreateTemp("", "bpxup-verify-*.tar.gz")
		if err != nil {
			return nil, err
		}
		tmpPath := tmp.Name()
		tmp.Close()
		defer os.Remove(tmpPath)
		if err := TarGz(t.Source, tmpPath); err != nil {
			return nil, fmt.Errorf("打包: %w", err)
		}
		st, err := os.Stat(tmpPath)
		if err != nil {
			return nil, err
		}
		remote := dir + "/" + t.Name + ".tar.gz"
		bad := []string{t.Name + ".tar.gz"}
		ok, err := e.remoteSizeMatches(remote, st.Size())
		if err != nil {
			return nil, err
		}
		if ok {
			return nil, nil
		}
		if !fix {
			return bad, nil
		}
		if err := e.Pan.UploadFile(remote, tmpPath); err != nil {
			return nil, err
		}
		// 复核:防 create 假成功
		if ok, err := e.remoteSizeMatches(remote, st.Size()); err != nil {
			return nil, err
		} else if !ok {
			return bad, nil
		}
		return nil, nil
	default: // raw
		taskDir := dir + "/" + t.Name
		expect, err := walkLocal(t.Source)
		if err != nil {
			return nil, fmt.Errorf("遍历源 %s: %w", t.Source, err)
		}
		actual, err := e.remoteSizes(taskDir)
		if err != nil {
			return nil, err
		}
		diffs := diff(expect, actual)
		if len(diffs) == 0 {
			return nil, nil
		}
		if !fix {
			rels := make([]string, len(diffs))
			for i, f := range diffs {
				rels[i] = f.Rel
			}
			return rels, nil
		}
		if _, _, err := e.syncRaw(taskDir, t.Source); err != nil {
			return nil, err // syncRaw 内部已复核,不一致会报错
		}
		return nil, nil
	}
}

// LatestDate 返回项目下最新备份日期目录(YYYYMMDD);无历史备份返回空串。
func (e *Engine) LatestDate(p config.Project) (string, error) {
	entries, err := e.Pan.List(e.Root + "/" + p.ID)
	if err != nil {
		if baidupan.IsErrno(err, -9) {
			return "", nil
		}
		return "", err
	}
	var latest string
	for _, en := range entries {
		if !en.IsDir || len(en.Name) != 8 {
			continue
		}
		if isDigits(en.Name) && en.Name > latest {
			latest = en.Name
		}
	}
	return latest, nil
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

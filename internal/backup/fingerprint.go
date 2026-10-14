package backup

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zivyuan/baidu-pcs-xbackup/internal/baidupan"
	"github.com/zivyuan/baidu-pcs-xbackup/internal/config"
)

// loadMachineID 读取本机识别码;不存在则生成随机 16 字节 hex 并落盘(0600)。
// 存放于 token 文件同目录的 fingerprint 文件,首次任意任务执行时创建。
func loadMachineID(path string) (string, error) {
	if b, err := os.ReadFile(path); err == nil {
		if s := string(b); s != "" {
			return s, nil
		}
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("生成机器识别码: %w", err)
	}
	id := hex.EncodeToString(raw)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("创建识别码目录: %w", err)
	}
	if err := os.WriteFile(path, []byte(id), 0o600); err != nil {
		return "", fmt.Errorf("写入机器识别码 %s: %w", path, err)
	}
	return id, nil
}

// ensureFingerprint 任务防冲突:项目目录下比对 <任务名>.fingerprint
// (内容为本机识别码,经 dlink 下载真实内容比对——列表接口对小文件
// 返回混淆 md5,不可用)。文件不存在 → 写入本机识别码(认领);
// 存在且不一致 → 拒绝执行。
// 放在项目目录而非日期目录:retention 只清日期目录内的任务数据,认领关系长期保留。
func (e *Engine) ensureFingerprint(p config.Project, t config.Task) error {
	id, err := loadMachineID(e.FingerprintFile)
	if err != nil {
		return err
	}
	name := t.Name + ".fingerprint"
	projDir := e.Root + "/" + p.ID
	entries, err := e.Pan.List(projDir)
	if err != nil && !baidupan.IsErrno(err, -9) { // -9: 目录不存在,视为未认领
		return fmt.Errorf("读取项目目录: %w", err)
	}
	for _, en := range entries {
		if en.IsDir || en.Name != name {
			continue
		}
		content, err := e.Pan.Download(en.FsID)
		if err != nil {
			return fmt.Errorf("读取 fingerprint: %w", err)
		}
		if strings.TrimSpace(string(content)) != id {
			return fmt.Errorf(
				"任务 %s/%s 已被其他机器认领(远端 %s 识别码不一致),拒绝执行;"+
					"如确认本机接管,请先在网盘删除该 fingerprint 文件",
				p.ID, t.Name, projDir+"/"+name)
		}
		return nil // 本机认领过,放行
	}

	// 未认领:写入本机识别码
	if err := e.ensureDir(projDir); err != nil {
		return err
	}
	tmp, err := os.CreateTemp("", "bpxup-fp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.WriteString(id); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()
	return e.Pan.UploadFile(projDir+"/"+name, tmpPath)
}

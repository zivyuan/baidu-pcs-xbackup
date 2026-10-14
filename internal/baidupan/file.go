package baidupan

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Entry 远端目录条目。
type Entry struct {
	Path  string
	Name  string
	IsDir bool
	Size  int64
}

// MkdirAll 逐级创建远端目录(errno -8 已存在视为成功)。
// "/apps" 是保留容器目录,应用无权创建也无需创建,直接跳过。
func (c *Client) MkdirAll(remoteDir string) error {
	parts := strings.Split(strings.Trim(remoteDir, "/"), "/")
	cur := ""
	for _, p := range parts {
		cur += "/" + p
		if cur == "/apps" {
			continue // 保留目录,不可创建,必然已存在
		}
		form := url.Values{
			"path":  {cur},
			"isdir": {"1"},
			"size":  {"0"},
			"rtype": {"0"},
		}
		var out struct {
			Errno int64 `json:"errno"`
		}
		err := c.callPOST(&out, "/rest/2.0/xpan/file",
			url.Values{"method": {"create"}}, form)
		if err != nil {
			if ae, ok := err.(*apiError); ok && ae.Errno == -8 {
				continue // 已存在
			}
			return fmt.Errorf("创建目录 %s: %w", cur, err)
		}
	}
	return nil
}

// List 列目录(自动翻页,每页 1000)。
func (c *Client) List(dir string) ([]Entry, error) {
	var all []Entry
	for start := 0; ; start += 1000 {
		params := url.Values{
			"method": {"list"},
			"dir":    {dir},
			"order":  {"name"},
			"start":  {strconv.Itoa(start)},
			"limit":  {"1000"},
		}
		var out struct {
			Errno int64 `json:"errno"`
			List  []struct {
				Path           string `json:"path"`
				ServerFilename string `json:"server_filename"`
				IsDir          int    `json:"isdir"`
				Size           int64  `json:"size"`
			} `json:"list"`
		}
		if err := c.callGET(&out, "/rest/2.0/xpan/file", params); err != nil {
			return nil, fmt.Errorf("列目录 %s: %w", dir, err)
		}
		for _, it := range out.List {
			all = append(all, Entry{Path: it.Path, Name: it.ServerFilename, IsDir: it.IsDir == 1, Size: it.Size})
		}
		if len(out.List) < 1000 {
			return all, nil
		}
	}
}

// Delete 删除文件/目录(目录整体递归删除)。
// 官方接口:POST /rest/2.0/xpan/file?method=filemanager&opera=delete,
// 表单参数 filelist(JSON 数组)+ async(1 自适应)。
func (c *Client) Delete(paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	j, _ := json.Marshal(paths)
	var out struct {
		Errno  int64  `json:"errno"`
		TaskID uint64 `json:"taskid"`
		Info   []struct {
			Path  string `json:"path"`
			Errno int64  `json:"errno"`
		} `json:"info"`
	}
	err := c.callPOST(&out, "/rest/2.0/xpan/file",
		url.Values{"method": {"filemanager"}, "opera": {"delete"}},
		url.Values{"filelist": {string(j)}, "async": {"1"}})
	if err != nil {
		return fmt.Errorf("删除失败 %v: %w", paths, err)
	}
	if len(out.Info) == 0 && out.TaskID != 0 {
		return nil // 转为异步任务,已受理
	}
	for _, it := range out.Info {
		if it.Errno != 0 && it.Errno != 12 && it.Errno != -9 { // 12/-9: 不存在,视为已删
			return fmt.Errorf("删除 %s 失败: errno=%d", it.Path, it.Errno)
		}
	}
	return nil
}

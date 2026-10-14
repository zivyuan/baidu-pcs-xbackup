package baidupan

// WebClient 网页版网盘接口客户端(BDUSS Cookie 认证)。
// 端点与参数参照 BaiduPCS-Go(Apache-2.0)已验证可用的组合:
// 非公开接口,百度随时可能变动;使用有账号限流/风控风险,建议小号专用。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	webAppID = "250528" // 网盘官方 AppID(BaiduPCS-Go 同款)
	// netdiskUA 网盘客户端 UA,部分接口(如下载)校验 UA,缺失会 403。
	netdiskUA = "netdisk;P2SP;3.0.0.8;netdisk;11.12.3;ANG-AN00;android-android;10.0;JSbridge4.4.0;jointBridge;1.1.0;"
)

// WebClient 实现 Provider 接口。
type WebClient struct {
	CredFile string // BDUSS 凭证文件路径
	HTTP     *http.Client
	bduss    string
}

func NewWebClient(credFile string) *WebClient {
	jar, _ := cookiejar.New(nil)
	return &WebClient{
		CredFile: credFile,
		HTTP: &http.Client{
			Timeout: 120 * time.Second,
			Jar:     jar,
			// 重定向(User-Agent 头不会被 Go 自动带到新请求)后仍需 UA,
			// 下载接口 302 到 d.pcs.baidu.com 时保持 netdisk UA。
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				req.Header.Set("User-Agent", netdiskUA)
				return nil
			},
		},
	}
}

// EnsureToken 加载 BDUSS 并装进 cookie jar。
func (w *WebClient) EnsureToken() error {
	if w.bduss != "" {
		return nil
	}
	data, err := os.ReadFile(w.CredFile)
	if err != nil {
		return fmt.Errorf("未找到 BDUSS 凭证,请先执行 bpxup login: %w", err)
	}
	var cred struct {
		Bduss string `json:"bduss"`
	}
	if err := json.Unmarshal(data, &cred); err != nil || cred.Bduss == "" {
		return fmt.Errorf("BDUSS 凭证文件 %s 无效,请重新 bpxup login", w.CredFile)
	}
	w.setBDUSS(cred.Bduss)
	return nil
}

func (w *WebClient) setBDUSS(bduss string) {
	w.bduss = bduss
	u := &url.URL{Scheme: "https", Host: "pan.baidu.com"}
	w.HTTP.Jar.SetCookies(u, []*http.Cookie{{Name: "BDUSS", Value: bduss, Domain: ".baidu.com"}})
}

// LoginWeb 校验 BDUSS(查配额)并保存到 CredFile(0600,原子写)。
func (w *WebClient) LoginWeb(bduss string) error {
	bduss = strings.TrimSpace(bduss)
	bduss = strings.TrimPrefix(strings.TrimPrefix(bduss, "BDUSS="), "bduss=")
	if bduss == "" {
		return fmt.Errorf("BDUSS 不能为空")
	}
	w.setBDUSS(bduss)
	var q struct {
		Total int64 `json:"total"`
		Used  int64 `json:"used"`
	}
	if err := w.get(&q, "https://pcs.baidu.com/rest/2.0/pcs/quota", url.Values{
		"method": {"info"}, "app_id": {webAppID},
		"checkfree": {"1"}, "checkexpire": {"1"},
	}); err != nil {
		return fmt.Errorf("BDUSS 校验失败(可能已失效): %w", err)
	}
	data, _ := json.MarshalIndent(map[string]string{"bduss": bduss}, "", "  ")
	if err := writeFileAtomic(w.CredFile, data, 0o600); err != nil {
		return fmt.Errorf("保存 BDUSS: %w", err)
	}
	fmt.Printf("登录成功,网盘已用 %.2fGB / %.2fGB\n",
		float64(q.Used)/1e9, float64(q.Total)/1e9)
	return nil
}

// redact 从错误文本中抹掉 BDUSS。
func (w *WebClient) redact(err error) error {
	if err != nil && w.bduss != "" {
		msg := err.Error()
		masked := strings.ReplaceAll(msg, w.bduss, "***")
		if masked != msg {
			return fmt.Errorf("%s", masked)
		}
	}
	return err
}

// get 发起 GET 请求并解析响应(带 BDUSS cookie 与 netdisk UA)。
func (w *WebClient) get(out any, rawURL string, params url.Values) error {
	if err := w.EnsureToken(); err != nil {
		return err
	}
	if params == nil {
		params = url.Values{}
	}
	params.Set("app_id", webAppID)
	req, err := http.NewRequest("GET", rawURL+"?"+params.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", netdiskUA)
	return w.do(out, req)
}

// postForm 发起 urlencoded POST 请求并解析响应。
func (w *WebClient) postForm(out any, rawURL string, params url.Values, form url.Values) error {
	if err := w.EnsureToken(); err != nil {
		return err
	}
	if params == nil {
		params = url.Values{}
	}
	params.Set("app_id", webAppID)
	req, err := http.NewRequest("POST", rawURL+"?"+params.Encode(),
		strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", netdiskUA)
	return w.do(out, req)
}

func (w *WebClient) do(out any, req *http.Request) error {
	resp, err := w.HTTP.Do(req)
	if err != nil {
		return w.redact(fmt.Errorf("%s 请求失败: %w", req.URL.Path, err))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return parseResp(out, body, req.URL.Path)
}

// MkdirAll 逐级创建远端目录(errno -8 已存在视为成功)。
func (w *WebClient) MkdirAll(remoteDir string) error {
	parts := strings.Split(strings.Trim(remoteDir, "/"), "/")
	cur := ""
	for _, p := range parts {
		cur += "/" + p
		var out struct {
			Errno int64 `json:"errno"`
		}
		err := w.postForm(&out, "https://pcs.baidu.com/rest/2.0/pcs/file",
			url.Values{"method": {"mkdir"}, "path": {cur}}, nil)
		if err != nil {
			if IsErrno(err, -8) {
				continue // 已存在
			}
			return fmt.Errorf("创建目录 %s: %w", cur, err)
		}
	}
	return nil
}

// List 列目录(PCS 接口,一次取全量)。
func (w *WebClient) List(dir string) ([]Entry, error) {
	var out struct {
		Errno int64 `json:"errno"`
		List  []struct {
			Path           string `json:"path"`
			ServerFilename string `json:"server_filename"`
			IsDir          int    `json:"isdir"`
			Size           int64  `json:"size"`
		} `json:"list"`
	}
	err := w.get(&out, "https://pcs.baidu.com/rest/2.0/pcs/file", url.Values{
		"method": {"list"},
		"path":   {dir},
		"by":     {"name"},
		"order":  {"asc"},
		"limit":  {"0-2147483647"},
	})
	if err != nil {
		return nil, fmt.Errorf("列目录 %s: %w", dir, err)
	}
	entries := make([]Entry, 0, len(out.List))
	for _, it := range out.List {
		name := it.ServerFilename
		if name == "" {
			name = path.Base(it.Path)
		}
		entries = append(entries, Entry{
			Path: it.Path, Name: name, IsDir: it.IsDir == 1, Size: it.Size,
		})
	}
	return entries, nil
}

// Download 按路径取回文件内容(仅小文件:fingerprint 比对用)。
// PCS download 接口 302 到下载地址,cookie jar 与 CheckRedirect 保证带上 BDUSS/UA。
func (w *WebClient) Download(path string) ([]byte, error) {
	if err := w.EnsureToken(); err != nil {
		return nil, err
	}
	params := url.Values{
		"method": {"download"},
		"path":   {path},
		"app_id": {webAppID},
	}
	req, err := http.NewRequest("GET", "https://pcs.baidu.com/rest/2.0/pcs/file?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", netdiskUA)
	resp, err := w.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("下载失败: %w", w.redact(err))
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("下载失败: HTTP %d %s", resp.StatusCode, string(body))
	}
	return body, nil
}

// Delete 批量删除文件/目录(PCS 接口,multipart form 传 param)。
func (w *WebClient) Delete(paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	if err := w.EnsureToken(); err != nil {
		return err
	}
	type item struct {
		Path string `json:"path"`
	}
	list := make([]item, 0, len(paths))
	for _, p := range paths {
		list = append(list, item{Path: p})
	}
	j, _ := json.Marshal(list)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormField("param")
	fw.Write(j)
	mw.Close()

	params := url.Values{"method": {"delete"}, "app_id": {webAppID}}
	req, err := http.NewRequest("POST",
		"https://pcs.baidu.com/rest/2.0/pcs/file?"+params.Encode(), &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("User-Agent", netdiskUA)

	var out struct {
		Errno int64 `json:"errno"`
		Info  []struct {
			Path  string `json:"path"`
			Errno int64 `json:"errno"`
		} `json:"info"`
	}
	if err := w.do(&out, req); err != nil {
		return fmt.Errorf("删除失败 %v: %w", paths, err)
	}
	for _, it := range out.Info {
		if it.Errno != 0 && it.Errno != 12 && it.Errno != -9 { // 12/-9: 不存在,视为已删
			return fmt.Errorf("删除 %s 失败: errno=%d", it.Path, it.Errno)
		}
	}
	return nil
}

// UploadFile 网页版三步上传:api/precreate → pcs superfile2 分片 → api/create。
// 与 oauth 实现同构:秒传命中直接返回;分片失败重试 3 次(1s/2s/4s)。
func (w *WebClient) UploadFile(remotePath, localPath string) error {
	size, _, blockJSON, err := md5Chunks(localPath)
	if err != nil {
		return err
	}

	// 1. precreate
	var pre struct {
		Errno      int64  `json:"errno"`
		ErrorMsg   string `json:"error_msg"`
		UploadID   string `json:"uploadid"`
		ReturnType int    `json:"return_type"`
		BlockList  []int  `json:"block_list"`
	}
	preForm := url.Values{
		"path":       {remotePath},
		"isdir":      {"0"},
		"size":       {strconv.FormatInt(size, 10)},
		"autoinit":   {"1"},
		"rtype":      {"3"}, // 同名覆盖
		"block_list": {blockJSON},
	}
	if err := w.postForm(&pre, "https://pan.baidu.com/api/precreate", nil, preForm); err != nil {
		return fmt.Errorf("预上传 %s: %w", remotePath, err)
	}
	// 同 oauth 实现:return_type 不可靠,仅"无 uploadid 且无需上传分片"才视为真秒传
	need := pre.BlockList
	if len(need) == 0 && size > 0 && pre.UploadID == "" {
		return nil // 真秒传命中
	}
	if len(need) == 0 && size > 0 {
		for i := 0; i < int((size+chunkSize-1)/chunkSize); i++ {
			need = append(need, i)
		}
	}

	// 2. superfile2 逐分片上传(带重试)
	for _, seq := range need {
		if err := w.uploadChunk(pre.UploadID, remotePath, seq, localPath); err != nil {
			return fmt.Errorf("分片 %d 上传 %s: %w", seq, remotePath, err)
		}
	}

	// 3. create 合并
	createForm := url.Values{
		"path":        {remotePath},
		"target_path": {path.Dir(remotePath) + "/"},
		"isdir":       {"0"},
		"size":        {strconv.FormatInt(size, 10)},
		"uploadid":    {pre.UploadID},
		"rtype":       {"3"},
		"block_list":  {blockJSON},
	}
	var cr struct {
		Errno    int64  `json:"errno"`
		ErrorMsg string `json:"error_msg"`
	}
	if err := w.postForm(&cr, "https://pan.baidu.com/api/create", nil, createForm); err != nil {
		return fmt.Errorf("创建文件 %s: %w", remotePath, err)
	}
	return nil
}

// uploadChunk 上传单个 4MB 分片到 pcs superfile2(multipart file 字段),
// 失败重试 3 次(1s/2s/4s)。
func (w *WebClient) uploadChunk(uploadID, remotePath string, seq int, localPath string) error {
	offset := int64(seq) * chunkSize
	chunk := make([]byte, chunkSize)
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(1<<(attempt-1)) * time.Second)
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

		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		fw, _ := mw.CreateFormFile("file", "blob")
		fw.Write(chunk[:n])
		mw.Close()

		params := url.Values{
			"app_id":   {webAppID},
			"method":   {"upload"},
			"type":     {"tmpfile"},
			"path":     {remotePath},
			"uploadid": {uploadID},
			"partseq":  {strconv.Itoa(seq)},
			"vip":      {"1"},
		}
		req, err := http.NewRequest("POST",
			"https://pcs.baidu.com/rest/2.0/pcs/superfile2?"+params.Encode(), &buf)
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.Header.Set("User-Agent", netdiskUA)

		var out struct {
			Errno int64  `json:"errno"`
			Error string `json:"error_msg"`
			MD5   string `json:"md5"`
		}
		if err := w.do(&out, req); err != nil {
			lastErr = err
			continue
		}
		if out.MD5 == "" { // 无 md5 视为未成功
			lastErr = fmt.Errorf("superfile2 errno=%d 无 md5 返回", out.Errno)
			continue
		}
		return nil
	}
	return lastErr
}

// writeFileAtomic 临时文件 + rename 原子写(oauth token 保存同款策略)。
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".cred-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}

package baidupan

// WebClient 网页版网盘接口客户端(BDUSS Cookie 认证)。
// 端点经 Playwright 抓包官方 Web 客户端 + Go 实测验证(2026-10):
//   - 列目录:pan.baidu.com/rest/2.0/xpan/file?method=list(BDUSS + netdisk UA)
//   - 建目录:pan.baidu.com/api/create(a=mkdir)
//   - 删 除:pan.baidu.com/api/filemanager?opera=delete(需 bdstoken)
//   - 上 传:api/precreate → d.pcs superfile2 → api/create(同 BaiduPCS-Go)
// 旧 pcs.baidu.com/rest/2.0/pcs/* 端点已对纯 BDUSS 返回 31030,不可再用。
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
	bdstoken string // 写操作的 CSRF token(filemanager 删除必需)
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
				if len(via) >= 10 { // 防重定向环
					return http.ErrUseLastResponse
				}
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
	if err := w.get(&q, "https://pan.baidu.com/api/quota", url.Values{
		"checkfree": {"1"}, "checkexpire": {"1"},
		"web": {"1"}, "clienttype": {"0"},
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

// bdstoken 拉取写操作的 CSRF token(缓存于客户端生命周期内)。
func (w *WebClient) getBdstoken() (string, error) {
	if w.bdstoken != "" {
		return w.bdstoken, nil
	}
	var out struct {
		Result struct {
			BDSToken string `json:"bdstoken"`
		} `json:"result"`
	}
	if err := w.getRetry(&out, "https://pan.baidu.com/api/gettemplatevariable", url.Values{
		"fields": {`["bdstoken"]`}, "clienttype": {"0"},
	}); err != nil {
		return "", fmt.Errorf("获取 bdstoken: %w", err)
	}
	if out.Result.BDSToken == "" {
		return "", fmt.Errorf("获取 bdstoken: 响应为空(BDUSS 可能已失效)")
	}
	w.bdstoken = out.Result.BDSToken
	return w.bdstoken, nil
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

// getRetry 同 get,但对风控性 errno=-6(登录态判定抖动)退避重试。
// 实测 BDUSS 有效时 xpan 列目录偶发 -6,重试 1-2 次即恢复。仅用于 GET。
func (w *WebClient) getRetry(out any, rawURL string, params url.Values) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * time.Second)
		}
		err = w.get(out, rawURL, params)
		if err == nil || !IsErrno(err, -6) {
			return err
		}
	}
	return err
}

// MkdirAll 逐级创建远端目录。
// 注意:api/create 对已存在路径不报错,而是自动改名新建(<名>_时间戳),
// 必须先列父目录确认不存在再创建,否则重复执行会累积垃圾目录。
func (w *WebClient) MkdirAll(remoteDir string) error {
	parts := strings.Split(strings.Trim(remoteDir, "/"), "/")
	cur := ""
	for _, p := range parts {
		parent := cur
		cur += "/" + p
		exists, err := w.dirExists(parent, p)
		if err != nil {
			return fmt.Errorf("检查目录 %s: %w", cur, err)
		}
		if exists {
			continue
		}
		var out struct {
			Errno int64  `json:"errno"`
			Path  string `json:"path"`
		}
		err = w.postForm(&out, "https://pan.baidu.com/api/create", url.Values{
			"a": {"mkdir"}, "web": {"1"}, "clienttype": {"0"},
		}, url.Values{
			"path": {cur}, "isdir": {"1"},
		})
		if err != nil {
			if IsErrno(err, -8) {
				continue // 已存在(兜底)
			}
			return fmt.Errorf("创建目录 %s: %w", cur, err)
		}
		// 并发竞态下仍可能被改名(返回 path ≠ 请求 path):删除误建目录,视为已存在
		if out.Path != "" && out.Path != cur {
			_ = w.Delete([]string{out.Path})
		}
	}
	return nil
}

// dirExists 列父目录判断子目录是否存在(-9 父目录不存在视为不存在)。
func (w *WebClient) dirExists(parent, name string) (bool, error) {
	if parent == "" {
		parent = "/"
	}
	entries, err := w.List(parent)
	if err != nil {
		if IsErrno(err, -9) {
			return false, nil
		}
		return false, err
	}
	for _, en := range entries {
		if en.Name == name && en.IsDir {
			return true, nil
		}
	}
	return false, nil
}

// List 列目录(xpan 接口;含 -6 退避重试)。
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
	err := w.getRetry(&out, "https://pan.baidu.com/rest/2.0/xpan/file", url.Values{
		"method": {"list"},
		"dir":    {dir},
		"order":  {"name"},
		"num":    {"1000"},
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
// 路径:列父目录取 fs_id(method=meta 不认 BDUSS)→ filemetas 取 dlink → GET dlink。
func (w *WebClient) Download(p string) ([]byte, error) {
	if err := w.EnsureToken(); err != nil {
		return nil, err
	}
	// 1. 列父目录 → fs_id
	var ls struct {
		Errno int64 `json:"errno"`
		List  []struct {
			ServerFilename string `json:"server_filename"`
			FsID           uint64 `json:"fs_id"`
		} `json:"list"`
	}
	if err := w.getRetry(&ls, "https://pan.baidu.com/rest/2.0/xpan/file", url.Values{
		"method": {"list"},
		"dir":    {path.Dir(p)},
		"order":  {"name"},
		"num":    {"1000"},
	}); err != nil {
		return nil, fmt.Errorf("列目录取文件信息 %s: %w", p, err)
	}
	var fsid uint64
	for _, it := range ls.List {
		if it.ServerFilename == path.Base(p) {
			fsid = it.FsID
			break
		}
	}
	if fsid == 0 {
		return nil, fmt.Errorf("文件不存在: %s", p)
	}
	// 2. fs_id → dlink
	fsids, _ := json.Marshal([]uint64{fsid})
	var fm struct {
		Errno int64 `json:"errno"`
		Info  []struct {
			Errno int64  `json:"errno"`
			Dlink string `json:"dlink"`
		} `json:"info"`
	}
	if err := w.getRetry(&fm, "https://pan.baidu.com/rest/2.0/xpan/file", url.Values{
		"method": {"filemetas"}, "fsids": {string(fsids)}, "dlink": {"1"},
	}); err != nil {
		return nil, fmt.Errorf("取下载链接 %s: %w", p, err)
	}
	if len(fm.Info) == 0 || fm.Info[0].Dlink == "" {
		return nil, fmt.Errorf("未取得下载链接: %s", p)
	}
	// 3. GET dlink(CheckRedirect 保持 UA 与 cookie)
	req, err := http.NewRequest("GET", fm.Info[0].Dlink, nil)
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

// Delete 批量删除文件/目录(api/filemanager,需 bdstoken)。
func (w *WebClient) Delete(paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	if err := w.EnsureToken(); err != nil {
		return err
	}
	token, err := w.getBdstoken()
	if err != nil {
		return err
	}
	list, _ := json.Marshal(paths)
	var out struct {
		Errno int64 `json:"errno"`
		Info  []struct {
			Path  string `json:"path"`
			Errno int64  `json:"errno"`
		} `json:"info"`
	}
	err = w.postForm(&out, "https://pan.baidu.com/api/filemanager", url.Values{
		"opera": {"delete"}, "async": {"0"},
		"web": {"1"}, "clienttype": {"0"}, "bdstoken": {token},
	}, url.Values{"filelist": {string(list)}})
	if err != nil {
		return fmt.Errorf("删除失败 %v: %w", paths, err)
	}
	for _, it := range out.Info {
		// 12/-9/8001: 不存在,视为已删
		if it.Errno != 0 && it.Errno != 12 && it.Errno != -9 && it.Errno != 8001 {
			return fmt.Errorf("删除 %s 失败: errno=%d", it.Path, it.Errno)
		}
	}
	return nil
}

// UploadFile 网页版三步上传:api/precreate → d.pcs superfile2 分片 → api/create。
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

// uploadChunk 上传单个 4MB 分片到 d.pcs superfile2(PUT 原始字节,
// 与 oauth 实现同构),失败重试 3 次(1s/2s/4s)。
func (w *WebClient) uploadChunk(uploadID, remotePath string, seq int, localPath string) error {
	if err := w.EnsureToken(); err != nil {
		return err
	}
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
			"method":   {"upload"},
			"type":     {"tmpfile"},
			"path":     {remotePath},
			"uploadid": {uploadID},
			"partseq":  {strconv.Itoa(seq)},
			"app_id":   {webAppID},
		}
		req, err := http.NewRequest("POST",
			"https://d.pcs.baidu.com/rest/2.0/pcs/superfile2?"+params.Encode(), &buf)
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.Header.Set("User-Agent", netdiskUA)
		resp, err := w.HTTP.Do(req)
		if err != nil {
			lastErr = w.redact(err)
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		var out struct {
			Errno int64  `json:"errno"`
			Error string `json:"error_msg"`
			MD5   string `json:"md5"`
		}
		json.Unmarshal(body, &out)
		if resp.StatusCode == 200 && out.Errno == 0 && out.MD5 != "" {
			return nil
		}
		lastErr = fmt.Errorf("superfile2 http=%d errno=%d body=%s",
			resp.StatusCode, out.Errno, string(body))
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

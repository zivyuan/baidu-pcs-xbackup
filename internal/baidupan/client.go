package baidupan

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client 百度网盘开放平台 API 客户端。
type Client struct {
	AppKey    string
	SecretKey string
	TokenFile string
	HTTP      *http.Client
	token     *Token
}

func New(appKey, secretKey, tokenFile string) *Client {
	return &Client{
		AppKey: appKey, SecretKey: secretKey, TokenFile: tokenFile,
		HTTP: &http.Client{Timeout: 120 * time.Second},
	}
}

// apiError 统一携带百度 errno。
type apiError struct {
	API   string
	Errno int64
	Msg   string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("%s 失败: errno=%d %s", e.API, e.Errno, e.Msg)
}

// callGET 发起 GET 请求,把响应 JSON 解到 out;errno!=0 时返回 *apiError。
// 每次调用前确保 token 有效(daemon 长期运行自动续期)。
func (c *Client) callGET(out any, api string, params url.Values) error {
	if err := c.EnsureToken(); err != nil {
		return err
	}
	params.Set("access_token", c.token.AccessToken)
	req, err := http.NewRequest("GET", "https://pan.baidu.com"+api+"?"+params.Encode(), nil)
	if err != nil {
		return err
	}
	return c.do(out, req, api)
}

func (c *Client) callPOST(out any, api string, params url.Values, form url.Values) error {
	if err := c.EnsureToken(); err != nil {
		return err
	}
	params.Set("access_token", c.token.AccessToken)
	req, err := http.NewRequest("POST", "https://pan.baidu.com"+api+"?"+params.Encode(),
		strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c.do(out, req, api)
}

// IsErrno 判断错误是否为指定百度 errno(如 -9 目录/文件不存在)。
func IsErrno(err error, errno int64) bool {
	var ae *apiError
	return errors.As(err, &ae) && ae.Errno == errno
}

// redact 从错误文本中抹掉 access_token,防止网络错误把含 token 的 URL 带进日志。
func (c *Client) redact(err error) error {
	if err != nil && c.token != nil && c.token.AccessToken != "" {
		msg := err.Error()
		masked := strings.ReplaceAll(msg, "access_token="+c.token.AccessToken, "access_token=***")
		if masked != msg {
			return errors.New(masked)
		}
	}
	return err
}

func (c *Client) do(out any, req *http.Request, api string) error {
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%s 请求失败: %w", api, c.redact(err))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return parseResp(out, body, api)
}

// parseResp 统一解析百度响应:errno!=0 或 error_code!=0 返回 *apiError,
// 否则把 body 解到 out。oauth 与 web 两种客户端共用。
func parseResp(out any, body []byte, api string) error {
	// 错误响应有两种风格:xpan 用 errno,开放平台网关用 error_code(如 31023),都要识别
	var env struct {
		Errno     int64  `json:"errno"`
		ErrorCode int64  `json:"error_code"`
		ErrorMsg  string `json:"error_msg"`
	}
	if err := json.Unmarshal(body, &env); err == nil {
		if env.Errno != 0 {
			return &apiError{API: api, Errno: env.Errno, Msg: env.ErrorMsg}
		}
		if env.ErrorCode != 0 {
			return &apiError{API: api, Errno: env.ErrorCode, Msg: env.ErrorMsg}
		}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("%s 响应解析失败: %s", api, string(body))
	}
	return nil
}

package baidupan

import (
	"encoding/json"
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
func (c *Client) callGET(out any, api string, params url.Values) error {
	params.Set("access_token", c.token.AccessToken)
	req, err := http.NewRequest("GET", "https://pan.baidu.com"+api+"?"+params.Encode(), nil)
	if err != nil {
		return err
	}
	return c.do(out, req, api)
}

func (c *Client) callPOST(out any, api string, params url.Values, form url.Values) error {
	params.Set("access_token", c.token.AccessToken)
	req, err := http.NewRequest("POST", "https://pan.baidu.com"+api+"?"+params.Encode(),
		strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c.do(out, req, api)
}

func (c *Client) do(out any, req *http.Request, api string) error {
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%s 请求失败: %w", api, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var env struct {
		Errno    int64  `json:"errno"`
		ErrorMsg string `json:"error_msg"`
	}
	if err := json.Unmarshal(body, &env); err == nil && env.Errno != 0 {
		return &apiError{API: api, Errno: env.Errno, Msg: env.ErrorMsg}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("%s 响应解析失败: %s", api, string(body))
	}
	return nil
}

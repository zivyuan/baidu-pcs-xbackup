package baidupan

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// Token 持久化的授权凭证(0600 保存于 TokenFile)。
type Token struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"` // unix 秒,已含 10 分钟提前量
}

func (c *Client) loadToken() (*Token, error) {
	if c.token != nil {
		return c.token, nil
	}
	data, err := os.ReadFile(c.TokenFile)
	if err != nil {
		return nil, err
	}
	t := &Token{}
	if err := json.Unmarshal(data, t); err != nil {
		return nil, err
	}
	c.token = t
	return t, nil
}

func (c *Client) saveToken(t *Token) error {
	dir := filepath.Dir(c.TokenFile)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(t, "", "  ")
	// 临时文件 + rename 原子写,避免刷新中断产生半截 token 文件
	tmp, err := os.CreateTemp(dir, ".token-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, c.TokenFile); err != nil {
		os.Remove(tmpPath)
		return err
	}
	c.token = t
	return nil
}

// Login 设备码授权:打印验证地址,轮询直到用户确认或超时。
func (c *Client) Login() error {
	q := url.Values{
		"client_id":     {c.AppKey},
		"scope":         {"basic,netdisk"},
		"response_type": {"device_code"},
	}
	resp, err := c.httpClient().PostForm("https://openapi.baidu.com/oauth/2.0/device/code", q)
	if err != nil {
		return fmt.Errorf("获取设备码失败: %w", err)
	}
	defer resp.Body.Close()
	var dc struct {
		Errno           int64  `json:"errno"`
		ErrorMsg        string `json:"error_msg"`
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURL string `json:"verification_url"`
		ExpiresIn       int    `json:"expires_in"`
		Interval        int    `json:"interval"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&dc); err != nil {
		return err
	}
	if dc.Errno != 0 {
		return fmt.Errorf("获取设备码失败: errno=%d %s", dc.Errno, dc.ErrorMsg)
	}
	fmt.Printf("请在浏览器打开: %s\n输入用户码: %s\n确认授权后本程序将自动继续...\n",
		dc.VerificationURL, dc.UserCode)

	interval := time.Duration(dc.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	deadline := time.Now().Add(time.Duration(dc.ExpiresIn) * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(interval)
		q := url.Values{
			"grant_type":    {"device_token"},
			"code":          {dc.DeviceCode},
			"client_id":     {c.AppKey},
			"client_secret": {c.SecretKey},
		}
		tr, err := c.httpClient().PostForm("https://openapi.baidu.com/oauth/2.0/token", q)
		if err != nil {
			continue
		}
		var tk struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			ExpiresIn    int64  `json:"expires_in"`
			Error        string `json:"error"`
		}
		json.NewDecoder(tr.Body).Decode(&tk)
		tr.Body.Close()
		if tk.Error == "authorization_pending" {
			continue
		}
		if tk.AccessToken == "" {
			return fmt.Errorf("获取 token 失败: %s", tk.Error)
		}
		t := &Token{
			AccessToken:  tk.AccessToken,
			RefreshToken: tk.RefreshToken,
			ExpiresAt:    time.Now().Unix() + tk.ExpiresIn - 600,
		}
		return c.saveToken(t)
	}
	return fmt.Errorf("授权超时,请重试 bpxup login")
}

// EnsureToken 保证持有有效 access_token,过期自动刷新。
func (c *Client) EnsureToken() error {
	t, err := c.loadToken()
	if err != nil {
		return fmt.Errorf("未找到 token,请先执行 bpxup login: %w", err)
	}
	if time.Now().Unix() < t.ExpiresAt {
		return nil
	}
	q := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {t.RefreshToken},
		"client_id":     {c.AppKey},
		"client_secret": {c.SecretKey},
	}
	resp, err := c.httpClient().PostForm("https://openapi.baidu.com/oauth/2.0/token", q)
	if err != nil {
		return fmt.Errorf("刷新 token 失败: %w", err)
	}
	defer resp.Body.Close()
	var tk struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Error        string `json:"error"`
		ErrorDesc    string `json:"error_description"`
	}
	json.NewDecoder(resp.Body).Decode(&tk)
	if tk.AccessToken == "" {
		return fmt.Errorf("刷新 token 失败(%s),请重新 bpxup login", tk.ErrorDesc)
	}
	return c.saveToken(&Token{
		AccessToken:  tk.AccessToken,
		RefreshToken: tk.RefreshToken,
		ExpiresAt:    time.Now().Unix() + tk.ExpiresIn - 600,
	})
}

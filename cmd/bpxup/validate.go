package main

import (
	"errors"
	"fmt"
	"net/url"
	"os"

	"github.com/zivyuan/baidu-pcs-xbackup/internal/baidupan"
	"github.com/zivyuan/baidu-pcs-xbackup/internal/config"
)

// cmdValidate 校验配置:静态(解析/source 可访问/webhook 格式)+ 连通性
// (token 有效、网盘根目录可访问)。任一失败退出码非 0。
func cmdValidate(args []string) error {
	fs := newFlagSet("validate", nil)
	fs.Parse(args)
	cfgPath := configPath(fs)

	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Println("✗ 配置解析:", err)
		return err
	}
	fmt.Printf("✓ 配置解析(%s)\n", cfgPath)

	ok := true
	for i := range cfg.Projects {
		p := &cfg.Projects[i]
		for j := range p.Tasks {
			t := p.Tasks[j]
			fi, err := os.Stat(t.Source)
			switch {
			case err != nil:
				ok = false
				fmt.Printf("✗ [%s/%s] source 不可访问: %v\n", p.ID, t.Name, err)
			case t.Mode == "raw" && !fi.IsDir():
				ok = false
				fmt.Printf("✗ [%s/%s] raw 模式 source 必须是目录: %s\n", p.ID, t.Name, t.Source)
			default:
				fmt.Printf("✓ [%s/%s] source 可访问\n", p.ID, t.Name)
			}
		}
	}
	if cfg.WebhookURL != "" {
		u, err := url.Parse(cfg.WebhookURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			ok = false
			fmt.Println("✗ webhook_url 不是合法的 http(s) URL")
		} else {
			fmt.Println("✓ webhook_url 格式")
		}
	}

	pan := baidupan.New(cfg.AppKey, cfg.SecretKey, cfg.AccessTokenFile)
	if err := pan.EnsureToken(); err != nil {
		fmt.Println("✗ 网盘授权:", err)
		return errors.New("存在校验失败项")
	}
	fmt.Println("✓ 网盘授权(token 有效)")
	if _, err := pan.List(cfg.Root()); err != nil {
		fmt.Println("✗ 网盘根目录访问:", err)
		return errors.New("存在校验失败项")
	}
	fmt.Printf("✓ 网盘根目录可访问(%s)\n", cfg.Root())

	if !ok {
		return errors.New("存在校验失败项")
	}
	fmt.Println("配置校验全部通过")
	return nil
}

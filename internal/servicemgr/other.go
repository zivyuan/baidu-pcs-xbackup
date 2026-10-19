//go:build !windows && !linux && !darwin

package servicemgr

import "errors"

func ensure(string) (string, error) { return "", ErrNoServiceManager }
func stop() error                   { return errors.New("服务未注册") }
func uninstall() error              { return errors.New("服务未注册") }

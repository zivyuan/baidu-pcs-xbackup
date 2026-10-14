//go:build !windows

package main

import "errors"

func serviceDispatch(args []string) error {
	return errors.New("service 子命令仅支持 Windows")
}

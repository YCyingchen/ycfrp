//go:build windows

package main

import (
	"fmt"
	"os/exec"
)

// openURL 用系统默认浏览器打开给定地址。
func openURL(url string) error {
	if err := exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start(); err != nil {
		return fmt.Errorf("无法调起系统浏览器：%w", err)
	}
	return nil
}

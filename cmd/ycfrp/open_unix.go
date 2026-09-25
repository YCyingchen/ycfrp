//go:build !windows

package main

import (
	"fmt"
	"os/exec"
)

// openURL 优先使用桌面环境的 xdg-open 打开地址。
func openURL(url string) error {
	if err := exec.Command("xdg-open", url).Start(); err != nil {
		return fmt.Errorf("无法调起浏览器（请手动访问）：%w", err)
	}
	return nil
}

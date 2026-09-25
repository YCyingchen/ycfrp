//go:build windows

// Package autostart 管理 Windows 开机自启动：把当前可执行文件写入
// HKCU\Software\Microsoft\Windows\CurrentVersion\Run 键，登录后自动运行。
// 只写当前用户的注册表，不需要管理员权限；删除键值即完成关闭。
package autostart

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows/registry"
)

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`
const valueName = "YCFRP"

// Command 返回写入注册表的启动命令：完整路径 + 后台参数。
// 用 -d -q 后台静默运行，开机后不弹窗口不打扰用户。
func Command() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`"%s" -d -q`, exe), nil
}

// Enabled 报告当前是否已开启自启动（键值存在且指向本程序）。
func Enabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetStringValue(valueName)
	if err != nil {
		return false
	}
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	// 键值里包含当前 exe 路径即认为是我们写入的（用户可能移动过程序位置）。
	return len(v) > 0 && containsPath(v, exe)
}

// Set 开启或关闭自启动。开启时写入/刷新键值（exe 路径变化后自动更新）。
func Set(enable bool) error {
	if !enable {
		k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
		if err != nil {
			return err
		}
		defer k.Close()
		// 只删除我们自己的键值，不动用户其它自启动项。
		if Enabled() {
			return k.DeleteValue(valueName)
		}
		return nil
	}
	cmd, err := Command()
	if err != nil {
		return err
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue(valueName, cmd)
}

// containsPath 简单判断命令行里是否引用了该 exe 路径（不区分大小写）。
func containsPath(cmd, exe string) bool {
	lc := toLower(cmd)
	le := toLower(exe)
	for i := 0; i+len(le) <= len(lc); i++ {
		if lc[i:i+len(le)] == le {
			return true
		}
	}
	return false
}

func toLower(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 32
		}
	}
	return string(b)
}

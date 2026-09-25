//go:build !windows

// Package autostart 在非 Windows 平台提供空实现，保持调用方无需条件编译。
package autostart

import "fmt"

// Enabled 恒为 false：自启动仅支持 Windows。
func Enabled() bool { return false }

// Set 非 Windows 平台不支持，恒返回错误提示。
func Set(bool) error { return fmt.Errorf("自启动仅支持 Windows 平台") }

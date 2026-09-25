//go:build windows

package updater

import (
	"os/exec"
	"syscall"
)

// Windows 进程创建标志：助手进程必须完全脱离面板进程的进程组与控制台，
// 否则面板退出时可能连带把助手一起结束，导致替换做到一半。
const (
	createNewProcessGroup = 0x00000200
	detachedProcess       = 0x00000008
	createNoWindow        = 0x08000000
)

func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNewProcessGroup | detachedProcess | createNoWindow,
	}
}

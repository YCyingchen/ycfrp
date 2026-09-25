//go:build !windows

package updater

import (
	"os/exec"
	"syscall"
)

// 通过 Setsid 让助手进程脱离当前会话与控制终端，面板退出后它仍能继续工作。
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

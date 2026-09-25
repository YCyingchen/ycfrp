//go:build !windows

package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// spawn 通过 Setsid 让子进程脱离当前会话与控制终端，从而在父进程退出后
// 继续运行，调用方无需 nohup。返回子进程号，供调用方等待其就绪。
func spawn(exe string, args []string, env []string) (int, error) {
	cmd := exec.Command(exe, args...)
	cmd.Env = env
	cmd.Dir = filepath.Dir(exe)
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	if err := cmd.Process.Release(); err != nil {
		return pid, err
	}
	return pid, nil
}

// alive 通过信号 0 探测进程是否存在。
func alive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// sameImage 校验目标进程的可执行文件是不是本程序，避免进程号复用后误杀。
func sameImage(pid int) bool {
	link := filepath.Join("/proc", strconv.Itoa(pid), "exe")
	target, err := os.Readlink(link)
	if err != nil {
		// 无 /proc 的环境（如部分精简容器）无法核对，退回按进程号判断。
		return true
	}
	self, err := os.Executable()
	if err != nil {
		return true
	}
	return strings.TrimSuffix(target, " (deleted)") == self
}

// forceKill 发送 SIGKILL。
func forceKill(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return nil
	}
	if err := proc.Signal(syscall.SIGKILL); err != nil {
		return nil
	}
	return nil
}

//go:build windows

package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"unsafe"
)

// Windows 进程创建标志。DETACHED_PROCESS 让子进程不再依附父进程的控制台，
// CREATE_NEW_PROCESS_GROUP 让它拥有独立进程组，父进程退出后其继续存活。
const (
	createNewProcessGroup = 0x00000200
	detachedProcess       = 0x00000008
	createNoWindow        = 0x08000000
)

var (
	kernel32          = syscall.NewLazyDLL("kernel32.dll")
	procOpenProcess   = kernel32.NewProc("OpenProcess")
	procTerminateProc = kernel32.NewProc("TerminateProcess")
	procQueryImage    = kernel32.NewProc("QueryFullProcessImageNameW")
	procGetExitCode   = kernel32.NewProc("GetExitCodeProcess")

	processTerminate uint32 = 0x0001
	processQueryInfo uint32 = 0x1000
)

// spawn 以完全脱离当前终端的方式拉起子进程，等价于双击运行且不显示窗口。
// 返回子进程号，供调用方等待其就绪或判断其是否已失败退出。
func spawn(exe string, args []string, env []string) (int, error) {
	cmd := exec.Command(exe, args...)
	cmd.Env = env
	cmd.Dir = filepath.Dir(exe)
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNewProcessGroup | detachedProcess | createNoWindow,
	}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	if err := cmd.Process.Release(); err != nil {
		return pid, err
	}
	return pid, nil
}

// alive 判断进程号对应的进程是否仍然存在。
func alive(pid int) bool {
	h, err := openProcess(processQueryInfo, pid)
	if err != nil || h == 0 {
		return false
	}
	defer syscall.CloseHandle(syscall.Handle(h))

	var code uint32
	ret, _, _ := procGetExitCode.Call(h, uintptr(unsafe.Pointer(&code)))
	if ret == 0 {
		return false
	}
	const stillActive = 259
	return code == stillActive
}

// sameImage 校验目标进程的可执行文件是不是本程序，避免进程号复用后误杀。
func sameImage(pid int) bool {
	h, err := openProcess(processQueryInfo, pid)
	if err != nil || h == 0 {
		return false
	}
	defer syscall.CloseHandle(syscall.Handle(h))

	buf := make([]uint16, syscall.MAX_PATH)
	size := uint32(len(buf))
	ret, _, _ := procQueryImage.Call(
		h,
		0,
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)),
	)
	if ret == 0 {
		return false
	}
	target := syscall.UTF16ToString(buf[:size])

	self, err := os.Executable()
	if err != nil {
		return true
	}
	return SameProgram(self, target)
}

// forceKill 强制结束进程。
func forceKill(pid int) error {
	h, err := openProcess(processTerminate, pid)
	if err != nil || h == 0 {
		// 进程已自行退出。
		return nil
	}
	defer syscall.CloseHandle(syscall.Handle(h))
	ret, _, callErr := procTerminateProc.Call(h, 1)
	if ret == 0 {
		return callErr
	}
	return nil
}

func openProcess(access uint32, pid int) (uintptr, error) {
	h, _, err := procOpenProcess.Call(uintptr(access), 0, uintptr(uint32(pid)))
	if h == 0 {
		return 0, err
	}
	return h, nil
}

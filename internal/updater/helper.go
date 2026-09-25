package updater

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	daemonpkg "github.com/ycfrp/ycfrp/internal/daemon"
)

// 助手进程使用的内部开关。它们由面板自动拼装，不属于用户可见的命令行接口。
const (
	FlagApply  = "--ycfrp-apply-update"
	flagTarget = "--ycfrp-target"
	flagStaged = "--ycfrp-staged"
	flagParent = "--ycfrp-parent-pid"
	flagData   = "--ycfrp-data-dir"
	flagPort   = "--ycfrp-port"
)

// ApplyRequest 描述一次待执行的替换动作。
type ApplyRequest struct {
	// Target 是正在运行的可执行文件路径，替换后由它继续提供服务。
	Target string
	// Staged 是已经下载并校验通过的新二进制路径。
	Staged string
	// ParentPID 是当前面板进程号，助手要等它退出后才能开始替换。
	ParentPID int
	// DataDir 与 Port 用于重启后面板，保持原有运行参数不变。
	DataDir string
	Port    int
}

// IsHelperInvocation 判断当前进程是否以助手身份被拉起。
func IsHelperInvocation(args []string) bool {
	for _, a := range args {
		if a == FlagApply {
			return true
		}
	}
	return false
}

// LaunchHelper 把当前可执行文件复制一份到临时目录，并以助手身份启动它。
//
// 之所以要「复制自身再启动」而不是直接重新执行原程序：Windows 上正在运行的
// 镜像被文件锁保护，直接覆盖会失败；助手独立于面板进程，可以在面板退出后
// 自由地替换掉原文件。
func LaunchHelper(req ApplyRequest) error {
	if strings.TrimSpace(req.Target) == "" || strings.TrimSpace(req.Staged) == "" {
		return fmt.Errorf("替换参数不完整")
	}
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("无法定位当前程序：%w", err)
	}
	dir, err := os.MkdirTemp("", "ycfrp-updater-")
	if err != nil {
		return fmt.Errorf("创建临时目录失败：%w", err)
	}
	helper := filepath.Join(dir, filepath.Base(self))
	if err := copyFile(self, helper, 0o755); err != nil {
		return fmt.Errorf("准备更新助手失败：%w", err)
	}

	args := []string{
		FlagApply,
		flagTarget, req.Target,
		flagStaged, req.Staged,
		flagParent, strconv.Itoa(req.ParentPID),
		flagData, req.DataDir,
		flagPort, strconv.Itoa(req.Port),
	}
	cmd := exec.Command(helper, args...)
	cmd.Dir = dir
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动更新助手失败：%w", err)
	}
	return cmd.Process.Release()
}

// ParseApplyRequest 从助手进程的命令行里解析替换参数。
func ParseApplyRequest(args []string) (ApplyRequest, error) {
	req := ApplyRequest{}
	get := func(flag string) string {
		for i, a := range args {
			if a == flag && i+1 < len(args) {
				return args[i+1]
			}
		}
		return ""
	}
	req.Target = get(flagTarget)
	req.Staged = get(flagStaged)
	req.DataDir = get(flagData)
	if v := get(flagParent); v != "" {
		req.ParentPID, _ = strconv.Atoi(v)
	}
	if v := get(flagPort); v != "" {
		req.Port, _ = strconv.Atoi(v)
	}
	if req.Target == "" || req.Staged == "" {
		return req, fmt.Errorf("替换参数不完整")
	}
	return req, nil
}

// RunApply 是助手进程的主流程：等待父进程退出，替换二进制，再重启面板。
// 返回值只用于设置自身退出码。
func RunApply(req ApplyRequest, logf func(string, ...any)) int {
	if logf == nil {
		logf = func(string, ...any) {}
	}

	if req.ParentPID > 0 {
		// 面板需要一点时间完成优雅退出并释放文件锁，这里等待至多 60 秒。
		deadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) {
			if !daemonpkg.Alive(req.ParentPID) {
				break
			}
			time.Sleep(300 * time.Millisecond)
		}
		if daemonpkg.Alive(req.ParentPID) {
			logf("等待原进程退出超时，已放弃本次更新")
			return 1
		}
	}
	// 父进程刚退出时文件句柄可能尚未完全释放，短暂等待以避免 Windows 上的占用错误。
	time.Sleep(800 * time.Millisecond)

	if err := VerifyBinary(req.Staged); err != nil {
		logf("校验新程序失败：%v", err)
		return 1
	}

	backup := req.Target + ".bak"
	_ = os.Remove(backup)

	// 先把旧程序改名备份，再把新程序改名就位。两步都是同目录内的改名操作，
	// 在 Linux 上是原子替换，在 Windows 上也能绕开运行中镜像的文件锁。
	if err := os.Rename(req.Target, backup); err != nil {
		logf("备份原程序失败：%v", err)
		return 1
	}
	if err := os.Rename(req.Staged, req.Target); err != nil {
		// 就位失败必须把原程序换回去，否则面板会彻底无法启动。
		if rbErr := os.Rename(backup, req.Target); rbErr != nil {
			logf("还原原程序也失败：%v（备份保留在 %s）", rbErr, backup)
			return 1
		}
		logf("替换新程序失败：%v", err)
		return 1
	}
	_ = os.Chmod(req.Target, 0o755)
	logf("新程序已就位：%s", req.Target)

	if err := restart(req); err != nil {
		logf("重启面板失败：%v，可手动启动", err)
		return 1
	}
	_ = os.Remove(backup)
	logf("更新完成，面板已重启")
	return 0
}

// restart 用与更新前一致的参数重新拉起面板。
func restart(req ApplyRequest) error {
	args := make([]string, 0, 4)
	if strings.TrimSpace(req.DataDir) != "" {
		args = append(args, "-c", req.DataDir)
	}
	if req.Port > 0 {
		args = append(args, "-p", strconv.Itoa(req.Port))
	}
	cmd := exec.Command(req.Target, args...)
	cmd.Dir = filepath.Dir(req.Target)
	cmd.Env = append(os.Environ(), daemonpkg.EnvMarker+"=1")
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

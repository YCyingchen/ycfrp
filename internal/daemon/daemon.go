// Package daemon 为 YCFRP 提供后台运行能力：脱离终端静默启动、进程记录文件
// 维护，以及停止与状态查询。平台差异收敛在 spawn_windows.go 与 spawn_unix.go
// 的实现里；正常停止统一走「停止请求文件」，由运行中的实例自行优雅退出，
// 避免依赖 Windows 上并不存在的 SIGTERM 语义。
package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// EnvMarker 标记当前进程由后台流程拉起，避免反复派生自身。
	EnvMarker = "YCFRP_DAEMONIZED"
	// PIDFileName 记录正在运行的面板进程号。
	PIDFileName = "ycfrp.pid"
	// StopFileName 是优雅退出的请求文件，由 -stop 写入、运行中的实例消费。
	StopFileName = "ycfrp.stop"
	// stopWait 是等待优雅退出的最长时间，超过后强制结束进程。
	stopWait = 8 * time.Second
)

// ErrNotRunning 表示未发现正在运行的实例。
var ErrNotRunning = errors.New("未检测到正在运行的 YCFRP 进程")

// SameProgram 判断两个可执行文件路径是否属于同一套 YCFRP 程序。
//
// Windows 安装包里同时提供 GUI 版与控制台版两个可执行文件，它们共用同一个
// 数据目录，必须能互相停止对方拉起的后台实例；因此同一目录下以 ycfrp 开头的
// 可执行文件都视为同一程序。两者不在同一目录时退回按文件名严格比较。
func SameProgram(self, target string) bool {
	self = filepath.Clean(self)
	target = filepath.Clean(target)

	if !strings.EqualFold(filepath.Dir(target), filepath.Dir(self)) {
		return strings.EqualFold(filepath.Base(target), filepath.Base(self))
	}
	name := strings.ToLower(strings.TrimSuffix(filepath.Base(target), filepath.Ext(target)))
	return strings.HasPrefix(name, "ycfrp")
}

// PIDPath 返回数据目录下的进程记录文件路径。
func PIDPath(dataDir string) string {
	return filepath.Join(dataDir, PIDFileName)
}

// StopPath 返回优雅退出请求文件的路径。
func StopPath(dataDir string) string {
	return filepath.Join(dataDir, StopFileName)
}

// Daemonized 判断当前进程是否已经处于后台运行状态。
func Daemonized() bool {
	return os.Getenv(EnvMarker) == "1"
}

// WritePID 把当前进程号写入数据目录，供后续停止与状态查询使用。
func WritePID(dataDir string) error {
	if strings.TrimSpace(dataDir) == "" {
		return nil
	}
	return os.WriteFile(PIDPath(dataDir), []byte(strconv.Itoa(os.Getpid())), 0o644)
}

// RemovePID 删除进程记录文件。
func RemovePID(dataDir string) {
	if strings.TrimSpace(dataDir) == "" {
		return
	}
	_ = os.Remove(PIDPath(dataDir))
}

// ReadPID 读取进程记录文件中的进程号。
func ReadPID(dataDir string) (int, error) {
	if strings.TrimSpace(dataDir) == "" {
		return 0, ErrNotRunning
	}
	data, err := os.ReadFile(PIDPath(dataDir))
	if err != nil {
		return 0, ErrNotRunning
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, ErrNotRunning
	}
	return pid, nil
}

// Running 返回正在运行的实例进程号，未运行时返回 ErrNotRunning。
// 记录文件指向的进程已退出、或该进程号已被系统分配给其它程序时，
// 都会顺手清理记录文件，避免误伤无关进程。
func Running(dataDir string) (int, error) {
	pid, err := ReadPID(dataDir)
	if err != nil {
		return 0, ErrNotRunning
	}
	if !alive(pid) || !sameImage(pid) {
		RemovePID(dataDir)
		return 0, ErrNotRunning
	}
	return pid, nil
}

// Spawn 以脱离当前终端的方式重新拉起本程序，并返回子进程号。数据目录为空时
// 由子进程沿用默认位置；port 大于 0 时一并传给子进程，保证后台实例监听在
// 用户指定的端口上。父进程会在派生成功后退出的场景下调用它。
func Spawn(dataDir string, port int) (int, error) {
	if Daemonized() {
		return 0, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return 0, fmt.Errorf("无法定位当前程序：%w", err)
	}
	args := make([]string, 0, 4)
	if strings.TrimSpace(dataDir) != "" {
		args = append(args, "-c", dataDir)
	}
	if port > 0 {
		args = append(args, "-p", strconv.Itoa(port))
	}
	env := append(os.Environ(), EnvMarker+"=1")
	return spawn(exe, args, env)
}

// Alive 报告进程号对应的进程是否仍然存在，供启动方等待子进程就绪时
// 判断「子进程已经失败退出」，避免一直空等到超时。
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return alive(pid)
}

// RequestStop 写入停止请求，运行中的实例会在下一次轮询时优雅退出。
func RequestStop(dataDir string) error {
	if strings.TrimSpace(dataDir) == "" {
		return errors.New("未指定数据目录")
	}
	return os.WriteFile(StopPath(dataDir), []byte(time.Now().Format(time.RFC3339)), 0o644)
}

// StopRequested 报告是否存在待处理的停止请求。
func StopRequested(dataDir string) bool {
	if strings.TrimSpace(dataDir) == "" {
		return false
	}
	_, err := os.Stat(StopPath(dataDir))
	return err == nil
}

// ClearStop 删除停止请求文件，供实例退出前与下次启动时清理。
func ClearStop(dataDir string) {
	if strings.TrimSpace(dataDir) == "" {
		return
	}
	_ = os.Remove(StopPath(dataDir))
}

// Stop 结束进程记录文件指向的实例并返回其进程号。先请求优雅退出，
// 超过等待时间仍未结束则强制终止。
func Stop(dataDir string) (int, error) {
	pid, err := Running(dataDir)
	if err != nil {
		return 0, err
	}

	// 先请求优雅退出，让面板有机会落盘配置与关闭内核监听端口。
	if err := RequestStop(dataDir); err != nil {
		return pid, fmt.Errorf("写入停止请求失败：%w", err)
	}

	deadline := time.Now().Add(stopWait)
	for time.Now().Before(deadline) {
		if !alive(pid) {
			ClearStop(dataDir)
			RemovePID(dataDir)
			return pid, nil
		}
		time.Sleep(200 * time.Millisecond)
	}

	// 优雅退出超时，强制结束。
	if err := forceKill(pid); err != nil {
		ClearStop(dataDir)
		return pid, fmt.Errorf("强制结束进程 %d 失败：%w", pid, err)
	}
	ClearStop(dataDir)
	RemovePID(dataDir)
	return pid, nil
}

// Watch 轮询停止请求文件，一旦出现就调用 onStop，让运行中的实例优雅退出。
// 它同时负责在退出时清理进程记录文件。ctx 结束时函数返回。
func Watch(ctx context.Context, dataDir string, onStop func()) {
	if strings.TrimSpace(dataDir) == "" || onStop == nil {
		return
	}
	ticker := time.NewTicker(400 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if StopRequested(dataDir) {
				ClearStop(dataDir)
				onStop()
				return
			}
		}
	}
}

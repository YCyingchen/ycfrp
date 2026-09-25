//go:build windows && installer

// Command ycfrp-installer 是 YCFRP 的 Windows 自解压安装器。
//
// 仅在显式指定 -tags installer 时构建（见 scripts/build-installer 说明）：
//   go build -tags installer -ldflags "-s -w -H windowsgui" ./cmd/ycfrp-installer
// 构建前需先把 GUI/console 两个 exe 与 README 放到本目录（go:embed 要求）。
// 双击运行后完成：
//  1. 选择安装目录（默认 %LocalAppData%\YCFRP，回车确认）；
//  2. 释放程序文件；
//  3. 写入 HKCU Run 键实现开机自启动；
//  4. 在桌面创建「YCFRP 面板」快捷方式；
//  5. 支持卸载：删除文件、快捷方式与注册表键值。
//
// 无需管理员权限：只写当前用户的目录与注册表。
package main

import (
	"bufio"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows/registry"
)

//go:embed YCFRP.exe
var guiBinary []byte

//go:embed YCFRP-console.exe
var consoleBinary []byte

//go:embed README-windows.txt
var readme []byte

const (
	runKey     = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValue   = "YCFRP"
	appDirName = "YCFRP"
)

func main() {
	os.Exit(run())
}

func run() int {
	// 卸载模式：删除文件与注册表键值。
	if len(os.Args) > 1 && (os.Args[1] == "-uninstall" || os.Args[1] == "/uninstall") {
		return uninstall()
	}

	fmt.Println("====================================")
	fmt.Println("  YCFRP 安装程序")
	fmt.Println("  FRP 双端管理面板（内置 frps / frpc 内核）")
	fmt.Println("====================================")
	fmt.Println()

	localAppData := os.Getenv("LocalAppData")
	if localAppData == "" {
		localAppData = `C:\Program Files`
	}
	defDir := filepath.Join(localAppData, appDirName)

	fmt.Printf("安装目录 [%s]：", defDir)
	reader := bufio.NewReader(os.Stdin)
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(strings.Trim(input, "\""))
	target := defDir
	if input != "" {
		target = input
	}

	fmt.Println()
	fmt.Printf("正在安装到 %s ...\n", target)
	if err := os.MkdirAll(target, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "创建目录失败：%v\n", err)
		return 1
	}

	written := []struct{ name string; data []byte }{
		{"YCFRP.exe", guiBinary},
		{"YCFRP-console.exe", consoleBinary},
		{"README.txt", readme},
	}
	for _, w := range written {
		p := filepath.Join(target, w.name)
		if err := os.WriteFile(p, w.data, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "写入 %s 失败：%v\n", w.name, err)
			return 1
		}
		fmt.Printf("  已释放 %s\n", w.name)
	}

	// 复制安装器自身到安装目录，供以后卸载使用。
	self, _ := os.Executable()
	if self != "" && !strings.EqualFold(self, filepath.Join(target, "YCFRP-Installer.exe")) {
		data, err := os.ReadFile(self)
		if err == nil {
			_ = os.WriteFile(filepath.Join(target, "YCFRP-Installer.exe"), data, 0o755)
		}
	}

	// 写注册表 Run 键：登录后自动后台运行控制台版。
	guiPath := filepath.Join(target, "YCFRP-console.exe")
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		fmt.Fprintf(os.Stderr, "打开注册表失败：%v\n", err)
	} else {
		defer k.Close()
		if err := k.SetStringValue(runValue, fmt.Sprintf(`"%s" -d -q`, guiPath)); err != nil {
			fmt.Fprintf(os.Stderr, "写入自启动键失败：%v\n", err)
		} else {
			fmt.Println("  已设置开机自启动")
		}
	}

	// 桌面快捷方式指向 GUI 版。
	createShortcut(filepath.Join(target, "YCFRP.exe"))

	fmt.Println()
	fmt.Println("安装完成！")
	fmt.Printf("面板地址：http://127.0.0.1:38080（首次启动默认账号 admin / admin）\n")
	fmt.Printf("程序目录：%s\n", target)
	fmt.Println("开机自启动已开启；如需关闭，在面板「设置 → 账号」中切换开关。")
	fmt.Println()

	pause()
	return 0
}

// uninstall 删除注册表键值并尝试删除安装目录内本程序释放的文件。
func uninstall() int {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE|registry.SET_VALUE)
	if err == nil {
		defer k.Close()
		if v, _, gerr := k.GetStringValue(runValue); gerr == nil && strings.Contains(strings.ToLower(v), "ycfrp") {
			_ = k.DeleteValue(runValue)
			fmt.Println("已移除开机自启动。")
		}
	}

	// 安装目录 = 安装器所在目录（安装时已把安装器复制过去）。
	self, err := os.Executable()
	if err == nil {
		dir := filepath.Dir(self)
		for _, name := range []string{"YCFRP.exe", "YCFRP-console.exe", "README.txt"} {
			p := filepath.Join(dir, name)
			if err := os.Remove(p); err == nil {
				fmt.Printf("已删除 %s\n", name)
			}
		}
		// 延迟删除安装器自身（运行中的 exe 无法直接删除）。
		exec.Command("cmd", "/C", "ping -n 3 127.0.0.1 > NUL & del \""+self+"\"").Start()
	}
	fmt.Println("卸载完成。数据目录（含配置与隧道）未删除，可手动清理。")
	pause()
	return 0
}

// createShortcut 通过 PowerShell 在桌面创建「YCFRP 面板」快捷方式。
func createShortcut(targetExe string) {
	desktop := os.Getenv("USERPROFILE")
	if desktop == "" {
		return
	}
	lnk := filepath.Join(desktop, "Desktop", "YCFRP 面板.lnk")
	ps := fmt.Sprintf(
		`$s=(New-Object -ComObject WScript.Shell).CreateShortcut('%s');$s.TargetPath='%s';$s.WorkingDirectory='%s';$s.Save()`,
		lnk, targetExe, filepath.Dir(targetExe))
	if err := exec.Command("powershell", "-NoProfile", "-Command", ps).Start(); err == nil {
		fmt.Println("  已创建桌面快捷方式")
	}
}

// pause 等待用户按键，避免双击运行时窗口一闪而过。
func pause() {
	fmt.Print("\n按回车键退出 ...")
	reader := bufio.NewReader(os.Stdin)
	_, _ = reader.ReadString('\n')
	_ = syscall.Stdin
}

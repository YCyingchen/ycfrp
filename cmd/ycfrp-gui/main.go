//go:build windows

// Command ycfrp-gui is the Windows desktop build. It starts the same embedded
// panel as the server build and renders it either in a native window powered
// by WebView2 or in the user's default browser.
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/jchv/go-webview2"
	"github.com/ycfrp/ycfrp/internal/app"
	"github.com/ycfrp/ycfrp/internal/bootstrap"
	"github.com/ycfrp/ycfrp/internal/config"
	daemonpkg "github.com/ycfrp/ycfrp/internal/daemon"
	"github.com/ycfrp/ycfrp/internal/logx"
	"github.com/ycfrp/ycfrp/internal/updater"
	"github.com/ycfrp/ycfrp/internal/version"
)

// GUI 版链接了 -H windowsgui，没有控制台，因此默认用消息框反馈结果。
// 但消息框是模态的，被脚本调用时会把调用方一直挡住，所以额外提供 -q
// 开关：静默模式下改为把提示写进数据目录内的日志文件。
var (
	quietMode bool
	quietDir  string
)

func main() {
	os.Exit(run())
}

func run() int {
	// 自更新助手入口必须先处理：GUI 版没有控制台，助手在后台完成替换后退出。
	if code, handled := updater.MaybeRunHelper(os.Args[1:]); handled {
		return code
	}

	fs := flag.NewFlagSet(version.ProductName, flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	fs.Usage = func() {
		fmt.Fprintf(os.Stdout, "%s %s —— Windows 桌面版\n\n", version.ProductName, version.Version)
		fmt.Fprintf(os.Stdout, "用法：YCFRP.exe [选项]\n\n")
		fs.PrintDefaults()
		fmt.Fprintf(os.Stdout, "\n示例：\n")
		fmt.Fprintf(os.Stdout, "  YCFRP.exe                  # 打开桌面窗口\n")
		fmt.Fprintf(os.Stdout, "  YCFRP.exe --browser        # 使用默认浏览器打开面板\n")
		fmt.Fprintf(os.Stdout, "  YCFRP.exe -d               # 后台运行，不显示窗口\n")
		fmt.Fprintf(os.Stdout, "  YCFRP.exe -status          # 查看后台运行状态\n")
		fmt.Fprintf(os.Stdout, "  YCFRP.exe -stop            # 停止后台运行的面板\n")
		fmt.Fprintf(os.Stdout, "  YCFRP.exe -d -q            # 后台运行且不弹提示框\n")
		fmt.Fprintf(os.Stdout, "  YCFRP.exe -c D:\\YCFRP      # 指定数据目录\n\n")
	}

	var (
		dataDir       = fs.String("c", "", "数据目录（配置文件、日志、隧道列表的存放位置）")
		dataDirLong   = fs.String("config", "", "同 -c")
		port          = fs.Int("p", 0, "面板监听端口，0 表示沿用配置文件中的值")
		portLong      = fs.Int("port", 0, "同 -p")
		useBrowser    = fs.Bool("browser", false, "只用默认浏览器打开面板，不显示桌面窗口")
		background    = fs.Bool("d", false, "后台运行，不显示窗口")
		backgroundAlt = fs.Bool("daemon", false, "同 -d")
		stopFlag      = fs.Bool("stop", false, "停止正在后台运行的面板")
		statusFlag    = fs.Bool("status", false, "查看后台运行状态")
		openFlag      = fs.Bool("open", false, "用默认浏览器打开面板")
		quiet         = fs.Bool("q", false, "静默运行，不弹出提示框（供脚本调用）")
		quietAlt      = fs.Bool("silent", false, "同 -q")
		showVersion   = fs.Bool("v", false, "显示版本信息")
		versionLong   = fs.Bool("version", false, "同 -v")
		resetPassword = fs.Bool("reset-password", false, "把登录账号重置为默认的 admin/admin")
	)
	if err := fs.Parse(os.Args[1:]); err != nil {
		return 2
	}

	if *showVersion || *versionLong {
		fmt.Printf("%s %s（frp 内核 v%s，Windows 桌面版）\n",
			version.ProductName, version.Version, version.KernelVersion)
		return 0
	}

	dir := firstNonEmpty(*dataDirLong, *dataDir)
	chosenPort := firstNonZero(*portLong, *port)
	quietMode = *quiet || *quietAlt
	quietDir = resolveDataDir(dir)

	if *statusFlag {
		reportStatus(dir)
		return 0
	}

	// 打开面板：不启动新实例，只按当前配置的端口拉起浏览器。
	if *openFlag {
		url := fmt.Sprintf("http://127.0.0.1:%d", panelPort(quietDir))
		if err := openBrowser(url); err != nil {
			fatal("打开浏览器失败：" + err.Error())
			return 1
		}
		return 0
	}

	if *stopFlag {
		target := resolveDataDir(dir)
		pid, err := daemonpkg.Stop(target)
		if err != nil {
			info("YCFRP 未在后台运行。")
			return 0
		}
		info(fmt.Sprintf("已停止 YCFRP（进程 %d）", pid))
		return 0
	}

	if *resetPassword {
		path, err := bootstrap.ResetPassword(dir)
		if err != nil {
			fatal("重置密码失败：" + err.Error())
			return 1
		}
		info(fmt.Sprintf("登录账号已重置为 admin / admin\n配置文件：%s", path))
		return 0
	}

	// 后台运行：静默派生自身后立即返回，不占用窗口也不占用控制台。
	if (*background || *backgroundAlt) && !daemonpkg.Daemonized() {
		target := resolveDataDir(dir)
		if pid, err := daemonpkg.Running(target); err == nil {
			info(fmt.Sprintf("YCFRP 已在后台运行（进程 %d），无需重复启动。", pid))
			return 0
		}
		childPID, err := daemonpkg.Spawn(dir, chosenPort)
		if err != nil {
			fatal("进入后台运行失败：" + err.Error())
			return 1
		}
		// 等后台实例监听端口后再提示，调用方接着打开浏览器必定可用。
		wait := chosenPort
		if wait == 0 {
			wait = panelPort(target)
		}
		waitPanelReady(wait, childPID, 20*time.Second)
		info(backgroundText(target))
		return 0
	}

	// 由后台流程拉起时不再显示窗口，只提供面板服务。
	if daemonpkg.Daemonized() {
		stack, err := bootstrap.Start(dir, "gui", chosenPort)
		if err != nil {
			return 1
		}
		defer stack.Stop()

		daemonpkg.ClearStop(stack.App.DataDir)
		if err := daemonpkg.WritePID(stack.App.DataDir); err != nil {
			stack.App.Logf(logx.LevelWarn, "面板", "写入进程记录文件失败：%v", err)
		}
		defer daemonpkg.RemovePID(stack.App.DataDir)

		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		daemonpkg.Watch(ctx, stack.App.DataDir, stop)

		exitCode := 0
		if err := stack.Wait(ctx); err != nil {
			exitCode = 1
		}
		stop()
		return exitCode
	}

	stack, err := bootstrap.Start(dir, "gui", chosenPort)
	if err != nil {
		fatal("启动失败：" + err.Error())
		return 1
	}
	defer stack.Stop()

	url := stack.PanelURL()
	if stack.FirstRun {
		stack.App.Logf(logx.LevelInfo, "面板", "首次启动，默认账号 %s / %s",
			stack.App.Cfg.Username, stack.GeneratedPassword)
	}

	if *useBrowser {
		info(startupText(stack, url))
		if err := openBrowser(url); err != nil {
			fatal("打开浏览器失败：" + err.Error())
			return 1
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := stack.Wait(ctx); err != nil {
			return 1
		}
		return 0
	}

	if err := runWindow(stack, url); err != nil {
		// WebView2 runtime missing: fall back to the default browser so the
		// user still reaches the panel instead of hitting a dead end.
		stack.App.Logf(logx.LevelWarn, "面板", "桌面窗口不可用：%v，已改用默认浏览器", err)
		if openErr := openBrowser(url); openErr != nil {
			fatal("桌面窗口与浏览器均不可用：" + err.Error())
			return 1
		}
		info(startupText(stack, url))
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if waitErr := stack.Wait(ctx); waitErr != nil {
			return 1
		}
	}
	return 0
}

// runWindow renders the panel in a native WebView2 window and blocks until the
// user closes it.
func runWindow(stack *bootstrap.Stack, url string) error {
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:     false,
		AutoFocus: true,
		WindowOptions: webview2.WindowOptions{
			Title:  fmt.Sprintf("%s %s", version.ProductName, version.Version),
			Width:  1280,
			Height: 820,
			Center: true,
		},
	})
	if w == nil {
		return fmt.Errorf("未检测到 WebView2 运行时")
	}
	defer w.Destroy()

	w.SetSize(1280, 820, webview2.HintNone)
	w.Navigate(url)
	w.Run()
	return nil
}

func startupText(stack *bootstrap.Stack, url string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s 已启动\n", version.ProductName, version.Version)
	fmt.Fprintf(&b, "面板地址：%s\n", url)
	fmt.Fprintf(&b, "数据目录：%s\n", stack.App.DataDir)
	if stack.FirstRun {
		fmt.Fprintf(&b, "首次启动，默认账号：%s / %s", stack.App.Cfg.Username, stack.GeneratedPassword)
	}
	return b.String()
}

// backgroundText 描述后台运行的结果与后续操作方式。
func backgroundText(dataDir string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s 已在后台运行\n", version.ProductName, version.Version)
	fmt.Fprintf(&b, "面板地址：http://127.0.0.1:%d\n", panelPort(dataDir))
	fmt.Fprintf(&b, "数据目录：%s\n\n", dataDir)
	b.WriteString("可直接用浏览器访问面板；\n关闭窗口不影响运行，需要停止时执行 YCFRP.exe -stop。")
	return b.String()
}

// reportStatus 通过消息框展示后台运行状态。
func reportStatus(dir string) {
	target := resolveDataDir(dir)
	pid, err := daemonpkg.Running(target)
	if err != nil {
		info(fmt.Sprintf("%s 未在后台运行。\n数据目录：%s", version.ProductName, target))
		return
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s 正在后台运行\n", version.ProductName)
	fmt.Fprintf(&b, "进程号：%d\n", pid)
	fmt.Fprintf(&b, "数据目录：%s\n\n", target)
	for _, u := range bootstrap.LocalURLs(panelPort(target)) {
		fmt.Fprintf(&b, "面板地址：%s\n", u)
	}
	info(b.String())
}

// resolveDataDir 把空的数据目录参数解析为实际使用的目录。
func resolveDataDir(dir string) string {
	if strings.TrimSpace(dir) == "" {
		return app.DefaultDataDir()
	}
	return dir
}

// panelPort 读取数据目录内的面板端口，读取失败时使用默认端口。
func panelPort(dir string) int {
	cfg, err := config.Load(filepath.Join(dir, "config.json"))
	if err != nil {
		return config.DefaultPort
	}
	return cfg.Port
}

func openBrowser(url string) error {
	return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}

// waitPanelReady 轮询直到面板端口开始监听或超时，用于后台启动后立刻
// 打开浏览器前确认服务确实可用。派生的子进程若已退出（如端口被占用）
// 则立即返回，避免空等。
func waitPanelReady(port, childPID int, timeout time.Duration) bool {
	if port <= 0 {
		port = config.DefaultPort
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	start := time.Now()
	deadline := time.Now().Add(timeout)
	const graceAfter = 3 * time.Second
	for {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			_ = conn.Close()
			return true
		}
		if time.Since(start) > graceAfter && childPID > 0 && !daemonpkg.Alive(childPID) {
			return false
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// info 展示一般提示。GUI 版没有控制台，默认用消息框；开启 -q 后改写日志。
func info(text string) {
	notify(version.ProductName, text, 0x40)
}

func fatal(text string) {
	notify(version.ProductName+" 启动失败", text, 0x10)
}

// notify 按当前模式输出提示：静默模式写日志，否则弹消息框。
func notify(title, text string, flags uintptr) {
	if quietMode {
		writeQuietLog(text)
		return
	}
	messageBox(title, text, flags)
}

// writeQuietLog 把静默模式下的提示追加到数据目录的 logs 子目录，
// 保证脚本调用不阻塞的同时仍留有可追溯的记录。写入失败就放弃。
func writeQuietLog(text string) {
	if strings.TrimSpace(quietDir) == "" {
		return
	}
	logDir := filepath.Join(quietDir, "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(logDir, "ycfrp-gui.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().Format("2006-01-02 15:04:05"), text)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func firstNonZero(values ...int) int {
	for _, v := range values {
		if v != 0 {
			return v
		}
	}
	return 0
}

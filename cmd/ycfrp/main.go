// Command ycfrp is the Linux / server entry point. It serves the YCFRP web
// panel and embeds both frps and frpc kernels in a single binary.
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/ycfrp/ycfrp/internal/app"
	"github.com/ycfrp/ycfrp/internal/bootstrap"
	"github.com/ycfrp/ycfrp/internal/config"
	daemonpkg "github.com/ycfrp/ycfrp/internal/daemon"
	"github.com/ycfrp/ycfrp/internal/logx"
	"github.com/ycfrp/ycfrp/internal/updater"
	"github.com/ycfrp/ycfrp/internal/version"
)

func main() {
	os.Exit(run())
}

func run() int {
	// 中文 Windows 控制台默认 GBK 代码页，先把输出转码到当前代码页，
	// 否则下面所有中文提示在 cmd 里都会变成乱码。
	logx.EnableConsoleEncoding()
	defer logx.FlushConsole()

	// 自更新的替换动作由派生出的助手进程执行，必须先于一切参数解析处理，
	// 否则会被下面的 flag 解析当成未知参数拒绝。
	if code, handled := updater.MaybeRunHelper(os.Args[1:]); handled {
		return code
	}

	fs := flag.NewFlagSet(version.ProductName, flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	fs.Usage = func() {
		fmt.Fprintf(os.Stdout, "%s %s —— %s\n\n", version.ProductName, version.Version, version.Description)
		fmt.Fprintf(os.Stdout, "用法：ycfrp [选项]\n\n")
		fs.PrintDefaults()
		fmt.Fprintf(os.Stdout, "\n示例：\n")
		fmt.Fprintf(os.Stdout, "  ycfrp                        # 使用默认数据目录启动面板\n")
		fmt.Fprintf(os.Stdout, "  ycfrp -c /var/lib/ycfrp -p 38080\n")
		fmt.Fprintf(os.Stdout, "  ycfrp -d                     # 后台运行（关闭终端后继续运行）\n")
		fmt.Fprintf(os.Stdout, "  ycfrp -status                # 查看后台运行状态\n")
		fmt.Fprintf(os.Stdout, "  ycfrp -stop                  # 停止后台运行的面板\n")
		fmt.Fprintf(os.Stdout, "  ycfrp --reset-password       # 重置为默认账号 admin/admin\n\n")
	}

	var (
		dataDir       = fs.String("c", "", "数据目录（配置文件、日志、隧道列表的存放位置）")
		dataDirLong   = fs.String("config", "", "同 -c")
		port          = fs.Int("p", 0, "面板监听端口，0 表示沿用配置文件中的值")
		portLong      = fs.Int("port", 0, "同 -p")
		daemon        = fs.Bool("d", false, "后台运行（守护模式）")
		daemonLong    = fs.Bool("daemon", false, "同 -d")
		stopFlag      = fs.Bool("stop", false, "停止正在后台运行的面板")
		statusFlag    = fs.Bool("status", false, "查看后台运行状态")
		openFlag      = fs.Bool("open", false, "用系统默认浏览器打开面板")
		logDir        = fs.String("log", "", "面板日志目录，默认位于数据目录下的 logs")
		showVersion   = fs.Bool("v", false, "显示版本信息")
		versionLong   = fs.Bool("version", false, "同 -v")
		resetPassword = fs.Bool("reset-password", false, "把登录账号重置为默认的 admin/admin")
	)
	if err := fs.Parse(os.Args[1:]); err != nil {
		return 2
	}

	if *showVersion || *versionLong {
		fmt.Printf("%s %s（frp 内核 v%s，%s/%s）\n",
			version.ProductName, version.Version, version.KernelVersion, runtime.GOOS, runtime.GOARCH)
		return 0
	}

	dir := firstNonEmpty(*dataDirLong, *dataDir)
	chosenPort := firstNonZero(*portLong, *port)
	background := *daemon || *daemonLong
	mode := "server"
	if runningInDocker() {
		mode = "docker"
	}

	if *statusFlag {
		reportStatus(dir)
		return 0
	}

	if *openFlag {
		url := fmt.Sprintf("http://127.0.0.1:%d", panelPort(resolveDataDir(dir)))
		if err := openURL(url); err != nil {
			fmt.Fprintf(os.Stderr, "打开浏览器失败：%v\n面板地址：%s\n", err, url)
			return 1
		}
		return 0
	}

	if *stopFlag {
		stopPath := resolveDataDir(dir)
		pid, err := daemonpkg.Stop(stopPath)
		if err != nil {
			fmt.Printf("未在运行：%v\n", err)
			return 0
		}
		fmt.Printf("已停止 YCFRP（进程 %d）\n", pid)
		return 0
	}

	if *resetPassword {
		path, err := bootstrap.ResetPassword(dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "重置密码失败：%v\n", err)
			return 1
		}
		fmt.Printf("登录账号已重置为 %s / %s\n配置文件：%s\n", "admin", "admin", path)
		return 0
	}

	if background && !daemonpkg.Daemonized() {
		target := resolveDataDir(dir)

		// 已经在跑的实例直接提示，避免端口冲突后才发现。
		if pid, err := daemonpkg.Running(target); err == nil {
			fmt.Printf("YCFRP 已在后台运行（进程 %d），无需重复启动。\n", pid)
			return 0
		}

		childPID, err := daemonpkg.Spawn(dir, chosenPort)
		if err != nil {
			fmt.Fprintf(os.Stderr, "进入后台运行失败：%v\n", err)
			return 1
		}
		// 等后台实例真正监听端口再返回：调用方（右键脚本）随后打开浏览器时
		// 面板必定已经可用，不必靠猜时间。
		wait := chosenPort
		if wait == 0 {
			wait = panelPort(target)
		}
		ready := waitReady(wait, childPID, 20*time.Second)
		fmt.Printf("YCFRP 已在后台启动。\n数据目录：%s\n日志文件位于该目录的 logs 子目录中。\n查看状态：ycfrp -status\n停止运行：ycfrp -stop\n",
			target)
		if ready {
			fmt.Printf("面板已就绪：http://127.0.0.1:%d\n", wait)
		} else {
			fmt.Printf("提示：等待面板监听 %d 端口超时，请稍后用 -status 查看。\n", wait)
		}
		return 0
	}

	stack, err := bootstrap.Start(dir, mode, chosenPort)
	if err != nil {
		fmt.Fprintf(os.Stderr, "启动失败：%v\n", err)
		return 1
	}

	if *logDir != "" {
		stack.App.Log.SetDir(*logDir)
	}

	// 记录进程号并清理上一轮可能遗留的停止请求，便于 -status / -stop 使用。
	daemonpkg.ClearStop(stack.App.DataDir)
	if err := daemonpkg.WritePID(stack.App.DataDir); err != nil {
		stack.App.Logf(logx.LevelWarn, "面板", "写入进程记录文件失败：%v", err)
	}
	defer daemonpkg.RemovePID(stack.App.DataDir)

	fmt.Print(stack.Banner())
	stack.App.Logf(logx.LevelInfo, "面板", "%s %s 已就绪", version.ProductName, version.Version)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 后台运行时监听停止请求，使 -stop 能触发优雅退出。
	daemonpkg.Watch(ctx, stack.App.DataDir, stop)

	if err := stack.Wait(ctx); err != nil {
		stack.App.Logf(logx.LevelError, "面板", "面板异常退出：%v", err)
		stack.Stop()
		return 1
	}

	stop()
	stack.Stop()
	return 0
}

// resolveDataDir 把空的数据目录参数解析为实际使用的目录，供状态查询与
// 停止操作复用，确保读写的是同一个位置。
func resolveDataDir(dir string) string {
	if strings.TrimSpace(dir) == "" {
		return app.DefaultDataDir()
	}
	return dir
}

// reportStatus 打印后台运行状态与面板地址。
func reportStatus(dir string) {
	target := resolveDataDir(dir)
	pid, err := daemonpkg.Running(target)
	if err != nil {
		fmt.Printf("YCFRP 未在后台运行。\n数据目录：%s\n", target)
		return
	}
	fmt.Printf("YCFRP 正在后台运行\n进程号：%d\n数据目录：%s\n", pid, target)
	for _, u := range bootstrap.LocalURLs(panelPort(target)) {
		fmt.Printf("面板地址：%s\n", u)
	}
}

// panelPort 读取数据目录内的面板端口，读取失败时使用默认端口。
func panelPort(dir string) int {
	cfg, err := config.Load(filepath.Join(dir, "config.json"))
	if err != nil {
		return 38080
	}
	return cfg.Port
}

// waitReady 轮询直到面板端口开始监听，或超过 timeout。
// 只要发现刚才派生的后台子进程已经退出，就立即返回失败（例如端口被占用），
// 不必空等到超时；端口与进程记录都就位后才算就绪，避免调用方紧接着执行
// -status 时误报「未在运行」。
func waitReady(port, childPID int, timeout time.Duration) bool {
	if port <= 0 {
		port = 38080
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	start := time.Now()
	deadline := start.Add(timeout)
	const graceAfter = 3 * time.Second
	listening := false
	for {
		if !listening {
			conn, err := net.DialTimeout("tcp", addr, time.Second)
			if err == nil {
				_ = conn.Close()
				listening = true
			}
		}
		if listening {
			return true
		} else if time.Since(start) > graceAfter {
			if childPID > 0 && !daemonpkg.Alive(childPID) {
				return false
			}
		}
		if !time.Now().Before(deadline) {
			return listening
		}
		time.Sleep(250 * time.Millisecond)
	}
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

// runningInDocker detects the container environment so the panel can label the
// running mode correctly.
func runningInDocker() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	data, err := os.ReadFile("/proc/1/cgroup")
	if err != nil {
		return false
	}
	text := string(data)
	return strings.Contains(text, "docker") || strings.Contains(text, "containerd")
}

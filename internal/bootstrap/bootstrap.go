// Package bootstrap assembles the application together with the HTTP panel so
// that the CLI entry point and the Windows desktop shell share one code path.
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ycfrp/ycfrp/internal/api"
	"github.com/ycfrp/ycfrp/internal/app"
	"github.com/ycfrp/ycfrp/internal/config"
	"github.com/ycfrp/ycfrp/internal/logx"
	"github.com/ycfrp/ycfrp/internal/version"
)

// Stack is a fully started YCFRP instance.
type Stack struct {
	App *app.App
	API *api.Server

	// FirstRun reports whether the configuration file had to be created.
	FirstRun bool
	// GeneratedPassword holds the bootstrap password on first run.
	GeneratedPassword string

	errCh chan error
}

// Start boots the kernels, the monitor and the HTTP panel.
func Start(dataDir, mode string, portOverride int) (*Stack, error) {
	if strings.TrimSpace(dataDir) == "" {
		dataDir = app.DefaultDataDir()
	}
	cfgPath := filepath.Join(dataDir, "config.json")
	_, statErr := os.Stat(cfgPath)
	firstRun := os.IsNotExist(statErr)

	a, err := app.New(dataDir, mode)
	if err != nil {
		return nil, err
	}

	if portOverride > 0 && portOverride != a.Cfg.Port {
		if err := a.UpdateConfig(func(c *config.Config) { c.Port = portOverride }); err != nil {
			return nil, fmt.Errorf("应用端口设置失败: %w", err)
		}
	}

	if err := a.Start(); err != nil {
		return nil, err
	}

	srv, err := api.New(a)
	if err != nil {
		a.Shutdown()
		return nil, err
	}

	st := &Stack{App: a, API: srv, FirstRun: firstRun, errCh: make(chan error, 1)}
	if firstRun {
		st.GeneratedPassword = a.Cfg.Password
	}
	go func() {
		st.errCh <- srv.ListenAndServe()
	}()

	// Give the listener a moment so callers can read a usable address.
	time.Sleep(150 * time.Millisecond)
	return st, nil
}

// PanelURL returns the loopback address of the management panel.
func (s *Stack) PanelURL() string {
	return fmt.Sprintf("http://%s", net.JoinHostPort("127.0.0.1", fmt.Sprint(s.App.Cfg.Port)))
}

// Wait blocks until the panel stops or the context is cancelled.
func (s *Stack) Wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return nil
	case err := <-s.errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}

// Stop shuts the panel and the kernels down in a bounded amount of time.
func (s *Stack) Stop() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if s.API != nil {
		_ = s.API.Shutdown(ctx)
	}
	if s.App != nil {
		s.App.Shutdown()
	}
}

// Banner renders the Chinese startup banner shown on the console.
func (s *Stack) Banner() string {
	cfg := s.App.Cfg.Snapshot()
	var b strings.Builder
	fmt.Fprintf(&b, "\n  %s %s\n", version.ProductName, version.Version)
	fmt.Fprintf(&b, "  内置 frp 内核：v%s\n", version.KernelVersion)
	fmt.Fprintf(&b, "  运行模式：%s\n", modeLabel(s.App.Mode))
	fmt.Fprintf(&b, "  数据目录：%s\n", s.App.DataDir)
	fmt.Fprintf(&b, "  访问地址：%s\n", s.PanelURL())
	for _, u := range LocalURLs(cfg.Port) {
		fmt.Fprintf(&b, "            %s\n", u)
	}
	if s.FirstRun {
		fmt.Fprintf(&b, "\n  首次启动，已生成默认账号：%s / %s\n", cfg.Username, s.GeneratedPassword)
		b.WriteString("  请登录后立即在“账号安全”中修改密码。\n")
	}
	// 以实例为准判断是否有内核在跑：实例才是内核的实际来源。
	if !s.App.HasEnabledInstances() {
		b.WriteString("\n  提示：尚未启用任何 frps / frpc 实例，请在面板「实例」页开启需要的角色。\n")
	}
	b.WriteString("\n")
	return b.String()
}

func modeLabel(mode string) string {
	switch mode {
	case "gui":
		return "Windows 桌面版"
	case "docker":
		return "Docker 容器"
	default:
		return "服务端（网页面板）"
	}
}

// LocalURLs lists the non-loopback addresses the panel can be reached at.
func LocalURLs(port int) []string {
	var out []string
	ifaces, err := net.Interfaces()
	if err != nil {
		return out
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipnet.IP.To4()
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			out = append(out, "http://"+net.JoinHostPort(ip.String(), fmt.Sprint(port)))
		}
	}
	return out
}

// ResetPassword restores the default credentials and reports the result.
func ResetPassword(dataDir string) (string, error) {
	if strings.TrimSpace(dataDir) == "" {
		dataDir = app.DefaultDataDir()
	}
	a, err := app.New(dataDir, "cli")
	if err != nil {
		return "", err
	}
	hashed, err := api.HashPassword(api.DefaultPassword)
	if err != nil {
		return "", fmt.Errorf("密码加密失败: %w", err)
	}
	if err := a.UpdateConfig(func(c *config.Config) {
		c.Username = api.DefaultUsername
		c.Password = hashed
	}); err != nil {
		return "", err
	}
	a.Log.Log(logx.LevelWarn, "面板", "登录账号已重置为默认值")
	return filepath.Join(dataDir, "config.json"), nil
}

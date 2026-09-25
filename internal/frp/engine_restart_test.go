package frp

import (
	"context"
	"testing"
	"time"

	"github.com/ycfrp/ycfrp/internal/config"
	"github.com/ycfrp/ycfrp/internal/logx"
)

// TestEngineServerRestartReleasesPorts guards the restart path used whenever the
// panel applies new frps settings: the kernel must be able to come back up on
// the very same ports without the old process still holding them.
func TestEngineServerRestartReleasesPorts(t *testing.T) {
	engine := New(logx.New(500))
	engine.Configure([]Instance{serverInstance("srv-1", "测试服务端", ServerConfigInput{
		BindAddr:       "127.0.0.1",
		BindPort:       17001,
		VhostHTTPPort:  18081,
		VhostHTTPSPort: 18444,
		DashboardAddr:  "127.0.0.1",
		DashboardPort:  17501,
		DashboardUser:  "admin",
		DashboardPwd:   "admin",
		Token:          "test-token",
		LogLevel:       "info",
	})}, nil)
	defer engine.Stop()

	for round := 1; round <= 3; round++ {
		if err := engine.Start(context.Background()); err != nil {
			t.Fatalf("第 %d 次启动服务端失败: %v", round, err)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// serverInstance 造一个服务端实例，把内核输入结构里的字段填进去。
func serverInstance(id, name string, in ServerConfigInput) Instance {
	return Instance{
		ID:   id,
		Name: name,
		Kind: KindServer,
		Server: config.FRPSConfig{
			Enable:            true,
			BindAddr:          in.BindAddr,
			BindPort:          in.BindPort,
			KCPBindPort:       in.KCPBindPort,
			QUICBindPort:      in.QUICBindPort,
			VhostHTTPPort:     in.VhostHTTPPort,
			VhostHTTPSPort:    in.VhostHTTPSPort,
			Token:             in.Token,
			SubdomainHost:     in.SubdomainHost,
			MaxPortsPerClient: in.MaxPortsPerClient,
			AllowPortsStart:   in.AllowPortsStart,
			AllowPortsEnd:     in.AllowPortsEnd,
			LogLevel:          in.LogLevel,
			LogMaxDays:        in.LogMaxDays,
			TransportTLS:      in.TransportTLS,
			DashboardPort:     in.DashboardPort,
			DashboardUser:     in.DashboardUser,
			DashboardPwd:      in.DashboardPwd,
		},
	}
}

// 多个服务端实例应能同时监听各自的端口；其中一个端口被占不该拖垮另一个。
func TestEngineRunsMultipleServers(t *testing.T) {
	engine := New(logx.New(500))
	engine.Configure([]Instance{
		serverInstance("srv-a", "服务端甲", ServerConfigInput{
			BindAddr: "127.0.0.1", BindPort: 17101,
			DashboardAddr: "127.0.0.1", DashboardPort: 17511,
			DashboardUser: "admin", DashboardPwd: "admin",
			Token: "t", LogLevel: "info",
		}),
		serverInstance("srv-b", "服务端乙", ServerConfigInput{
			BindAddr: "127.0.0.1", BindPort: 17102,
			DashboardAddr: "127.0.0.1", DashboardPort: 17512,
			DashboardUser: "admin", DashboardPwd: "admin",
			Token: "t", LogLevel: "info",
		}),
	}, nil)
	defer engine.Stop()

	if err := engine.Start(context.Background()); err != nil {
		t.Fatalf("启动两个服务端实例失败: %v", err)
	}
	time.Sleep(400 * time.Millisecond)

	statuses := engine.InstanceStatuses()
	if len(statuses) != 2 {
		t.Fatalf("应有 2 个实例，实际 %d", len(statuses))
	}
	for _, st := range statuses {
		if !st.Running {
			t.Errorf("实例 %s 应处于运行中，实际: %+v", st.Name, st)
		}
	}
}

// 一个实例启用、另一个停用时，只有启用的那个会被拉起。
func TestEngineSkipsDisabledInstance(t *testing.T) {
	engine := New(logx.New(500))
	disabled := serverInstance("srv-off", "停用的服务端", ServerConfigInput{
		BindAddr: "127.0.0.1", BindPort: 17201, LogLevel: "info", Token: "t",
	})
	disabled.Server.Enable = false

	engine.Configure([]Instance{
		serverInstance("srv-on", "启用的服务端", ServerConfigInput{
			BindAddr: "127.0.0.1", BindPort: 17202, LogLevel: "info", Token: "t",
		}),
		disabled,
	}, nil)
	defer engine.Stop()

	if err := engine.Start(context.Background()); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	for _, st := range engine.InstanceStatuses() {
		if st.ID == "srv-off" && st.Running {
			t.Error("停用的实例不应被启动")
		}
		if st.ID == "srv-on" && !st.Running {
			t.Error("启用的实例应处于运行中")
		}
	}
}

// tunnelsFor 只返回归属该实例的隧道，未归属的（InstanceID 为空）不投给任何实例。
func TestTunnelsForFiltersByInstance(t *testing.T) {
	all := []Tunnel{
		{ID: "t1", Name: "a", InstanceID: "inst-a"},
		{ID: "t2", Name: "b", InstanceID: "inst-b"},
		{ID: "t3", Name: "c", InstanceID: "inst-a"},
		{ID: "t4", Name: "orphan"},
	}
	got := tunnelsFor(all, "inst-a")
	if len(got) != 2 {
		t.Fatalf("inst-a 应有 2 条隧道，实际 %d", len(got))
	}
	for _, tn := range got {
		if tn.InstanceID != "inst-a" {
			t.Errorf("混入了非本实例的隧道: %+v", tn)
		}
	}
	if n := len(tunnelsFor(all, "")); n != 0 {
		t.Errorf("空实例标识不应匹配任何隧道，实际 %d 条", n)
	}
}

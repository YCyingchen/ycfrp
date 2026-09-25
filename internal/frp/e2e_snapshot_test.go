package frp

import (
	"context"
	"testing"
	"time"

	"github.com/ycfrp/ycfrp/internal/config"
	"github.com/ycfrp/ycfrp/internal/logx"
)

// 起一个真实的服务端实例 + 客户端实例 + 一条隧道，检查快照里的隧道状态。
//
// 这是「多实例下隧道状态」的关键回归测试：服务端与客户端是两个独立内核，
// 隧道的在线状态必须由服务端仪表盘回填。此前出现过仪表盘明明报 online、
// 面板却显示 offline 的情况，用这个测试把它钉死。
func TestSnapshotReportsTunnelOnline(t *testing.T) {
	engine := New(logx.New(1000))

	srv := Instance{
		ID: "srv-t", Name: "测试服务端", Kind: KindServer,
		Server: config.FRPSConfig{
			Enable: true, BindAddr: "127.0.0.1", BindPort: 17601,
			DashboardPort: 17901, DashboardUser: "admin", DashboardPwd: "admin",
			Token: "test-token", LogLevel: "info",
		},
	}
	cli := Instance{
		ID: "cli-t", Name: "测试客户端", Kind: KindClient,
		Client: config.FRPCConfig{
			Enable: true, ServerAddr: "127.0.0.1", ServerPort: 17601,
			Token: "test-token", LogLevel: "info",
		},
	}
	tunnel := Tunnel{
		ID: "tn-e2e", Name: "端到端隧道", Type: "tcp", Enabled: true,
		InstanceID: "cli-t", LocalIP: "127.0.0.1", LocalPort: 17610, RemotePort: 17620,
	}

	engine.Configure([]Instance{srv, cli}, []Tunnel{tunnel})
	if err := engine.Start(context.Background()); err != nil {
		t.Fatalf("启动实例失败: %v", err)
	}
	defer engine.Stop()

	// 等客户端登录、代理注册、仪表盘可读。
	var last string
	for i := 0; i < 20; i++ {
		time.Sleep(500 * time.Millisecond)
		snap := engine.Snapshot()
		for _, tn := range snap.Tunnels {
			if tn.ID != tunnel.ID {
				continue
			}
			last = tn.Status
			if tn.Status == "online" {
				// 顺带确认流量字段确实来自仪表盘（而非恰好为零值）。
				if tn.RemoteAddr == "" {
					t.Errorf("在线隧道应有远程地址，实际为空")
				}
				return
			}
		}
	}
	// 失败时把仪表盘原始数据带出来，便于判断是抓取失败还是匹配失败。
	rt := &runtime{dash: dashFor(srv)}
	if stats, err := engine.fetchProxyStats(rt); err != nil {
		t.Fatalf("隧道最终状态为 %q，且读取仪表盘失败: %v", last, err)
	} else {
		t.Fatalf("隧道最终状态为 %q，但仪表盘返回 %d 条代理: %+v", last, len(stats), stats)
	}
}

// 两个服务端实例各自独立：客户端只连其中一个，另一个不该看到这条隧道。
func TestSnapshotKeepsServersIsolated(t *testing.T) {
	engine := New(logx.New(1000))

	srvA := Instance{
		ID: "srv-a", Name: "服务端甲", Kind: KindServer,
		Server: config.FRPSConfig{
			Enable: true, BindAddr: "127.0.0.1", BindPort: 17631,
			DashboardPort: 17931, DashboardUser: "admin", DashboardPwd: "admin",
			Token: "tok-a", LogLevel: "info",
		},
	}
	srvB := Instance{
		ID: "srv-b", Name: "服务端乙", Kind: KindServer,
		Server: config.FRPSConfig{
			Enable: true, BindAddr: "127.0.0.1", BindPort: 17632,
			DashboardPort: 17932, DashboardUser: "admin", DashboardPwd: "admin",
			Token: "tok-b", LogLevel: "info",
		},
	}
	// 客户端连到甲。
	cli := Instance{
		ID: "cli-a", Name: "客户端甲", Kind: KindClient,
		Client: config.FRPCConfig{
			Enable: true, ServerAddr: "127.0.0.1", ServerPort: 17631,
			Token: "tok-a", LogLevel: "info",
		},
	}

	engine.Configure([]Instance{srvA, srvB, cli}, []Tunnel{{
		ID: "tn-a", Name: "甲的隧道", Type: "tcp", Enabled: true,
		InstanceID: "cli-a", LocalIP: "127.0.0.1", LocalPort: 17640, RemotePort: 17650,
	}})
	if err := engine.Start(context.Background()); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	defer engine.Stop()

	time.Sleep(2 * time.Second)

	// 甲乙都在跑。
	running := map[string]bool{}
	for _, st := range engine.InstanceStatuses() {
		running[st.ID] = st.Running
	}
	if !running["srv-a"] || !running["srv-b"] {
		t.Fatalf("两个服务端都应运行中，实际: %+v", running)
	}

	// 乙的仪表盘上不该有甲的隧道：连入客户端数应为 0。
	for _, st := range engine.InstanceStatuses() {
		if st.ID == "srv-b" {
			if len(st.Clients) != 0 {
				t.Errorf("服务端乙不该有接入客户端，实际 %d 个", len(st.Clients))
			}
		}
		if st.ID == "srv-a" && len(st.Clients) == 0 {
			t.Error("服务端甲应有 1 个接入客户端")
		}
	}

	// 关键：存在两个服务端时，甲上的隧道状态仍要正确回填为在线。
	// 这里曾出现「仪表盘报 online、面板显示 offline」的真实缺陷。
	var last string
	for i := 0; i < 20; i++ {
		time.Sleep(500 * time.Millisecond)
		for _, tn := range engine.Snapshot().Tunnels {
			if tn.ID != "tn-a" {
				continue
			}
			last = tn.Status
			if tn.Status == "online" {
				return
			}
		}
	}
	rt := &runtime{dash: dashFor(srvA)}
	stats, err := engine.fetchProxyStats(rt)
	if err != nil {
		t.Fatalf("双服务端场景下隧道状态为 %q；读取甲仪表盘失败: %v", last, err)
	}
	t.Fatalf("双服务端场景下隧道状态为 %q；甲仪表盘返回 %d 条代理: %+v", last, len(stats), stats)
}

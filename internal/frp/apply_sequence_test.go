package frp

import (
	"context"
	"testing"
	"time"

	"github.com/ycfrp/ycfrp/internal/config"
	"github.com/ycfrp/ycfrp/internal/logx"
)

// 复现面板的真实操作顺序：逐个创建实例（每次都会收敛一次内核），
// 最后才通过 ApplyTunnels 热更新加上隧道。
//
// 这条顺序与「一次性配置好再启动」不同，正是线上出现「隧道明明启动成功、
// 面板却显示离线」的场景。
func TestIncrementalApplyThenAddTunnel(t *testing.T) {
	engine := New(logx.New(1000))

	srv := Instance{
		ID: "srv-inc", Name: "增量服务端", Kind: KindServer,
		Server: config.FRPSConfig{
			Enable: true, BindAddr: "127.0.0.1", BindPort: 17701,
			DashboardPort: 17951, DashboardUser: "admin", DashboardPwd: "pw",
			Token: "tok", LogLevel: "info",
		},
	}
	cli := Instance{
		ID: "cli-inc", Name: "增量客户端", Kind: KindClient,
		Client: config.FRPCConfig{
			Enable: true, ServerAddr: "127.0.0.1", ServerPort: 17701,
			Token: "tok", LogLevel: "info",
		},
	}

	// 第一步：只配置服务端并启动（模拟「新建服务端实例」）。
	engine.Configure([]Instance{srv}, nil)
	if err := engine.Apply(context.Background()); err != nil {
		t.Fatalf("启动服务端失败: %v", err)
	}
	defer engine.Stop()

	// 第二步：加上客户端，再次收敛（模拟「新建客户端实例」）。
	engine.Configure([]Instance{srv, cli}, nil)
	if err := engine.Apply(context.Background()); err != nil {
		t.Fatalf("启动客户端失败: %v", err)
	}
	time.Sleep(1 * time.Second)

	// 第三步：热更新加上隧道（模拟「新增隧道」）。
	tunnel := Tunnel{
		ID: "tn-inc", Name: "增量隧道", Type: "tcp", Enabled: true,
		InstanceID: "cli-inc", LocalIP: "127.0.0.1", LocalPort: 17710, RemotePort: 17720,
	}
	engine.Configure([]Instance{srv, cli}, []Tunnel{tunnel})
	if err := engine.ApplyTunnels([]Tunnel{tunnel}); err != nil {
		t.Fatalf("热更新隧道失败: %v", err)
	}

	var last string
	for i := 0; i < 24; i++ {
		time.Sleep(500 * time.Millisecond)
		for _, tn := range engine.Snapshot().Tunnels {
			if tn.ID != tunnel.ID {
				continue
			}
			last = tn.Status
			if tn.Status == "online" {
				return
			}
		}
	}

	// 失败时把仪表盘原始数据带出来，区分「抓取失败」与「匹配失败」。
	rt := &runtime{dash: dashFor(srv)}
	stats, err := engine.fetchProxyStats(rt)
	if err != nil {
		t.Fatalf("隧道最终状态为 %q；读取仪表盘失败: %v", last, err)
	}
	t.Fatalf("隧道最终状态为 %q；仪表盘返回 %d 条代理: %+v", last, len(stats), stats)
}

// 模拟「隧道热更新之后又收敛了一次实例」——这会让 Apply 用旧快照覆盖内核状态。
func TestApplyAfterTunnelUpdateKeepsStatus(t *testing.T) {
	engine := New(logx.New(1000))

	srv := Instance{
		ID: "srv-y", Name: "服务端", Kind: KindServer,
		Server: config.FRPSConfig{
			Enable: true, BindAddr: "127.0.0.1", BindPort: 17751,
			DashboardPort: 17961, DashboardUser: "admin", DashboardPwd: "pw",
			Token: "tok", LogLevel: "info",
		},
	}
	cli := Instance{
		ID: "cli-y", Name: "客户端", Kind: KindClient,
		Client: config.FRPCConfig{
			Enable: true, ServerAddr: "127.0.0.1", ServerPort: 17751,
			Token: "tok", LogLevel: "info",
		},
	}
	tunnel := Tunnel{
		ID: "tn-y", Name: "隧道Y", Type: "tcp", Enabled: true,
		InstanceID: "cli-y", LocalIP: "127.0.0.1", LocalPort: 17760, RemotePort: 17770,
	}

	engine.Configure([]Instance{srv, cli}, []Tunnel{tunnel})
	if err := engine.Apply(context.Background()); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	defer engine.Stop()
	time.Sleep(2 * time.Second)

	// 再收敛一次（模拟改了通知设置等会触发 applySettings 的操作）。
	engine.Configure([]Instance{srv, cli}, []Tunnel{tunnel})
	if err := engine.Apply(context.Background()); err != nil {
		t.Fatalf("二次收敛失败: %v", err)
	}
	time.Sleep(2 * time.Second)

	for _, tn := range engine.Snapshot().Tunnels {
		if tn.ID == tunnel.ID && tn.Status != "online" {
			t.Fatalf("二次收敛后隧道应仍在线，实际 %q", tn.Status)
		}
	}
}

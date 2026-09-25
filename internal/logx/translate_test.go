package logx

import (
	"strings"
	"testing"
)

func TestCleanKernelMessage(t *testing.T) {
	cases := []struct {
		line string
		want string
	}{
		{
			"2026-09-23 12:00:00.000 [I] [client/service.go:123] try to connect to server...",
			"正在连接服务端...",
		},
		{
			"2026-09-23 12:00:01.000 [W] [client/service.go:130] connect to server error: dial tcp 1.2.3.4:7000: i/o timeout",
			"连接服务端失败: dial tcp 1.2.3.4:7000: i/o timeout",
		},
		{
			"2026-09-23 12:00:02.000 [I] [client/control.go:99] [web-1] start proxy success",
			"[web-1] 隧道启动成功",
		},
		{
			"2026-09-23 12:00:03.000 [E] [client/proxy/proxy.go:200] connect to local service [127.0.0.1:8080] error: connection refused",
			"连接本地服务 [127.0.0.1:8080] 失败: connection refused",
		},
		{
			"2026-09-23 12:00:04.000 [I] [server/control.go:300] new proxy [web] type [tcp] success",
			"新增隧道 [web]（类型 tcp）成功",
		},
		{
			"2026-09-23 12:00:05.000 [I] [something/else.go:1] some totally custom english line",
			"some totally custom english line",
		},
	}
	for _, c := range cases {
		if got := cleanKernelMessage(c.line); got != c.want {
			t.Errorf("cleanKernelMessage(%q)\n got: %q\nwant: %q", c.line, got, c.want)
		}
	}
}

// token 不一致是历史高频报错，必须整条翻成中文（含错误原因），
// 而不是只翻译前缀、把英文错误串原样丢给用户。
func TestTranslateTokenMismatch(t *testing.T) {
	cases := []struct{ line, wantPart string }{
		{
			"2026-09-23 12:00:00.000 [E] [server/control.go:99] register control error: token in login doesn't match token from configuration",
			"登录令牌与服务端配置不一致",
		},
		{
			"2026-09-23 12:00:00.000 [E] [client/service.go:99] connect to server error: token in login doesn't match token from configuration",
			"登录令牌与服务端配置不一致",
		},
		{
			"2026-09-23 12:00:00.000 [W] [server/service.go:99] listener for incoming connections from client closed",
			"客户端连接监听已关闭",
		},
		{
			"2026-09-23 12:00:00.000 [W] [server/service.go:99] dashboard server exit with error: http: Server closed",
			"仪表盘服务退出",
		},
		{
			"2026-09-23 12:00:00.000 [E] [client/service.go:99] login to the server failed: <nil>. With loginFailExit enabled, no additional retries will be attempted",
			"登录失败即退出",
		},
		{
			// 用户 QQ 告警里收到的真实报错（截图）：HTTP 虚拟主机找不到路由
			"2026-09-24 09:48:43.469 [W] [httputil/reverseproxy.go:556] do http proxy request [host: 203.0.113.5:8080] error: no route found: 203.0.113.5 /",
			"找不到对应的路由",
		},
	}
	for _, c := range cases {
		got := cleanKernelMessage(c.line)
		if !strings.Contains(got, c.wantPart) {
			t.Errorf("cleanKernelMessage(%q)\n got: %q\n 缺少: %q", c.line, got, c.wantPart)
		}
		if strings.Contains(got, "token in login doesn't match") {
			t.Errorf("token 错误串未被翻译，仍有英文残留: %q", got)
		}
	}
}

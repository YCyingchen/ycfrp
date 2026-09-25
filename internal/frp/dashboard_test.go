package frp

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ycfrp/ycfrp/internal/logx"
)

// 仪表盘上报的代理要通过元数据里的 ycfrpID 匹配回面板隧道。
// 这里用真实响应体结构复现：匹配成功则状态应变成 online。
func TestMergeStatsMatchesByMetadataID(t *testing.T) {
	tunnels := []Tunnel{{
		ID: "tnd011b46a195e183a", Name: "穿透测试", Type: "tcp",
		Enabled: true, InstanceID: "inst-x",
	}}
	stats := map[string]ProxyStatus{
		"穿透测试": {
			Name: "穿透测试", Status: "online",
			Conf: ProxyConf{
				Name:      "穿透测试",
				Type:      "tcp",
				Metadatas: map[string]string{"ycfrpID": "tnd011b46a195e183a"},
			},
			CurConns: 3,
		},
	}
	got := mergeStats(tunnels, stats)
	if len(got) != 1 {
		t.Fatalf("应返回 1 条隧道，实际 %d", len(got))
	}
	if got[0].Status != "online" {
		t.Errorf("状态应合并为 online，实际 %q", got[0].Status)
	}
	if got[0].CurConns != 3 {
		t.Errorf("连接数应为 3，实际 %d", got[0].CurConns)
	}
}

// 仪表盘上未被本地隧道认领的代理会作为只读行补进来。
func TestMergeStatsAdoptsUnknownProxies(t *testing.T) {
	local := []Tunnel{{ID: "tn-local", Name: "本地隧道", Type: "tcp", Enabled: true}}
	stats := map[string]ProxyStatus{
		"别人的代理": {
			Name: "别人的代理", Status: "online",
			Conf: ProxyConf{Type: "tcp"},
		},
	}
	got := mergeStats(local, stats)
	if len(got) != 2 {
		t.Fatalf("应补进 1 条只读代理，共 2 条，实际 %d", len(got))
	}
	var adopted *Tunnel
	for i := range got {
		if got[i].External {
			adopted = &got[i]
		}
	}
	if adopted == nil {
		t.Fatal("应有一条被标记为只读（external）")
	}
	if adopted.Name != "别人的代理" || adopted.Status != "online" {
		t.Errorf("只读代理字段有误: %+v", adopted)
	}
}

// fetchProxyStats 必须能从仪表盘响应里解析出代理，并带上元数据。
// 这里用与真实仪表盘一致的 JSON 结构。
func TestFetchProxyStatsParsesDashboard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/proxy/tcp":
			// 与真实仪表盘返回结构一致
			_, _ = w.Write([]byte(`{"proxies":[{"name":"穿透测试","status":"online",
				"curConns":2,"todayTrafficIn":100,"todayTrafficOut":200,
				"conf":{"name":"穿透测试","type":"tcp","remotePort":17520,
				"metadatas":{"ycfrpID":"tnd011b46a195e183a"}}}]}`))
		case strings.HasPrefix(r.URL.Path, "/api/proxy/"):
			_, _ = w.Write([]byte(`{"proxies":[]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	rt := &runtime{dash: dashAuth{addr: u.Host}}
	e := New(logx.New(100))

	stats, err := e.fetchProxyStats(rt)
	if err != nil {
		t.Fatalf("读取仪表盘代理失败: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("应解析出 1 条代理，实际 %d 条: %+v", len(stats), stats)
	}
	p, ok := stats["穿透测试"]
	if !ok {
		t.Fatalf("未按名称索引到代理，实际键: %v", keysOf(stats))
	}
	if p.Status != "online" {
		t.Errorf("状态应为 online，实际 %q", p.Status)
	}
	if p.Conf.Metadatas["ycfrpID"] != "tnd011b46a195e183a" {
		t.Errorf("元数据 ycfrpID 丢失: %+v", p.Conf.Metadatas)
	}
	if p.CurConns != 2 || p.TodayTrafficIn != 100 || p.TodayTrafficOut != 200 {
		t.Errorf("流量/连接数解析有误: %+v", p)
	}
}

func keysOf(m map[string]ProxyStatus) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// 仪表盘关闭时应返回可识别的哨兵错误，便于调用方区分「没开仪表盘」与「读失败」。
func TestFetchProxyStatsDashboardDisabled(t *testing.T) {
	rt := &runtime{dash: dashAuth{addr: "127.0.0.1:0"}}
	e := New(logx.New(100))
	_, err := e.fetchProxyStats(rt)
	if err == nil || !strings.Contains(err.Error(), "仪表盘未启用") {
		t.Errorf("仪表盘端口为 0 时应报未启用，实际: %v", err)
	}
}

// frp 服务端会为已断开的代理保留一条离线记录。多个服务端并存时，
// 同一个名字可能一台报在线、另一台报离线（残留），合并必须保住在线那条。
//
// 这是真实缺陷的回归测试：曾出现服务端甲明明报 online，面板却因为
// 服务端乙的残留记录而显示「隧道离线」。
func TestPreferProxyKeepsOnlineOverStaleOffline(t *testing.T) {
	online := ProxyStatus{
		Name: "甲的隧道", Status: "online",
		Conf: ProxyConf{Metadatas: map[string]string{"ycfrpID": "tn-a"}},
	}
	stale := ProxyStatus{
		Name: "甲的隧道", Status: "offline",
		Conf: ProxyConf{Metadatas: map[string]string{}},
	}
	if !preferProxy(online, stale) {
		t.Error("在线记录应胜过残留离线记录")
	}
	if preferProxy(stale, online) {
		t.Error("残留离线记录不该覆盖在线记录")
	}

	// 两条都在线时，带 ycfrpID 的（本面板真正对应的那条）优先。
	withID := ProxyStatus{Status: "online",
		Conf: ProxyConf{Metadatas: map[string]string{"ycfrpID": "tn"}}}
	withoutID := ProxyStatus{Status: "online", Conf: ProxyConf{}}
	if !preferProxy(withID, withoutID) {
		t.Error("带 ycfrpID 的记录应优先")
	}
	if preferProxy(withoutID, withID) {
		t.Error("无元数据的记录不该覆盖带元数据的记录")
	}

	// 完全等价时保持先到的，保证结果稳定。
	a := ProxyStatus{Status: "online", Conf: ProxyConf{}}
	if preferProxy(a, a) {
		t.Error("等价记录不应替换，避免结果抖动")
	}
}

// 合并入口层面的行为：残留离线记录不得把在线状态冲掉。
func TestMergeStatsIgnoresStaleOfflineDuplicate(t *testing.T) {
	// 这里模拟合并后的结果表：只有一条，且必须是在线那条。
	stats := map[string]ProxyStatus{}
	online := ProxyStatus{Status: "online",
		Conf: ProxyConf{Metadatas: map[string]string{"ycfrpID": "tn-a"}}}
	stale := ProxyStatus{Status: "offline", Conf: ProxyConf{}}
	// 先来离线残留，再来在线：应保留在线。
	stats["甲的隧道"] = stale
	if preferProxy(online, stats["甲的隧道"]) {
		stats["甲的隧道"] = online
	}
	got := mergeStats([]Tunnel{{
		ID: "tn-a", Name: "甲的隧道", Type: "tcp", Enabled: true,
	}}, stats)
	if len(got) != 1 {
		t.Fatalf("应只有 1 条隧道，实际 %d", len(got))
	}
	if got[0].Status != "online" {
		t.Errorf("残留离线记录不该覆盖在线状态，实际 %q", got[0].Status)
	}
}

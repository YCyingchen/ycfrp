package frp

import (
	"errors"
	"strings"
	"testing"

	"github.com/ycfrp/ycfrp/internal/config"
)

func TestInstanceValidateServer(t *testing.T) {
	ok := Instance{
		Name: "主服务端", Kind: KindServer,
		Server: config.FRPSConfig{BindPort: 7000, DashboardPort: 7500},
	}
	if err := ok.Validate(); err != nil {
		t.Errorf("合法服务端配置不该报错: %v", err)
	}

	// 监听端口必填且在合法区间内
	for _, tc := range []struct {
		name string
		port int
		want string
	}{
		{"端口为零", 0, "监听端口"},
		{"端口越界", 70000, "监听端口"},
	} {
		bad := Instance{Name: "x", Kind: KindServer,
			Server: config.FRPSConfig{BindPort: tc.port}}
		err := bad.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s：应报「%s」，实际 %v", tc.name, tc.want, err)
		}
	}
}

// 仪表盘端口与监听端口撞在一起时，frp 内核只会报一句底层 bind 错误，
// 面板应提前挡下来。
func TestInstanceValidateRejectsPortCollision(t *testing.T) {
	inst := Instance{
		Name: "撞端口", Kind: KindServer,
		Server: config.FRPSConfig{BindPort: 7000, DashboardPort: 7000},
	}
	err := inst.Validate()
	if err == nil || !strings.Contains(err.Error(), "仪表盘端口不能与监听端口相同") {
		t.Errorf("应拦下端口冲突，实际: %v", err)
	}
}

func TestInstanceValidateClient(t *testing.T) {
	ok := Instance{
		Name: "公司内网", Kind: KindClient,
		Client: config.FRPCConfig{ServerAddr: "1.2.3.4", ServerPort: 7000},
	}
	if err := ok.Validate(); err != nil {
		t.Errorf("合法客户端配置不该报错: %v", err)
	}

	noAddr := Instance{Name: "x", Kind: KindClient,
		Client: config.FRPCConfig{ServerPort: 7000}}
	if err := noAddr.Validate(); err == nil || !strings.Contains(err.Error(), "服务端地址") {
		t.Errorf("缺服务端地址应报错，实际: %v", err)
	}

	noPort := Instance{Name: "x", Kind: KindClient,
		Client: config.FRPCConfig{ServerAddr: "1.2.3.4"}}
	if err := noPort.Validate(); err == nil || !strings.Contains(err.Error(), "服务端端口") {
		t.Errorf("缺服务端端口应报错，实际: %v", err)
	}
}

func TestInstanceKindAndEnable(t *testing.T) {
	srv := Instance{Kind: KindServer, Server: config.FRPSConfig{Enable: true}}
	if !srv.IsServer() || srv.KindLabel() != "服务端" || !srv.Enabled() {
		t.Errorf("服务端实例判定有误: %+v", srv)
	}
	cli := Instance{Kind: KindClient, Client: config.FRPCConfig{Enable: false}}
	if cli.IsServer() || cli.KindLabel() != "客户端" || cli.Enabled() {
		t.Errorf("客户端实例判定有误: %+v", cli)
	}
	// 开关要落在对应的一侧，不能互相串。
	cli.SetEnabled(true)
	if !cli.Client.Enable || cli.Server.Enable {
		t.Errorf("启用客户端不该改动服务端开关: %+v", cli)
	}
	srv.SetEnabled(false)
	if srv.Server.Enable || srv.Client.Enable {
		t.Errorf("停用服务端不该改动客户端开关: %+v", srv)
	}
}

// Ports 汇总的是实际会被绑定的端口，关闭的（0）不该出现。
func TestInstancePorts(t *testing.T) {
	srv := Instance{Kind: KindServer, Server: config.FRPSConfig{
		BindPort: 7000, DashboardPort: 7500,
		VhostHTTPPort: 8080, VhostHTTPSPort: 0,
		KCPBindPort: 0, QUICBindPort: 0,
	}}
	got := srv.Ports()
	want := map[int]bool{7000: true, 7500: true, 8080: true}
	if len(got) != len(want) {
		t.Fatalf("应汇总 3 个端口，实际 %v", got)
	}
	for _, p := range got {
		if !want[p] {
			t.Errorf("多出不该出现的端口 %d", p)
		}
	}

	// 客户端实例不绑定任何本地端口。
	if n := len((Instance{Kind: KindClient}).Ports()); n != 0 {
		t.Errorf("客户端实例不该报告端口，实际 %d 个", n)
	}
}

func TestInstanceDisplayName(t *testing.T) {
	inst := Instance{Name: "主服务端", Kind: KindServer}
	if got := inst.DisplayName(); got != "主服务端（服务端）" {
		t.Errorf("展示名应带类型：%s", got)
	}
	// 名称为空时回退到标识，避免日志里出现空白。
	anon := Instance{ID: "inst-1", Kind: KindClient}
	if got := anon.DisplayName(); got != "inst-1" {
		t.Errorf("无名实例应回退到标识：%s", got)
	}
}

func TestInstanceNormalize(t *testing.T) {
	inst := Instance{Name: "  主服务端  ", Kind: " SERVER ", Note: "  备注 "}
	inst.Normalize()
	if inst.Name != "主服务端" || inst.Kind != KindServer || inst.Note != "备注" {
		t.Errorf("规范化结果有误: %+v", inst)
	}
}

// 多实例下最容易撞端口，内核的原始报错很难看懂，应翻成可操作的中文。
func TestDescribeStartErrorTranslatesPortConflict(t *testing.T) {
	inst := Instance{Name: "第二服务端", Kind: KindServer}
	raw := "服务端初始化失败: listen tcp 127.0.0.1:7500: bind: " +
		"Only one usage of each socket address (protocol/network address/port) is normally permitted."
	got := inst.DescribeStartError(errors.New(raw))
	if !strings.Contains(got, "7500") {
		t.Errorf("应点出冲突的端口号：%s", got)
	}
	if !strings.Contains(got, "已被占用") || !strings.Contains(got, "换一个端口") {
		t.Errorf("应给出可操作建议：%s", got)
	}
	// 原始报错要保留，便于深入排查。
	if !strings.Contains(got, "bind:") {
		t.Errorf("应保留原始错误：%s", got)
	}
}

// 绑定地址不属于本机（云服务器公网 IP 是 NAT 映射的）与端口占用是两回事，
// 提示绝不能混为一谈 —— 曾因此误导用户白白换了一串端口。
func TestDescribeStartErrorTranslatesBadBindAddress(t *testing.T) {
	inst := Instance{Name: "飞牛NAS", Kind: KindServer}
	raw := "服务端初始化失败: create server listener error, " +
		"listen tcp 203.0.113.5:7000: bind: cannot assign requested address"
	got := inst.DescribeStartError(errors.New(raw))
	if strings.Contains(got, "端口") && strings.Contains(got, "已被占用") {
		t.Errorf("地址不可用不应被说成端口占用：%s", got)
	}
	if !strings.Contains(got, "203.0.113.5") || !strings.Contains(got, "不属于本机") {
		t.Errorf("应点出问题地址与原因：%s", got)
	}
	if !strings.Contains(got, "0.0.0.0") {
		t.Errorf("应建议改绑定地址为 0.0.0.0：%s", got)
	}
}

// 非端口类的失败原样返回，不要瞎猜。
func TestDescribeStartErrorPassthrough(t *testing.T) {
	inst := Instance{Kind: KindClient}
	got := inst.DescribeStartError(errors.New("隧道参数不合法: name is required"))
	if got != "隧道参数不合法: name is required" {
		t.Errorf("非端口错误应原样返回：%s", got)
	}
	if inst.DescribeStartError(nil) != "" {
		t.Error("nil 错误应返回空串")
	}
}

func TestPortFromBindError(t *testing.T) {
	cases := []struct {
		msg  string
		want int
	}{
		{"listen tcp 0.0.0.0:7000: bind: address already in use", 7000},
		{"listen tcp 127.0.0.1:7500: bind: Only one usage", 7500},
		{"listen udp [::]:8443: bind: permission denied", 8443},
		{"隧道参数不合法: name is required", 0},
		{"", 0},
	}
	for _, tc := range cases {
		if got := portFromBindError(tc.msg); got != tc.want {
			t.Errorf("portFromBindError(%q) = %d，期望 %d", tc.msg, got, tc.want)
		}
	}
}

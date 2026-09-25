package frp

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ycfrp/ycfrp/internal/config"
)

// 实例类型：面板把 frps 服务端与 frpc 客户端统一抽象为「实例」，
// 两者可各建多个，互不干扰。
const (
	KindServer = "server"
	KindClient = "client"
)

// Instance 是面板托管的一个具名 frps 服务端或 frpc 客户端。
//
// 一台面板可以同时跑多个实例：每个 frpc 实例拥有自己的隧道集合，
// 每个 frps 实例拥有自己的仪表盘与流量统计。这样就能用同一个面板同时
// 管理「本机 frps + 指向远端 frps 的 frpc」，或同时连多个服务端。
//
// Server / Client 直接复用面板配置结构，避免字段重复定义；
// 按 Kind 决定哪一侧生效，另一侧保持零值。
type Instance struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Note   string `json:"note"`
	Server config.FRPSConfig `json:"server"`
	Client config.FRPCConfig `json:"client"`

	// 运行态由引擎在快照时填充，不落盘。
	Running      bool   `json:"running"`
	Err          string `json:"err"`
	TunnelCount  int    `json:"tunnelCount"`
	// DashboardURL 仅服务端实例有值，用于「打开仪表盘」深链。
	DashboardURL string `json:"dashboardUrl"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// EntityID implements store.Entity.
func (i Instance) EntityID() string { return i.ID }

// EntityCreatedAt implements store.Entity.
func (i Instance) EntityCreatedAt() time.Time { return i.CreatedAt }

// SetEntityMeta implements store.Mutable.
func (i *Instance) SetEntityMeta(id string, created, updated time.Time) {
	i.ID = id
	if !created.IsZero() {
		i.CreatedAt = created
	}
	i.UpdatedAt = updated
}

// IsServer 报告该实例是否为服务端。
func (i Instance) IsServer() bool { return i.Kind == KindServer }

// KindLabel 给出实例类型的中文名，供日志与界面展示。
func (i Instance) KindLabel() string {
	if i.IsServer() {
		return "服务端"
	}
	return "客户端"
}

// Enabled 报告该实例是否已开启。开关按类型落在对应的一侧，
// 免去再维护一个易与两侧失配的独立字段。
func (i Instance) Enabled() bool {
	if i.IsServer() {
		return i.Server.Enable
	}
	return i.Client.Enable
}

// SetEnabled 按实例类型写入启用开关。
func (i *Instance) SetEnabled(v bool) {
	if i.IsServer() {
		i.Server.Enable = v
		return
	}
	i.Client.Enable = v
}

// DisplayName 返回「名称（类型）」，日志里用来区分同名不同类的实例。
func (i Instance) DisplayName() string {
	if strings.TrimSpace(i.Name) == "" {
		return i.ID
	}
	return fmt.Sprintf("%s（%s）", i.Name, i.KindLabel())
}

// Normalize 统一名称与类型的写法，供写入与校验前调用。
func (i *Instance) Normalize() {
	i.Name = strings.TrimSpace(i.Name)
	i.Note = strings.TrimSpace(i.Note)
	i.Kind = strings.ToLower(strings.TrimSpace(i.Kind))
}

// Validate 检查实例的必填项。端口范围与 frp 内核的要求保持一致。
func (i Instance) Validate() error {
	if strings.TrimSpace(i.Name) == "" {
		return fmt.Errorf("实例名称不能为空")
	}
	switch i.Kind {
	case KindServer:
		if i.Server.BindPort <= 0 || i.Server.BindPort > 65535 {
			return fmt.Errorf("服务端监听端口必须在 1 到 65535 之间")
		}
		if i.Server.DashboardPort < 0 || i.Server.DashboardPort > 65535 {
			return fmt.Errorf("仪表盘端口必须在 0 到 65535 之间（0 表示关闭）")
		}
		if i.Server.DashboardPort > 0 && i.Server.DashboardPort == i.Server.BindPort {
			return fmt.Errorf("仪表盘端口不能与监听端口相同")
		}
		if i.Server.VhostHTTPPort > 0 && i.Server.VhostHTTPPort == i.Server.BindPort {
			return fmt.Errorf("HTTP 虚拟主机端口不能与监听端口相同")
		}
		if i.Server.VhostHTTPSPort > 0 && i.Server.VhostHTTPSPort == i.Server.BindPort {
			return fmt.Errorf("HTTPS 虚拟主机端口不能与监听端口相同")
		}
	case KindClient:
		if strings.TrimSpace(i.Client.ServerAddr) == "" {
			return fmt.Errorf("客户端必须填写服务端地址")
		}
		if i.Client.ServerPort <= 0 || i.Client.ServerPort > 65535 {
			return fmt.Errorf("服务端端口必须在 1 到 65535 之间")
		}
	default:
		return fmt.Errorf("实例类型必须是 %s 或 %s", KindServer, KindClient)
	}
	return nil
}

// ServerInput 把实例的服务端配置转换成内核所需的输入结构。
func (i Instance) ServerInput() ServerConfigInput {
	s := i.Server
	return ServerConfigInput{
		BindAddr:          s.BindAddr,
		BindPort:          s.BindPort,
		KCPBindPort:       s.KCPBindPort,
		QUICBindPort:      s.QUICBindPort,
		VhostHTTPPort:     s.VhostHTTPPort,
		VhostHTTPSPort:    s.VhostHTTPSPort,
		Token:             s.Token,
		SubdomainHost:     s.SubdomainHost,
		MaxPortsPerClient: s.MaxPortsPerClient,
		AllowPortsStart:   s.AllowPortsStart,
		AllowPortsEnd:     s.AllowPortsEnd,
		LogLevel:          s.LogLevel,
		LogMaxDays:        s.LogMaxDays,
		TransportTLS:      s.TransportTLS,
		DashboardAddr:     "127.0.0.1",
		DashboardPort:     s.DashboardPort,
		DashboardUser:     s.DashboardUser,
		DashboardPwd:      s.DashboardPwd,
	}
}

// ClientInput 把实例的客户端配置转换成内核所需的输入结构。
func (i Instance) ClientInput() ClientCommonInput {
	c := i.Client
	return ClientCommonInput{
		ServerAddr:     c.ServerAddr,
		ServerPort:     c.ServerPort,
		Token:          c.Token,
		User:           c.User,
		LoginFailExit:  c.LoginFailExit,
		LogLevel:       c.LogLevel,
		LogMaxDays:     c.LogMaxDays,
		TransportTLS:   c.TransportTLS,
		TransportProto: c.TransportProto,
	}
}

// InstanceStatus 是单个实例在一次快照里的运行态汇总。
type InstanceStatus struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Enable  bool   `json:"enable"`
	Running bool   `json:"running"`
	Err     string `json:"err"`
	// 服务端实例的仪表盘概览。
	Server *ServerInfo `json:"server,omitempty"`
	// 服务端实例上连入的客户端。
	Clients []ClientStatus `json:"clients,omitempty"`
	// 隧道数量，便于界面在不展开的情况下展示概览。
	TunnelCount int `json:"tunnelCount"`
	// 该实例占用的端口列表，用于端口冲突自检提示。
	Ports []int `json:"ports"`
}

// Ports 汇总实例实际会绑定的端口，供界面做冲突检查。
func (i Instance) Ports() []int {
	var out []int
	if i.IsServer() {
		for _, p := range []int{i.Server.BindPort, i.Server.DashboardPort, i.Server.VhostHTTPPort, i.Server.VhostHTTPSPort, i.Server.KCPBindPort, i.Server.QUICBindPort} {
			if p > 0 {
				out = append(out, p)
			}
		}
	}
	return out
}

// DescribeStartError 把内核启动失败的原因翻成可操作的中文说明。
//
// 绑定失败必须区分两种完全不同的成因，不能一律说「端口被占用」：
//   - address already in use：端口真的被别的程序/实例占着 → 换端口；
//   - cannot assign requested address：填写的绑定地址不属于本机
//     （云服务器公网 IP 是 NAT 映射的，本机网卡上没有这个地址，
//     只能绑 0.0.0.0 或内网地址）→ 改绑定地址，换端口没有用。
func (i Instance) DescribeStartError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if strings.Contains(msg, "cannot assign requested address") {
		if addr := addrFromBindError(msg); addr != "" {
			return fmt.Sprintf("无法绑定到地址 %s：该地址不属于本机。"+
				"云服务器的公网 IP 通常是运营商 NAT 映射的，本机网卡上并不存在，"+
				"绑定地址应填 0.0.0.0（监听所有网卡）或内网地址。原始错误：%s",
				addr, msg)
		}
		return "绑定地址不属于本机，无法启动。云服务器的公网 IP 是 NAT 映射的，" +
			"绑定地址应填 0.0.0.0 或内网地址。原始错误：" + msg
	}
	if port := portFromBindError(msg); port > 0 {
		return fmt.Sprintf("端口 %d 已被占用，无法启动 %s。请确认该端口没有被其它实例或其它程序使用，"+
			"然后在实例设置里换一个端口。原始错误：%s", port, i.KindLabel(), msg)
	}
	return msg
}

// addrFromBindError 从绑定失败的报错里提取地址部分（含端口）。
func addrFromBindError(msg string) string {
	re := regexp.MustCompile(`listen (?:tcp|udp) ([^\s:]+:[0-9]+)`)
	if m := re.FindStringSubmatch(msg); len(m) >= 2 {
		return m[1]
	}
	return ""
}

// portFromBindError 从绑定失败的报错里提取端口号。
//
// 内核报错形如 "listen tcp 127.0.0.1:7500: bind: ..."，
// 注意端口与 bind 之间还有一个冒号，所以不能简单地取最后一个冒号。
// 这里在 bind 之前的部分里找第一个 ":<数字>"。
func portFromBindError(msg string) int {
	head := msg
	if idx := strings.Index(msg, "bind:"); idx >= 0 {
		head = msg[:idx]
	} else if !strings.Contains(msg, "listen tcp") && !strings.Contains(msg, "listen udp") {
		// 不是绑定失败，别硬猜端口。
		return 0
	}
	m := bindPortPattern.FindStringSubmatch(head)
	if len(m) < 2 {
		return 0
	}
	p, err := strconv.Atoi(m[1])
	if err != nil || p <= 0 || p > 65535 {
		return 0
	}
	return p
}

// bindPortPattern 匹配 ":<端口>"，用于从 listen 失败信息里取端口。
var bindPortPattern = regexp.MustCompile(`:(\d{1,5})`)

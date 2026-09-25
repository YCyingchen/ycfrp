package frp

import (
	"fmt"
	"strconv"
	"strings"

	v1 "github.com/fatedier/frp/pkg/config/v1"
	"github.com/fatedier/frp/pkg/config/v1/validation"
	"github.com/fatedier/frp/pkg/policy/security"
	"github.com/fatedier/frp/pkg/config/types"
	"github.com/fatedier/frp/pkg/util/util"
)

// configsource builds frp v1 configuration structures from panel settings.
// Keeping the translation in one place makes it obvious which panel fields map
// onto which kernel option.

// ServerConfigInput carries the panel level frps settings.
type ServerConfigInput struct {
	BindAddr          string
	BindPort          int
	KCPBindPort       int
	QUICBindPort      int
	VhostHTTPPort     int
	VhostHTTPSPort    int
	Token             string
	SubdomainHost     string
	MaxPortsPerClient int
	AllowPortsStart   int
	AllowPortsEnd     int
	LogLevel          string
	LogMaxDays        int
	TransportTLS      bool
	DashboardAddr     string
	DashboardPort     int
	DashboardUser     string
	DashboardPwd      string
}

// BuildServerConfig converts panel settings into a kernel server config.
func BuildServerConfig(in ServerConfigInput) (*v1.ServerConfig, error) {
	cfg := &v1.ServerConfig{}
	cfg.BindAddr = util.EmptyOr(in.BindAddr, "0.0.0.0")
	cfg.BindPort = in.BindPort
	cfg.KCPBindPort = in.KCPBindPort
	cfg.QUICBindPort = in.QUICBindPort
	cfg.VhostHTTPPort = in.VhostHTTPPort
	cfg.VhostHTTPSPort = in.VhostHTTPSPort
	cfg.SubDomainHost = in.SubdomainHost
	cfg.MaxPortsPerClient = int64(in.MaxPortsPerClient)
	cfg.Auth.Method = "token"
	cfg.Auth.Token = in.Token

	if in.AllowPortsStart > 0 && in.AllowPortsEnd >= in.AllowPortsStart {
		cfg.AllowPorts = []types.PortsRange{{
			Start: in.AllowPortsStart,
			End:   in.AllowPortsEnd,
		}}
	}

	cfg.Log.Level = util.EmptyOr(in.LogLevel, "info")
	if in.LogMaxDays > 0 {
		cfg.Log.MaxDays = int64(in.LogMaxDays)
	}
	cfg.Log.To = "console"
	cfg.Log.DisablePrintColor = true

	cfg.Transport.TLS.Force = in.TransportTLS

	if in.DashboardPort > 0 {
		cfg.WebServer.Addr = util.EmptyOr(in.DashboardAddr, "127.0.0.1")
		cfg.WebServer.Port = in.DashboardPort
		cfg.WebServer.User = util.EmptyOr(in.DashboardUser, "admin")
		cfg.WebServer.Password = util.EmptyOr(in.DashboardPwd, "admin")
	}

	if err := cfg.Complete(); err != nil {
		return nil, fmt.Errorf("服务端配置校验失败: %w", err)
	}
	unsafe := security.NewUnsafeFeatures(nil)
	validator := validation.NewConfigValidator(unsafe)
	if _, err := validator.ValidateServerConfig(cfg); err != nil {
		return nil, fmt.Errorf("服务端参数不合法: %w", err)
	}
	return cfg, nil
}

// ClientCommonInput carries the panel level frpc settings.
type ClientCommonInput struct {
	ServerAddr     string
	ServerPort     int
	Token          string
	User           string
	LoginFailExit  bool
	LogLevel       string
	LogMaxDays     int
	TransportTLS   bool
	TransportProto string
}

// BuildClientCommon converts panel settings into a kernel client common config.
func BuildClientCommon(in ClientCommonInput) *v1.ClientCommonConfig {
	cfg := &v1.ClientCommonConfig{}
	cfg.ServerAddr = util.EmptyOr(in.ServerAddr, "127.0.0.1")
	if in.ServerPort > 0 {
		cfg.ServerPort = in.ServerPort
	} else {
		cfg.ServerPort = 7000
	}
	cfg.User = in.User
	cfg.Auth.Method = "token"
	cfg.Auth.Token = in.Token
	loginFail := in.LoginFailExit
	cfg.LoginFailExit = &loginFail
	cfg.Log.Level = util.EmptyOr(in.LogLevel, "info")
	if in.LogMaxDays > 0 {
		cfg.Log.MaxDays = int64(in.LogMaxDays)
	}
	cfg.Log.To = "console"
	cfg.Log.DisablePrintColor = true
	cfg.Transport.Protocol = util.EmptyOr(in.TransportProto, "tcp")
	cfg.Transport.TLS.Enable = boolPtr(in.TransportTLS)
	_ = cfg.Complete()
	return cfg
}

func boolPtr(v bool) *bool { return &v }

// TunnelToProxy converts a stored tunnel entry into a kernel proxy config.
func TunnelToProxy(t Tunnel) (v1.ProxyConfigurer, error) {
	base := v1.ProxyBaseConfig{
		Name: t.Name,
		Type: t.Type,
	}
	enabled := t.Enabled
	base.Enabled = &enabled
	base.LocalIP = util.EmptyOr(t.LocalIP, "127.0.0.1")
	base.LocalPort = t.LocalPort
	if t.BandwidthLimit != "" {
		qty, err := parseBandwidth(t.BandwidthLimit)
		if err != nil {
			return nil, err
		}
		base.Transport.BandwidthLimit = qty
	}
	base.Transport.UseEncryption = t.UseEncryption
	base.Transport.UseCompression = t.UseCompression
	base.Transport.ProxyProtocolVersion = t.ProxyProtocolVersion
	base.Metadatas = map[string]string{}
	if t.Note != "" {
		base.Metadatas["note"] = t.Note
	}
	base.Metadatas["ycfrpID"] = t.ID

	// 负载均衡：同一 GroupKey 下名称相同的多条隧道由服务端轮询分发。
	// 这是 frp 的通用能力，对 tcp/udp/http/https/tcpmux 都适用。
	if strings.TrimSpace(t.LoadBalanceKey) != "" {
		base.LoadBalancer.GroupKey = strings.TrimSpace(t.LoadBalanceKey)
	}
	if strings.TrimSpace(t.LoadBalanceGroup) != "" {
		base.LoadBalancer.Group = strings.TrimSpace(t.LoadBalanceGroup)
	}

	if t.HealthCheckType != "" {
		base.HealthCheck.Type = t.HealthCheckType
		base.HealthCheck.Path = t.HealthCheckURL
		if t.HealthCheckInterval > 0 {
			base.HealthCheck.IntervalSeconds = t.HealthCheckInterval
		}
		if t.HealthCheckTimeout > 0 {
			base.HealthCheck.TimeoutSeconds = t.HealthCheckTimeout
		}
		if t.HealthCheckMaxFailed > 0 {
			base.HealthCheck.MaxFailed = t.HealthCheckMaxFailed
		}
	}

	switch strings.ToLower(t.Type) {
	case "tcp":
		cfg := &v1.TCPProxyConfig{ProxyBaseConfig: base}
		cfg.RemotePort = t.RemotePort
		return cfg, nil
	case "udp":
		cfg := &v1.UDPProxyConfig{ProxyBaseConfig: base}
		cfg.RemotePort = t.RemotePort
		return cfg, nil
	case "http":
		cfg := &v1.HTTPProxyConfig{ProxyBaseConfig: base}
		cfg.CustomDomains = t.CustomDomains
		cfg.SubDomain = t.Subdomain
		applyHTTPExtras(cfg, t)
		return cfg, nil
	case "https":
		cfg := &v1.HTTPSProxyConfig{ProxyBaseConfig: base}
		cfg.CustomDomains = t.CustomDomains
		cfg.SubDomain = t.Subdomain
		return cfg, nil
	case "tcpmux":
		cfg := &v1.TCPMuxProxyConfig{ProxyBaseConfig: base}
		cfg.CustomDomains = t.CustomDomains
		cfg.SubDomain = t.Subdomain
		cfg.Multiplexer = "httpconnect"
		return cfg, nil
	case "stcp":
		cfg := &v1.STCPProxyConfig{ProxyBaseConfig: base}
		cfg.Secretkey = t.SecretKey
		cfg.AllowUsers = splitList(t.Group)
		return cfg, nil
	case "xtcp":
		cfg := &v1.XTCPProxyConfig{ProxyBaseConfig: base}
		cfg.Secretkey = t.SecretKey
		cfg.AllowUsers = splitList(t.Group)
		return cfg, nil
	case "sudp":
		cfg := &v1.SUDPProxyConfig{ProxyBaseConfig: base}
		cfg.Secretkey = t.SecretKey
		cfg.AllowUsers = splitList(t.Group)
		return cfg, nil
	default:
		return nil, fmt.Errorf("不支持的隧道类型: %s", t.Type)
	}
}

// applyHTTPExtras 把 HTTP 类型隧道的高级选项写进内核配置：
// 路径路由、BasicAuth 鉴权、Host 头改写、按用户路由与请求/响应头改写。
func applyHTTPExtras(cfg *v1.HTTPProxyConfig, t Tunnel) {
	if len(t.Locations) > 0 {
		cfg.Locations = append([]string(nil), t.Locations...)
	}
	cfg.HTTPUser = strings.TrimSpace(t.HTTPUser)
	cfg.HTTPPassword = t.HTTPPassword
	cfg.HostHeaderRewrite = strings.TrimSpace(t.HostHeaderRewrite)
	cfg.RouteByHTTPUser = strings.TrimSpace(t.RouteByHTTPUser)
	if len(t.RequestHeaders) > 0 {
		cfg.RequestHeaders.Set = copyHeaders(t.RequestHeaders)
	}
	if len(t.ResponseHeaders) > 0 {
		cfg.ResponseHeaders.Set = copyHeaders(t.ResponseHeaders)
	}
}

func copyHeaders(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func splitList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == ';' })
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func parseBandwidth(s string) (types.BandwidthQuantity, error) {
	q, err := types.NewBandwidthQuantity(s)
	if err != nil {
		return q, fmt.Errorf("带宽限制格式不正确: %w", err)
	}
	return q, nil
}

// ParsePortRange is a helper for the UI when validating port input.
func ParsePortRange(s string) (int, int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, 0, nil
	}
	parts := strings.SplitN(s, "-", 2)
	start, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, fmt.Errorf("端口起始值不合法")
	}
	if len(parts) == 1 {
		return start, start, nil
	}
	end, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return 0, 0, fmt.Errorf("端口结束值不合法")
	}
	return start, end, nil
}

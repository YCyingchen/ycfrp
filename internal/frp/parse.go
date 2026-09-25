package frp

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	frpconfig "github.com/fatedier/frp/pkg/config"
	v1 "github.com/fatedier/frp/pkg/config/v1"

	"github.com/ycfrp/ycfrp/internal/version"
)

// ParseTunnelResult is returned when a pasted frpc configuration is converted
// into panel tunnel entries.
type ParseTunnelResult struct {
	Tunnels   []Tunnel `json:"tunnels"`
	Common    ParsedCommon `json:"common"`
	Format    string   `json:"format"`
	Warnings  []string `json:"warnings"`
}

// ParsedCommon carries the connection level fields discovered in a pasted
// frpc configuration so the panel can offer to adopt them.
type ParsedCommon struct {
	ServerAddr   string `json:"serverAddr"`
	ServerPort   int    `json:"serverPort"`
	Token        string `json:"token"`
	User         string `json:"user"`
	TLS          bool   `json:"tls"`
	Protocol     string `json:"protocol"`
	LogLevel     string `json:"logLevel"`
}

// ParseClientConfigText accepts a pasted frpc configuration in TOML, YAML,
// JSON or the legacy INI layout and converts every proxy into a tunnel entry.
// Visitor only entries are skipped with a warning because the panel manages
// proxies.
func ParseClientConfigText(content string) (*ParseTunnelResult, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, fmt.Errorf("配置内容为空")
	}

	dir, err := os.MkdirTemp("", "ycfrp-parse-*")
	if err != nil {
		return nil, fmt.Errorf("创建临时目录失败: %w", err)
	}
	defer os.RemoveAll(dir)

	ext := detectTextFormat(content)
	path := filepath.Join(dir, "frpc"+ext)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return nil, fmt.Errorf("写入临时配置失败: %w", err)
	}

	common, proxies, visitors, legacy, err := frpconfig.LoadClientConfig(path, false)
	if err != nil {
		return nil, fmt.Errorf("解析配置失败: %w", err)
	}

	res := &ParseTunnelResult{Format: formatLabel(legacy, ext)}
	if common != nil {
		res.Common = ParsedCommon{
			ServerAddr: common.ServerAddr,
			ServerPort: common.ServerPort,
			Token:      common.Auth.Token,
			User:       common.User,
			TLS:        common.Transport.TLS.Enable != nil && *common.Transport.TLS.Enable,
			Protocol:   common.Transport.Protocol,
			LogLevel:   common.Log.Level,
		}
	}

	seen := make(map[string]bool)
	for _, p := range proxies {
		t, err := proxyToTunnel(p)
		if err != nil {
			res.Warnings = append(res.Warnings, err.Error())
			continue
		}
		if seen[t.Name] {
			res.Warnings = append(res.Warnings, fmt.Sprintf("隧道名称 %s 重复，已跳过", t.Name))
			continue
		}
		seen[t.Name] = true
		res.Tunnels = append(res.Tunnels, t)
	}
	for _, v := range visitors {
		base := v.GetBaseConfig()
		res.Warnings = append(res.Warnings,
			fmt.Sprintf("访问者 %s（%s 类型）需要单独在 frpc 配置中维护，面板未导入", base.Name, base.Type))
	}
	if res.Tunnels == nil {
		res.Tunnels = []Tunnel{}
	}
	sort.SliceStable(res.Tunnels, func(i, j int) bool { return res.Tunnels[i].Name < res.Tunnels[j].Name })
	return res, nil
}

func detectTextFormat(content string) string {
	trimmed := strings.TrimLeft(content, " \t\r\n")
	switch {
	case strings.HasPrefix(trimmed, "{"):
		return ".json"
	case strings.HasPrefix(trimmed, "[common]"), strings.Contains(trimmed, "\n[common]"):
		return ".ini"
	case strings.HasPrefix(trimmed, "serverAddr"), strings.HasPrefix(trimmed, "#"), strings.Contains(trimmed, "\nserverAddr"):
		return ".toml"
	default:
		return ".yaml"
	}
}

func formatLabel(legacy bool, ext string) string {
	if legacy {
		return "INI（旧版格式）"
	}
	switch ext {
	case ".json":
		return "JSON"
	case ".toml":
		return "TOML"
	default:
		return "YAML"
	}
}

func proxyToTunnel(p v1.ProxyConfigurer) (Tunnel, error) {
	base := p.GetBaseConfig()
	t := Tunnel{
		Name:                 base.Name,
		Type:                 base.Type,
		Enabled:              base.Enabled == nil || *base.Enabled,
		LocalIP:              base.LocalIP,
		LocalPort:            base.LocalPort,
		UseEncryption:        base.Transport.UseEncryption,
		UseCompression:       base.Transport.UseCompression,
		ProxyProtocolVersion: base.Transport.ProxyProtocolVersion,
		Group:                base.LoadBalancer.Group,
		GroupKey:             base.LoadBalancer.GroupKey,
	}
	if base.Transport.BandwidthLimit.String() != "0" {
		t.BandwidthLimit = base.Transport.BandwidthLimit.String()
	}
	if base.HealthCheck.Type != "" {
		t.HealthCheckType = base.HealthCheck.Type
		t.HealthCheckURL = base.HealthCheck.Path
		t.HealthCheckInterval = base.HealthCheck.IntervalSeconds
		t.HealthCheckTimeout = base.HealthCheck.TimeoutSeconds
		t.HealthCheckMaxFailed = base.HealthCheck.MaxFailed
	}
	if base.Metadatas != nil {
		t.Note = base.Metadatas["note"]
	}

	switch cfg := p.(type) {
	case *v1.TCPProxyConfig:
		t.Type = "tcp"
		t.RemotePort = cfg.RemotePort
	case *v1.UDPProxyConfig:
		t.Type = "udp"
		t.RemotePort = cfg.RemotePort
	case *v1.HTTPProxyConfig:
		t.Type = "http"
		t.CustomDomains = cfg.CustomDomains
		t.Subdomain = cfg.SubDomain
	case *v1.HTTPSProxyConfig:
		t.Type = "https"
		t.CustomDomains = cfg.CustomDomains
		t.Subdomain = cfg.SubDomain
	case *v1.TCPMuxProxyConfig:
		t.Type = "tcpmux"
		t.CustomDomains = cfg.CustomDomains
		t.Subdomain = cfg.SubDomain
	case *v1.STCPProxyConfig:
		t.Type = "stcp"
		t.SecretKey = cfg.Secretkey
		t.Group = strings.Join(cfg.AllowUsers, ",")
	case *v1.XTCPProxyConfig:
		t.Type = "xtcp"
		t.SecretKey = cfg.Secretkey
		t.Group = strings.Join(cfg.AllowUsers, ",")
	case *v1.SUDPProxyConfig:
		t.Type = "sudp"
		t.SecretKey = cfg.Secretkey
		t.Group = strings.Join(cfg.AllowUsers, ",")
	default:
		return t, fmt.Errorf("暂不支持的隧道类型 %s（%s）", base.Type, base.Name)
	}
	return t, nil
}

// ---------------------------------------------------------------------------
// Export: panel tunnels -> frpc configuration
// ---------------------------------------------------------------------------

// ExportOptions controls how the downloadable frpc configuration is rendered.
type ExportOptions struct {
	ServerAddr string
	ServerPort int
	Token      string
	User       string
	TLS        bool
	Protocol   string
	LogLevel   string
	// Format is "toml" or "json".
	Format string
}

// ExportClientConfig renders the supplied tunnels as a frpc configuration the
// user can drop next to an official frpc binary.
func ExportClientConfig(tunnels []Tunnel, opt ExportOptions) string {
	if strings.EqualFold(opt.Format, "json") {
		return exportJSON(tunnels, opt)
	}
	return exportTOML(tunnels, opt)
}

func exportTOML(tunnels []Tunnel, opt ExportOptions) string {
	var sb strings.Builder
	sb.WriteString("# YCFRP 导出的 frpc 配置\n")
	sb.WriteString("# 版本：" + versionBanner + "\n")
	sb.WriteString("# 将该文件与官方 frpc 二进制放在同一目录，执行 frpc -c frpc.toml 即可启动\n\n")
	sb.WriteString("serverAddr = " + tomlString(opt.ServerAddr) + "\n")
	sb.WriteString("serverPort = " + strconv.Itoa(opt.ServerPort) + "\n")
	if opt.User != "" {
		sb.WriteString("user = " + tomlString(opt.User) + "\n")
	}
	if opt.Token != "" {
		sb.WriteString("auth.method = \"token\"\n")
		sb.WriteString("auth.token = " + tomlString(opt.Token) + "\n")
	}
	if opt.Protocol != "" && opt.Protocol != "tcp" {
		sb.WriteString("transport.protocol = " + tomlString(opt.Protocol) + "\n")
	}
	sb.WriteString("transport.tls.enable = " + strconv.FormatBool(opt.TLS) + "\n")
	if opt.LogLevel != "" {
		sb.WriteString("log.level = " + tomlString(opt.LogLevel) + "\n")
	}
	sb.WriteString("log.to = \"console\"\n")

	for _, t := range tunnels {
		sb.WriteString("\n[[proxies]]\n")
		sb.WriteString("name = " + tomlString(t.Name) + "\n")
		sb.WriteString("type = " + tomlString(strings.ToLower(t.Type)) + "\n")
		sb.WriteString("localIP = " + tomlString(defaultLocalIP(t.LocalIP)) + "\n")
		sb.WriteString("localPort = " + strconv.Itoa(t.LocalPort) + "\n")

		switch strings.ToLower(t.Type) {
		case "tcp", "udp":
			sb.WriteString("remotePort = " + strconv.Itoa(t.RemotePort) + "\n")
		case "http", "https", "tcpmux":
			if len(t.CustomDomains) > 0 {
				sb.WriteString("customDomains = " + tomlStringList(t.CustomDomains) + "\n")
			}
			if t.Subdomain != "" {
				sb.WriteString("subdomain = " + tomlString(t.Subdomain) + "\n")
			}
		case "stcp", "xtcp", "sudp":
			sb.WriteString("secretKey = " + tomlString(t.SecretKey) + "\n")
			if allow := splitList(t.Group); len(allow) > 0 {
				sb.WriteString("allowUsers = " + tomlStringList(allow) + "\n")
			}
		}
		if t.UseEncryption {
			sb.WriteString("transport.useEncryption = true\n")
		}
		if t.UseCompression {
			sb.WriteString("transport.useCompression = true\n")
		}
		if t.BandwidthLimit != "" {
			sb.WriteString("transport.bandwidthLimit = " + tomlString(t.BandwidthLimit) + "\n")
		}
		if t.ProxyProtocolVersion != "" {
			sb.WriteString("transport.proxyProtocolVersion = " + tomlString(t.ProxyProtocolVersion) + "\n")
		}
		if t.HealthCheckType != "" {
			sb.WriteString("healthCheck.type = " + tomlString(t.HealthCheckType) + "\n")
			if t.HealthCheckURL != "" {
				sb.WriteString("healthCheck.path = " + tomlString(t.HealthCheckURL) + "\n")
			}
			if t.HealthCheckInterval > 0 {
				sb.WriteString("healthCheck.intervalSeconds = " + strconv.Itoa(t.HealthCheckInterval) + "\n")
			}
			if t.HealthCheckTimeout > 0 {
				sb.WriteString("healthCheck.timeoutSeconds = " + strconv.Itoa(t.HealthCheckTimeout) + "\n")
			}
			if t.HealthCheckMaxFailed > 0 {
				sb.WriteString("healthCheck.maxFailed = " + strconv.Itoa(t.HealthCheckMaxFailed) + "\n")
			}
		}
	}
	return sb.String()
}

func exportJSON(tunnels []Tunnel, opt ExportOptions) string {
	var sb strings.Builder
	sb.WriteString("{\n")
	sb.WriteString("  \"serverAddr\": " + jsonString(opt.ServerAddr) + ",\n")
	sb.WriteString("  \"serverPort\": " + strconv.Itoa(opt.ServerPort) + ",\n")
	if opt.User != "" {
		sb.WriteString("  \"user\": " + jsonString(opt.User) + ",\n")
	}
	if opt.Token != "" {
		sb.WriteString("  \"auth\": { \"method\": \"token\", \"token\": " + jsonString(opt.Token) + " },\n")
	}
	sb.WriteString("  \"transport\": { \"tls\": { \"enable\": " + strconv.FormatBool(opt.TLS) + " } },\n")
	sb.WriteString("  \"log\": { \"level\": " + jsonString(opt.LogLevel) + ", \"to\": \"console\" },\n")
	sb.WriteString("  \"proxies\": [\n")
	for i, t := range tunnels {
		sb.WriteString("    {")
		fields := []string{
			"\"name\": " + jsonString(t.Name),
			"\"type\": " + jsonString(strings.ToLower(t.Type)),
			"\"localIP\": " + jsonString(defaultLocalIP(t.LocalIP)),
			"\"localPort\": " + strconv.Itoa(t.LocalPort),
		}
		switch strings.ToLower(t.Type) {
		case "tcp", "udp":
			fields = append(fields, "\"remotePort\": "+strconv.Itoa(t.RemotePort))
		case "http", "https", "tcpmux":
			if len(t.CustomDomains) > 0 {
				fields = append(fields, "\"customDomains\": "+jsonStringList(t.CustomDomains))
			}
			if t.Subdomain != "" {
				fields = append(fields, "\"subdomain\": "+jsonString(t.Subdomain))
			}
		case "stcp", "xtcp", "sudp":
			fields = append(fields, "\"secretKey\": "+jsonString(t.SecretKey))
		}
		sb.WriteString(strings.Join(fields, ", "))
		if i < len(tunnels)-1 {
			sb.WriteString("},\n")
		} else {
			sb.WriteString("}\n")
		}
	}
	sb.WriteString("  ]\n}\n")
	return sb.String()
}

const versionBanner = "YCFRP " + version.Version + " / frp 内核 " + version.KernelVersion

func defaultLocalIP(ip string) string {
	if strings.TrimSpace(ip) == "" {
		return "127.0.0.1"
	}
	return ip
}

func tomlString(s string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "\n", "\\n")
	return "\"" + replacer.Replace(s) + "\""
}

func tomlStringList(items []string) string {
	parts := make([]string, 0, len(items))
	for _, s := range items {
		s = strings.TrimSpace(s)
		if s != "" {
			parts = append(parts, tomlString(s))
		}
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func jsonString(s string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "\n", "\\n", "\t", "\\t")
	return "\"" + replacer.Replace(s) + "\""
}

func jsonStringList(items []string) string {
	parts := make([]string, 0, len(items))
	for _, s := range items {
		s = strings.TrimSpace(s)
		if s != "" {
			parts = append(parts, jsonString(s))
		}
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

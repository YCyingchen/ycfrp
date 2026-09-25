package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ycfrp/ycfrp/internal/app"
	"github.com/ycfrp/ycfrp/internal/config"
	"github.com/ycfrp/ycfrp/internal/frp"
	"github.com/ycfrp/ycfrp/internal/logx"
	"github.com/ycfrp/ycfrp/internal/store"
	"github.com/ycfrp/ycfrp/internal/version"
)

// Server exposes the YCFRP HTTP API plus the embedded web panel.
type Server struct {
	app    *app.App
	http   *http.Server
	assets *assets

	uploadDir string
}

// New builds an HTTP server bound to the supplied application.
func New(a *app.App) (*Server, error) {
	s := &Server{
		app:       a,
		uploadDir: filepath.Join(a.DataDir, "uploads"),
	}
	if err := os.MkdirAll(s.uploadDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建上传目录失败: %w", err)
	}
	a.Logf(logx.LevelInfo, "面板", "数据目录就绪：%s", a.DataDir)
	assets, err := newAssets()
	if err != nil {
		return nil, err
	}
	s.assets = assets
	return s, nil
}

// ListenAndServe starts the HTTP server and blocks until it exits.
func (s *Server) ListenAndServe() error {
	cfg := s.app.Cfg.Snapshot()
	addr := fmt.Sprintf("0.0.0.0:%d", cfg.Port)
	mux := http.NewServeMux()
	s.routes(mux)

	s.http = &http.Server{
		Addr:              addr,
		Handler:           s.withCommon(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}
	s.app.Logf(logx.LevelInfo, "面板", "管理面板已监听 http://%s", addr)
	if err := s.http.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Addr reports the bound address once the listener is active.
func (s *Server) Addr() string {
	if s.http == nil {
		return ""
	}
	return s.http.Addr
}

// Shutdown gracefully stops the HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.http == nil {
		return nil
	}
	return s.http.Shutdown(ctx)
}

// withCommon attaches permissive CORS headers for the local desktop shell and
// logs unexpected panics instead of dropping the connection silently.
func (s *Server) withCommon(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		defer func() {
			if rec := recover(); rec != nil {
				s.app.Logf(logx.LevelError, "面板", "请求 %s 处理异常：%v", r.URL.Path, rec)
				writeErr(w, http.StatusInternalServerError, "服务器内部错误")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) routes(mux *http.ServeMux) {
	// Static panel assets.
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/favicon.ico", s.handleFavicon)

	// Public endpoints.
	mux.HandleFunc("/api/auth/login", s.handleLogin)
	mux.HandleFunc("/api/auth/logout", s.handleLogout)
	mux.HandleFunc("/api/auth/status", s.handleAuthStatus)
	mux.HandleFunc("/api/meta", s.handleMeta)

	// Authenticated endpoints.
	guard := func(fn http.HandlerFunc) http.HandlerFunc { return s.requireAuth(fn) }
	mux.HandleFunc("/api/overview", guard(s.handleOverview))
	mux.HandleFunc("/api/status", guard(s.handleStatus))
	mux.HandleFunc("/api/host", guard(s.handleHost))
	mux.HandleFunc("/api/series", guard(s.handleSeries))
	mux.HandleFunc("/api/config", guard(s.handleConfig))
	mux.HandleFunc("/api/kernel", guard(s.handleKernel))
	mux.HandleFunc("/api/tunnels", guard(s.handleTunnels))
	mux.HandleFunc("/api/tunnels/import", guard(s.handleImportTunnels))
	mux.HandleFunc("/api/tunnels/export", guard(s.handleExportTunnels))
	mux.HandleFunc("/api/tunnels/", guard(s.handleTunnelByID))
	mux.HandleFunc("/api/instances", guard(s.handleInstances))
	mux.HandleFunc("/api/instances/", guard(s.handleInstanceByID))
	mux.HandleFunc("/api/logs", guard(s.handleLogs))
	mux.HandleFunc("/api/logs/export", guard(s.handleLogsExport))
	mux.HandleFunc("/api/logs/stream", guard(s.handleLogStream))
	mux.HandleFunc("/api/logs/analyze", guard(s.handleLogsAnalyze))
	mux.HandleFunc("/api/notify/test", guard(s.handleNotifyTest))
	mux.HandleFunc("/api/notify/log", guard(s.handleNotifyLog))
	mux.HandleFunc("/api/qqbot/status", guard(s.handleQQBotStatus))
	mux.HandleFunc("/api/qqbot/discover", guard(s.handleQQBotDiscover))
	mux.HandleFunc("/api/qqbot/bind", guard(s.handleQQBind))
	mux.HandleFunc("/api/qqbot/bind/poll", guard(s.handleQQBindPoll))
	mux.HandleFunc("/api/ui/upload", guard(s.handleUpload))
	mux.HandleFunc("/api/ui/wallpapers", guard(s.handleWallpapers))
	mux.HandleFunc("/api/ui/wallpaper", guard(s.handleDeleteWallpaper))
	mux.HandleFunc("/api/account", guard(s.handleAccount))
	mux.HandleFunc("/api/stream/events", guard(s.handleEventStream))
	mux.HandleFunc("/api/net/interfaces", guard(s.handleInterfaces))
	mux.HandleFunc("/api/tools/ping", guard(s.handlePing))
	mux.HandleFunc("/api/update/check", guard(s.handleUpdateCheck))
	mux.HandleFunc("/api/update/checknow", guard(s.handleUpdateCheckNow))
	mux.HandleFunc("/api/update/apply", guard(s.handleUpdateApply))
	mux.HandleFunc("/api/update/upload", guard(s.handleUpdateUpload))
	mux.HandleFunc("/api/update/docker-apply", guard(s.handleDockerApply))
	mux.HandleFunc("/api/update/docker-upload", guard(s.handleDockerUpload))
	mux.HandleFunc("/api/autostart", guard(s.handleAutostart))

	// Uploaded wallpapers and logos.
	mux.Handle("/uploads/", http.StripPrefix("/uploads/", http.FileServer(http.Dir(s.uploadDir))))
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeOK(w http.ResponseWriter, v any) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": v})
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"ok": false, "error": msg})
}

func decodeBody(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 4<<20))
	if err := dec.Decode(v); err != nil {
		// 空请求体单独报出来：调用方（如实例启停）需要区分「没传值，按取反处理」
		// 与「传了格式错误的内容」。
		if errors.Is(err, io.EOF) {
			return errEmptyBody
		}
		return fmt.Errorf("请求内容格式不正确: %w", err)
	}
	return nil
}

func (s *Server) tunnelByID(id string) (frp.Tunnel, error) {
	for _, t := range s.app.Tunnels.List() {
		if t.ID == id {
			return t, nil
		}
	}
	return frp.Tunnel{}, store.ErrNotFound
}

// ---------------------------------------------------------------------------
// Auth handlers
// ---------------------------------------------------------------------------

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "仅支持 POST")
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Remember bool   `json:"remember"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	cfg := s.app.Cfg.Snapshot()
	if strings.TrimSpace(req.Username) != cfg.Username || !checkPassword(cfg.Password, req.Password) {
		s.app.Logf(logx.LevelWarn, "面板", "登录失败，账号 %s，来源 %s", req.Username, clientIP(r))
		writeErr(w, http.StatusUnauthorized, "账号或密码不正确")
		return
	}
	token, err := s.signToken(cfg.Username)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	maxAge := 0
	if req.Remember {
		maxAge = int(tokenTTL.Seconds())
	}
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   maxAge,
	})
	s.app.Logf(logx.LevelInfo, "面板", "登录成功，账号 %s，来源 %s", cfg.Username, clientIP(r))
	writeOK(w, map[string]any{"token": token, "username": cfg.Username, "version": version.Version})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
	writeOK(w, nil)
}

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	cfg := s.app.Cfg.Snapshot()
	logged := false
	if user, err := s.verifyToken(s.tokenFrom(r)); err == nil && user == cfg.Username {
		logged = true
	}
	writeOK(w, map[string]any{
		"loggedIn": logged,
		"username": cfg.Username,
	})
}

func (s *Server) handleMeta(w http.ResponseWriter, r *http.Request) {
	cfg := s.app.Cfg.Snapshot()
	writeOK(w, map[string]any{
		"product":       version.ProductName,
		"version":       version.Version,
		"kernelVersion": version.KernelVersion,
		"description":   version.Description,
		"ui":            cfg.UI,
		"startedAt":     s.app.StartedAt(),
		"uptime":        int64(s.app.Uptime().Seconds()),
		"mode":          s.app.Mode,
		"goVersion":     goVersion(),
		"frpTypes":      []string{"tcp", "udp", "http", "https", "tcpmux", "stcp", "xtcp", "sudp"},
		"update":        s.updateInfo(),
		"updateMode":    s.updateModeInfo(),
	})
}

func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// ---------------------------------------------------------------------------
// Status handlers
// ---------------------------------------------------------------------------

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	cfg := s.app.Cfg.Snapshot()
	status := s.app.Snapshot()
	host := s.app.HostStats()

	online := 0
	errorsCount := 0
	for _, t := range status.Tunnels {
		switch t.Status {
		case "online", "running":
			online++
		case "start error", "check failed":
			errorsCount++
		}
	}

	writeOK(w, map[string]any{
		"serverRunning": status.ServerRunning,
		"clientRunning": status.ClientRunning,
		"server":        status.Server,
		"clients":       status.Clients,
		"tunnels":       status.Tunnels,
		"host":          host,
		"stats": map[string]any{
			"tunnelTotal":  len(status.Tunnels),
			"tunnelOnline": online,
			"tunnelError":  errorsCount,
			"clientCount":  len(status.Clients),
		},
		"quota":       cfg.Monitor.MonthlyQuotaGB,
		"updatedAt":   time.Now(),
		"notifyReady": cfg.Notify.Enable,
	})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeOK(w, s.app.Snapshot())
}

func (s *Server) handleHost(w http.ResponseWriter, r *http.Request) {
	writeOK(w, s.app.HostStats())
}

func (s *Server) handleSeries(w http.ResponseWriter, r *http.Request) {
	writeOK(w, map[string]any{"points": s.app.Monitor.Series()})
}

func (s *Server) handleInterfaces(w http.ResponseWriter, r *http.Request) {
	stats := s.app.HostStats()
	writeOK(w, map[string]any{"interfaces": stats.Interfaces})
}

// ---------------------------------------------------------------------------
// Configuration handlers
// ---------------------------------------------------------------------------

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg := s.app.Cfg.Snapshot()
		// frps / frpc 以「默认实例」为准：实例才是内核配置的真正来源，
		// config.json 里的这两段只作为旧版本遗留字段保留。
		writeOK(w, map[string]any{
			"port":     cfg.Port,
			"username": cfg.Username,
			"frps":     s.effectiveFRPS(cfg),
			"frpc":     s.effectiveFRPC(cfg),
			"monitor":  cfg.Monitor,
			"notify":   notifyView(cfg.Notify),
			"ui":       cfg.UI,
			"update":   cfg.Updates,
		})
	case http.MethodPut, http.MethodPost:
		s.updateConfig(w, r)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "不支持的方法")
	}
}

// effectiveFRPS 返回第一个服务端实例的设置，没有实例时退回配置文件里的旧值。
func (s *Server) effectiveFRPS(cfg *config.Config) config.FRPSConfig {
	if inst := s.app.FirstServerInstance(); inst != nil {
		return inst.Server
	}
	return cfg.FRPS
}

// effectiveFRPC 返回第一个客户端实例的设置，没有实例时退回配置文件里的旧值。
func (s *Server) effectiveFRPC(cfg *config.Config) config.FRPCConfig {
	if inst := s.app.FirstClientInstance(); inst != nil {
		return inst.Client
	}
	return cfg.FRPC
}

// notifyView 在通知配置上附加一组「密钥是否已配置」的布尔标记。
//
// 密钥不回显明文，前端因此无法判断某通道是否配好。这里用 hasXxx 标记
// 把状态透出去，让界面能显示「已配置（留空保持不变）」；明文一律不外发。
func notifyView(n config.NotifyConfig) map[string]any {
	has := func(v string) bool { return strings.TrimSpace(v) != "" }
	return map[string]any{
		"enable":         n.Enable,
		"monitorErrors":  n.MonitorErrors,
		"monitorOffline": n.MonitorOffline,
		"monitorTraffic": n.MonitorTraffic,
		"trafficLimitGB": n.TrafficLimitGB,
		"keywords":       n.Keywords,
		"quietMinutes":   n.QuietMinutes,
		"channels":       n.Channels,
		"qq": map[string]any{
			"enable": n.QQ.Enable, "protocol": n.QQ.Protocol,
			"host": n.QQ.Host, "port": n.QQ.Port,
			"adminQQ": n.QQ.AdminQQ, "groupIds": n.QQ.GroupIDs,
			"hasAccessToken": has(n.QQ.AccessToken),
		},
		"qqOfficial": map[string]any{
			"enable": n.QQOfficial.Enable, "appId": n.QQOfficial.AppID,
			"sandbox": n.QQOfficial.Sandbox, "groupOpenIds": n.QQOfficial.GroupOpenIDs,
			"userOpenId": n.QQOfficial.UserOpenID, "msgId": n.QQOfficial.MsgID,
			"proxy": n.QQOfficial.Proxy, "listen": n.QQOfficial.Listen,
			"discovered": n.QQOfficial.Discovered,
			"hasAppSecret": has(n.QQOfficial.AppSecret),
		},
		"wecom": map[string]any{
			"enable": n.WeCom.Enable, "mentionList": n.WeCom.MentionList,
			"mentionAll": n.WeCom.MentionAll, "hasKey": has(n.WeCom.Key),
		},
		"dingtalk": map[string]any{
			"enable": n.DingTalk.Enable, "webhook": n.DingTalk.Webhook,
			"atMobiles": n.DingTalk.AtMobiles, "atAll": n.DingTalk.AtAll,
			"hasSecret": has(n.DingTalk.Secret),
		},
		"feishu": map[string]any{
			"enable": n.Feishu.Enable, "webhook": n.Feishu.Webhook,
			"hasSecret": has(n.Feishu.Secret),
		},
		"serverChan": map[string]any{
			"enable": n.ServerChan.Enable, "channel": n.ServerChan.Channel,
			"hasSendKey": has(n.ServerChan.SendKey),
		},
		"bark": map[string]any{
			"enable": n.Bark.Enable, "server": n.Bark.Server,
			"group": n.Bark.Group, "hasDeviceKey": has(n.Bark.DeviceKey),
		},
		"telegram": map[string]any{
			"enable": n.Telegram.Enable, "chatId": n.Telegram.ChatID,
			"proxy": n.Telegram.Proxy, "hasBotToken": has(n.Telegram.BotToken),
		},
	}
}

// configPatch mirrors the configurable subset accepted from the panel.
type configPatch struct {
	Port    *int           `json:"port"`
	FRPS    *frpSection    `json:"frps"`
	FRPC    *frpcSection   `json:"frpc"`
	Monitor *monitorSection `json:"monitor"`
	Notify  *notifySection `json:"notify"`
	UI      *uiSection     `json:"ui"`
	Update  *updateSection `json:"update"`
}

// updateSection 对应更新源设置，改动它不会影响内核运行。
type updateSection struct {
	SourceURL          *string `json:"sourceUrl"`
	Proxy              *string `json:"proxy"`
	AllowUpload        *bool   `json:"allowUpload"`
	CheckEnabled       *bool   `json:"checkEnabled"`
	CheckIntervalHours *int    `json:"checkIntervalHours"`
	NotifyOnUpdate     *bool   `json:"notifyOnUpdate"`
}

type frpSection struct {
	Enable            *bool   `json:"enable"`
	BindAddr          *string `json:"bindAddr"`
	BindPort          *int    `json:"bindPort"`
	KCPBindPort       *int    `json:"kcpBindPort"`
	QUICBindPort      *int    `json:"quicBindPort"`
	DashboardPort     *int    `json:"dashboardPort"`
	DashboardUser     *string `json:"dashboardUser"`
	DashboardPwd      *string `json:"dashboardPwd"`
	VhostHTTPPort     *int    `json:"vhostHttpPort"`
	VhostHTTPSPort    *int    `json:"vhostHttpsPort"`
	Token             *string `json:"token"`
	SubdomainHost     *string `json:"subdomainHost"`
	MaxPortsPerClient *int    `json:"maxPortsPerClient"`
	AllowPortsStart   *int    `json:"allowPortsStart"`
	AllowPortsEnd     *int    `json:"allowPortsEnd"`
	LogLevel          *string `json:"logLevel"`
	LogMaxDays        *int    `json:"logMaxDays"`
	TransportTLS      *bool   `json:"transportTLS"`
}

type frpcSection struct {
	Enable         *bool   `json:"enable"`
	ServerAddr     *string `json:"serverAddr"`
	ServerPort     *int    `json:"serverPort"`
	Token          *string `json:"token"`
	User           *string `json:"user"`
	LoginFailExit  *bool   `json:"loginFailExit"`
	LogLevel       *string `json:"logLevel"`
	LogMaxDays     *int    `json:"logMaxDays"`
	TransportTLS   *bool   `json:"transportTLS"`
	TransportProto *string `json:"transportProtocol"`
	UseEncryption  *bool   `json:"useEncryption"`
	UseCompression *bool   `json:"useCompression"`
}

type monitorSection struct {
	Enable          *bool    `json:"enable"`
	IntervalSeconds *int     `json:"intervalSeconds"`
	Interface       *string  `json:"interface"`
	RetentionDays   *int     `json:"retentionDays"`
	MonthlyQuotaGB  *float64 `json:"monthlyQuotaGb"`
}

type notifySection struct {
	Enable         *bool                 `json:"enable"`
	QQ             *qqSection            `json:"qq"`
	QQOfficial     *qqOfficialSection    `json:"qqOfficial"`
	WeCom          *wecomSection         `json:"wecom"`
	DingTalk       *dingtalkSection      `json:"dingtalk"`
	Feishu         *feishuSection        `json:"feishu"`
	ServerChan     *serverChanSection    `json:"serverChan"`
	Bark           *barkSection          `json:"bark"`
	Telegram       *telegramSection      `json:"telegram"`
	Channels       *[]frpChannelSection  `json:"channels"`
	MonitorErrors  *bool                 `json:"monitorErrors"`
	MonitorOffline *bool                 `json:"monitorOffline"`
	MonitorTraffic *bool                 `json:"monitorTraffic"`
	TrafficLimGB   *float64              `json:"trafficLimitGB"`
	Keywords       *[]string             `json:"keywords"`
	QuietMinutes   *int                  `json:"quietMinutes"`
}

type qqSection struct {
	Enable      *bool     `json:"enable"`
	Protocol    *string   `json:"protocol"`
	Host        *string   `json:"host"`
	Port        *int      `json:"port"`
	AccessToken *string   `json:"accessToken"`
	AdminQQ     *string   `json:"adminQQ"`
	GroupIDs    *[]string `json:"groupIds"`
}

type qqOfficialSection struct {
	Enable       *bool   `json:"enable"`
	AppID        *string `json:"appId"`
	AppSecret    *string `json:"appSecret"`
	Sandbox      *bool   `json:"sandbox"`
	GroupOpenIDs *string `json:"groupOpenIds"`
	UserOpenID   *string `json:"userOpenId"`
	MsgID        *string `json:"msgId"`
	Proxy        *string `json:"proxy"`
	Listen       *bool   `json:"listen"`
}

type wecomSection struct {
	Enable      *bool   `json:"enable"`
	Key         *string `json:"key"`
	MentionList *string `json:"mentionList"`
	MentionAll  *bool   `json:"mentionAll"`
}

type dingtalkSection struct {
	Enable    *bool   `json:"enable"`
	Webhook   *string `json:"webhook"`
	Secret    *string `json:"secret"`
	AtMobiles *string `json:"atMobiles"`
	AtAll     *bool   `json:"atAll"`
}

type feishuSection struct {
	Enable  *bool   `json:"enable"`
	Webhook *string `json:"webhook"`
	Secret  *string `json:"secret"`
}

type serverChanSection struct {
	Enable  *bool   `json:"enable"`
	SendKey *string `json:"sendKey"`
	Channel *string `json:"channel"`
}

type barkSection struct {
	Enable    *bool   `json:"enable"`
	Server    *string `json:"server"`
	DeviceKey *string `json:"deviceKey"`
	Sound     *string `json:"sound"`
	Group     *string `json:"group"`
}

type telegramSection struct {
	Enable   *bool   `json:"enable"`
	BotToken *string `json:"botToken"`
	ChatID   *string `json:"chatId"`
	Proxy    *string `json:"proxy"`
}

type frpChannelSection struct {
	ID      string            `json:"id"`
	Name    string            `json:"name"`
	Type    string            `json:"type"`
	Enable  bool              `json:"enable"`
	URL     string            `json:"url"`
	Secret  string            `json:"secret"`
	Headers map[string]string `json:"headers"`
}

type uiSection struct {
	Theme            *string   `json:"theme"`
	AccentColor      *string   `json:"accentColor"`
	Wallpaper        *string   `json:"wallpaper"`
	WallpaperURL     *string   `json:"wallpaperUrl"`
	WallpaperOpacity *float64  `json:"wallpaperOpacity"`
	SiteName         *string   `json:"siteName"`
	SiteLogo         *string   `json:"siteLogo"`
	SiteFooter       *string   `json:"siteFooter"`
	GlassEffect      *bool     `json:"glassEffect"`
	NavLayout        *string   `json:"navLayout"`
	UIStyle          *string   `json:"uiStyle"`
	OnlineWallpapers *[]string `json:"onlineWallpapers"`
}

func (s *Server) updateConfig(w http.ResponseWriter, r *http.Request) {
	var patch configPatch
	if err := decodeBody(r, &patch); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	var kernelChanged, tunnelChanged bool
	err := s.app.UpdateConfig(func(cfg *config.Config) {
		if patch.Port != nil && *patch.Port > 0 && *patch.Port < 65536 {
			kernelChanged = kernelChanged || cfg.Port != *patch.Port
			cfg.Port = *patch.Port
		}
		// frps / frpc 两段同时写入默认实例与 config.json：实例是内核的实际
		// 来源，config.json 里的副本保持同步，是为了让「实例集合被清空」时
		// 的迁移兜底仍然能还原出用户的配置。
		if frpSec := patch.FRPS; frpSec != nil {
			kernelChanged = true
			setBool(&cfg.FRPS.Enable, frpSec.Enable)
			setStr(&cfg.FRPS.BindAddr, frpSec.BindAddr)
			setInt(&cfg.FRPS.BindPort, frpSec.BindPort)
			setInt(&cfg.FRPS.KCPBindPort, frpSec.KCPBindPort)
			setInt(&cfg.FRPS.QUICBindPort, frpSec.QUICBindPort)
			setInt(&cfg.FRPS.DashboardPort, frpSec.DashboardPort)
			setStr(&cfg.FRPS.DashboardUser, frpSec.DashboardUser)
			setStr(&cfg.FRPS.DashboardPwd, frpSec.DashboardPwd)
			setInt(&cfg.FRPS.VhostHTTPPort, frpSec.VhostHTTPPort)
			setInt(&cfg.FRPS.VhostHTTPSPort, frpSec.VhostHTTPSPort)
			setStr(&cfg.FRPS.Token, frpSec.Token)
			setStr(&cfg.FRPS.SubdomainHost, frpSec.SubdomainHost)
			setInt(&cfg.FRPS.MaxPortsPerClient, frpSec.MaxPortsPerClient)
			setInt(&cfg.FRPS.AllowPortsStart, frpSec.AllowPortsStart)
			setInt(&cfg.FRPS.AllowPortsEnd, frpSec.AllowPortsEnd)
			setStr(&cfg.FRPS.LogLevel, frpSec.LogLevel)
			setInt(&cfg.FRPS.LogMaxDays, frpSec.LogMaxDays)
			setBool(&cfg.FRPS.TransportTLS, frpSec.TransportTLS)
			// 令牌与仪表盘密码走「留空不覆盖」，与实例编辑弹窗保持一致。
			setSecret(&cfg.FRPS.Token, frpSec.Token)
			setSecret(&cfg.FRPS.DashboardPwd, frpSec.DashboardPwd)
		}
		if client := patch.FRPC; client != nil {
			kernelChanged = true
			setBool(&cfg.FRPC.Enable, client.Enable)
			setStr(&cfg.FRPC.ServerAddr, client.ServerAddr)
			setInt(&cfg.FRPC.ServerPort, client.ServerPort)
			setStr(&cfg.FRPC.Token, client.Token)
			setStr(&cfg.FRPC.User, client.User)
			setBool(&cfg.FRPC.LoginFailExit, client.LoginFailExit)
			setStr(&cfg.FRPC.LogLevel, client.LogLevel)
			setInt(&cfg.FRPC.LogMaxDays, client.LogMaxDays)
			setBool(&cfg.FRPC.TransportTLS, client.TransportTLS)
			setStr(&cfg.FRPC.TransportProto, client.TransportProto)
			setSecret(&cfg.FRPC.Token, client.Token)
		}
		if mon := patch.Monitor; mon != nil {
			setBool(&cfg.Monitor.Enable, mon.Enable)
			setInt(&cfg.Monitor.IntervalSeconds, mon.IntervalSeconds)
			setStr(&cfg.Monitor.Interface, mon.Interface)
			setInt(&cfg.Monitor.RetentionDays, mon.RetentionDays)
			setFloat(&cfg.Monitor.MonthlyQuotaGB, mon.MonthlyQuotaGB)
		}
		if n := patch.Notify; n != nil {
			setBool(&cfg.Notify.Enable, n.Enable)
			setBool(&cfg.Notify.MonitorErrors, n.MonitorErrors)
			setBool(&cfg.Notify.MonitorOffline, n.MonitorOffline)
			setBool(&cfg.Notify.MonitorTraffic, n.MonitorTraffic)
			setFloat(&cfg.Notify.TrafficLimitGB, n.TrafficLimGB)
			if n.QuietMinutes != nil {
				cfg.Notify.QuietMinutes = *n.QuietMinutes
			}
			if n.Keywords != nil {
				cfg.Notify.Keywords = append([]string(nil), (*n.Keywords)...)
			}
			if qq := n.QQ; qq != nil {
				setBool(&cfg.Notify.QQ.Enable, qq.Enable)
				setStr(&cfg.Notify.QQ.Protocol, qq.Protocol)
				setStr(&cfg.Notify.QQ.Host, qq.Host)
				setInt(&cfg.Notify.QQ.Port, qq.Port)
				setSecret(&cfg.Notify.QQ.AccessToken, qq.AccessToken)
				setStr(&cfg.Notify.QQ.AdminQQ, qq.AdminQQ)
				if qq.GroupIDs != nil {
					cfg.Notify.QQ.GroupIDs = append([]string(nil), (*qq.GroupIDs)...)
				}
			}
			if q := n.QQOfficial; q != nil {
				setBool(&cfg.Notify.QQOfficial.Enable, q.Enable)
				setStr(&cfg.Notify.QQOfficial.AppID, q.AppID)
				setSecret(&cfg.Notify.QQOfficial.AppSecret, q.AppSecret)
				setBool(&cfg.Notify.QQOfficial.Sandbox, q.Sandbox)
				setStr(&cfg.Notify.QQOfficial.GroupOpenIDs, q.GroupOpenIDs)
				setStr(&cfg.Notify.QQOfficial.UserOpenID, q.UserOpenID)
				setStr(&cfg.Notify.QQOfficial.MsgID, q.MsgID)
				setStr(&cfg.Notify.QQOfficial.Proxy, q.Proxy)
				setBool(&cfg.Notify.QQOfficial.Listen, q.Listen)
			}
			if w := n.WeCom; w != nil {
				setBool(&cfg.Notify.WeCom.Enable, w.Enable)
				setSecret(&cfg.Notify.WeCom.Key, w.Key)
				setStr(&cfg.Notify.WeCom.MentionList, w.MentionList)
				setBool(&cfg.Notify.WeCom.MentionAll, w.MentionAll)
			}
			if d := n.DingTalk; d != nil {
				setBool(&cfg.Notify.DingTalk.Enable, d.Enable)
				setStr(&cfg.Notify.DingTalk.Webhook, d.Webhook)
				setSecret(&cfg.Notify.DingTalk.Secret, d.Secret)
				setStr(&cfg.Notify.DingTalk.AtMobiles, d.AtMobiles)
				setBool(&cfg.Notify.DingTalk.AtAll, d.AtAll)
			}
			if f := n.Feishu; f != nil {
				setBool(&cfg.Notify.Feishu.Enable, f.Enable)
				setStr(&cfg.Notify.Feishu.Webhook, f.Webhook)
				setSecret(&cfg.Notify.Feishu.Secret, f.Secret)
			}
			if sc := n.ServerChan; sc != nil {
				setBool(&cfg.Notify.ServerChan.Enable, sc.Enable)
				setSecret(&cfg.Notify.ServerChan.SendKey, sc.SendKey)
				setStr(&cfg.Notify.ServerChan.Channel, sc.Channel)
			}
			if b := n.Bark; b != nil {
				setBool(&cfg.Notify.Bark.Enable, b.Enable)
				setStr(&cfg.Notify.Bark.Server, b.Server)
				setSecret(&cfg.Notify.Bark.DeviceKey, b.DeviceKey)
				setStr(&cfg.Notify.Bark.Sound, b.Sound)
				setStr(&cfg.Notify.Bark.Group, b.Group)
			}
			if tg := n.Telegram; tg != nil {
				setBool(&cfg.Notify.Telegram.Enable, tg.Enable)
				setSecret(&cfg.Notify.Telegram.BotToken, tg.BotToken)
				setStr(&cfg.Notify.Telegram.ChatID, tg.ChatID)
				setStr(&cfg.Notify.Telegram.Proxy, tg.Proxy)
			}
			if n.Channels != nil {
				list := make([]config.NotifyChannel, 0, len(*n.Channels))
				for _, ch := range *n.Channels {
					id := ch.ID
					if id == "" {
						id = app.NewID("ch")
					}
					list = append(list, config.NotifyChannel{
						ID: id, Name: ch.Name, Type: ch.Type,
						Enable: ch.Enable, URL: ch.URL, Secret: ch.Secret,
						Headers: ch.Headers,
					})
				}
				cfg.Notify.Channels = list
			}
		}
		if ui := patch.UI; ui != nil {
			setStr(&cfg.UI.Theme, ui.Theme)
			setStr(&cfg.UI.AccentColor, ui.AccentColor)
			setStr(&cfg.UI.Wallpaper, ui.Wallpaper)
			setStr(&cfg.UI.WallpaperURL, ui.WallpaperURL)
			setFloat(&cfg.UI.WallpaperOpacity, ui.WallpaperOpacity)
			setStr(&cfg.UI.SiteName, ui.SiteName)
			setStr(&cfg.UI.SiteLogo, ui.SiteLogo)
			setStr(&cfg.UI.SiteFooter, ui.SiteFooter)
			setBool(&cfg.UI.GlassEffect, ui.GlassEffect)
			setStr(&cfg.UI.NavLayout, ui.NavLayout)
			setStr(&cfg.UI.UIStyle, ui.UIStyle)
			if ui.OnlineWallpapers != nil {
				cfg.UI.OnlineWallpapers = append([]string(nil), (*ui.OnlineWallpapers)...)
			}
		}
		if u := patch.Update; u != nil {
			setStr(&cfg.Updates.SourceURL, u.SourceURL)
			setStr(&cfg.Updates.Proxy, u.Proxy)
			setBool(&cfg.Updates.AllowUpload, u.AllowUpload)
			setBool(&cfg.Updates.CheckEnabled, u.CheckEnabled)
			setInt(&cfg.Updates.CheckIntervalHours, u.CheckIntervalHours)
			setBool(&cfg.Updates.NotifyOnUpdate, u.NotifyOnUpdate)
		}
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// 把同一份改动同步进默认实例：实例是内核配置的实际来源，
	// 只改 config.json 的话保存完看起来「没生效」。
	if patch.FRPS != nil || patch.FRPC != nil {
		if err := s.syncDefaultInstances(patch); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if tunnelChanged {
		_ = s.app.ReloadTunnels()
	}
	if kernelChanged {
		if err := s.app.RestartKernels(); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	writeOK(w, map[string]any{"restarted": kernelChanged})
}

// syncDefaultInstances 把 /api/config 的 frps / frpc 补丁写到第一个对应实例上。
//
// 服务端/客户端两个设置页走的是这条路径，而内核只认实例配置，
// 因此这里必须做一次转发，否则保存后配置不会真正生效。
func (s *Server) syncDefaultInstances(patch configPatch) error {
	if frpSec := patch.FRPS; frpSec != nil {
		if inst := s.app.FirstServerInstance(); inst != nil {
			if err := s.app.UpdateInstance(inst.ID, func(inst *frp.Instance) {
				applyServerPatch(&inst.Server, frpSec)
			}); err != nil {
				return err
			}
		}
	}
	if clientSec := patch.FRPC; clientSec != nil {
		if inst := s.app.FirstClientInstance(); inst != nil {
			if err := s.app.UpdateInstance(inst.ID, func(inst *frp.Instance) {
				applyClientPatch(&inst.Client, clientSec)
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

// applyServerPatch 把配置补丁的字段合并进服务端实例。
// 令牌与仪表盘密码留空表示保持不变。
func applyServerPatch(s *config.FRPSConfig, p *frpSection) {
	setBool(&s.Enable, p.Enable)
	setStr(&s.BindAddr, p.BindAddr)
	setInt(&s.BindPort, p.BindPort)
	setInt(&s.KCPBindPort, p.KCPBindPort)
	setInt(&s.QUICBindPort, p.QUICBindPort)
	setInt(&s.DashboardPort, p.DashboardPort)
	setStr(&s.DashboardUser, p.DashboardUser)
	setInt(&s.VhostHTTPPort, p.VhostHTTPPort)
	setInt(&s.VhostHTTPSPort, p.VhostHTTPSPort)
	setStr(&s.SubdomainHost, p.SubdomainHost)
	setInt(&s.MaxPortsPerClient, p.MaxPortsPerClient)
	setInt(&s.AllowPortsStart, p.AllowPortsStart)
	setInt(&s.AllowPortsEnd, p.AllowPortsEnd)
	setStr(&s.LogLevel, p.LogLevel)
	setInt(&s.LogMaxDays, p.LogMaxDays)
	setBool(&s.TransportTLS, p.TransportTLS)
	setSecret(&s.Token, p.Token)
	setSecret(&s.DashboardPwd, p.DashboardPwd)
}

// applyClientPatch 把配置补丁的字段合并进客户端实例，令牌留空保持不变。
func applyClientPatch(c *config.FRPCConfig, p *frpcSection) {
	setBool(&c.Enable, p.Enable)
	setStr(&c.ServerAddr, p.ServerAddr)
	setInt(&c.ServerPort, p.ServerPort)
	setStr(&c.User, p.User)
	setBool(&c.LoginFailExit, p.LoginFailExit)
	setStr(&c.LogLevel, p.LogLevel)
	setInt(&c.LogMaxDays, p.LogMaxDays)
	setBool(&c.TransportTLS, p.TransportTLS)
	setStr(&c.TransportProto, p.TransportProto)
	setBool(&c.UseEncryption, p.UseEncryption)
	setBool(&c.UseCompression, p.UseCompression)
	setSecret(&c.Token, p.Token)
}

func setBool(dst *bool, v *bool) {
	if v != nil {
		*dst = *v
	}
}

func setInt(dst *int, v *int) {
	if v != nil {
		*dst = *v
	}
}

func setFloat(dst *float64, v *float64) {
	if v != nil {
		*dst = *v
	}
}

func setStr(dst *string, v *string) {
	if v != nil {
		*dst = *v
	}
}

// setSecret 用于密钥类字段：只在传入非空值时才覆盖。
//
// 密码框出于安全不回显已保存的值，前端提交时该字段必然是空字符串。
// 若无条件覆盖，用户每点一次「保存」都会把已配置的密钥抹掉，因此这里
// 把空值视为「保持原值」。需要真正清除密钥时，请显式改成别的写法
// （当前界面不提供清空入口）。
func setSecret(dst *string, v *string) {
	if v == nil {
		return
	}
	if strings.TrimSpace(*v) == "" {
		return
	}
	*dst = *v
}

// ---------------------------------------------------------------------------
// Kernel control
// ---------------------------------------------------------------------------

func (s *Server) handleKernel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "仅支持 POST")
		return
	}
	var req struct {
		Action string `json:"action"`
		Target string `json:"target"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	switch req.Action {
	case "restart":
		if err := s.app.RestartKernels(); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	case "stop":
		s.app.Engine.Stop()
		s.app.Logf(logx.LevelWarn, "面板", "已通过面板停止内核")
	case "start":
		if err := s.app.RestartKernels(); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	case "reset-traffic":
		s.app.Monitor.ResetCounters()
		s.app.Logf(logx.LevelInfo, "监控", "流量统计已重置")
	default:
		writeErr(w, http.StatusBadRequest, "未知操作: "+req.Action)
		return
	}
	server, client := s.app.Engine.Running()
	writeOK(w, map[string]any{"serverRunning": server, "clientRunning": client})
}

// ---------------------------------------------------------------------------
// Tunnel handlers
// ---------------------------------------------------------------------------

func (s *Server) handleTunnels(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeOK(w, map[string]any{"tunnels": s.app.Snapshot().Tunnels})
	case http.MethodPost:
		var t frp.Tunnel
		if err := decodeBody(r, &t); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := s.validateTunnel(&t, ""); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		saved, err := s.app.Tunnels.Put(t)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if err := s.app.ReloadTunnels(); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		s.app.Logf(logx.LevelInfo, "隧道", "已新增隧道 %s（%s）", saved.Name, frp.ProxyTypeLabel(saved.Type))
		writeOK(w, saved)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "不支持的方法")
	}
}

func (s *Server) handleTunnelByID(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/tunnels/")
	id = strings.Trim(id, "/")
	if id == "" {
		writeErr(w, http.StatusBadRequest, "缺少隧道标识")
		return
	}
	if strings.HasSuffix(id, "/toggle") {
		s.toggleTunnel(w, r, strings.TrimSuffix(id, "/toggle"))
		return
	}
	old, err := s.tunnelByID(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "隧道不存在")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeOK(w, old)
	case http.MethodPut, http.MethodPost:
		var t frp.Tunnel
		if err := decodeBody(r, &t); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		t.ID = id
		if err := s.validateTunnel(&t, id); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		saved, err := s.app.Tunnels.Put(t)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if err := s.app.ReloadTunnels(); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		s.app.Logf(logx.LevelInfo, "隧道", "已更新隧道 %s", saved.Name)
		writeOK(w, saved)
	case http.MethodDelete:
		if err := s.app.Tunnels.Delete(id); err != nil {
			writeErr(w, http.StatusNotFound, "隧道不存在")
			return
		}
		if err := s.app.ReloadTunnels(); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		s.app.Logf(logx.LevelWarn, "隧道", "已删除隧道 %s", old.Name)
		writeOK(w, nil)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "不支持的方法")
	}
}

func (s *Server) toggleTunnel(w http.ResponseWriter, r *http.Request, id string) {
	t, err := s.tunnelByID(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "隧道不存在")
		return
	}
	t.Enabled = !t.Enabled
	if t.ID == "" {
		t.ID = app.NewID("tn")
	}
	saved, err := s.app.Tunnels.Put(t)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.app.ReloadTunnels(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	state := "已启用"
	if !saved.Enabled {
		state = "已停用"
	}
	s.app.Logf(logx.LevelInfo, "隧道", "隧道 %s %s", saved.Name, state)
	writeOK(w, saved)
}

// validateTunnel checks required fields and rejects duplicate names.
func (s *Server) validateTunnel(t *frp.Tunnel, selfID string) error {
	t.Name = strings.TrimSpace(t.Name)
	if t.Name == "" {
		return errors.New("隧道名称不能为空")
	}
	t.Type = strings.ToLower(strings.TrimSpace(t.Type))
	switch t.Type {
	case "tcp", "udp", "http", "https", "tcpmux", "stcp", "xtcp", "sudp":
	default:
		return fmt.Errorf("不支持的隧道类型: %s", t.Type)
	}
	if t.Type == "tcp" || t.Type == "udp" {
		if t.RemotePort <= 0 || t.RemotePort > 65535 {
			return errors.New("远程端口必须在 1 到 65535 之间")
		}
	}
	if t.Type == "http" || t.Type == "https" || t.Type == "tcpmux" {
		if len(t.CustomDomains) == 0 && strings.TrimSpace(t.Subdomain) == "" {
			return errors.New("HTTP 类隧道需要填写自定义域名或子域名")
		}
	}
	if t.Type == "stcp" || t.Type == "xtcp" || t.Type == "sudp" {
		if strings.TrimSpace(t.SecretKey) == "" {
			return errors.New("加密隧道需要填写密钥")
		}
	}
	if t.Type != "stcp" && t.Type != "xtcp" && t.Type != "sudp" {
		if t.LocalPort <= 0 || t.LocalPort > 65535 {
			return errors.New("本地端口必须在 1 到 65535 之间")
		}
	}
	if t.ID == "" {
		t.ID = app.NewID("tn")
	}
	// 隧道必须归属到一个 frpc 实例，否则没有内核会去加载它。
	if err := s.resolveTunnelInstance(t); err != nil {
		return err
	}
	// 名称唯一性按实例判定：不同 frpc 实例连的是不同服务端，
	// 各自用同名隧道互不影响。
	for _, other := range s.app.Tunnels.List() {
		if other.Name == t.Name && other.ID != selfID && other.InstanceID == t.InstanceID {
			return fmt.Errorf("隧道名称 %s 在该实例下已存在", t.Name)
		}
	}
	return nil
}

// defaultClientInstanceID 返回默认的客户端实例标识。
//
// 未指定实例时用它兜底：单实例老用户升级后隧道就落在默认实例上，
// 直接新增隧道也不必先手动选实例。
func (s *Server) defaultClientInstanceID() string {
	clients := s.app.ClientInstances()
	if len(clients) == 0 {
		return ""
	}
	for _, c := range clients {
		if c.Enabled() {
			return c.ID
		}
	}
	return clients[0].ID
}

// resolveTunnelInstance 确定隧道归属的客户端实例，并校验其存在性与类型。
func (s *Server) resolveTunnelInstance(t *frp.Tunnel) error {
	t.InstanceID = strings.TrimSpace(t.InstanceID)
	if t.InstanceID == "" {
		t.InstanceID = s.defaultClientInstanceID()
		if t.InstanceID == "" {
			return errors.New("请先在「实例」页创建一个客户端实例，再添加隧道")
		}
		return nil
	}
	inst, err := s.app.InstanceByID(t.InstanceID)
	if err != nil {
		return errors.New("指定的实例不存在")
	}
	if inst.IsServer() {
		return errors.New("隧道只能归属于客户端实例")
	}
	return nil
}

func (s *Server) handleImportTunnels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "仅支持 POST")
		return
	}
	var req struct {
		Content    string `json:"content"`
		Adopt      bool   `json:"adopt"`
		Overwrite  bool   `json:"overwrite"`
		InstanceID string `json:"instanceId"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := frp.ParseClientConfigText(req.Content)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	if req.Adopt {
		// 只更新用户所选的客户端实例；未指定则落到默认实例上。
		targetID := strings.TrimSpace(req.InstanceID)
		if targetID == "" {
			targetID = s.defaultClientInstanceID()
		}
		if targetID != "" {
			if err := s.app.UpdateInstance(targetID, func(inst *frp.Instance) {
				if result.Common.ServerAddr != "" {
					inst.Client.ServerAddr = result.Common.ServerAddr
				}
				if result.Common.ServerPort > 0 {
					inst.Client.ServerPort = result.Common.ServerPort
				}
				if result.Common.Token != "" {
					inst.Client.Token = result.Common.Token
				}
				if result.Common.User != "" {
					inst.Client.User = result.Common.User
				}
				inst.Client.TransportTLS = result.Common.TLS
				if result.Common.Protocol != "" {
					inst.Client.TransportProto = result.Common.Protocol
				}
				if result.Common.LogLevel != "" {
					inst.Client.LogLevel = result.Common.LogLevel
				}
			}); err != nil {
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
		}
	}

	// 导入的隧道统一归属到目标客户端实例。
	importTarget := strings.TrimSpace(req.InstanceID)
	if importTarget == "" {
		importTarget = s.defaultClientInstanceID()
	}
	if importTarget == "" {
		writeErr(w, http.StatusBadRequest, "请先在「实例」页创建一个客户端实例，再导入隧道")
		return
	}

	existing := make(map[string]bool)
	for _, t := range s.app.Tunnels.List() {
		if t.InstanceID == importTarget {
			existing[t.Name] = true
		}
	}
	added, skipped := 0, 0
	for _, t := range result.Tunnels {
		if existing[t.Name] && !req.Overwrite {
			result.Warnings = append(result.Warnings, fmt.Sprintf("隧道 %s 已存在，未覆盖", t.Name))
			skipped++
			continue
		}
		if t.ID == "" {
			t.ID = app.NewID("tn")
		}
		t.InstanceID = importTarget
		if _, err := s.app.Tunnels.Put(t); err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("保存隧道 %s 失败：%v", t.Name, err))
			skipped++
			continue
		}
		added++
	}
	if err := s.app.ReloadTunnels(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.app.Logf(logx.LevelInfo, "隧道", "导入配置完成，新增 %d 条，跳过 %d 条", added, skipped)
	writeOK(w, map[string]any{
		"added":    added,
		"skipped":  skipped,
		"format":   result.Format,
		"common":   result.Common,
		"warnings": result.Warnings,
		"tunnels":  result.Tunnels,
	})
}

func (s *Server) handleExportTunnels(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	format := q.Get("format")
	instanceID := strings.TrimSpace(q.Get("instanceId"))

	tunnels := s.app.Tunnels.List()
	// 按实例筛选：导出的是「某个客户端实例的完整 frpc 配置」，
	// 连带该实例自己的服务端地址与令牌，拿去就能直接跑。
	if instanceID != "" {
		filtered := make([]frp.Tunnel, 0, len(tunnels))
		for _, t := range tunnels {
			if t.InstanceID == instanceID {
				filtered = append(filtered, t)
			}
		}
		tunnels = filtered
	}
	if q.Get("enabledOnly") == "1" {
		filtered := make([]frp.Tunnel, 0, len(tunnels))
		for _, t := range tunnels {
			if t.Enabled {
				filtered = append(filtered, t)
			}
		}
		tunnels = filtered
	}

	opts := s.exportOptionsFor(instanceID)
	opts.Format = format
	text := frp.ExportClientConfig(tunnels, opts)

	name := "frpc.toml"
	if strings.EqualFold(format, "json") {
		name = "frpc.json"
	}
	// 指定了实例时把实例名带进文件名，方便同时管理多个客户端。
	if instanceID != "" {
		if inst, err := s.app.InstanceByID(instanceID); err == nil {
			base := strings.TrimSuffix(name, filepath.Ext(name))
			name = base + "-" + sanitizeFileName(inst.Name) + filepath.Ext(name)
		}
	}
	if q.Get("download") == "1" {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition",
			"attachment; filename=\""+name+"\"; filename*=UTF-8''"+name)
		_, _ = io.WriteString(w, text)
		return
	}
	writeOK(w, map[string]any{"filename": name, "content": text})
}

// exportOptionsFor 取指定实例的连接参数；未指定实例时回落到默认客户端，
// 再兜底到全局配置，保证老调用方式仍然可用。
func (s *Server) exportOptionsFor(instanceID string) frp.ExportOptions {
	if instanceID == "" {
		instanceID = s.defaultClientInstanceID()
	}
	if inst, err := s.app.InstanceByID(instanceID); err == nil && !inst.IsServer() {
		c := inst.Client
		return frp.ExportOptions{
			ServerAddr: c.ServerAddr, ServerPort: c.ServerPort,
			Token: c.Token, User: c.User,
			TLS: c.TransportTLS, Protocol: c.TransportProto, LogLevel: c.LogLevel,
		}
	}
	cfg := s.app.Cfg.Snapshot()
	return frp.ExportOptions{
		ServerAddr: cfg.FRPC.ServerAddr,
		ServerPort: cfg.FRPC.ServerPort,
		Token:      cfg.FRPC.Token,
		User:       cfg.FRPC.User,
		TLS:        cfg.FRPC.TransportTLS,
		Protocol:   cfg.FRPC.TransportProto,
		LogLevel:   cfg.FRPC.LogLevel,
	}
}

// sanitizeFileName 把实例名里不适合出现在文件名中的字符替换掉。
func sanitizeFileName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "instance"
	}
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "*", "_",
		"?", "_", "\"", "_", "<", "_", ">", "_", "|", "_", " ", "_")
	return replacer.Replace(s)
}

// ---------------------------------------------------------------------------
// Log handlers
// ---------------------------------------------------------------------------

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	minLevel := logx.Level(q.Get("level"))
	if minLevel == "" {
		minLevel = logx.LevelInfo
	}
	limit := 500
	if v, err := strconv.Atoi(q.Get("limit")); err == nil && v > 0 && v <= 5000 {
		limit = v
	}
	entries := s.app.Log.Query(minLevel, q.Get("keyword"), limit)
	writeOK(w, map[string]any{"entries": entries, "count": len(entries)})
}

func (s *Server) handleLogsExport(w http.ResponseWriter, r *http.Request) {
	text := s.app.Log.Export()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if r.URL.Query().Get("download") == "1" {
		name := "ycfrp-log-" + time.Now().Format("20060102-150405") + ".txt"
		w.Header().Set("Content-Disposition",
			"attachment; filename=\""+name+"\"; filename*=UTF-8''"+name)
	}
	_, _ = io.WriteString(w, text)
}

func (s *Server) handleLogsAnalyze(w http.ResponseWriter, r *http.Request) {
	writeOK(w, map[string]any{"issues": s.app.Log.Analyze(20)})
}

// handleLogStream pushes new log entries using Server-Sent Events, which needs
// no extra dependency and works from a plain browser EventSource.
func (s *Server) handleLogStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "当前环境不支持流式推送")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Replay recent lines so the viewer is not empty right after connecting.
	minLevel := logx.Level(r.URL.Query().Get("level"))
	if minLevel == "" {
		minLevel = logx.LevelInfo
	}
	id, ch := s.app.Log.Subscribe()
	defer s.app.Log.Unsubscribe(id)

	for _, e := range reverse(s.app.Log.Query(minLevel, "", 60)) {
		writeSSE(w, "log", e)
	}
	flusher.Flush()

	ctx := r.Context()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case e, open := <-ch:
			if !open {
				return
			}
			if logx.LevelRank(e.Level) < logx.LevelRank(minLevel) {
				continue
			}
			writeSSE(w, "log", e)
			flusher.Flush()
		case <-ping.C:
			_, _ = io.WriteString(w, ": keep-alive\n\n")
			flusher.Flush()
		}
	}
}

// handleEventStream pushes periodic status snapshots for the dashboard charts.
func (s *Server) handleEventStream(w http.ResponseWriter, r *http.Request) {
	if _, ok := w.(http.Flusher); !ok {
		writeErr(w, http.StatusInternalServerError, "当前环境不支持流式推送")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher := w.(http.Flusher)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	send := func() {
		writeSSE(w, "status", map[string]any{
			"host":    s.app.HostStats(),
			"status":  s.app.Snapshot(),
			"time":    time.Now(),
			"latency": 0,
		})
		flusher.Flush()
	}
	send()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			send()
		}
	}
}

func writeSSE(w http.ResponseWriter, event string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
}

func reverse(in []logx.Entry) []logx.Entry {
	out := make([]logx.Entry, len(in))
	for i := range in {
		out[len(in)-1-i] = in[i]
	}
	return out
}

// ---------------------------------------------------------------------------
// Notification handlers
// ---------------------------------------------------------------------------

func (s *Server) handleNotifyTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "仅支持 POST")
		return
	}
	results := s.app.Notifier.Test()
	ok := false
	for _, r := range results {
		if r.OK {
			ok = true
		}
	}
	writeOK(w, map[string]any{"results": results, "anySuccess": ok})
}

func (s *Server) handleNotifyLog(w http.ResponseWriter, r *http.Request) {
	entries := s.app.Log.Query(logx.LevelInfo, "通知", 200)
	writeOK(w, map[string]any{"entries": entries})
}

// ---------------------------------------------------------------------------
// UI asset handlers
// ---------------------------------------------------------------------------

var allowedImageExt = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".webp": true,
	".gif": true, ".bmp": true, ".svg": true, ".avif": true,
}

const maxUploadBytes = 12 << 20

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "仅支持 POST")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
		writeErr(w, http.StatusBadRequest, "上传内容过大或格式不正确")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "未收到上传文件")
		return
	}
	defer file.Close()

	ext := strings.ToLower(filepath.Ext(header.Filename))
	if !allowedImageExt[ext] {
		writeErr(w, http.StatusBadRequest, "仅支持 PNG / JPG / WEBP / GIF / BMP / SVG 图片")
		return
	}
	name := "wp-" + time.Now().Format("20060102") + "-" + app.NewID("") + ext
	dst, err := os.Create(filepath.Join(s.uploadDir, name))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "保存文件失败: "+err.Error())
		return
	}
	defer dst.Close()
	written, err := io.Copy(dst, file)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "写入文件失败: "+err.Error())
		return
	}
	urlPath := "/uploads/" + name
	_ = s.app.UpdateConfig(func(cfg *config.Config) {
		cfg.UI.OnlineWallpapers = append(cfg.UI.OnlineWallpapers, urlPath)
		cfg.UI.Wallpaper = "custom"
		cfg.UI.WallpaperURL = urlPath
	})
	s.app.Logf(logx.LevelInfo, "面板", "已上传自定义图片 %s（%d 字节）", name, written)
	writeOK(w, map[string]any{"url": urlPath, "name": name, "size": written})
}

func (s *Server) handleWallpapers(w http.ResponseWriter, r *http.Request) {
	cfg := s.app.Cfg.Snapshot()
	writeOK(w, map[string]any{"wallpapers": cfg.UI.OnlineWallpapers})
}

func (s *Server) handleDeleteWallpaper(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete && r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "不支持的方法")
		return
	}
	target := r.URL.Query().Get("url")
	if !strings.HasPrefix(target, "/uploads/") {
		writeErr(w, http.StatusBadRequest, "只能删除已上传的图片")
		return
	}
	name := filepath.Base(target)
	if name == "" || strings.Contains(name, "..") {
		writeErr(w, http.StatusBadRequest, "文件名不合法")
		return
	}
	_ = os.Remove(filepath.Join(s.uploadDir, name))
	_ = s.app.UpdateConfig(func(cfg *config.Config) {
		kept := cfg.UI.OnlineWallpapers[:0]
		for _, u := range cfg.UI.OnlineWallpapers {
			if u != target {
				kept = append(kept, u)
			}
		}
		cfg.UI.OnlineWallpapers = kept
		if cfg.UI.WallpaperURL == target {
			cfg.UI.WallpaperURL = ""
			cfg.UI.Wallpaper = "w1"
		}
	})
	writeOK(w, nil)
}

// ---------------------------------------------------------------------------
// Account handlers
// ---------------------------------------------------------------------------

func (s *Server) handleAccount(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "仅支持 POST")
		return
	}
	var req struct {
		OldPassword string `json:"oldPassword"`
		NewUsername string `json:"newUsername"`
		NewPassword string `json:"newPassword"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	cfg := s.app.Cfg.Snapshot()
	if !checkPassword(cfg.Password, req.OldPassword) {
		writeErr(w, http.StatusBadRequest, "当前密码不正确")
		return
	}
	if req.NewUsername == "" && req.NewPassword == "" {
		writeErr(w, http.StatusBadRequest, "请填写新的账号或密码")
		return
	}
	if req.NewPassword != "" && len([]rune(req.NewPassword)) < 4 {
		writeErr(w, http.StatusBadRequest, "新密码长度至少 4 位")
		return
	}
	var newPassword string
	if req.NewPassword != "" {
		hashed, err := hashPassword(req.NewPassword)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "密码加密失败")
			return
		}
		newPassword = hashed
	}
	if err := s.app.UpdateConfig(func(c *config.Config) {
		if req.NewUsername != "" {
			c.Username = strings.TrimSpace(req.NewUsername)
		}
		if newPassword != "" {
			c.Password = newPassword
		}
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.app.Logf(logx.LevelWarn, "面板", "账号信息已更新")
	writeOK(w, nil)
}

// ---------------------------------------------------------------------------
// Diagnostics
// ---------------------------------------------------------------------------

func (s *Server) handlePing(w http.ResponseWriter, r *http.Request) {
	host := strings.TrimSpace(r.URL.Query().Get("host"))
	if host == "" {
		writeErr(w, http.StatusBadRequest, "缺少 host 参数")
		return
	}
	port := r.URL.Query().Get("port")
	target := host
	if port != "" {
		if _, err := strconv.Atoi(port); err != nil {
			writeErr(w, http.StatusBadRequest, "端口必须是数字")
			return
		}
		target = net.JoinHostPort(host, port)
	}
	start := time.Now()
	conn, err := net.DialTimeout("tcp", target, 4*time.Second)
	cost := time.Since(start)
	if err != nil {
		writeOK(w, map[string]any{"ok": false, "costMs": cost.Milliseconds(), "error": err.Error()})
		return
	}
	_ = conn.Close()
	writeOK(w, map[string]any{"ok": true, "costMs": cost.Milliseconds()})
}

// ---------------------------------------------------------------------------
// Static assets
// ---------------------------------------------------------------------------

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeErr(w, http.StatusNotFound, "接口不存在: "+r.URL.Path)
		return
	}
	data, err := s.assets.read("index.html")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "面板资源缺失")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
}

func (s *Server) handleFavicon(w http.ResponseWriter, r *http.Request) {
	data, err := s.assets.read("favicon.svg")
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(data)
}

// ServeStatic exposes an additional file from the embedded asset bundle.
func (s *Server) ServeStatic(name string) ([]byte, string, error) {
	data, err := s.assets.read(name)
	if err != nil {
		return nil, "", err
	}
	ctype := mime.TypeByExtension(filepath.Ext(name))
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	return data, ctype, nil
}

func goVersion() string {
	return runtimeVersion()
}

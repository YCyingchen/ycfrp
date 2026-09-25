package frp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	client "github.com/fatedier/frp/client"
	"github.com/fatedier/frp/pkg/config/source"
	v1 "github.com/fatedier/frp/pkg/config/v1"
	"github.com/fatedier/frp/pkg/config/v1/validation"
	"github.com/fatedier/frp/pkg/policy/security"
	goliblog "github.com/fatedier/golib/log"
	frplog "github.com/fatedier/frp/pkg/util/log"
	"github.com/fatedier/frp/server"

	"github.com/ycfrp/ycfrp/internal/logx"
)

// Engine 在同一个进程里监管多个具名 frps / frpc 实例。
//
// 面板按「实例」组织内核：一个 Engine 可以同时运行多个 frps 服务端与多个
// frpc 客户端。每个 frpc 实例拥有自己的隧道集合、各自连接各自的服务端；
// 每个 frps 实例拥有自己的仪表盘与流量统计。
//
// desired 是「期望状态」（由 Configure 写入），runtimes 是「实际状态」；
// 二者分开是为了让改通知设置之类的操作不会误杀掉正在跑的内核。
type Engine struct {
	mu     sync.RWMutex
	logger *logx.Logger

	desired  []Instance
	tunnels  []Tunnel
	order    []string
	runtimes map[string]*runtime

	traffic map[string]trafficSnapshot

	httpClient *http.Client
}

// runtime 承载单个实例的内核句柄与运行态。
type runtime struct {
	inst Instance

	tunnels []Tunnel

	serverSvc *server.Service
	clientSvc *client.Service
	cfgSource *source.ConfigSource

	ctx    context.Context
	cancel context.CancelFunc

	up          bool
	err         string
	tunnelCount int

	dash dashAuth
}

type dashAuth struct {
	addr string
	user string
	pass string
}

type trafficSnapshot struct {
	in    int64
	out   int64
	total int64
}

// New creates an engine writing kernel output into the supplied logger.
func New(logger *logx.Logger) *Engine {
	e := &Engine{
		logger:   logger,
		runtimes: make(map[string]*runtime),
		traffic:  make(map[string]trafficSnapshot),
		httpClient: &http.Client{
			Timeout: 3 * time.Second,
		},
	}
	e.attachKernelLogger()
	return e
}

// attachKernelLogger redirects the frp kernel logger into the panel logger so
// that every kernel line shows up in the unified Chinese log view.
func (e *Engine) attachKernelLogger() {
	if e.logger == nil {
		return
	}
	frplog.Logger = frplog.Logger.WithOptions(
		goliblog.WithOutput(&kernelWriter{logger: e.logger}),
	)
}

type kernelWriter struct{ logger *logx.Logger }

func (w *kernelWriter) Write(p []byte) (int, error) {
	for _, line := range strings.Split(string(p), "\n") {
		if strings.TrimSpace(line) == "" || isDashboardNoise(line) {
			continue
		}
		w.logger.LogRaw("内核", line)
	}
	return len(p), nil
}

// isDashboardNoise drops the access log lines produced by the frps dashboard
// HTTP service. The panel polls that API every few seconds and the resulting
// trace would otherwise bury real kernel events.
func isDashboardNoise(line string) bool {
	if !strings.Contains(line, "http/middleware.go") {
		return false
	}
	return strings.Contains(line, "http request:") || strings.Contains(line, "http response")
}

// Configure replaces the desired instance and tunnel set. It deliberately does
// not touch running kernels: the caller decides when to apply the change
// through Start or ApplyTunnels.
func (e *Engine) Configure(instances []Instance, tunnels []Tunnel) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.desired = append([]Instance(nil), instances...)
	e.tunnels = append([]Tunnel(nil), tunnels...)
}

// Apply reconciles the running instances with the desired state.
//
// 新增/编辑/删除单个实例时走这里，而不是把全部实例推倒重来：
// 只有真正发生变化（新增、删除、配置改动、启停翻转）的实例才会被重启，
// 其余实例的连接与隧道不受影响。这对多实例面板很关键 —— 否则改一个
// 实例就会把其它实例上正在跑的隧道全部抖断一次。
func (e *Engine) Apply(parent context.Context) error {
	e.mu.Lock()
	desired := append([]Instance(nil), e.desired...)
	tunnels := append([]Tunnel(nil), e.tunnels...)
	runtimes := make(map[string]*runtime, len(e.runtimes))
	for id, rt := range e.runtimes {
		runtimes[id] = rt
	}
	e.mu.Unlock()

	want := make(map[string]Instance, len(desired))
	order := make([]string, 0, len(desired))
	for _, inst := range desired {
		inst.Normalize()
		if inst.ID == "" {
			continue
		}
		want[inst.ID] = inst
		order = append(order, inst.ID)
	}

	var failures []string

	// 1) 删掉已不在期望集合里的实例。
	for id, rt := range runtimes {
		if _, keep := want[id]; keep {
			continue
		}
		rt.close()
		delete(runtimes, id)
		e.logf(logx.LevelInfo, rt.inst.KindLabel(), "实例 %s 已移除", rt.inst.Name)
	}

	// 2) 逐个实例收敛：启用的拉起/重启，停用的关停。
	for _, id := range order {
		inst := want[id]
		rt := runtimes[id]

		if !inst.Enabled() {
			if rt != nil && rt.up {
				rt.close()
				rt.inst = inst
				e.logf(logx.LevelInfo, inst.KindLabel(), "实例 %s 已停用", inst.Name)
			} else if rt != nil {
				rt.inst = inst
			} else {
				runtimes[id] = &runtime{inst: inst, dash: dashFor(inst)}
			}
			continue
		}

		// 已在运行、且配置没变的实例原地保留，不动它的连接。
		if rt != nil && rt.up && sameInstanceConfig(rt.inst, inst) {
			rt.inst = inst
			continue
		}
		if rt != nil {
			wasUp := rt.up
			rt.close()
			// 刚停用的实例马上重启时，旧内核释放端口需要一点时间，
			// 立刻绑定会误报「端口已被占用」。等它真正释放再启动。
			if wasUp && inst.IsServer() {
				waitPortsReleased(inst.Ports(), 5*time.Second)
			}
		} else {
			rt = &runtime{}
		}
		rt.inst = inst
		rt.dash = dashFor(inst)
		rt.tunnels = tunnelsFor(tunnels, inst.ID)
		rt.tunnelCount = len(rt.tunnels)

		if err := e.startInstance(parent, rt, tunnels); err != nil {
			rt.err = inst.DescribeStartError(err)
			rt.up = false
			e.logf(logx.LevelError, inst.KindLabel(), "实例 %s 启动失败: %v", inst.Name, rt.err)
			failures = append(failures, fmt.Sprintf("%s（%s）：%v", inst.Name, inst.KindLabel(), rt.err))
		} else {
			rt.err = ""
			rt.up = true
			e.logf(logx.LevelInfo, inst.KindLabel(), "实例 %s 已启动%s", inst.Name, rt.startSummary())
		}
		runtimes[id] = rt
	}

	e.mu.Lock()
	e.runtimes = runtimes
	e.order = order
	e.mu.Unlock()

	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "；"))
	}
	return nil
}

// sameInstanceConfig 判断两个实例的内核配置是否等价。
// 运行态字段（Running/Err/TunnelCount）不参与比较，名称与备注的改动
// 也不需要重启内核。
func sameInstanceConfig(a, b Instance) bool {
	if a.Kind != b.Kind || a.Enabled() != b.Enabled() {
		return false
	}
	if !reflect.DeepEqual(a.Server, b.Server) {
		return false
	}
	return reflect.DeepEqual(a.Client, b.Client)
}

// waitPortsReleased 等待指定端口全部可用（未被监听），最多等待 timeout。
//
// 服务端实例停用后内核异步关闭监听，若立刻重启会因端口仍在 TIME_WAIT /
// 尚未 close 而绑定失败。轮询检测端口空闲，超时则放行（让真正的绑定错误
// 原样暴露，不掩盖其它问题）。
func waitPortsReleased(ports []int, timeout time.Duration) {
	if len(ports) == 0 {
		return
	}
	deadline := time.Now().Add(timeout)
	for {
		busy := false
		for _, p := range ports {
			ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
			if err != nil {
				busy = true
				continue
			}
			_ = ln.Close()
		}
		if !busy || time.Now().After(deadline) {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// dashFor 组装某个实例的仪表盘访问参数。
func dashFor(inst Instance) dashAuth {	return dashAuth{
		addr: fmt.Sprintf("127.0.0.1:%d", inst.Server.DashboardPort),
		user: inst.Server.DashboardUser,
		pass: inst.Server.DashboardPwd,
	}
}

// Start launches every enabled instance from the desired state, tearing down
// whatever was running before. Used at boot and for an explicit full restart.
//
// 单个实例启动失败只记录在该实例上，不影响其它实例，避免一个配错的实例
// 把整个面板拖下水。所有失败会汇总成一条错误返回。
func (e *Engine) Start(parent context.Context) error {
	e.Stop()
	return e.Apply(parent)
}

// startSummary 生成实例启动后的一句话摘要，方便在日志里一眼看清绑了什么。
func (rt *runtime) startSummary() string {
	if rt.inst.IsServer() {
		s := fmt.Sprintf("，监听端口 %d", rt.inst.Server.BindPort)
		if rt.inst.Server.DashboardPort > 0 {
			s += fmt.Sprintf("，仪表盘端口 %d", rt.inst.Server.DashboardPort)
		}
		return s
	}
	return fmt.Sprintf("，共 %d 条隧道，连接 %s:%d",
		rt.tunnelCount, rt.inst.Client.ServerAddr, rt.inst.Client.ServerPort)
}

func (e *Engine) startInstance(parent context.Context, rt *runtime, all []Tunnel) error {
	ctx, cancel := context.WithCancel(parent)
	rt.ctx, rt.cancel = ctx, cancel

	var err error
	if rt.inst.IsServer() {
		err = e.startServer(ctx, rt)
	} else {
		err = e.startClient(ctx, rt, tunnelsFor(all, rt.inst.ID))
	}
	if err != nil {
		cancel()
		rt.cancel = nil
		return err
	}
	return nil
}

func (e *Engine) startServer(ctx context.Context, rt *runtime) error {
	svrCfg, err := BuildServerConfig(rt.inst.ServerInput())
	if err != nil {
		return err
	}
	svc, err := server.NewService(svrCfg)
	if err != nil {
		return fmt.Errorf("服务端初始化失败: %w", err)
	}
	go svc.Run(ctx)
	rt.serverSvc = svc
	return nil
}

func (e *Engine) startClient(ctx context.Context, rt *runtime, tunnels []Tunnel) error {
	proxies, visitors, err := buildProxies(tunnels)
	if err != nil {
		return err
	}
	commonCfg := BuildClientCommon(rt.inst.ClientInput())

	configSource := source.NewConfigSource()
	if err := configSource.ReplaceAll(proxies, visitors); err != nil {
		return fmt.Errorf("隧道配置不合法: %w", err)
	}
	aggregator := source.NewAggregator(configSource)
	unsafe := security.NewUnsafeFeatures(nil)

	loadedProxies, loadedVisitors, err := aggregator.Load()
	if err != nil {
		return fmt.Errorf("加载隧道配置失败: %w", err)
	}
	loadedProxies = completeProxies(loadedProxies)
	loadedVisitors = completeVisitors(loadedVisitors)
	if _, err := validation.ValidateAllClientConfig(commonCfg, loadedProxies, loadedVisitors, unsafe); err != nil {
		return fmt.Errorf("隧道参数不合法: %w", err)
	}

	svc, err := client.NewService(client.ServiceOptions{
		Common:                 commonCfg,
		ConfigSourceAggregator: aggregator,
		UnsafeFeatures:         unsafe,
	})
	if err != nil {
		return fmt.Errorf("客户端初始化失败: %w", err)
	}
	go func() {
		if err := svc.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			e.logf(logx.LevelError, "客户端", "实例 %s 运行中断: %v", rt.inst.Name, err)
		}
	}()

	rt.clientSvc = svc
	rt.cfgSource = configSource
	rt.tunnels = append([]Tunnel(nil), tunnels...)
	rt.tunnelCount = len(tunnels)
	return nil
}

// ApplyTunnels 把最新的隧道列表热更新到各客户端实例，不断开已建立的连接。
// 每条隧道通过 InstanceID 归属到具体实例，互不干扰。
func (e *Engine) ApplyTunnels(tunnels []Tunnel) error {
	e.mu.Lock()
	e.tunnels = append([]Tunnel(nil), tunnels...)
	runtimes := e.orderedRuntimesLocked()
	e.mu.Unlock()

	var firstErr error
	for _, rt := range runtimes {
		if rt.inst.IsServer() || rt.clientSvc == nil {
			continue
		}
		mine := tunnelsFor(tunnels, rt.inst.ID)
		if err := e.applyInstanceTunnels(rt, mine); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			e.logf(logx.LevelError, "客户端", "实例 %s 隧道热更新失败: %v", rt.inst.Name, err)
			continue
		}
		rt.tunnels = append([]Tunnel(nil), mine...)
		rt.tunnelCount = len(mine)
		e.logf(logx.LevelInfo, "客户端", "实例 %s 隧道已热更新，共 %d 条", rt.inst.Name, len(mine))
	}
	return firstErr
}

func (e *Engine) applyInstanceTunnels(rt *runtime, tunnels []Tunnel) error {
	proxies, visitors, err := buildProxies(tunnels)
	if err != nil {
		return err
	}
	commonCfg := BuildClientCommon(rt.inst.ClientInput())
	proxies = completeProxies(proxies)
	visitors = completeVisitors(visitors)
	if err := rt.clientSvc.UpdateConfigSource(commonCfg, proxies, visitors); err != nil {
		return fmt.Errorf("应用隧道配置失败: %w", err)
	}
	return nil
}

// Stop shuts down every running instance.
func (e *Engine) Stop() {
	e.mu.Lock()
	runtimes := e.orderedRuntimesLocked()
	e.runtimes = make(map[string]*runtime)
	e.order = nil
	e.mu.Unlock()

	for _, rt := range runtimes {
		rt.close()
	}
}

// close 释放单个实例的内核资源。先优雅关闭客户端，让已建立的代理连接
// 有机会收尾，再取消上下文并关闭服务端监听。
func (rt *runtime) close() {
	if rt.clientSvc != nil {
		rt.clientSvc.GracefulClose(500 * time.Millisecond)
	}
	if rt.cancel != nil {
		rt.cancel()
	}
	if rt.serverSvc != nil {
		_ = rt.serverSvc.Close()
	}
	rt.serverSvc = nil
	rt.clientSvc = nil
	rt.cfgSource = nil
	rt.cancel = nil
	rt.up = false
}

// orderedRuntimesLocked 按固定顺序返回运行中的实例，调用方需持有读锁。
func (e *Engine) orderedRuntimesLocked() []*runtime {
	out := make([]*runtime, 0, len(e.order))
	for _, id := range e.order {
		if rt := e.runtimes[id]; rt != nil {
			out = append(out, rt)
		}
	}
	return out
}

// Running reports whether any server and any client instance is active.
func (e *Engine) Running() (server, client bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	for _, rt := range e.orderedRuntimesLocked() {
		if !rt.up {
			continue
		}
		if rt.inst.IsServer() {
			server = true
		} else {
			client = true
		}
	}
	return server, client
}

// LastError returns the most recent startup failure message.
func (e *Engine) LastError() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	for _, rt := range e.orderedRuntimesLocked() {
		if rt.err != "" {
			return rt.err
		}
	}
	return ""
}

// InstanceStatuses 汇总每个实例的运行态，供实例管理页展示。
func (e *Engine) InstanceStatuses() []InstanceStatus {
	e.mu.RLock()
	runtimes := e.orderedRuntimesLocked()
	e.mu.RUnlock()

	out := make([]InstanceStatus, 0, len(runtimes))
	for _, rt := range runtimes {
		st := InstanceStatus{
			ID:          rt.inst.ID,
			Name:        rt.inst.Name,
			Kind:        rt.inst.Kind,
			Enable:      rt.inst.Enabled(),
			Running:     rt.up,
			Err:         rt.err,
			TunnelCount: rt.tunnelCount,
			Ports:       rt.inst.Ports(),
		}
		if rt.up && rt.inst.IsServer() {
			if info, err := e.fetchServerInfo(rt); err == nil {
				st.Server = info
			}
			if cls, err := e.fetchClients(rt); err == nil {
				st.Clients = cls
			}
		}
		out = append(out, st)
	}
	return out
}

// Snapshot collects the consolidated runtime view across every instance.
func (e *Engine) Snapshot() StatusResponse {
	e.mu.RLock()
	runtimes := e.orderedRuntimesLocked()
	tunnels := append([]Tunnel(nil), e.tunnels...)
	e.mu.RUnlock()

	out := StatusResponse{
		Tunnels:   tunnels,
		UpdatedAt: time.Now(),
	}

	// 所有服务端实例的仪表盘数据汇总到一张表。
	//
	// frp 服务端会为**已断开**的代理保留一条离线记录，因此不同服务端上可能
	// 同时存在同名代理：一个报 online（真正承载它的那台），另一个报 offline
	// （残留记录）。若直接按名字合并，残留记录会把在线状态覆盖掉，
	// 导致面板显示「隧道离线」而实际在跑。
	//
	// 因此合并时以「在线优先」为准，并且宁可保留带 ycfrpID 元数据的那条 ——
	// 它才是面板隧道真正的对应记录。
	stats := make(map[string]ProxyStatus)
	for _, rt := range runtimes {
		if !rt.up {
			continue
		}
		if !rt.inst.IsServer() {
			out.ClientRunning = true
			continue
		}
		out.ServerRunning = true
		if out.Server == nil {
			if info, err := e.fetchServerInfo(rt); err == nil {
				out.Server = info
			}
		}
		if s, err := e.fetchProxyStats(rt); err == nil {
			for k, v := range s {
				if prev, dup := stats[k]; dup && !preferProxy(v, prev) {
					continue
				}
				stats[k] = v
			}
		} else if !errors.Is(err, errDashboardDisabled) {
			e.logf(logx.LevelDebug, "监控", "实例 %s 读取隧道流量失败: %v", rt.inst.Name, err)
		}
		if cls, err := e.fetchClients(rt); err == nil {
			out.Clients = append(out.Clients, cls...)
		}
	}

	if len(stats) > 0 {
		out.Tunnels = mergeStats(out.Tunnels, stats)
	}
	// 客户端实例各自回填自己隧道的精确状态（远端 frps 的仪表盘这边读不到）。
	for _, rt := range runtimes {
		if rt.up && !rt.inst.IsServer() {
			e.fillClientStatus(rt, out.Tunnels)
		}
	}

	// A tunnel that neither a local dashboard nor its frpc reported on is not
	// currently reachable. Disabled entries are labelled separately so an
	// intentionally stopped tunnel is not mistaken for a failure.
	for i := range out.Tunnels {
		if out.Tunnels[i].Status != "" {
			continue
		}
		if out.Tunnels[i].External {
			out.Tunnels[i].Status = "offline"
			continue
		}
		if !out.Tunnels[i].Enabled {
			out.Tunnels[i].Status = "not running"
			continue
		}
		out.Tunnels[i].Status = "offline"
	}

	if out.Tunnels == nil {
		out.Tunnels = []Tunnel{}
	}
	if out.Clients == nil {
		out.Clients = []ClientStatus{}
	}

	e.publishTraffic(out.Tunnels)
	return out
}

// tunnelsFor 取出归属于某个实例的隧道。InstanceID 为空的隧道视为未归属，
// 不投递给任何实例，避免历史数据被误挂到随机实例上。
func tunnelsFor(tunnels []Tunnel, instanceID string) []Tunnel {
	if instanceID == "" {
		return nil
	}
	out := make([]Tunnel, 0, len(tunnels))
	for _, t := range tunnels {
		if t.InstanceID == instanceID {
			out = append(out, t)
		}
	}
	return out
}

// preferProxy 决定两个同名代理记录该保留哪一条。
//
// 顺序是：先看是否在线（在线的一定比残留的离线记录可信），
// 再看是否带面板写入的 ycfrpID 元数据（带元数据的才是本面板的隧道）。
// 两者都相同则保留先到的，保证结果稳定。
func preferProxy(candidate, current ProxyStatus) bool {
	rank := func(p ProxyStatus) int {
		score := 0
		if p.Status == "online" {
			score += 2
		}
		if p.Conf.Metadatas["ycfrpID"] != "" {
			score++
		}
		return score
	}
	return rank(candidate) > rank(current)
}

func mergeStats(tunnels []Tunnel, stats map[string]ProxyStatus) []Tunnel {
	if len(stats) == 0 {
		return tunnels
	}

	// The dashboard prefixes every proxy name with the owning frpc user, so a
	// name lookup alone cannot match a dashboard proxy back to its panel
	// entry. The tunnel id travels in the proxy metadata instead, with the
	// name treated as a best effort fallback.
	byID := make(map[string]dashEntry, len(stats))
	byName := make(map[string]dashEntry, len(stats))
	for name, st := range stats {
		byName[name] = dashEntry{key: name, st: st}
		if id := st.Conf.Metadatas["ycfrpID"]; id != "" {
			if _, dup := byID[id]; !dup {
				byID[id] = dashEntry{key: name, st: st}
			}
		}
	}

	out := make([]Tunnel, 0, len(tunnels))
	matched := make(map[string]bool, len(stats))
	for _, t := range tunnels {
		entry, ok := lookupProxy(byID, byName, t)
		if !ok {
			// The local dashboard only knows about proxies it hosts itself. A
			// tunnel whose frpc points at a remote frps therefore cannot be
			// judged here, so the status is left for fillClientStatus.
			out = append(out, t)
			continue
		}
		st := entry.st
		t.Status = st.Status
		t.CurConns = st.CurConns
		t.TodayIn = st.TodayTrafficIn
		t.TodayOut = st.TodayTrafficOut
		applyConf(&t, st)
		matched[entry.key] = true
		out = append(out, t)
	}

	// In frps-only deployments the panel owns no tunnel definitions, yet the
	// operator still wants to see every proxy the server is hosting. Proxies
	// that no local entry claimed are therefore surfaced as read-only rows.
	adopted := make([]Tunnel, 0, len(stats))
	for name, st := range stats {
		if matched[name] {
			continue
		}
		t := Tunnel{
			ID:       kernelID(name, st),
			Name:     stripUserPrefix(st.User, name),
			Type:     firstNonEmpty(st.Type, st.Conf.Type),
			Enabled:  true,
			External: true,
			Status:   st.Status,
			Note:     st.Conf.Metadatas["note"],
		}
		t.CurConns = st.CurConns
		t.TodayIn = st.TodayTrafficIn
		t.TodayOut = st.TodayTrafficOut
		applyConf(&t, st)
		adopted = append(adopted, t)
	}
	sort.Slice(adopted, func(i, j int) bool { return adopted[i].ID < adopted[j].ID })
	return append(out, adopted...)
}

// dashEntry pairs a dashboard map key with its decoded proxy.
type dashEntry struct {
	key string
	st  ProxyStatus
}

// lookupProxy resolves the dashboard entry belonging to one panel tunnel. The
// metadata id is authoritative; the name lookups only exist to stay compatible
// with clients that do not echo the panel metadata back.
func lookupProxy(byID map[string]dashEntry, byName map[string]dashEntry, t Tunnel) (dashEntry, bool) {
	if e, ok := byID[t.ID]; ok {
		return e, true
	}
	if e, ok := byName[t.Name]; ok {
		return e, true
	}
	// Fall back to the "<user>.<name>" shape when the owning user is unknown.
	suffix := "." + t.Name
	keys := make([]string, 0, 2)
	for key := range byName {
		if strings.HasSuffix(key, suffix) {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return dashEntry{}, false
	}
	// Several users can host a proxy of the same name; pick deterministically.
	sort.Strings(keys)
	return byName[keys[0]], true
}

// kernelID gives a dashboard-only proxy a stable identifier that cannot clash
// with a panel generated tunnel id.
func kernelID(dashName string, st ProxyStatus) string {
	if id := st.Conf.Metadatas["ycfrpID"]; id != "" {
		return id
	}
	return "kernel:" + dashName
}

// stripUserPrefix removes the "<user>." prefix the server adds to names.
func stripUserPrefix(user, name string) string {
	if user == "" {
		return name
	}
	return strings.TrimPrefix(name, user+".")
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// fillClientStatus backfills the runtime state that only the frpc kernel knows
// about, limited to the tunnels owned by this instance.
//
// 这一步对「frpc 连着远端 frps」的场景至关重要：那种情况下本机没有对应的
// 仪表盘，隧道的真实状态只能由客户端自己上报。
func (e *Engine) fillClientStatus(rt *runtime, tunnels []Tunnel) {
	svc := rt.clientSvc
	if svc == nil || len(tunnels) == 0 {
		return
	}
	exporter := svc.StatusExporter()
	if exporter == nil {
		return
	}
	for i := range tunnels {
		if tunnels[i].InstanceID != rt.inst.ID {
			continue
		}
		st, ok := exporter.GetProxyStatus(tunnels[i].Name)
		if !ok || st == nil {
			continue
		}
		switch {
		case st.Err != "":
			tunnels[i].Err = st.Err
			if tunnels[i].Status != "online" {
				tunnels[i].Status = "start error"
			}
		case tunnels[i].Status == "":
			tunnels[i].Status = clientPhase(st.Phase)
		}
		if tunnels[i].RemoteAddr == "" {
			tunnels[i].RemoteAddr = st.RemoteAddr
		}
	}
}

// clientPhase normalises the frpc proxy phase onto the vocabulary the rest of
// the panel already speaks, where "online" is the healthy state.
func clientPhase(phase string) string {
	switch strings.ToLower(strings.TrimSpace(phase)) {
	case "running":
		return "online"
	case "":
		return ""
	case "start error":
		return "start error"
	case "check failed":
		return "check failed"
	default:
		return phase
	}
}

// applyConf fills in fields the dashboard only exposes through the proxy
// definition. Values configured on the panel win, so a local edit is never
// overwritten by the kernel echo.
func applyConf(t *Tunnel, st ProxyStatus) {
	conf := st.Conf
	if t.LocalIP == "" {
		t.LocalIP = conf.LocalIP
	}
	if t.LocalPort == 0 {
		t.LocalPort = conf.LocalPort
	}
	if t.RemotePort == 0 {
		t.RemotePort = conf.RemotePort
	}
	if len(t.CustomDomains) == 0 {
		t.CustomDomains = conf.CustomDomains
	}
	if t.Subdomain == "" {
		t.Subdomain = conf.Subdomain
	}
	t.RemoteAddr = remoteAddrOf(*t, st)
}

// remoteAddrOf renders the address a client actually dials for this tunnel.
// frp v0.71.0 dropped the pre-composed remote_addr field, so it is rebuilt
// from the tunnel type: port based proxies use the remote port while domain
// based ones use their custom domain or subdomain.
func remoteAddrOf(t Tunnel, st ProxyStatus) string {
	switch strings.ToLower(t.Type) {
	case "tcp", "udp":
		port := t.RemotePort
		if port == 0 {
			port = st.Conf.RemotePort
		}
		if port == 0 {
			return ""
		}
		return ":" + strconv.Itoa(port)
	default:
		if len(t.CustomDomains) > 0 {
			return t.CustomDomains[0]
		}
		if t.Subdomain != "" {
			return t.Subdomain
		}
		return ""
	}
}

// publishTraffic records the aggregate throughput of all running tunnels.
func (e *Engine) publishTraffic(tunnels []Tunnel) {
	var totalIn, totalOut int64
	for _, t := range tunnels {
		totalIn += t.TodayIn
		totalOut += t.TodayOut
	}
	e.mu.Lock()
	e.traffic["_total"] = trafficSnapshot{in: totalIn, out: totalOut}
	e.mu.Unlock()
}

// ---------------------------------------------------------------------------
// Dashboard API access
// ---------------------------------------------------------------------------

var errDashboardDisabled = errors.New("仪表盘未启用")

// dashGet calls one of the frps dashboard JSON endpoints on the given
// instance. The v1 dashboard API (serverinfo / clients / proxy) answers with
// the bare payload rather than the {code,msg,data} envelope used by the v2
// routes, so the body is decoded straight into out. Errors are still reported
// through GeneralResponse, hence the code/msg fallback below.
func (e *Engine) dashGet(rt *runtime, path string, out any) error {
	dash := rt.dash
	if dash.addr == "" || strings.HasSuffix(dash.addr, ":0") {
		return errDashboardDisabled
	}
	u := "http://" + dash.addr + path
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	if dash.user != "" {
		req.SetBasicAuth(dash.user, dash.pass)
	}
	resp, err := e.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		var gen struct {
			Code int    `json:"Code"`
			Msg  string `json:"Msg"`
		}
		if json.Unmarshal(body, &gen) == nil && gen.Msg != "" {
			return fmt.Errorf("仪表盘返回错误 %d: %s", gen.Code, gen.Msg)
		}
		return fmt.Errorf("仪表盘返回状态 %d", resp.StatusCode)
	}
	if len(body) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("解析仪表盘数据失败: %w", err)
	}
	return nil
}

func (e *Engine) fetchServerInfo(rt *runtime) (*ServerInfo, error) {
	var info ServerInfo
	if err := e.dashGet(rt, "/api/serverinfo", &info); err != nil {
		return nil, err
	}
	return &info, nil
}

func (e *Engine) fetchProxyStats(rt *runtime) (map[string]ProxyStatus, error) {
	types := []string{"tcp", "udp", "http", "https", "tcpmux", "stcp", "xtcp", "sudp"}
	out := make(map[string]ProxyStatus)
	var firstErr error
	okAny := false
	for _, t := range types {
		var payload struct {
			Proxies []ProxyStatus `json:"proxies"`
		}
		if err := e.dashGet(rt, "/api/proxy/"+t, &payload); err != nil {
			if errors.Is(err, errDashboardDisabled) {
				return nil, err
			}
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		okAny = true
		for _, p := range payload.Proxies {
			// The dashboard groups proxies by the requested type, so the
			// response itself carries no reliable type field.
			p.Type = t
			if p.Conf.Type != "" {
				p.Type = p.Conf.Type
			}
			out[p.Name] = p
		}
	}
	if !okAny && firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

func (e *Engine) fetchClients(rt *runtime) ([]ClientStatus, error) {
	var raw []struct {
		Key              string `json:"key"`
		User             string `json:"user"`
		ClientID         string `json:"clientID"`
		Version          string `json:"version"`
		Hostname         string `json:"hostname"`
		ClientIP         string `json:"clientIP"`
		FirstConnectedAt int64  `json:"firstConnectedAt"`
		LastConnectedAt  int64  `json:"lastConnectedAt"`
		Online           bool   `json:"online"`
	}
	if err := e.dashGet(rt, "/api/clients", &raw); err != nil {
		return nil, err
	}
	out := make([]ClientStatus, 0, len(raw))
	for _, c := range raw {
		out = append(out, ClientStatus{
			Key:              c.Key,
			User:             c.User,
			ClientID:         c.ClientID,
			Version:          c.Version,
			Hostname:         c.Hostname,
			IP:               c.ClientIP,
			Online:           c.Online,
			FirstConnectedAt: formatUnix(c.FirstConnectedAt),
			LastConnectedAt:  formatUnix(c.LastConnectedAt),
		})
	}
	return out, nil
}

func formatUnix(ts int64) string {
	if ts <= 0 {
		return ""
	}
	return time.Unix(ts, 0).Format("2006-01-02 15:04:05")
}

// ServerDashboardURL exposes the dashboard address of the first running server
// instance, for deep links. Empty when no server instance has a dashboard.
func (e *Engine) ServerDashboardURL() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	for _, rt := range e.orderedRuntimesLocked() {
		if !rt.inst.IsServer() || rt.inst.Server.DashboardPort <= 0 {
			continue
		}
		u := url.URL{
			Scheme: "http",
			Host:   fmt.Sprintf("127.0.0.1:%d", rt.inst.Server.DashboardPort),
		}
		return u.String()
	}
	return ""
}

func (e *Engine) logf(level logx.Level, source, format string, args ...any) {
	if e.logger == nil {
		return
	}
	e.logger.Log(level, source, fmt.Sprintf(format, args...))
}

func buildProxies(tunnels []Tunnel) ([]v1.ProxyConfigurer, []v1.VisitorConfigurer, error) {
	proxies := make([]v1.ProxyConfigurer, 0, len(tunnels))
	for _, t := range tunnels {
		if strings.TrimSpace(t.Name) == "" {
			return nil, nil, fmt.Errorf("隧道名称不能为空")
		}
		p, err := TunnelToProxy(t)
		if err != nil {
			return nil, nil, fmt.Errorf("隧道 %s: %w", t.Name, err)
		}
		proxies = append(proxies, p)
	}
	return proxies, nil, nil
}

func completeProxies(in []v1.ProxyConfigurer) []v1.ProxyConfigurer {
	for _, p := range in {
		p.Complete()
	}
	return in
}

func completeVisitors(in []v1.VisitorConfigurer) []v1.VisitorConfigurer {
	for _, v := range in {
		v.Complete()
	}
	return in
}

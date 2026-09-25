package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ycfrp/ycfrp/internal/config"
	"github.com/ycfrp/ycfrp/internal/frp"
	"github.com/ycfrp/ycfrp/internal/logx"
	"github.com/ycfrp/ycfrp/internal/store"
)

// instancePath 返回实例集合的落盘位置。
func instancePath(dataDir string) string {
	return filepath.Join(dataDir, "instances.json")
}

// 旧版面板只有一份 frps 与一份 frpc 配置，没有「实例」概念。
// 升级到多实例后，仅在旧配置确实启用了对应角色（或存在旧隧道）时才创建实例，
// 全新安装不再生成「默认服务端/默认客户端」示例，由用户自己点新增。

// loadInstances 读取实例集合，并在需要时把旧版单实例配置迁移过来。
func loadInstances(dataDir string, cfg *config.Config, logger *logx.Logger) (*store.Collection[frp.Instance], error) {
	coll, err := store.NewCollection[frp.Instance](instancePath(dataDir))
	if err != nil {
		return nil, fmt.Errorf("读取实例列表失败: %w", err)
	}

	list := coll.List()
	if len(list) > 0 {
		// 已有实例则修一次 token 一致性（旧版本可能留下「服务端有 token、客户端空」的不一致状态）。
		fixTokenConsistency(coll, logger)
		return coll, nil
	}

	// 实例集合为空：仅当旧配置确有用得到实例的迹象时才迁移，否则留空。
	// 是否存在隧道由调用方读取后传入。
	created, err := migrateInstances(coll, cfg, len(tunnelList(dataDir)) > 0)
	if err != nil {
		return nil, err
	}
	for _, inst := range created {
		logger.Log(logx.LevelInfo, "实例", "已从原有配置创建实例 "+inst.DisplayName())
	}
	return coll, nil
}

// tunnelList 读取旧版隧道列表（读取失败视为空）。
func tunnelList(dataDir string) []frp.Tunnel {
	var list []frp.Tunnel
	data, err := os.ReadFile(filepath.Join(dataDir, "tunnels.json"))
	if err != nil {
		return nil
	}
	_ = json.Unmarshal(data, &list)
	return list
}

// migrateInstances 依据 config.json 里的 FRPS / FRPC 生成初始实例。
//
// 全新安装不生成任何实例（用户自己点「新增」创建第一个）；
// 只有旧版本升级上来、且确有使用痕迹（启用过角色或存在隧道）时才创建，
// 让老用户的环境原样保留。
func migrateInstances(coll *store.Collection[frp.Instance], cfg *config.Config, hasTunnels bool) ([]frp.Instance, error) {
	// 全新安装：config.json 是刚生成的默认值，没有任何使用痕迹。
	if !cfg.FRPS.Enable && !cfg.FRPC.Enable && !hasTunnels {
		return nil, nil
	}
	// 旧版默认配置里 frps.enable 就是 true，无法区分「全新安装」与
	// 「旧用户启用了服务端」。用「存在隧道或有非默认令牌」作为旧用户的判据；
	// 两者都没有时视为全新安装，不生成示例。
	hasCustomToken := strings.TrimSpace(cfg.FRPS.Token) != "" ||
		strings.TrimSpace(cfg.FRPC.Token) != ""
	if !hasTunnels && !hasCustomToken {
		return nil, nil
	}

	var created []frp.Instance

	// 服务端与客户端的 token 必须一致才能连通。老配置常出现「服务端有、
	// 客户端空」的不一致（历史 bug），迁移时统一对齐。
	serverToken := strings.TrimSpace(cfg.FRPS.Token)
	clientToken := strings.TrimSpace(cfg.FRPC.Token)
	if serverToken != "" && clientToken == "" {
		cfg.FRPC.Token = serverToken
	}
	if serverToken == "" && clientToken != "" {
		cfg.FRPS.Token = clientToken
	}

	if cfg.FRPS.Enable {
		server := frp.Instance{
			ID:     NewID("inst"),
			Name:   "服务端",
			Kind:   frp.KindServer,
			Server: cfg.FRPS,
		}
		if _, err := coll.Put(server); err != nil {
			return nil, fmt.Errorf("创建服务端实例失败: %w", err)
		}
		created = append(created, server)
	}

	if cfg.FRPC.Enable {
		client := frp.Instance{
			ID:     NewID("inst"),
			Name:   "客户端",
			Kind:   frp.KindClient,
			Client: cfg.FRPC,
		}
		if _, err := coll.Put(client); err != nil {
			return nil, fmt.Errorf("创建客户端实例失败: %w", err)
		}
		created = append(created, client)
	}

	return created, nil
}

// fixTokenConsistency 修掉老版本留下的「服务端有 token、客户端空」之类的不一致。
//
// 这是历史 bug 的收尾：面板曾给服务端 token 留空自动填随机值，导致服务端
// 与客户端 token 对不上、连不上。这里把「一边有、一边空」统一为同一边的值。
//
// 取「第一个」该类型实例参与对齐；实例本身必须找到（哪怕 token 为空），
// 否则「客户端全空」的老配置会因为没有非空实例可记录而被跳过。
func fixTokenConsistency(coll *store.Collection[frp.Instance], logger *logx.Logger) {
	var serverInst, clientInst *frp.Instance
	for _, inst := range coll.List() {
		c := inst
		if inst.IsServer() {
			if serverInst == nil {
				serverInst = &c
			}
		} else if clientInst == nil {
			clientInst = &c
		}
	}
	if serverInst == nil || clientInst == nil {
		return
	}
	serverToken := strings.TrimSpace(serverInst.Server.Token)
	clientToken := strings.TrimSpace(clientInst.Client.Token)

	switch {
	case serverToken != "" && clientToken == "":
		clientInst.Client.Token = serverInst.Server.Token
		_, _ = coll.Put(*clientInst)
		logger.Log(logx.LevelInfo, "实例", "已把客户端令牌与服务端对齐（修复旧版本不一致）")
	case clientToken != "" && serverToken == "":
		serverInst.Server.Token = clientInst.Client.Token
		_, _ = coll.Put(*serverInst)
		logger.Log(logx.LevelInfo, "实例", "已把服务端令牌与客户端对齐（修复旧版本不一致）")
	}
}

// adoptOrphanTunnels 把没有归属的隧道挂到默认客户端实例上。
//
// 旧版 tunnels.json 里没有 instanceId 字段，升级后这些隧道若不自报家门，
// 就会因为没有承载实例而永远显示离线。这里统一归到默认客户端。
func (a *App) adoptOrphanTunnels() error {
	if a.Instances == nil {
		return nil
	}
	// 只有存在客户端实例时才谈得上归属。
	client := a.FirstClientInstance()
	if client == nil {
		return nil
	}

	var changed []frp.Tunnel
	for _, t := range a.Tunnels.List() {
		if strings.TrimSpace(t.InstanceID) != "" {
			continue
		}
		t.InstanceID = client.ID
		changed = append(changed, t)
	}
	if len(changed) == 0 {
		return nil
	}
	for _, t := range changed {
		if _, err := a.Tunnels.Put(t); err != nil {
			return fmt.Errorf("迁移隧道 %s 归属失败: %w", t.Name, err)
		}
	}
	a.Logf(logx.LevelInfo, "实例", "已将 %d 条未归属隧道归入实例「%s」", len(changed), client.Name)
	return nil
}

// FirstClientInstance 返回第一个客户端实例，没有则返回 nil。
func (a *App) FirstClientInstance() *frp.Instance {
	for _, inst := range a.Instances.List() {
		if !inst.IsServer() {
			c := inst
			return &c
		}
	}
	return nil
}

// FirstServerInstance 返回第一个服务端实例，没有则返回 nil。
func (a *App) FirstServerInstance() *frp.Instance {
	for _, inst := range a.Instances.List() {
		if inst.IsServer() {
			c := inst
			return &c
		}
	}
	return nil
}

// instanceByID 按标识取出实例。
func (a *App) instanceByID(id string) (*frp.Instance, error) {
	inst, err := a.Instances.Get(id)
	if err != nil {
		return nil, fmt.Errorf("实例不存在")
	}
	return &inst, nil
}

// serverInstances / clientInstances 按类型拆分实例，供界面与引擎使用。
func (a *App) serverInstances() []frp.Instance {
	return a.instancesOfKind(true)
}

func (a *App) clientInstances() []frp.Instance {
	return a.instancesOfKind(false)
}

func (a *App) instancesOfKind(server bool) []frp.Instance {
	var out []frp.Instance
	for _, inst := range a.Instances.List() {
		if inst.IsServer() == server {
			out = append(out, inst)
		}
	}
	return out
}

// anyInstanceEnabled 报告是否存在任一已开启的实例，供启动横幅提示使用。
func (a *App) anyInstanceEnabled() bool {
	for _, inst := range a.Instances.List() {
		if inst.Enabled() {
			return true
		}
	}
	return false
}

// HasEnabledInstances 供 bootstrap 判断是否需要在启动横幅里提示去开启实例。
func (a *App) HasEnabledInstances() bool { return a.anyInstanceEnabled() }

// instanceName 取实例名，找不到时回退为标识，避免界面出现空白。
func (a *App) instanceName(id string) string {
	if inst, err := a.Instances.Get(id); err == nil {
		return inst.Name
	}
	return id
}

// SaveInstance 校验并保存一个实例，然后重载内核。
//
// 名称在同一类型内必须唯一：frps 与 frpc 可以同名（类型不同不冲突）。
func (a *App) SaveInstance(inst frp.Instance) (frp.Instance, error) {
	inst.Normalize()
	if inst.Kind == frp.KindServer && strings.TrimSpace(inst.Server.BindAddr) == "" {
		inst.Server.BindAddr = "0.0.0.0"
	}
	if err := inst.Validate(); err != nil {
		return frp.Instance{}, err
	}
	if err := a.checkInstanceConflicts(inst); err != nil {
		return frp.Instance{}, err
	}
	if inst.ID == "" {
		inst.ID = NewID("inst")
	}
	saved, err := a.Instances.Put(inst)
	if err != nil {
		return frp.Instance{}, err
	}
	a.Logf(logx.LevelInfo, "实例", "已保存实例 %s", saved.DisplayName())
	return saved, nil
}

// checkInstanceConflicts 拦截同类重名，以及服务端之间的端口冲突。
//
// 端口冲突必须在保存时就挡住：frp 内核在端口被占用时只会报一句底层
// bind 错误，用户很难意识到是自己新建的实例和已有实例抢了同一个端口。
func (a *App) checkInstanceConflicts(inst frp.Instance) error {
	for _, other := range a.Instances.List() {
		if other.ID == inst.ID {
			continue
		}
		if other.Kind == inst.Kind && strings.EqualFold(strings.TrimSpace(other.Name), inst.Name) {
			return fmt.Errorf("%s实例名称「%s」已存在", inst.KindLabel(), inst.Name)
		}
		if other.Kind != inst.Kind || !inst.IsServer() {
			continue
		}
		if !other.Enabled() || !inst.Enabled() {
			continue
		}
		if conflict, port := firstPortConflict(other.Ports(), inst.Ports()); conflict {
			return fmt.Errorf("端口 %d 已被实例「%s」占用，请换一个端口", port, other.Name)
		}
	}
	return nil
}

// firstPortConflict 找出两个实例之间第一个重复的端口。
func firstPortConflict(a, b []int) (bool, int) {
	seen := make(map[int]bool, len(a))
	for _, p := range a {
		seen[p] = true
	}
	for _, p := range b {
		if seen[p] {
			return true, p
		}
	}
	return false, 0
}

// DeleteInstance 删除实例，并连带清理它名下的隧道。
//
// 隧道不能留下成为孤儿：没有承载实例的隧道既启动不了，界面上也只会
// 显示为永久离线，不如随实例一并移除，并在日志里交代清楚。
func (a *App) DeleteInstance(id string) (removedTunnels int, err error) {
	inst, err := a.instanceByID(id)
	if err != nil {
		return 0, err
	}
	for _, t := range a.Tunnels.List() {
		if t.InstanceID != id {
			continue
		}
		if derr := a.Tunnels.Delete(t.ID); derr != nil {
			return removedTunnels, fmt.Errorf("删除隧道 %s 失败: %w", t.Name, derr)
		}
		removedTunnels++
	}
	if err := a.Instances.Delete(id); err != nil {
		return removedTunnels, err
	}
	a.Logf(logx.LevelWarn, "实例", "已删除实例 %s（同时移除 %d 条隧道）", inst.DisplayName(), removedTunnels)
	return removedTunnels, nil
}

// ToggleInstance 切换实例启停，并重启内核使其生效。
func (a *App) ToggleInstance(id string, enabled bool) (frp.Instance, error) {
	inst, err := a.instanceByID(id)
	if err != nil {
		return frp.Instance{}, err
	}
	inst.SetEnabled(enabled)
	if err := inst.Validate(); err != nil && enabled {
		return frp.Instance{}, err
	}
	if enabled {
		if err := a.checkInstanceConflicts(*inst); err != nil {
			return frp.Instance{}, err
		}
	}
	saved, err := a.Instances.Put(*inst)
	if err != nil {
		return frp.Instance{}, err
	}
	state := "已停用"
	if enabled {
		state = "已启用"
	}
	a.Logf(logx.LevelInfo, "实例", "实例 %s %s", saved.DisplayName(), state)
	return saved, nil
}

// InstanceStatuses 返回引擎视角的实例运行态。
func (a *App) InstanceStatuses() []frp.InstanceStatus {
	return a.Engine.InstanceStatuses()
}

// InstanceByID 按标识取出实例，供 API 层使用。
func (a *App) InstanceByID(id string) (frp.Instance, error) {
	return a.Instances.Get(id)
}

// UpdateInstance 就地修改一个实例并落盘。
//
// 与 SaveInstance 的区别是不做重名/端口冲突校验：这里服务于「把导入的
// 连接参数灌进实例」这类内部改写，字段值来自已存在的配置，不会引入冲突。
func (a *App) UpdateInstance(id string, fn func(*frp.Instance)) error {
	inst, err := a.instanceByID(id)
	if err != nil {
		return err
	}
	fn(inst)
	inst.Normalize()
	if err := inst.Validate(); err != nil {
		return err
	}
	if _, err := a.Instances.Put(*inst); err != nil {
		return err
	}
	a.applySettings()
	return nil
}

// ServerInstances 返回全部服务端实例。
func (a *App) ServerInstances() []frp.Instance { return a.serverInstances() }

// ClientInstances 返回全部客户端实例。
func (a *App) ClientInstances() []frp.Instance { return a.clientInstances() }

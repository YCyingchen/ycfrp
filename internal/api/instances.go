package api

import (
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/ycfrp/ycfrp/internal/frp"
	"github.com/ycfrp/ycfrp/internal/logx"
)

// ---------------------------------------------------------------------------
// 实例管理
//
// 面板支持同时托管多个具名 frps 服务端与 frpc 客户端。隧道归属到具体
// frpc 实例，因此 API 也需要暴露实例的增删改查与启停。
// ---------------------------------------------------------------------------

// handleInstances 处理 /api/instances 的列表与新增。
func (s *Server) handleInstances(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeOK(w, map[string]any{
			"instances": s.instanceViews(),
			"summary": map[string]any{
				"servers": len(s.app.ServerInstances()),
				"clients": len(s.app.ClientInstances()),
			},
		})
	case http.MethodPost:
		var inst frp.Instance
		if err := decodeBody(r, &inst); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if inst.ID != "" {
			writeErr(w, http.StatusBadRequest, "新增实例时不应携带标识")
			return
		}
		s.saveInstanceAndReload(w, inst, true)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "不支持的方法")
	}
}

// handleInstanceByID 处理 /api/instances/{id} 的读取、更新与删除。
func (s *Server) handleInstanceByID(w http.ResponseWriter, r *http.Request) {
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/instances/"), "/")
	if id == "" {
		writeErr(w, http.StatusBadRequest, "缺少实例标识")
		return
	}
	if strings.HasSuffix(id, "/toggle") {
		s.toggleInstance(w, r, strings.TrimSuffix(id, "/toggle"))
		return
	}
	if strings.HasSuffix(id, "/tunnels") {
		s.listInstanceTunnels(w, strings.TrimSuffix(id, "/tunnels"))
		return
	}

	switch r.Method {
	case http.MethodGet:
		inst, err := s.app.InstanceByID(id)
		if err != nil {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		writeOK(w, s.instanceView(inst))
	case http.MethodPut, http.MethodPost:
		var inst frp.Instance
		if err := decodeBody(r, &inst); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		inst.ID = id
		s.saveInstanceAndReload(w, inst, false)
	case http.MethodDelete:
		removed, err := s.app.DeleteInstance(id)
		if err != nil {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		// 删除会关停该实例并摘掉它的隧道，走增量收敛即可，
		// 不必惊动其它正在运行的实例。
		if err := s.app.ApplyInstances(); err != nil {
			writeOK(w, map[string]any{"removedTunnels": removed, "warning": err.Error()})
			return
		}
		writeOK(w, map[string]any{"removedTunnels": removed})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "不支持的方法")
	}
}

// saveInstanceAndReload 保存实例并按需重启内核。
func (s *Server) saveInstanceAndReload(w http.ResponseWriter, inst frp.Instance, isNew bool) {
	// 密钥类字段留空表示「保持不变」，与通知设置的处理保持一致。
	if !isNew {
		if old, err := s.app.InstanceByID(inst.ID); err == nil {
			keepInstanceSecrets(&inst, old)
		}
	}
	saved, err := s.app.SaveInstance(inst)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// 必须先收敛再取视图：视图里带运行态，顺序反了会把启动前的旧状态
	// 回给前端，界面上就会显示「已停止」。
	warn := ""
	if err := s.app.ApplyInstances(); err != nil {
		warn = "实例已保存，但内核未能启动：" + err.Error()
	}
	view := s.instanceView(saved)
	if warn != "" {
		// 实例确实存下了，只是内核没起来。用 200 + warning 表达这种
		// 「保存成功、启动失败」的部分成功，避免前端把已保存的改动当成失败
		// 而让用户重复提交。真实原因会以红字显示在实例卡片上。
		view["warning"] = warn
	}
	writeOK(w, view)
}

// keepInstanceSecrets 在密码/令牌留空时沿用原值。
//
// 这两个字段在界面上是密码框、不回显明文，前端提交时必然是空串；
// 若不特殊处理，用户每次点保存都会把已配好的令牌抹掉。
func keepInstanceSecrets(next *frp.Instance, old frp.Instance) {
	if strings.TrimSpace(next.Server.Token) == "" {
		next.Server.Token = old.Server.Token
	}
	if strings.TrimSpace(next.Server.DashboardPwd) == "" {
		next.Server.DashboardPwd = old.Server.DashboardPwd
	}
	if strings.TrimSpace(next.Client.Token) == "" {
		next.Client.Token = old.Client.Token
	}
	if next.Kind == "" {
		next.Kind = old.Kind
	}
}

// toggleInstance 启停单个实例。
func (s *Server) toggleInstance(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "仅支持 POST")
		return
	}
	var req struct {
		Enable *bool `json:"enable"`
	}
	// 不带 body 时视为取反，兼容老前端的简单调用。
	if err := decodeBody(r, &req); err != nil && !errors.Is(err, errEmptyBody) {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	inst, err := s.app.InstanceByID(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	enable := !inst.Enabled()
	if req.Enable != nil {
		enable = *req.Enable
	}
	saved, err := s.app.ToggleInstance(id, enable)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// 同样先收敛再取视图，否则开关会显示成切换前的状态。
	warn := ""
	if err := s.app.ApplyInstances(); err != nil {
		warn = err.Error()
	}
	view := s.instanceView(saved)
	if warn != "" {
		view["warning"] = warn
	}
	writeOK(w, view)
}

// instanceViews 返回带运行态的实例列表，按类型与名称排序，便于界面稳定展示。
func (s *Server) instanceViews() []map[string]any {
	statuses := make(map[string]frp.InstanceStatus)
	for _, st := range s.app.InstanceStatuses() {
		statuses[st.ID] = st
	}
	list := s.app.Instances.List()
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].Kind != list[j].Kind {
			// 服务端排前面，符合「先配服务端再配客户端」的直觉。
			return list[i].Kind == frp.KindServer
		}
		return list[i].Name < list[j].Name
	})
	out := make([]map[string]any, 0, len(list))
	for _, inst := range list {
		out = append(out, s.instanceViewWith(inst, statuses[inst.ID]))
	}
	return out
}

func (s *Server) instanceView(inst frp.Instance) map[string]any {
	var st frp.InstanceStatus
	for _, v := range s.app.InstanceStatuses() {
		if v.ID == inst.ID {
			st = v
			break
		}
	}
	return s.instanceViewWith(inst, st)
}

// instanceViewWith 组装单个实例的对外视图。
//
// 密钥一律不外发：令牌与仪表盘密码只回 hasToken / hasDashboardPwd 布尔标记，
// 让界面能显示「已配置」而看不到明文。
func (s *Server) instanceViewWith(inst frp.Instance, st frp.InstanceStatus) map[string]any {
	server := inst.Server
	client := inst.Client
	has := func(v string) bool { return strings.TrimSpace(v) != "" }
	return map[string]any{
		"id":        inst.ID,
		"name":      inst.Name,
		"kind":      inst.Kind,
		"kindLabel": inst.KindLabel(),
		"note":      inst.Note,
		"enabled":   inst.Enabled(),
		"running":   st.Running,
		"err":       st.Err,
		"ports":     inst.Ports(),
		"tunnelCount": st.TunnelCount,
		"createdAt": inst.CreatedAt,
		"updatedAt": inst.UpdatedAt,
		"server": map[string]any{
			"bindAddr": server.BindAddr, "bindPort": server.BindPort,
			"kcpBindPort": server.KCPBindPort, "quicBindPort": server.QUICBindPort,
			"dashboardPort": server.DashboardPort, "dashboardUser": server.DashboardUser,
			"vhostHttpPort": server.VhostHTTPPort, "vhostHttpsPort": server.VhostHTTPSPort,
			"subdomainHost": server.SubdomainHost,
			"maxPortsPerClient": server.MaxPortsPerClient,
			"allowPortsStart":   server.AllowPortsStart,
			"allowPortsEnd":     server.AllowPortsEnd,
			"logLevel":          server.LogLevel, "logMaxDays": server.LogMaxDays,
			"transportTLS": server.TransportTLS,
			"hasToken":     has(server.Token),
			"hasDashboardPwd": has(server.DashboardPwd),
			// 仪表盘概览仅服务端实例有。
			"info": st.Server,
		},
		"client": map[string]any{
			"serverAddr": client.ServerAddr, "serverPort": client.ServerPort,
			"user": client.User, "loginFailExit": client.LoginFailExit,
			"logLevel": client.LogLevel, "logMaxDays": client.LogMaxDays,
			"transportTLS": client.TransportTLS, "transportProtocol": client.TransportProto,
			"useEncryption": client.UseEncryption, "useCompression": client.UseCompression,
			"hasToken": has(client.Token),
		},
		"clients": st.Clients,
	}
}

// listInstanceTunnels 列出某个实例名下的隧道，供编辑页与隧道页筛选。
func (s *Server) listInstanceTunnels(w http.ResponseWriter, id string) {
	if id == "" {
		writeErr(w, http.StatusBadRequest, "缺少实例标识")
		return
	}
	if _, err := s.app.InstanceByID(id); err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	out := []frp.Tunnel{}
	for _, t := range s.app.Tunnels.List() {
		if t.InstanceID == id {
			out = append(out, t)
		}
	}
	writeOK(w, map[string]any{"tunnels": out, "instanceId": id})
}

// errEmptyBody 表示请求体为空，用于把「取反」与「显式传值」区分开。
var errEmptyBody = errors.New("空请求体")

// logInstanceChange 在日志里记录实例相关的变更，方便回溯。
func (s *Server) logInstanceChange(level logx.Level, format string, args ...any) {
	s.app.Logf(level, "实例", format, args...)
}

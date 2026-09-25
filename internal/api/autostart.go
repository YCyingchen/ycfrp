package api

import (
	"net/http"
	"runtime"

	"github.com/ycfrp/ycfrp/internal/autostart"
	"github.com/ycfrp/ycfrp/internal/logx"
)

// autostartSupported 报告当前平台是否支持自启动（仅 Windows）。
func autostartSupported() bool { return runtime.GOOS == "windows" }

// handleAutostart 读取/设置开机自启动（仅 Windows 生效）。
// GET 返回 {enabled, supported}；POST body {enabled:bool} 设置状态。
func (s *Server) handleAutostart(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeOK(w, map[string]any{
			"enabled":   autostart.Enabled(),
			"supported": autostartSupported(),
		})
	case http.MethodPost:
		var body struct {
			Enabled *bool `json:"enabled"`
		}
		if err := decodeBody(r, &body); err != nil || body.Enabled == nil {
			writeErr(w, http.StatusBadRequest, "缺少 enabled 字段")
			return
		}
		if err := autostart.Set(*body.Enabled); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if *body.Enabled {
			s.app.Logf(logx.LevelInfo, "面板", "已开启开机自启动")
		} else {
			s.app.Logf(logx.LevelInfo, "面板", "已关闭开机自启动")
		}
		writeOK(w, map[string]any{"enabled": autostart.Enabled()})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "不支持的方法")
	}
}

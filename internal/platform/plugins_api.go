package platform

import (
	"net/http"
	"strconv"
)

// builtinPlugin is a capability that ships with the binary. Channel adapters
// already speak the versioned process protocol, so they are listed here but
// configured on the channels page.
type builtinPlugin struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	State     string `json:"state"`
	ManagedBy string `json:"managed_by"`
	Note      string `json:"note"`
}

func (s *Server) builtinPlugins() []builtinPlugin {
	values := []builtinPlugin{}
	if s.channelProviders == nil {
		return values
	}
	for _, descriptor := range s.channelProviders.Descriptors() {
		values = append(values, builtinPlugin{
			ID:        "channel." + descriptor.ID,
			Name:      descriptor.Name,
			Type:      "channel",
			State:     "ready",
			ManagedBy: "channels",
			Note:      "随系统分发，在“渠道接入”中配置和启停。",
		})
	}
	return values
}

func (s *Server) listPlugins(w http.ResponseWriter, r *http.Request) {
	installed, err := s.plugins.List()
	if err != nil {
		internal(w)
		return
	}
	candidates, err := s.plugins.Discover()
	if err != nil {
		internal(w)
		return
	}
	respond(w, 200, map[string]any{"installed": installed, "candidates": candidates, "builtin": s.builtinPlugins()})
}

func (s *Server) installPlugin(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Version string `json:"version"`
	}
	if !decode(w, r, &input) {
		return
	}
	value, err := s.plugins.Install(r.Context(), r.PathValue("id"), input.Version)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	s.audit("plugin.install", value.ID, value.Version)
	respond(w, 200, value)
}

func (s *Server) enablePlugin(w http.ResponseWriter, r *http.Request) {
	value, err := s.plugins.SetEnabled(r.Context(), r.PathValue("id"), true)
	if err != nil {
		fail(w, 404, "扩展不存在。")
		return
	}
	s.audit("plugin.enable", value.ID, value.Version)
	respond(w, 200, value)
}

func (s *Server) disablePlugin(w http.ResponseWriter, r *http.Request) {
	value, err := s.plugins.SetEnabled(r.Context(), r.PathValue("id"), false)
	if err != nil {
		fail(w, 404, "扩展不存在。")
		return
	}
	s.audit("plugin.disable", value.ID, value.Version)
	respond(w, 200, value)
}

func (s *Server) rollbackPlugin(w http.ResponseWriter, r *http.Request) {
	value, err := s.plugins.Rollback(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, 409, err.Error())
		return
	}
	s.audit("plugin.rollback", value.ID, value.Version)
	respond(w, 200, value)
}

func (s *Server) uninstallPlugin(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.plugins.Uninstall(r.Context(), id); err != nil {
		internal(w)
		return
	}
	s.audit("plugin.uninstall", id, "")
	respond(w, 200, map[string]bool{"deleted": true})
}

func (s *Server) pluginEvents(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	values, err := s.plugins.Events(r.URL.Query().Get("plugin_id"), limit)
	if err != nil {
		internal(w)
		return
	}
	respond(w, 200, values)
}

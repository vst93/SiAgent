package platform

import (
	"net/http"
	"sort"
	"time"
)

// ProviderPreset describes a model provider the UI can offer. Base presets are
// always available; official presets ship with an optional provider pack so the
// first-run experience stays small.
type ProviderPreset struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Group         string `json:"group"`
	API           string `json:"api"`
	BaseURL       string `json:"base_url"`
	Documentation string `json:"documentation,omitempty"`
	Pack          string `json:"pack,omitempty"`
	Available     bool   `json:"available"`
}

// ProviderPack is an installable bundle of provider presets. Installing a pack
// only records it; no third-party code runs in M1.
type ProviderPack struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Providers   []string `json:"providers"`
	Installed   bool     `json:"installed"`
	InstalledAt int64    `json:"installed_at,omitempty"`
}

const piProvidersPack = "pi-providers"

var providerPresets = []ProviderPreset{
	{ID: "openai", Name: "OpenAI", Group: "base", API: "openai-completions", BaseURL: "https://api.openai.com/v1", Documentation: "https://platform.openai.com/docs/api-reference"},
	{ID: "anthropic", Name: "Anthropic", Group: "base", API: "anthropic-messages", BaseURL: "https://api.anthropic.com", Documentation: "https://docs.anthropic.com/en/api/messages"},
	{ID: "custom", Name: "自定义 OpenAI 兼容网关", Group: "base", API: "openai-completions"},

	{ID: "openrouter", Name: "OpenRouter", Group: "official", API: "openai-completions", BaseURL: "https://openrouter.ai/api/v1", Pack: piProvidersPack},
	{ID: "deepseek", Name: "DeepSeek", Group: "official", API: "openai-completions", BaseURL: "https://api.deepseek.com/v1", Pack: piProvidersPack},
	{ID: "groq", Name: "Groq", Group: "official", API: "openai-completions", BaseURL: "https://api.groq.com/openai/v1", Pack: piProvidersPack},
	{ID: "mistral", Name: "Mistral", Group: "official", API: "openai-completions", BaseURL: "https://api.mistral.ai/v1", Pack: piProvidersPack},
	{ID: "xai", Name: "xAI", Group: "official", API: "openai-completions", BaseURL: "https://api.x.ai/v1", Pack: piProvidersPack},
	{ID: "together", Name: "Together AI", Group: "official", API: "openai-completions", BaseURL: "https://api.together.xyz/v1", Pack: piProvidersPack},
	{ID: "fireworks", Name: "Fireworks", Group: "official", API: "openai-completions", BaseURL: "https://api.fireworks.ai/inference/v1", Pack: piProvidersPack},
	{ID: "google", Name: "Google Gemini (OpenAI 兼容)", Group: "official", API: "openai-completions", BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai", Pack: piProvidersPack},
	{ID: "ollama", Name: "Ollama（本地）", Group: "official", API: "openai-completions", BaseURL: "http://localhost:11434/v1", Pack: piProvidersPack},
	{ID: "vllm", Name: "vLLM（本地）", Group: "official", API: "openai-completions", BaseURL: "http://localhost:8000/v1", Pack: piProvidersPack},
	{ID: "lmstudio", Name: "LM Studio（本地）", Group: "official", API: "openai-completions", BaseURL: "http://localhost:1234/v1", Pack: piProvidersPack},
}

var providerPacks = []ProviderPack{
	{
		ID:          piProvidersPack,
		Name:        "Pi 模型供应商",
		Description: "OpenRouter、DeepSeek、Groq、Mistral、xAI、Together、Fireworks、Gemini、Ollama、vLLM、LM Studio 等供应商预设。安装后只增加配置项，不执行第三方代码。",
		Providers:   []string{"openrouter", "deepseek", "groq", "mistral", "xai", "together", "fireworks", "google", "ollama", "vllm", "lmstudio"},
	},
}

// providerAllowsAPI keeps protocol and provider consistent so a connection
// cannot claim Anthropic while sending OpenAI-shaped requests.
func providerAllowsAPI(id, api string) bool {
	switch id {
	case "anthropic":
		return api == "anthropic-messages"
	case "custom":
		return api == "openai-completions" || api == "openai-responses" || api == "anthropic-messages"
	default:
		return api == "openai-completions" || api == "openai-responses"
	}
}

func presetByID(id string) (ProviderPreset, bool) {
	for _, preset := range providerPresets {
		if preset.ID == id {
			return preset, true
		}
	}
	return ProviderPreset{}, false
}

// installedPacks returns the set of installed pack ids.
func (s *Store) installedPacks() (map[string]int64, error) {
	rows, err := s.db.Query("SELECT id,installed_at FROM provider_packs")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := map[string]int64{}
	for rows.Next() {
		var id string
		var at int64
		if err := rows.Scan(&id, &at); err != nil {
			return nil, err
		}
		values[id] = at
	}
	return values, rows.Err()
}

// providerAvailable reports whether a provider can be used for new runs.
func (s *Store) providerAvailable(id string) bool {
	preset, ok := presetByID(id)
	if !ok {
		return false
	}
	if preset.Pack == "" {
		return true
	}
	var count int
	if err := s.db.QueryRow("SELECT count(*) FROM provider_packs WHERE id=?", preset.Pack).Scan(&count); err != nil {
		return false
	}
	return count > 0
}

// resolveProvider normalizes legacy rows that predate the provider column.
func resolveProvider(id, api string) string {
	if preset, ok := presetByID(id); ok {
		return preset.ID
	}
	if api == "anthropic-messages" {
		return "anthropic"
	}
	return "openai"
}

func (s *Server) listModelProviders(w http.ResponseWriter, r *http.Request) {
	packs, err := s.store.installedPacks()
	if err != nil {
		internal(w)
		return
	}
	presets := make([]ProviderPreset, 0, len(providerPresets))
	for _, preset := range providerPresets {
		preset.Available = preset.Pack == "" || packs[preset.Pack] > 0
		presets = append(presets, preset)
	}
	values := make([]ProviderPack, 0, len(providerPacks))
	for _, pack := range providerPacks {
		at, ok := packs[pack.ID]
		pack.Installed = ok
		pack.InstalledAt = at
		values = append(values, pack)
	}
	sort.SliceStable(presets, func(i, j int) bool { return presets[i].ID < presets[j].ID })
	respond(w, 200, map[string]any{"presets": presets, "packs": values})
}

func (s *Server) installProviderPack(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	known := false
	for _, pack := range providerPacks {
		if pack.ID == id {
			known = true
			break
		}
	}
	if !known {
		fail(w, 404, "供应商扩展不存在。")
		return
	}
	if _, err := s.store.db.Exec("INSERT OR IGNORE INTO provider_packs(id,installed_at) VALUES(?,?)", id, time.Now().UnixMilli()); err != nil {
		internal(w)
		return
	}
	s.listModelProviders(w, r)
}

func (s *Server) uninstallProviderPack(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == piProvidersPack {
		var used int
		if err := s.store.db.QueryRow("SELECT count(*) FROM models WHERE provider IN ('openrouter','deepseek','groq','mistral','xai','together','fireworks','google','ollama','vllm','lmstudio')").Scan(&used); err != nil {
			internal(w)
			return
		}
		if used > 0 {
			fail(w, 409, "仍有模型连接使用该扩展的供应商，请先改用其它供应商。")
			return
		}
	}
	if _, err := s.store.db.Exec("DELETE FROM provider_packs WHERE id=?", id); err != nil {
		internal(w)
		return
	}
	s.listModelProviders(w, r)
}

// modelProviderError explains why a saved connection cannot run. An empty
// string means the provider is usable.
func (s *Store) modelProviderError(model Model) string {
	if s.providerAvailable(model.Provider) {
		return ""
	}
	if preset, ok := presetByID(model.Provider); ok && preset.Pack != "" {
		return "该模型使用的供应商扩展未加载，请在“模型连接”中安装供应商扩展后重试。"
	}
	return "模型供应商无效，请重新选择供应商。"
}

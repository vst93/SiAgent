package platform

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"siagent/internal/selfupdate"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/bcrypt"
	"siagent/internal/channels"
	"siagent/internal/knowledge"
	"siagent/internal/plugins"
	agentruntime "siagent/internal/runtime"
)

type Runtime interface{ Send(any) error }
type Server struct {
	updater            *selfupdate.Manager
	updateStop         func()
	updateGate         sync.RWMutex
	updating           atomic.Bool
	updateDraining     atomic.Bool
	maintenanceWork    sync.Mutex
	channelConfigMu    sync.Mutex
	channelProviders   *channels.Registry
	plugins            *plugins.Registry
	channelTests       map[string]bool
	channelConnections channelConnections
	channelWork        sync.Mutex
	// drafts remembers when a run last staged a channel draft, so progressive
	// updates stay throttled without touching the database per token.
	drafts                map[string]draftMark
	store                 *Store
	runtime               Runtime
	ready                 func() bool
	mu                    sync.Mutex
	token                 string
	publicURL             string
	secure                bool
	loginFailures         int
	loginWindow           time.Time
	limits                Limits
	configuredLimits      Limits
	attachmentLimits      AttachmentLimits
	stopping              bool
	advancedToolsDisabled bool
	apiRate               map[string]*rateWindow
	apiRateMu             sync.Mutex
	lastStatus            time.Time
	restarting            bool
	schedulerCtx          context.Context
	pendingTests          map[string]chan agentruntime.Event
	knowledge             *knowledge.Core
	knowledgePending      map[string]context.CancelFunc
	knowledgeWork         sync.WaitGroup
}

func New(store *Store, runtime Runtime, ready func() bool, publicURL string, options ...ServerOptions) (*Server, error) {
	configuredLimits := defaultLimits()
	attachmentLimits := defaultAttachmentLimits()
	knowledgeLimits := knowledge.DefaultLimits()
	if len(options) > 0 {
		if options[0].Limits != nil {
			configuredLimits = *options[0].Limits
		}
		attachmentLimits = mergeAttachmentLimits(options[0].Attachments)
		if options[0].Knowledge != nil {
			knowledgeLimits = options[0].Knowledge.Normalize()
		}
	}
	s := &Server{pendingTests: make(map[string]chan agentruntime.Event), schedulerCtx: context.Background(), store: store, runtime: runtime, ready: ready, publicURL: strings.TrimRight(publicURL, "/"), configuredLimits: configuredLimits, attachmentLimits: attachmentLimits, limits: configuredLimits}
	s.advancedToolsDisabled = advancedToolsDisabledConfig(options)
	s.channelConnections.values = make(map[string]channelConnection)
	s.channelTests = make(map[string]bool)
	s.drafts = make(map[string]draftMark)
	if err := s.ConfigureChannelAdapters("node", "runtime/dist/channel-wecom.js"); err != nil {
		return nil, err
	}
	if publicURL != "" {
		parsed, err := url.Parse(publicURL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
			return nil, fmt.Errorf("public URL must be an http(s) origin without a path")
		}
		s.publicURL = parsed.Scheme + "://" + parsed.Host
		s.secure = parsed.Scheme == "https"
	}
	var initialized int
	if err := store.db.QueryRow("SELECT count(*) FROM admin").Scan(&initialized); err != nil {
		return nil, err
	}
	if initialized == 0 {
		s.token = id()
	}
	limits, err := s.loadLimits()
	if err != nil {
		return nil, err
	}
	s.limits = limits
	if knowledgeLimits, err = s.loadKnowledgeLimits(knowledgeLimits); err != nil {
		return nil, err
	}
	core, err := knowledge.New(store.db, store.directory, knowledgeLimits)
	if err != nil {
		return nil, err
	}
	registry, err := plugins.New(store.db, store.directory)
	if err != nil {
		return nil, err
	}
	s.plugins = registry
	registry.SetCipher(store.encrypt, store.decrypt)
	core.SetExternalParser(func(ctx context.Context, ext, path string) (string, error) {
		value, ok := registry.ParserFor("." + ext)
		if !ok {
			return "", errors.New("no enabled parser extension")
		}
		return registry.Parse(ctx, value, path)
	})
	core.SetReranker(&modelReranker{store: store})
	s.knowledge = core
	s.knowledgePending = make(map[string]context.CancelFunc)
	if err := s.interruptAll("服务已重启，未完成任务不会自动重试；请核对已有内容后重新发起。", true); err != nil {
		return nil, err
	}

	if err := s.recoverChannels(); err != nil {
		return nil, err
	}
	return s, nil
}

// SetupToken is printed once to the local terminal, never exposed by HTTP.
func (s *Server) SetupToken() string { return s.token }
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /api/updates", s.authorize(http.HandlerFunc(s.updateStatus)))
	mux.Handle("POST /api/updates/start", s.authorize(http.HandlerFunc(s.startUpdate)))
	mux.Handle("POST /api/updates/check", s.authorize(http.HandlerFunc(s.checkUpdate)))
	mux.Handle("POST /api/updates/prepare", s.authorize(http.HandlerFunc(s.prepareUpdate)))
	mux.Handle("POST /api/updates/cancel", s.authorize(http.HandlerFunc(s.cancelUpdate)))
	mux.Handle("POST /api/updates/apply", s.authorize(http.HandlerFunc(s.applyUpdate)))
	mux.HandleFunc("GET /api/auth/status", s.authStatus)
	mux.HandleFunc("POST /api/auth/setup", s.setup)
	mux.HandleFunc("POST /api/auth/login", s.login)
	mux.Handle("POST /api/auth/logout", s.authorize(http.HandlerFunc(s.logout)))
	mux.Handle("GET /api/model", s.authorize(http.HandlerFunc(s.getModel)))
	mux.Handle("PUT /api/model", s.authorize(http.HandlerFunc(s.saveModel)))
	mux.Handle("POST /api/model/test", s.authorize(http.HandlerFunc(s.testModel)))
	mux.Handle("GET /api/agent", s.authorize(http.HandlerFunc(s.getAgent)))
	mux.Handle("PUT /api/agent", s.authorize(http.HandlerFunc(s.saveAgent)))
	mux.Handle("GET /api/conversations", s.authorize(http.HandlerFunc(s.listConversations)))
	mux.Handle("POST /api/conversations", s.authorize(http.HandlerFunc(s.createConversation)))
	mux.Handle("DELETE /api/conversations/{id}", s.authorize(http.HandlerFunc(s.deleteConversation)))
	mux.Handle("GET /api/storage", s.authorize(http.HandlerFunc(s.storage)))
	mux.Handle("GET /api/conversations/{id}", s.authorize(http.HandlerFunc(s.getConversation)))
	mux.Handle("GET /api/conversations/{id}/events", s.authorize(http.HandlerFunc(s.events)))
	mux.Handle("POST /api/conversations/{id}/attachments", s.authorize(http.HandlerFunc(s.uploadAttachment)))
	mux.Handle("GET /api/attachments/{id}", s.authorize(http.HandlerFunc(s.serveAttachment)))
	mux.Handle("DELETE /api/attachments/{id}", s.authorize(http.HandlerFunc(s.deleteAttachment)))
	mux.Handle("POST /api/conversations/{id}/runs", s.authorize(http.HandlerFunc(s.startRun)))
	mux.Handle("POST /api/runs/{id}/cancel", s.authorize(http.HandlerFunc(s.cancelRun)))
	mux.Handle("GET /api/runs/{id}", s.authorize(http.HandlerFunc(s.runDetails)))
	mux.Handle("GET /api/runs/{id}/evidence", s.authorize(http.HandlerFunc(s.runEvidence)))
	mux.Handle("GET /api/knowledge/gaps", s.authorize(http.HandlerFunc(s.listGaps)))
	mux.Handle("POST /api/knowledge/gaps/{id}/status", s.authorize(http.HandlerFunc(s.setGapStatus)))
	mux.Handle("DELETE /api/knowledge/gaps/{id}", s.authorize(http.HandlerFunc(s.deleteGap)))
	mux.Handle("GET /api/diagnostics", s.authorize(http.HandlerFunc(s.diagnostics)))
	mux.Handle("GET /api/skills", s.authorize(http.HandlerFunc(s.listSkills)))
	mux.Handle("POST /api/skills", s.authorize(http.HandlerFunc(s.importSkill)))
	mux.Handle("POST /api/skills/import", s.authorize(http.HandlerFunc(s.importSkill)))
	mux.Handle("GET /api/skills/{id}/files/{path...}", s.authorize(http.HandlerFunc(s.skillFile)))
	mux.Handle("PUT /api/skills/{id}/files/{path...}", s.authorize(http.HandlerFunc(s.updateSkillFile)))
	mux.Handle("GET /api/channel-providers", s.authorize(http.HandlerFunc(s.listChannelProviders)))
	mux.Handle("GET /api/channels", s.authorize(http.HandlerFunc(s.listGenericChannels)))
	mux.Handle("POST /api/channels", s.authorize(http.HandlerFunc(s.saveGenericChannel)))
	mux.Handle("PUT /api/channels/{channel}", s.authorize(http.HandlerFunc(s.saveGenericChannel)))
	mux.Handle("DELETE /api/channels/{channel}", s.authorize(http.HandlerFunc(s.archiveChannel)))
	mux.Handle("POST /api/channels/{channel}/test", s.authorize(http.HandlerFunc(s.testGenericChannel)))
	mux.Handle("GET /api/channels/{channel}/diagnostics", s.authorize(http.HandlerFunc(s.channelDiagnostics)))
	mux.Handle("POST /api/channel-deliveries/{delivery}/retry", s.authorize(http.HandlerFunc(s.retryChannelSend)))
	mux.Handle("GET /api/wecom/channels", s.authorize(http.HandlerFunc(s.listChannels)))
	mux.Handle("POST /api/wecom/channels", s.authorize(http.HandlerFunc(s.saveChannel)))
	mux.Handle("PUT /api/wecom/channels/{channel}", s.authorize(http.HandlerFunc(s.saveChannel)))
	mux.Handle("DELETE /api/wecom/channels/{channel}", s.authorize(http.HandlerFunc(s.archiveChannel)))
	mux.Handle("POST /api/wecom/channels/{channel}/test", s.authorize(http.HandlerFunc(s.testChannel)))
	mux.Handle("GET /api/wecom/channels/{channel}/diagnostics", s.authorize(http.HandlerFunc(s.channelDiagnostics)))
	mux.Handle("POST /api/wecom/deliveries/{delivery}/retry", s.authorize(http.HandlerFunc(s.retryChannelSend)))
	mux.Handle("GET /api/models", s.authorize(http.HandlerFunc(s.listModels)))
	mux.Handle("GET /api/model-providers", s.authorize(http.HandlerFunc(s.listModelProviders)))
	mux.Handle("GET /api/api-keys", s.authorize(http.HandlerFunc(s.listAPIKeys)))
	mux.Handle("POST /api/api-keys", s.authorize(http.HandlerFunc(s.createAPIKey)))
	mux.Handle("DELETE /api/api-keys/{id}", s.authorize(http.HandlerFunc(s.revokeAPIKey)))
	mux.Handle("GET /api/api-keys/{id}/reveal", s.authorize(http.HandlerFunc(s.revealAPIKey)))
	mux.Handle("DELETE /api/api-keys/{id}/permanent", s.authorize(http.HandlerFunc(s.deleteAPIKey)))
	mux.Handle("POST /v1/agents/{agent}/runs", s.integration(scopeAgentRun, s.v1CreateRun))
	mux.Handle("GET /v1/runs/{run}", s.integration(scopeAgentRead, s.v1GetRun))
	mux.Handle("GET /v1/runs/{run}/events", s.integration(scopeAgentRead, s.v1RunEvents))
	mux.Handle("POST /v1/runs/{run}/cancel", s.integration(scopeAgentRun, s.v1CancelRun))
	mux.Handle("GET /v1/knowledge/bases", s.integration(scopeKnowledgeRead, s.v1ListKnowledge))
	mux.Handle("POST /v1/knowledge/{base}/documents", s.integration(scopeKnowledgeWrite, s.v1UploadDocument))
	mux.Handle("GET /v1/knowledge/documents/{document}", s.integration(scopeKnowledgeRead, s.v1DocumentStatus))
	mux.Handle("DELETE /v1/knowledge/documents/{document}", s.integration(scopeKnowledgeWrite, s.v1DeleteDocument))
	mux.Handle("POST /v1/knowledge/{base}/search", s.integration(scopeKnowledgeRead, s.v1Search))
	mux.Handle("GET /v1/knowledge/sources/{source}", s.integration(scopeKnowledgeRead, s.v1Source))
	mux.Handle("POST /v1/chat/completions", s.integration(scopeAgentRun, s.openAICompat))
	mux.HandleFunc("GET /v1/openapi.yaml", s.serveOpenAPI)
	mux.Handle("/mcp", s.mcpAuth(s.mcpHandler))
	mux.Handle("GET /api/webhooks", s.authorize(http.HandlerFunc(s.listWebhooks)))
	mux.Handle("POST /api/webhooks", s.authorize(http.HandlerFunc(s.saveWebhook)))
	mux.Handle("PUT /api/webhooks/{id}", s.authorize(http.HandlerFunc(s.saveWebhook)))
	mux.Handle("DELETE /api/webhooks/{id}", s.authorize(http.HandlerFunc(s.deleteWebhook)))
	mux.Handle("POST /api/webhooks/{id}/test", s.authorize(http.HandlerFunc(s.sendTestWebhook)))
	mux.Handle("GET /api/webhook-deliveries", s.authorize(http.HandlerFunc(s.listWebhookDeliveries)))
	mux.Handle("GET /api/plugins", s.authorize(http.HandlerFunc(s.listPlugins)))
	mux.Handle("GET /api/plugins/events", s.authorize(http.HandlerFunc(s.pluginEvents)))
	mux.Handle("POST /api/plugins/upload", s.authorize(http.HandlerFunc(s.uploadPlugin)))
	mux.Handle("POST /api/plugins/{id}/install", s.authorize(http.HandlerFunc(s.installPlugin)))
	mux.Handle("PUT /api/plugins/{id}/config", s.authorize(http.HandlerFunc(s.savePluginConfig)))
	mux.Handle("POST /api/plugins/{id}/health", s.authorize(http.HandlerFunc(s.checkPluginHealth)))
	mux.Handle("POST /api/plugins/{id}/enable", s.authorize(http.HandlerFunc(s.enablePlugin)))
	mux.Handle("POST /api/plugins/{id}/disable", s.authorize(http.HandlerFunc(s.disablePlugin)))
	mux.Handle("POST /api/plugins/{id}/rollback", s.authorize(http.HandlerFunc(s.rollbackPlugin)))
	mux.Handle("DELETE /api/plugins/{id}", s.authorize(http.HandlerFunc(s.uninstallPlugin)))
	mux.Handle("POST /api/model-providers/packs/{id}/install", s.authorize(http.HandlerFunc(s.installProviderPack)))
	mux.Handle("DELETE /api/model-providers/packs/{id}", s.authorize(http.HandlerFunc(s.uninstallProviderPack)))
	mux.Handle("POST /api/models/discover", s.authorize(http.HandlerFunc(s.discoverModels)))
	mux.Handle("POST /api/models", s.authorize(http.HandlerFunc(s.saveModel)))
	mux.Handle("PUT /api/models/{model}", s.authorize(http.HandlerFunc(s.saveModel)))
	mux.Handle("POST /api/models/{model}/test", s.authorize(http.HandlerFunc(s.testModel)))
	mux.Handle("DELETE /api/models/{model}", s.authorize(http.HandlerFunc(s.archiveModel)))
	mux.Handle("GET /api/agents", s.authorize(http.HandlerFunc(s.listAgents)))
	mux.Handle("POST /api/agents", s.authorize(http.HandlerFunc(s.saveAgent)))
	mux.Handle("GET /api/agents/{agent}", s.authorize(http.HandlerFunc(s.getAgent)))
	mux.Handle("GET /api/agents/{agent}/environment", s.authorize(http.HandlerFunc(s.revealAgentEnvironment)))
	mux.Handle("POST /api/auth/confirm", s.authorize(http.HandlerFunc(s.confirmPassword)))
	mux.Handle("PUT /api/agents/{agent}", s.authorize(http.HandlerFunc(s.saveAgent)))
	mux.Handle("DELETE /api/agents/{agent}", s.authorize(http.HandlerFunc(s.archiveAgent)))
	mux.Handle("GET /api/agents/{agent}/capabilities", s.authorize(http.HandlerFunc(s.getCapabilities)))
	mux.Handle("PUT /api/agents/{agent}/capabilities", s.authorize(http.HandlerFunc(s.saveCapabilities)))
	mux.Handle("GET /api/agents/{agent}/knowledge", s.authorize(http.HandlerFunc(s.getKnowledgeBindings)))
	mux.Handle("PUT /api/agents/{agent}/knowledge", s.authorize(http.HandlerFunc(s.saveKnowledgeBindings)))
	mux.Handle("GET /api/mcps", s.authorize(http.HandlerFunc(s.listMCPs)))
	mux.Handle("POST /api/mcps", s.authorize(http.HandlerFunc(s.saveMCP)))
	mux.Handle("PUT /api/mcps/{id}", s.authorize(http.HandlerFunc(s.saveMCP)))
	mux.Handle("POST /api/mcps/{id}/test", s.authorize(http.HandlerFunc(s.testMCP)))
	mux.Handle("DELETE /api/{kind}/{id}", s.authorize(http.HandlerFunc(s.deleteCapability)))
	mux.Handle("GET /api/skills/{id}/agents", s.authorize(http.HandlerFunc(s.linkedCapabilityAgents)))
	mux.Handle("GET /api/mcps/{id}/agents", s.authorize(http.HandlerFunc(s.linkedCapabilityAgents)))
	mux.Handle("DELETE /api/skills/{id}/agents/{agent}", s.authorize(http.HandlerFunc(s.unlinkCapabilityAgent)))
	mux.Handle("DELETE /api/mcps/{id}/agents/{agent}", s.authorize(http.HandlerFunc(s.unlinkCapabilityAgent)))
	mux.Handle("GET /api/agent/capabilities", s.authorize(http.HandlerFunc(s.getCapabilities)))
	mux.Handle("PUT /api/agent/capabilities", s.authorize(http.HandlerFunc(s.saveCapabilities)))
	mux.Handle("GET /api/runs/{id}/tools/{call}", s.authorize(http.HandlerFunc(s.toolDetails)))
	mux.Handle("GET /api/knowledge/bases", s.authorize(http.HandlerFunc(s.listKnowledge)))
	mux.Handle("POST /api/knowledge/bases", s.authorize(http.HandlerFunc(s.saveKnowledge)))
	mux.Handle("PUT /api/knowledge/bases/{id}", s.authorize(http.HandlerFunc(s.saveKnowledge)))
	mux.Handle("DELETE /api/knowledge/bases/{id}", s.authorize(http.HandlerFunc(s.deleteKnowledge)))
	mux.Handle("GET /api/knowledge/status", s.authorize(http.HandlerFunc(s.knowledgeStatus)))
	mux.Handle("GET /api/knowledge/limits", s.authorize(http.HandlerFunc(s.getKnowledgeLimits)))
	mux.Handle("PUT /api/knowledge/limits", s.authorize(http.HandlerFunc(s.saveKnowledgeLimits)))
	mux.Handle("GET /api/knowledge/bases/{id}/documents", s.authorize(http.HandlerFunc(s.listDocuments)))
	mux.Handle("POST /api/knowledge/bases/{id}/documents", s.authorize(http.HandlerFunc(s.uploadDocument)))
	mux.Handle("PUT /api/knowledge/bases/{id}/documents/{document}", s.authorize(http.HandlerFunc(s.uploadDocument)))
	mux.Handle("DELETE /api/knowledge/documents/{document}", s.authorize(http.HandlerFunc(s.deleteDocument)))
	mux.Handle("POST /api/knowledge/documents/batch-delete", s.authorize(http.HandlerFunc(s.batchDeleteDocuments)))
	mux.Handle("POST /api/knowledge/documents/{document}/retry", s.authorize(http.HandlerFunc(s.retryDocument)))
	mux.Handle("POST /api/knowledge/search", s.authorize(http.HandlerFunc(s.searchKnowledge)))
	mux.Handle("GET /api/knowledge/bases/{id}/eval/questions", s.authorize(http.HandlerFunc(s.listEvalQuestions)))
	mux.Handle("POST /api/knowledge/bases/{id}/eval/questions", s.authorize(http.HandlerFunc(s.saveEvalQuestion)))
	mux.Handle("PUT /api/knowledge/bases/{id}/eval/questions/{question}", s.authorize(http.HandlerFunc(s.saveEvalQuestion)))
	mux.Handle("DELETE /api/knowledge/eval/questions/{question}", s.authorize(http.HandlerFunc(s.deleteEvalQuestion)))
	mux.Handle("POST /api/knowledge/bases/{id}/conflicts/detect", s.authorize(http.HandlerFunc(s.detectConflicts)))
	mux.Handle("GET /api/knowledge/bases/{id}/conflicts", s.authorize(http.HandlerFunc(s.listConflicts)))
	mux.Handle("POST /api/knowledge/conflicts/{id}/status", s.authorize(http.HandlerFunc(s.setConflictStatus)))
	mux.Handle("GET /api/knowledge/outdated", s.authorize(http.HandlerFunc(s.listOutdatedSources)))
	mux.Handle("GET /api/knowledge/bases/{id}/vocabulary/candidates", s.authorize(http.HandlerFunc(s.listVocabCandidates)))
	mux.Handle("GET /api/knowledge/bases/{id}/config", s.authorize(http.HandlerFunc(s.getBaseConfig)))
	mux.Handle("PUT /api/knowledge/bases/{id}/config", s.authorize(http.HandlerFunc(s.saveBaseConfig)))
	mux.Handle("GET /api/knowledge/bases/{id}/vocabulary", s.authorize(http.HandlerFunc(s.listVocabulary)))
	mux.Handle("POST /api/knowledge/bases/{id}/vocabulary", s.authorize(http.HandlerFunc(s.saveVocabulary)))
	mux.Handle("DELETE /api/knowledge/bases/{id}/vocabulary", s.authorize(http.HandlerFunc(s.deleteVocabulary)))
	mux.Handle("POST /api/knowledge/bases/{id}/eval/questions/import", s.authorize(http.HandlerFunc(s.importEvalQuestions)))
	mux.Handle("GET /api/knowledge/bases/{id}/eval/questions/export", s.authorize(http.HandlerFunc(s.exportEvalQuestions)))
	mux.Handle("POST /api/knowledge/bases/{id}/eval/run", s.authorize(http.HandlerFunc(s.runEval)))
	mux.Handle("POST /api/knowledge/bases/{id}/eval/answers", s.authorize(http.HandlerFunc(s.runAnswerEval)))
	mux.Handle("GET /api/knowledge/bases/{id}/eval/runs", s.authorize(http.HandlerFunc(s.listEvalRuns)))
	mux.Handle("GET /api/knowledge/eval/runs/{run}", s.authorize(http.HandlerFunc(s.getEvalRun)))
	mux.Handle("GET /api/knowledge/sources/{source}", s.authorize(http.HandlerFunc(s.readKnowledgeSource)))
	mux.Handle("GET /api/agent/knowledge", s.authorize(http.HandlerFunc(s.getKnowledgeBindings)))
	mux.Handle("PUT /api/agent/knowledge", s.authorize(http.HandlerFunc(s.saveKnowledgeBindings)))
	mux.Handle("GET /api/limits", s.authorize(http.HandlerFunc(s.getLimits)))
	mux.Handle("PUT /api/limits", s.authorize(http.HandlerFunc(s.saveLimits)))
	guarded := s.updateBarrier(mux)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// Integration API uses Bearer keys, not cookies, so browser origin rules
		// do not apply to /v1.
		if r.Method != "GET" && !strings.HasPrefix(r.URL.Path, "/v1/") && !s.sameOrigin(r) {
			fail(w, 403, "请求来源不匹配，请从配置的管理地址访问。")
			return
		}
		guarded.ServeHTTP(w, r)
	})
}
func (s *Server) sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return r.Header.Get("Sec-Fetch-Site") != "cross-site"
	}
	expected := s.publicURL
	if expected == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		expected = scheme + "://" + r.Host
	}
	return origin == expected
}
func hashToken(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func (s *Server) session(r *http.Request) (string, bool) {
	cookie, err := r.Cookie("siagent_session")
	if err != nil {
		return "", false
	}
	var csrf string
	err = s.store.db.QueryRow("SELECT csrf FROM sessions WHERE token_hash=? AND expires>?", hashToken(cookie.Value), time.Now().Unix()).Scan(&csrf)
	return csrf, err == nil
}
func (s *Server) authorize(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		csrf, ok := s.session(r)
		if !ok {
			fail(w, 401, "请先登录管理员账户。")
			return
		}
		if r.Method != "GET" && subtle.ConstantTimeCompare([]byte(csrf), []byte(r.Header.Get("X-CSRF-Token"))) != 1 {
			fail(w, 403, "安全校验失败，请刷新页面后重试。")
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (s *Server) issueSession(w http.ResponseWriter) (string, error) {
	token, csrf := id(), id()
	expires := time.Now().Add(8 * time.Hour)
	tx, err := s.store.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("DELETE FROM sessions WHERE expires<=?", time.Now().Unix()); err != nil {
		return "", err
	}
	// Name the columns: later migrations add columns to this table.
	if _, err = tx.Exec("INSERT INTO sessions(token_hash,csrf,expires) VALUES (?,?,?)", hashToken(token), csrf, expires.Unix()); err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	http.SetCookie(w, &http.Cookie{Name: "siagent_session", Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: s.secure, MaxAge: 8 * 60 * 60, Expires: expires})
	return csrf, nil
}
func (s *Server) authStatus(w http.ResponseWriter, r *http.Request) {
	var initialized int
	if err := s.store.db.QueryRow("SELECT count(*) FROM admin").Scan(&initialized); err != nil {
		internal(w)
		return
	}
	csrf, ok := s.session(r)
	respond(w, 200, map[string]any{"initialized": initialized > 0, "authenticated": ok, "csrf": csrf, "requires_setup_token": true})
}
func (s *Server) setup(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Password string `json:"password"`
		Token    string `json:"token"`
	}
	if !decode(w, r, &input) {
		return
	}
	if len(input.Password) < 12 || len(input.Password) > 72 {
		fail(w, 400, "密码需要 12–72 字节，请使用较长且独立的密码。")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var count int
	if err := s.store.db.QueryRow("SELECT count(*) FROM admin").Scan(&count); err != nil {
		internal(w)
		return
	}
	if count > 0 {
		fail(w, 409, "管理员已设置，请直接登录。")
		return
	}
	if subtle.ConstantTimeCompare([]byte(input.Token), []byte(s.token)) != 1 {
		fail(w, 403, "首次启动令牌无效，请查看服务启动终端。")
		return
	}
	password, err := bcrypt.GenerateFromPassword([]byte(input.Password), 12)
	if err != nil {
		internal(w)
		return
	}
	if _, err := s.store.db.Exec("INSERT INTO admin VALUES(1,?)", password); err != nil {
		internal(w)
		return
	}
	s.token = ""
	csrf, err := s.issueSession(w)
	if err != nil {
		internal(w)
		return
	}
	respond(w, 201, map[string]string{"csrf": csrf})
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Password string `json:"password"`
	}
	if !decode(w, r, &input) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if time.Since(s.loginWindow) > 15*time.Minute {
		s.loginFailures = 0
		s.loginWindow = time.Now()
	}
	if s.loginFailures >= 10 {
		w.Header().Set("Retry-After", "900")
		fail(w, 429, "登录失败次数过多，请在 15 分钟后重试。")
		return
	}
	var hash []byte
	err := s.store.db.QueryRow("SELECT password_hash FROM admin WHERE id=1").Scan(&hash)
	if err == sql.ErrNoRows {
		fail(w, 409, "请先设置管理员密码。")
		return
	}
	if err != nil {
		internal(w)
		return
	}
	if len(input.Password) > 72 || bcrypt.CompareHashAndPassword(hash, []byte(input.Password)) != nil {
		s.loginFailures++
		fail(w, 401, "密码不正确，请检查后重试。")
		return
	}
	s.loginFailures = 0
	csrf, err := s.issueSession(w)
	if err != nil {
		internal(w)
		return
	}
	respond(w, 200, map[string]string{"csrf": csrf})
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	cookie, _ := r.Cookie("siagent_session")
	if _, err := s.store.db.Exec("DELETE FROM sessions WHERE token_hash=?", hashToken(cookie.Value)); err != nil {
		internal(w)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "siagent_session", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteStrictMode})
	respond(w, 200, map[string]bool{"ok": true})
}
func decode(w http.ResponseWriter, r *http.Request, target any) bool {
	if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
		fail(w, 415, "请使用 JSON 请求。")
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		fail(w, 400, "请求内容无效或超过大小限制。")
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		fail(w, 400, "请求只能包含一个 JSON 对象。")
		return false
	}
	return true
}
func respond(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}
func fail(w http.ResponseWriter, status int, message string) {
	respond(w, status, map[string]string{"error": message})
}
func internal(w http.ResponseWriter) {
	slog.Error("platform operation failed")
	fail(w, 500, "服务暂时无法完成操作，请检查数据目录和服务状态后重试。")
}

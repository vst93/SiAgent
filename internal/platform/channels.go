package platform

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"siagent/internal/channels"
)

type Channel struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Provider     string            `json:"provider"`
	Config       map[string]string `json:"config"`
	AgentID      string            `json:"agent_id"`
	Enabled      bool              `json:"enabled"`
	Archived     bool              `json:"archived"`
	Version      int               `json:"version"`
	Verified     bool              `json:"verified"`
	HasSecrets   bool              `json:"has_secrets"`
	Available    bool              `json:"available"`
	CallbackPath string            `json:"callback_path,omitempty"`
	Status       channels.Status   `json:"status"`
}

func (s *Server) loadChannel(ctx context.Context, id string) (Channel, map[string]string, error) {
	var c Channel
	var config, encrypted string
	err := s.store.db.QueryRowContext(ctx, "SELECT id,name,provider,config,agent_id,enabled,archived,version,verified,secrets FROM channels WHERE id=?", id).Scan(&c.ID, &c.Name, &c.Provider, &config, &c.AgentID, &c.Enabled, &c.Archived, &c.Version, &c.Verified, &encrypted)
	if err != nil {
		return c, nil, err
	}
	if err = json.Unmarshal([]byte(config), &c.Config); err != nil {
		return c, nil, err
	}
	raw, err := s.store.decrypt(encrypted)
	if err != nil {
		return c, nil, err
	}
	secrets := map[string]string{}
	if err = json.Unmarshal([]byte(raw), &secrets); err != nil {
		return c, nil, err
	}
	c.HasSecrets = len(secrets) > 0
	provider, available := s.channelProviders.Get(c.Provider)
	c.Available = available
	if callback, ok := provider.(channels.CallbackProvider); ok {
		c.CallbackPath = callback.CallbackPath(c.ID)
	}
	return c, secrets, nil
}
func (s *Server) listChannelProviders(w http.ResponseWriter, r *http.Request) {
	respond(w, 200, s.channelProviders.Descriptors())
}
func (s *Server) listGenericChannels(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.db.QueryContext(r.Context(), "SELECT id FROM channels ORDER BY rowid")
	if err != nil {
		internal(w)
		return
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) != nil {
			rows.Close()
			internal(w)
			return
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		internal(w)
		return
	}
	values := []Channel{}
	for _, id := range ids {
		c, _, err := s.loadChannel(r.Context(), id)
		if err != nil {
			internal(w)
			return
		}
		c.Status = s.channelConnectionStatus(id)
		if !c.Available {
			c.Status = channels.Status{State: "unavailable", Message: "适配器未安装，配置和历史仍保留。"}
		}
		values = append(values, c)
	}
	respond(w, 200, values)
}

type channelInput struct {
	Name     string            `json:"name"`
	Provider string            `json:"provider"`
	Config   map[string]string `json:"config"`
	Secrets  map[string]string `json:"secrets"`
	AgentID  string            `json:"agent_id"`
	Enabled  bool              `json:"enabled"`
	Confirm  bool              `json:"confirm"`
}

func (s *Server) saveGenericChannel(w http.ResponseWriter, r *http.Request) {
	var input channelInput
	if !decode(w, r, &input) {
		return
	}
	c, err := s.persistChannel(r.Context(), r.PathValue("channel"), input)
	if err != nil {
		writeAdmissionError(w, err)
		return
	}
	status := 200
	if r.Method == "POST" {
		status = 201
	}
	respond(w, status, c)
}
func writeAdmissionError(w http.ResponseWriter, err error) {
	var rejected *admissionError
	if errors.As(err, &rejected) {
		fail(w, rejected.Status, rejected.Message)
	} else {
		internal(w)
	}
}
func (s *Server) persistChannel(ctx context.Context, channelID string, input channelInput) (Channel, error) {
	s.channelConfigMu.Lock()
	defer s.channelConfigMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	bad := func(status int, message string) (Channel, error) { return Channel{}, &admissionError{status, message} }
	if s.channelTests[channelID] {
		return Channel{}, &admissionError{409, "渠道正在连接测试，请等待结束后再保存。"}
	}
	version := 1
	verified := false
	secrets := map[string]string{}
	identity := ""
	var old Channel
	if channelID != "" {
		var err error
		old, secrets, err = s.loadChannel(ctx, channelID)
		if err == sql.ErrNoRows {
			return bad(404, "渠道不存在。")
		}
		if err != nil {
			return Channel{}, err
		}
		if old.Archived {
			return bad(409, "渠道已归档。")
		}
		if old.Provider != input.Provider {
			return bad(409, "渠道类型保存后固定，其他渠道请新增。")
		}
		if old.Enabled && old.AgentID != input.AgentID {
			return bad(409, "请先停用渠道，再修改助手绑定。")
		}
		version = old.Version + 1
		verified = old.Verified
	} else {
		channelID = id()
	}
	originalSecrets, _ := json.Marshal(secrets)
	if input.Config == nil {
		input.Config = map[string]string{}
	}
	for key, value := range input.Secrets {
		if value != "" {
			if secrets[key] != value {
				verified = false
			}
			secrets[key] = value
		}
	}
	provider, available := s.channelProviders.Get(input.Provider)
	if !available {
		if old.ID == "" || input.Enabled {
			return bad(409, "渠道适配器未安装，无法启用。")
		}
		input.Config = old.Config
		secrets = map[string]string{}
		_ = json.Unmarshal(originalSecrets, &secrets)
		if err := s.store.db.QueryRowContext(ctx, "SELECT identity FROM channels WHERE id=?", old.ID).Scan(&identity); err != nil {
			return Channel{}, err
		}
	} else {
		var err error
		config := channels.Config{ID: channelID, Provider: input.Provider, Version: version, Values: input.Config, Secrets: secrets}
		identity, err = channels.ValidateFields(provider.Descriptor(), config)
		if err != nil {
			return bad(400, err.Error())
		}
		if len(identity) > 512 {
			return bad(400, "渠道标识过长。")
		}
		if err := provider.Validate(config); err != nil {
			return bad(400, err.Error())
		}
		if old.ID != "" {
			oldIdentity, _ := channels.ValidateFields(provider.Descriptor(), channels.Config{Values: old.Config, Secrets: secrets})
			if oldIdentity != identity {
				return bad(409, "连接标识保存后固定，其他账号或机器人请新增渠道。")
			}
		}
	}
	if old.ID != "" {
		updatedSecrets, _ := json.Marshal(secrets)
		transportChanged := !reflect.DeepEqual(old.Config, input.Config) || string(updatedSecrets) != string(originalSecrets)
		if old.Enabled && input.Enabled && transportChanged {
			return bad(409, "请先停用渠道，再修改连接参数或密钥。")
		}
		if !transportChanged && old.Enabled == input.Enabled && old.AgentID == input.AgentID {
			version = old.Version
		}
	}
	if input.AgentID != "" {
		a, err := s.agentByID(input.AgentID)
		if err == sql.ErrNoRows || a.Archived {
			return bad(400, "请选择未归档的助手。")
		}
		if err != nil {
			return Channel{}, err
		}
		if input.Enabled && !a.Enabled {
			return bad(409, "绑定助手已停用，请先启用助手。")
		}
	}
	if input.Enabled && (input.AgentID == "" || !input.Confirm) {
		return bad(400, "启用前请选择助手，并确认成员消息及检索内容会发送至该助手配置的模型服务。")
	}
	name := strings.TrimSpace(input.Name)
	if name == "" && available {
		name = provider.Descriptor().Name + " · " + strings.ReplaceAll(identity, "\x1f", "/")
		if utf8.RuneCountInString(name) > 100 {
			name = string([]rune(name)[:100])
		}
	}
	if name == "" {
		name = old.Name
	}
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) > 100 || strings.ContainsAny(name, "\x00\r\n") {
		return bad(400, "渠道名称须为不超过 100 字的单行文本。")
	}
	var duplicate int
	if err := s.store.db.QueryRowContext(ctx, "SELECT count(*) FROM channels WHERE provider=? AND identity=? AND archived=0 AND id<>?", input.Provider, identity, channelID).Scan(&duplicate); err != nil {
		return Channel{}, err
	}
	if duplicate > 0 {
		return bad(409, "同一连接标识已有渠道，请编辑既有配置。")
	}
	raw, _ := json.Marshal(secrets)
	config, _ := json.Marshal(input.Config)
	if len(raw)+len(config) > 128*1024 {
		return bad(400, "渠道配置和密钥合计不能超过 128 KiB。")
	}
	_, err := s.store.db.ExecContext(ctx, "INSERT INTO channels(id,name,provider,identity,config,agent_id,enabled,secrets,version,verified) VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name,config=excluded.config,agent_id=excluded.agent_id,enabled=excluded.enabled,secrets=excluded.secrets,version=excluded.version,verified=excluded.verified", channelID, name, input.Provider, identity, string(config), input.AgentID, input.Enabled, s.store.encrypt(string(raw)), version, verified)
	if err != nil {
		return Channel{}, err
	}
	c, _, err := s.loadChannel(ctx, channelID)
	return c, err
}
func (s *Server) testGenericChannel(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("channel")
	s.channelConfigMu.Lock()
	c, secret, err := s.loadChannel(r.Context(), channelID)
	if err != nil || c.Archived {
		s.channelConfigMu.Unlock()
		fail(w, 404, "渠道不存在或已归档。")
		return
	}
	provider, ok := s.channelProviders.Get(c.Provider)
	if !ok {
		s.channelConfigMu.Unlock()
		fail(w, 409, "渠道适配器未安装。")
		return
	}
	_, readOnlyTest := provider.(channels.CredentialTester)
	if c.Enabled && !readOnlyTest {
		s.channelConfigMu.Unlock()
		status := s.channelConnectionStatus(c.ID)
		if status.State != "connected" && status.State != "webhook" {
			fail(w, 409, status.Message)
			return
		}
		respond(w, 200, map[string]string{"message": "渠道已启用，当前连接状态：" + status.Message})
		return
	}
	if s.channelTests[channelID] {
		s.channelConfigMu.Unlock()
		fail(w, 409, "该渠道正在连接测试。")
		return
	}
	s.channelTests[channelID] = true
	s.channelConfigMu.Unlock()
	defer func() { s.channelConfigMu.Lock(); delete(s.channelTests, channelID); s.channelConfigMu.Unlock() }()
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	config := channels.Config{ID: c.ID, Provider: c.Provider, Version: c.Version, Values: c.Config, Secrets: secret}
	if tester, ok := provider.(channels.CredentialTester); ok {
		result := tester.Test(ctx, config)
		if result.State != "sent" {
			fail(w, 502, result.Message)
		} else {
			respond(w, 200, map[string]string{"message": result.Message})
		}
		return
	}
	connection, err := provider.Open(ctx, config, nil)
	if err != nil {
		fail(w, 502, "无法启动渠道适配器。")
		return
	}
	defer connection.Close()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			fail(w, 502, "渠道连接或认证未在时限内完成，请检查凭据和网络。")
			return
		case <-ticker.C:
			status := connection.Status()
			if status.State == "connected" {
				respond(w, 200, map[string]string{"message": "连接和认证成功；测试连接已关闭，不发送业务消息。"})
				return
			}
			if status.State == "failed" || status.State == "disconnected" {
				fail(w, 502, status.Message)
				return
			}
		}
	}
}

func (s *Server) receiveChannel(ctx context.Context, channelID string, version int, in channels.Incoming) error {
	s.updateGate.RLock()
	defer s.updateGate.RUnlock()
	if s.updating.Load() || s.updateDraining.Load() {
		return errors.New("server updating")
	}
	if err := normalizeIncoming(&in); err != nil {
		return err
	}
	if in.ID == "" || len(in.ID) > 256 || in.Session == "" || len(in.Session) > 2048 || len(in.Text) > 100000 || len(in.Kind) > 64 || !utf8.ValidString(in.ID+in.Session+in.Text+in.Kind) || strings.ContainsAny(in.ID+in.Session, "\x00\r\n") {
		return errors.New("invalid channel message")
	}
	reply, err := json.Marshal(in.ReplyContext)
	if err != nil || len(reply) > 8192 || len(in.ReplyContext) > 16 {
		return errors.New("invalid reply context")
	}
	original, _ := json.Marshal([]string{in.Session, in.Kind, in.Text})
	digest := sha256.Sum256(original)
	fingerprint := hex.EncodeToString(digest[:])
	if in.Fingerprint != "" {
		fingerprint = in.Fingerprint
	}
	s.channelConfigMu.Lock()
	defer s.channelConfigMu.Unlock()
	var saved string
	err = s.store.db.QueryRowContext(ctx, "SELECT fingerprint FROM channel_inbox WHERE channel_id=? AND msg_id=?", channelID, in.ID).Scan(&saved)
	if err == nil {
		if saved != fingerprint {
			return errors.New("conflicting channel message")
		}
		// A replay can carry a fresh transport request ID. Refresh only before any
		// external send; never resurrect personal data in cleanup tombstones.
		if len(in.ReplyContext) > 0 {
			_, err = s.store.db.ExecContext(ctx, "UPDATE channel_inbox SET reply_context=? WHERE channel_id=? AND msg_id=? AND member<>'' AND NOT EXISTS(SELECT 1 FROM channel_outbox o WHERE o.inbox_id=channel_inbox.id AND o.state IN ('sending','sent','unknown'))", string(reply), channelID, in.ID)
			return err
		}
		return nil
	}
	if err != sql.ErrNoRows {
		return err
	}
	c, _, err := s.loadChannel(ctx, channelID)
	if err != nil || c.Version != version || !c.Enabled || c.Archived {
		return errors.New("channel unavailable")
	}
	var waiting int
	if err := s.store.db.QueryRowContext(ctx, "SELECT count(*) FROM channel_inbox WHERE channel_id=? AND state='pending'", channelID).Scan(&waiting); err != nil {
		return err
	}
	if waiting >= 1000 {
		return errors.New("channel queue full")
	}
	kind, group, sender := channelIdentity(in.Session)
	if sender == "" || (kind == "group" && group == "") {
		return errors.New("invalid conversation identity")
	}
	text := strings.TrimSpace(in.Text)
	state, reason := "pending", ""
	if in.Kind != "text" {
		state, reason = "rejected", "目前仅支持文字消息，请发送文本问题。"
	} else if text == "" {
		state, reason = "rejected", "未收到可处理的文本，请重新发送问题。"
	}
	inboxID := id()
	_, err = s.store.db.ExecContext(ctx, "INSERT INTO channel_inbox(id,channel_id,msg_id,member,agent_id,content,fingerprint,state,error,created,reply_context) VALUES(?,?,?,?,?,?,?,?,?,?,?)", inboxID, channelID, in.ID, in.Session, c.AgentID, text, fingerprint, state, reason, time.Now().UnixMilli(), string(reply))
	if err != nil {
		return err
	}
	if state != "pending" {
		return nil
	}
	// Tell the member immediately that the message arrived instead of waiting for
	// the first progress threshold. Only in-place streaming adapters receive it,
	// and a courtesy notice never blocks durable admission.
	_ = s.queueChannelAck(ctx, inboxID, channelID, in.Session, c.AgentID)
	return nil
}

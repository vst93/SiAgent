package platform

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"siagent/internal/knowledge"
	"strings"
	"testing"
)

func TestVersionOneUpgradePreservesCredentialsAndHistory(t *testing.T) {
	directory := t.TempDir()
	key := bytes.Repeat([]byte{42}, 32)
	if err := os.WriteFile(filepath.Join(directory, "master.key"), key, 0600); err != nil {
		t.Fatal(err)
	}
	old, err := sql.Open("sqlite", filepath.Join(directory, "platform.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	schema, err := os.ReadFile("testdata/schema-v1.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`INSERT INTO admin VALUES(1,'existing-password-hash');INSERT INTO conversations(id,title,created) VALUES('chat','old history',1);INSERT INTO runs(id,conversation_id,status,config,created) VALUES('old-run','chat','running','{}',1);INSERT INTO messages(conversation_id,run_id,role,content,created) VALUES('chat','old-run','user','preserved input',1),('chat','old-run','assistant','preserved partial',1)`); err != nil {
		t.Fatal(err)
	}
	block, _ := aes.NewCipher(key)
	aead, _ := cipher.NewGCM(block)
	nonce := bytes.Repeat([]byte{1}, aead.NonceSize())
	encrypted := base64.RawStdEncoding.EncodeToString(aead.Seal(nonce, nonce, []byte("prior-model-key"), []byte("siagent:model-key:v1")))
	if _, err := old.Exec("INSERT INTO models VALUES(1,'https://example.com/v1','prior-model',?,4)", encrypted); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	model, err := store.model()
	if err != nil || model.APIKey != "prior-model-key" || model.Version != 4 {
		t.Fatal("migration lost saved model credentials or version")
	}
	var version int
	if err := store.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 24 {
		t.Fatalf("wrong migration version: %d", version)
	}
	server, err := New(store, &fakeRuntime{}, func() bool { return true }, "")
	if err != nil {
		t.Fatal(err)
	}
	if server.SetupToken() != "" {
		t.Fatal("upgrade reset administrator")
	}
	snapshot, err := server.snapshot("chat")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Messages) != 2 || snapshot.Messages[1].Content != "preserved partial" || snapshot.Runs[0].Status != "interrupted" {
		t.Fatalf("upgrade lost prior state: %+v", snapshot)
	}
	// Version-one terminal records had no execution limits. The diagnostics path
	// must still show them without requiring a new conversation or a model key.
	data, _ := json.Marshal(snapshot)
	if !bytes.Contains(data, []byte("preserved input")) {
		t.Fatal("old history not readable")
	}
	actual, err := os.ReadFile(filepath.Join(directory, "master.key"))
	if err != nil || !bytes.Equal(actual, key) {
		t.Fatal("upgrade replaced encryption key")
	}
	var hash string
	if err := store.db.QueryRow("SELECT password_hash FROM admin").Scan(&hash); err != nil || hash != "existing-password-hash" {
		t.Fatal("upgrade changed password hash")
	}
}

func TestVersionTwoUpgradePreservesToolHistory(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "prior-model", "prior-secret")
	downgradeChannelFixture(t, f.store)
	dropQualityArtifacts(t, f.store.db)
	if _, err := f.store.db.Exec(`
 DROP TABLE conversation_cleanup; ALTER TABLE models DROP COLUMN options; ALTER TABLE runs DROP COLUMN citation_valid; ALTER TABLE runs DROP COLUMN evidence_state;  DROP TABLE wecom_outbox; DROP TABLE wecom_inbox; DROP TABLE wecom_sessions; DROP TABLE wecom_channels; ALTER TABLE conversations DROP COLUMN channel_id; DROP INDEX conversations_agent; ALTER TABLE conversations DROP COLUMN agent_id; ALTER TABLE agents DROP COLUMN model_id; ALTER TABLE agents DROP COLUMN archived; ALTER TABLE models DROP COLUMN archived; DROP TABLE run_sources; DROP TABLE knowledge_bindings; DROP TABLE knowledge_fts; DROP TABLE knowledge_chunks; DROP TABLE knowledge_versions; DROP TABLE knowledge_documents; DROP TABLE knowledge_bases;DROP TABLE retrieval_traces; DROP TABLE knowledge_eval_runs; DROP TABLE knowledge_eval_questions; 
 DROP TABLE agent_capabilities; DROP TABLE skills; DROP TABLE mcps;
 ALTER TABLE tool_calls DROP COLUMN arguments; ALTER TABLE tool_calls DROP COLUMN result;
 INSERT INTO conversations(id,title,created) VALUES('prior-chat','Prior chat',1);
 INSERT INTO runs(id,conversation_id,status,config,created) VALUES('prior-run','prior-chat','completed','{}',1);
 INSERT INTO tool_calls VALUES('prior-run','prior-call','prior-tool','completed');
 DROP TABLE IF EXISTS answer_feedback; DROP TABLE knowledge_gaps; ALTER TABLE models DROP COLUMN provider; DROP TABLE provider_packs; DROP TABLE audit_log; DROP TABLE plugins; DROP TABLE plugin_events; DROP TABLE api_keys; DROP TABLE api_idempotency; DROP TABLE api_conversations; DROP TABLE knowledge_external; ALTER TABLE agents DROP COLUMN revision; ALTER TABLE runs DROP COLUMN agent_revision; ALTER TABLE runs DROP COLUMN source; DROP TABLE webhook_subscriptions; DROP TABLE webhook_deliveries; PRAGMA user_version=2;`); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(f.directory)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	var name, status, args, result string
	if err := upgraded.db.QueryRow("SELECT name,status,arguments,result FROM tool_calls WHERE run_id='prior-run'").Scan(&name, &status, &args, &result); err != nil {
		t.Fatal(err)
	}
	if name != "prior-tool" || status != "completed" || args != "" || result != "" {
		t.Fatal("migration lost existing tool history")
	}
	model, err := upgraded.model()
	if err != nil || model.APIKey != "prior-secret" {
		t.Fatal("migration lost prior model credential")
	}
}

func TestVersionThreeUpgradePreservesCapabilities(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "prior-model", "prior-model-secret")
	skill, err := f.store.importSkill(skillDirectory(t))
	if err != nil {
		t.Fatal(err)
	}
	config := MCPConfig{ID: id(), Name: "Existing MCP", Transport: "http", URL: "https://example.com/mcp", Tools: []MCPTool{{Name: "read", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)}}}
	raw, _ := json.Marshal(config)
	secret, _ := json.Marshal(MCPSecret{Headers: map[string]string{"Authorization": "Bearer prior-mcp-secret"}, Env: map[string]string{}})
	dropQualityArtifacts(t, f.store.db)
	if _, err := f.store.db.Exec("INSERT INTO mcps(id,config,secrets,version) VALUES(?,?,?,7)", config.ID, string(raw), f.store.encrypt(string(secret))); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, f.request(t, "PUT", "/api/agent/capabilities", map[string]any{"skills": []string{skill.ID}, "mcps": []MCPBinding{{ID: config.ID, Tools: []string{"read"}}}, "confirm": true}, true, nil), 200)
	downgradeChannelFixture(t, f.store)
	dropQualityArtifacts(t, f.store.db)
	if _, err := f.store.db.Exec(`DROP TABLE conversation_cleanup; ALTER TABLE models DROP COLUMN options; ALTER TABLE runs DROP COLUMN citation_valid; ALTER TABLE runs DROP COLUMN evidence_state;  DROP TABLE wecom_outbox; DROP TABLE wecom_inbox; DROP TABLE wecom_sessions; DROP TABLE wecom_channels; ALTER TABLE conversations DROP COLUMN channel_id; DROP INDEX conversations_agent; ALTER TABLE conversations DROP COLUMN agent_id; ALTER TABLE agents DROP COLUMN model_id; ALTER TABLE agents DROP COLUMN archived; ALTER TABLE models DROP COLUMN archived; DROP TABLE run_sources; DROP TABLE knowledge_bindings; DROP TABLE knowledge_fts; DROP TABLE knowledge_chunks; DROP TABLE knowledge_versions; DROP TABLE knowledge_documents; DROP TABLE knowledge_bases; DROP TABLE retrieval_traces; DROP TABLE knowledge_eval_runs; DROP TABLE knowledge_eval_questions; DROP TABLE IF EXISTS answer_feedback; DROP TABLE knowledge_gaps; ALTER TABLE models DROP COLUMN provider; DROP TABLE provider_packs; DROP TABLE audit_log; DROP TABLE plugins; DROP TABLE plugin_events; DROP TABLE api_keys; DROP TABLE api_idempotency; DROP TABLE api_conversations; DROP TABLE knowledge_external; ALTER TABLE agents DROP COLUMN revision; ALTER TABLE runs DROP COLUMN agent_revision; ALTER TABLE runs DROP COLUMN source; DROP TABLE webhook_subscriptions; DROP TABLE webhook_deliveries; PRAGMA user_version=3;`); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(f.directory)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	_, credentials, version, err := upgraded.mcp(config.ID)
	if err != nil || version != 7 || credentials.Headers["Authorization"] != "Bearer prior-mcp-secret" {
		t.Fatal("migration lost MCP credential or version")
	}
	bindings, err := upgraded.bindings("default")
	if err != nil || len(bindings.Skills) != 1 || bindings.Skills[0] != skill.ID || len(bindings.MCPs) != 1 || bindings.MCPs[0].Tools[0] != "read" {
		t.Fatal("migration lost explicit capabilities")
	}
	if _, err := os.Stat(filepath.Join(f.directory, "skills", skill.ID, "SKILL.md")); err != nil {
		t.Fatal("migration lost imported skill files")
	}
	var count int
	if err := upgraded.db.QueryRow("SELECT count(*) FROM knowledge_bases").Scan(&count); err != nil || count != 0 {
		t.Fatal("native knowledge schema did not initialize")
	}
}

func TestVersionFourUpgradeKeepsNativeKnowledgeAndConversationOwnership(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "prior-model", "prior-secret")
	b, err := f.server.knowledge.SaveBase(context.Background(), "", "Invoices", "")
	if err != nil {
		t.Fatal(err)
	}
	document, err := f.server.knowledge.Upload(context.Background(), b.ID, "", "invoice.txt", strings.NewReader("发票开具流程：七个工作日。"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.server.RunKnowledge(ctx) }()
	waitDocument(t, f, document.ID)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := f.server.knowledge.Bind(context.Background(), "default", []string{b.ID}); err != nil {
		t.Fatal(err)
	}
	conversation := f.conversation(t)
	run := f.start(t, conversation, "Existing knowledge question")
	hits, err := f.server.knowledge.Search(context.Background(), knowledge.Scope{KnowledgeIDs: []string{b.ID}}, knowledge.SearchRequest{Query: "发票"})
	if err != nil || len(hits.Hits) == 0 {
		t.Fatal("prior native index missing")
	}
	reference := hits.Hits[0]
	reference.Text = ""
	reference.Score = nil
	raw, _ := json.Marshal(reference)
	dropQualityArtifacts(t, f.store.db)
	if _, err := f.store.db.Exec("INSERT INTO run_sources VALUES(?,?,?)", run, reference.SourceID, string(raw)); err != nil {
		t.Fatal(err)
	}
	downgradeChannelFixture(t, f.store)
	dropQualityArtifacts(t, f.store.db)
	if _, err := f.store.db.Exec(`DROP TABLE conversation_cleanup; ALTER TABLE models DROP COLUMN options; ALTER TABLE runs DROP COLUMN citation_valid; ALTER TABLE runs DROP COLUMN evidence_state;  DROP TABLE wecom_outbox; DROP TABLE wecom_inbox; DROP TABLE wecom_sessions; DROP TABLE wecom_channels; ALTER TABLE conversations DROP COLUMN channel_id; DROP INDEX conversations_agent; ALTER TABLE conversations DROP COLUMN agent_id; ALTER TABLE agents DROP COLUMN model_id; ALTER TABLE agents DROP COLUMN archived; ALTER TABLE models DROP COLUMN archived; DROP TABLE retrieval_traces; DROP TABLE knowledge_eval_runs; DROP TABLE knowledge_eval_questions; ALTER TABLE knowledge_chunks DROP COLUMN parent_id; ALTER TABLE knowledge_chunks DROP COLUMN block_type; ALTER TABLE knowledge_chunks DROP COLUMN section_path; ALTER TABLE knowledge_chunks DROP COLUMN clause; ALTER TABLE knowledge_chunks DROP COLUMN content_hash; DROP TABLE IF EXISTS answer_feedback; DROP TABLE knowledge_gaps; ALTER TABLE models DROP COLUMN provider; DROP TABLE provider_packs; DROP TABLE audit_log; DROP TABLE plugins; DROP TABLE plugin_events; DROP TABLE api_keys; DROP TABLE api_idempotency; DROP TABLE api_conversations; DROP TABLE knowledge_external; ALTER TABLE agents DROP COLUMN revision; ALTER TABLE runs DROP COLUMN agent_revision; ALTER TABLE runs DROP COLUMN source; DROP TABLE webhook_subscriptions; DROP TABLE webhook_deliveries; PRAGMA user_version=4;`); err != nil {
		t.Fatal(err)
	}
	f.server.StopKnowledge()
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(f.directory)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	server, err := New(upgraded, &fakeRuntime{}, func() bool { return true }, "")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := server.snapshot(conversation)
	if err != nil || snapshot.Conversation.AgentID != "default" || len(snapshot.Sources) != 1 {
		t.Fatal("migration lost ownership or sources")
	}
	a, err := server.agent()
	if err != nil || a.ModelID != 1 || a.Archived {
		t.Fatal("migration lost default association")
	}
	source, err := server.knowledge.Read(context.Background(), knowledge.Scope{KnowledgeIDs: []string{b.ID}}, knowledge.ReadRequest{SourceID: reference.SourceID})
	if err != nil || !strings.Contains(source.DocumentText, "七个工作日") {
		t.Fatal("migration lost parsed original")
	}
	ids, err := server.knowledge.Bindings(context.Background(), "default")
	if err != nil || len(ids) != 1 || ids[0] != b.ID {
		t.Fatal("migration lost native binding")
	}
}

func TestVersionFiveUpgradePreservesMultipleModelsAndAgents(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "original", "original-secret")
	model := createTestModel(t, f, "second", "second-secret")
	role := saveTestAgent(t, f, Agent{Name: "Second role", Enabled: true, ModelID: model.ID})
	conversation := agentConversation(t, f, role.ID)
	downgradeChannelFixture(t, f.store)
	dropQualityArtifacts(t, f.store.db)
	if _, err := f.store.db.Exec(`DROP TABLE conversation_cleanup; ALTER TABLE models DROP COLUMN options; ALTER TABLE runs DROP COLUMN citation_valid; ALTER TABLE runs DROP COLUMN evidence_state;  DROP TABLE wecom_outbox; DROP TABLE wecom_inbox; DROP TABLE wecom_sessions; DROP TABLE wecom_channels; ALTER TABLE conversations DROP COLUMN channel_id; DROP TABLE retrieval_traces; DROP TABLE knowledge_eval_runs; DROP TABLE knowledge_eval_questions; ALTER TABLE knowledge_chunks DROP COLUMN parent_id; ALTER TABLE knowledge_chunks DROP COLUMN block_type; ALTER TABLE knowledge_chunks DROP COLUMN section_path; ALTER TABLE knowledge_chunks DROP COLUMN clause; ALTER TABLE knowledge_chunks DROP COLUMN content_hash; DROP TABLE IF EXISTS answer_feedback; DROP TABLE knowledge_gaps; ALTER TABLE models DROP COLUMN provider; DROP TABLE provider_packs; DROP TABLE audit_log; DROP TABLE plugins; DROP TABLE plugin_events; DROP TABLE api_keys; DROP TABLE api_idempotency; DROP TABLE api_conversations; DROP TABLE knowledge_external; ALTER TABLE agents DROP COLUMN revision; ALTER TABLE runs DROP COLUMN agent_revision; ALTER TABLE runs DROP COLUMN source; DROP TABLE webhook_subscriptions; DROP TABLE webhook_deliveries; PRAGMA user_version=5;`); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(f.directory)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	server, err := New(upgraded, &fakeRuntime{}, func() bool { return true }, "")
	if err != nil {
		t.Fatal(err)
	}
	saved, err := upgraded.modelByID(model.ID)
	if err != nil || saved.APIKey != "second-secret" {
		t.Fatal("channel migration changed model credential")
	}
	snapshot, err := server.snapshot(conversation)
	if err != nil || snapshot.Conversation.AgentID != role.ID || snapshot.Conversation.ChannelID != "" {
		t.Fatal("channel migration changed existing conversation")
	}
	var channels int
	if err := upgraded.db.QueryRow("SELECT count(*) FROM channels").Scan(&channels); err != nil || channels != 0 {
		t.Fatal("channel schema not initialized")
	}
}

func TestVersionSixUpgradeAddsMaintenanceWithoutChangingChannelCredentials(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "key")
	channel := createChannel(t, f, false)
	conversation := f.conversation(t)
	downgradeChannelFixture(t, f.store)
	dropQualityArtifacts(t, f.store.db)
	if _, err := f.store.db.Exec("DROP TABLE conversation_cleanup; ALTER TABLE models DROP COLUMN options; ALTER TABLE runs DROP COLUMN citation_valid; ALTER TABLE runs DROP COLUMN evidence_state;  DROP TABLE retrieval_traces; DROP TABLE knowledge_eval_runs; DROP TABLE knowledge_eval_questions; ALTER TABLE knowledge_chunks DROP COLUMN parent_id; ALTER TABLE knowledge_chunks DROP COLUMN block_type; ALTER TABLE knowledge_chunks DROP COLUMN section_path; ALTER TABLE knowledge_chunks DROP COLUMN clause; ALTER TABLE knowledge_chunks DROP COLUMN content_hash; DROP TABLE IF EXISTS answer_feedback; DROP TABLE knowledge_gaps; ALTER TABLE models DROP COLUMN provider; DROP TABLE provider_packs; DROP TABLE audit_log; DROP TABLE plugins; DROP TABLE plugin_events; DROP TABLE api_keys; DROP TABLE api_idempotency; DROP TABLE api_conversations; DROP TABLE knowledge_external; ALTER TABLE agents DROP COLUMN revision; ALTER TABLE runs DROP COLUMN agent_revision; ALTER TABLE runs DROP COLUMN source; DROP TABLE webhook_subscriptions; DROP TABLE webhook_deliveries; PRAGMA user_version=6;"); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(f.directory)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	server, err := New(upgraded, &fakeRuntime{}, func() bool { return true }, "")
	if err != nil {
		t.Fatal(err)
	}
	_, secret, err := server.channel(channel.ID)
	if err != nil || secret.Secret != "channel-secret" || secret.AESKey != callbackKey {
		t.Fatal("maintenance migration changed encrypted channel credentials")
	}
	if _, err := server.snapshot(conversation); err != nil {
		t.Fatal(err)
	}
	var pending int
	if err := upgraded.db.QueryRow("SELECT count(*) FROM conversation_cleanup").Scan(&pending); err != nil || pending != 0 {
		t.Fatal("maintenance table not initialized")
	}
}

func downgradeChannelFixture(t *testing.T, s *Store) {
	t.Helper()
	_, err := s.db.Exec(`
 ALTER TABLE agents DROP COLUMN policy;
 ALTER TABLE runs DROP COLUMN thinking;
 DROP INDEX IF EXISTS active_channel_identity;
 ALTER TABLE channels RENAME TO wecom_channels;
 ALTER TABLE wecom_channels ADD COLUMN corp_id TEXT NOT NULL DEFAULT '';
 ALTER TABLE wecom_channels ADD COLUMN app_id INTEGER NOT NULL DEFAULT 0;
 UPDATE wecom_channels SET corp_id=json_extract(config,'$.corp_id'),app_id=CAST(json_extract(config,'$.app_id') AS INTEGER);
 ALTER TABLE channel_sessions RENAME TO wecom_sessions;
 ALTER TABLE channel_inbox RENAME TO wecom_inbox;
 ALTER TABLE channel_outbox RENAME TO wecom_outbox;
 `)
	if err != nil {
		t.Fatal(err)
	}
}

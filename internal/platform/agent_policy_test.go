package platform

import (
	"context"
	"encoding/json"
	"siagent/internal/channels"
	agentruntime "siagent/internal/runtime"
	"strings"
	"testing"
	"time"
)

func setTestPolicy(t *testing.T, f *fixture, p AgentPolicy) Agent {
	t.Helper()
	a, err := f.server.agent()
	if err != nil {
		t.Fatal(err)
	}
	response := f.request(t, "PUT", "/api/agents/default", map[string]any{"name": a.Name, "instructions": a.Instructions, "enabled": a.Enabled, "model_id": a.ModelID, "policy": p}, true, nil)
	requireStatus(t, response, 200)
	var saved Agent
	if err := json.Unmarshal(response.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	return saved
}
func TestAgentPolicyValidationPreservationAndFrozenQueue(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "key")
	p := defaultAgentPolicy()
	p.Context.Strategy = "time"
	p.Timezone = "America/New_York"
	p.Context.MaxTurns = 7
	p.Output.Thinking = true
	p.Output.Tools = "detailed"
	setTestPolicy(t, f, p)
	// Older clients may update basic fields without erasing policy.
	response := f.request(t, "PUT", "/api/agent", map[string]any{"name": "updated", "enabled": true, "instructions": "role"}, true, nil)
	requireStatus(t, response, 200)
	a, err := f.server.agent()
	if err != nil || a.Policy != p {
		t.Fatal("basic edit erased policy", a, err)
	}
	for _, bad := range []any{map[string]any{"timezone": "Mars/Olympus"}, map[string]any{"timezone": "Local"}, map[string]any{"timezone": ""}, map[string]any{"session": map[string]any{"types": "everyone"}}, map[string]any{"session": map[string]any{"group_scope": "global"}}, map[string]any{"session": map[string]any{"idle_minutes": -1}}, map[string]any{"context": map[string]any{"strategy": "unknown"}}, map[string]any{"context": map[string]any{"max_turn": 3}}, map[string]any{"context": map[string]any{"max_turns": 0}}, map[string]any{"context": map[string]any{"summary_threshold": 99}}, map[string]any{"output": map[string]any{"tools": "raw"}}, nil} {
		requireStatus(t, f.request(t, "PUT", "/api/agent", map[string]any{"name": "updated", "enabled": true, "policy": bad}, true, nil), 400)
	}
	f.server.limits.Concurrent = 1
	chat := f.conversation(t)
	first := f.start(t, chat, "one")
	second := f.start(t, chat, "two")
	var frozenRaw string
	if err := f.store.db.QueryRow("SELECT config FROM runs WHERE id=?", second).Scan(&frozenRaw); err != nil {
		t.Fatal(err)
	}
	var frozen runConfig
	if err := json.Unmarshal([]byte(frozenRaw), &frozen); err != nil {
		t.Fatal(err)
	}
	if frozen.TimeContext == nil || frozen.TimeContext.Timezone != "America/New_York" {
		t.Fatal("time context not frozen", frozen.TimeContext)
	}
	p.Timezone = "UTC"
	p.Context.Strategy = "recent"
	p.Output.Thinking = false
	p.Output.Tools = "hidden"
	setTestPolicy(t, f, p)
	emit(t, f, first, "run_started", 1)
	emit(t, f, first, "run_completed", 2)
	emit(t, f, first, "run_released", 3)
	req := f.runtime.requests[len(f.runtime.requests)-1]
	if req["run_id"] != second || req["context_policy"].(ContextPolicy).Strategy != "time" || !req["output_policy"].(OutputPolicy).Thinking {
		t.Fatal("queued policy changed", req)
	}
	clock, ok := req["time_context"].(*TurnTime)
	if !ok || *clock != *frozen.TimeContext {
		t.Fatal("queued time context changed", req["time_context"], frozen.TimeContext)
	}
	if req["instructions"] != frozen.Agent.Instructions {
		t.Fatal("time was added to role instructions")
	}

}
func TestAgentSessionScopesRestrictionsIdleAndReset(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "key")
	c := createChannel(t, f, true)
	ctx := context.Background()
	resolve := func(member string, reset bool) string {
		t.Helper()
		chat, err := f.server.channelConversation(ctx, c.ID, member, "default", reset)
		if err != nil {
			t.Fatal(err)
		}
		return chat
	}
	one := `["group","room-a","alice"]`
	two := `["group","room-a","bob"]`
	other := `["group","room-b","alice"]`
	single := `["single","","alice"]`
	first := resolve(one, false)
	if first == resolve(two, false) || first == resolve(other, false) || first == resolve(single, false) || first != resolve(one, false) {
		t.Fatal("member/group/private contexts not isolated")
	}
	p := defaultAgentPolicy()
	p.Session.GroupScope = "shared"
	setTestPolicy(t, f, p)
	shared := resolve(one, false)
	if shared == first || shared != resolve(two, false) || shared == resolve(other, false) || shared == resolve(single, false) {
		t.Fatal("shared group scope incorrect")
	}
	reset := resolve(two, true)
	if reset == shared || reset != resolve(one, false) {
		t.Fatal("shared reset did not affect entire group")
	}
	p.Session.AllowReset = false
	setTestPolicy(t, f, p)
	if _, err := f.server.channelConversation(ctx, c.ID, one, "default", true); err == nil {
		t.Fatal("disabled reset accepted")
	}
	p.Session.IdleMinutes = 1
	setTestPolicy(t, f, p)
	if _, err := f.store.db.Exec("UPDATE conversations SET created=? WHERE id=?", time.Now().Add(-2*time.Minute).UnixMilli(), reset); err != nil {
		t.Fatal(err)
	}
	fresh := resolve(one, false)
	if fresh == reset {
		t.Fatal("idle session not rotated")
	}
	p.Session.Types = "private"
	setTestPolicy(t, f, p)
	if _, err := f.server.channelConversation(ctx, c.ID, one, "default", false); err == nil {
		t.Fatal("private-only agent admitted group")
	}
	resolve(single, false)
	p.Session.Types = "group"
	setTestPolicy(t, f, p)
	if _, err := f.server.channelConversation(ctx, c.ID, single, "default", false); err == nil {
		t.Fatal("group-only agent admitted private")
	}
	requireStatus(t, f.request(t, "POST", "/api/conversations", nil, true, nil), 403)
	if _, err := f.server.enqueueRun(ctx, first, "allowed group question", 0, nil); err != nil {
		t.Fatal(err)
	}
	// Even when the session is old, pending/running work must keep the same scope.
	if _, err := f.store.db.Exec("UPDATE conversations SET created=? WHERE id=?", time.Now().Add(-2*time.Minute).UnixMilli(), fresh); err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.enqueueRun(ctx, fresh, "pending group question", 0, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.db.Exec("UPDATE runs SET created=? WHERE conversation_id=?", time.Now().Add(-2*time.Minute).UnixMilli(), fresh); err != nil {
		t.Fatal(err)
	}
	if resolve(one, false) != fresh {
		t.Fatal("idle rotation split pending work")
	}
}
func TestChannelStructuredIdentityPolicyAndReplyTarget(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "key")
	c := createChannel(t, f, true)
	p := defaultAgentPolicy()
	p.Session.GroupScope = "shared"
	setTestPolicy(t, f, p)
	for _, sender := range []string{"alice", "bob"} {
		incoming := channels.Incoming{ID: sender, ChatType: "group", SenderID: sender, GroupID: "room", Kind: "text", Text: "question", ReplyContext: map[string]string{"sender": sender}}
		if err := f.server.receiveChannel(context.Background(), c.ID, c.Version, incoming); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.server.admitChannelInbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	var distinct int
	f.store.db.QueryRow("SELECT count(DISTINCT conversation_id) FROM channel_inbox WHERE channel_id=?", c.ID).Scan(&distinct)
	if distinct != 1 {
		t.Fatal("structured group not shared")
	}
	var member string
	f.store.db.QueryRow("SELECT member FROM channel_inbox WHERE msg_id='alice'").Scan(&member)
	if member != `["group","room","alice"]` {
		t.Fatal("reply target was replaced by shared key", member)
	}
	p.Session.Types = "private"
	setTestPolicy(t, f, p)
	if err := f.server.receiveChannel(context.Background(), c.ID, c.Version, channels.Incoming{ID: "denied", Session: `["group","room","alice"]`, Kind: "text", Text: "denied question"}); err != nil {
		t.Fatal(err)
	}
	if err := f.server.admitChannelInbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	var state string
	f.store.db.QueryRow("SELECT state FROM channel_inbox WHERE msg_id='denied'").Scan(&state)
	if state != "rejected" {
		t.Fatal("restricted group admitted", state)
	}
	if err := f.server.receiveChannel(context.Background(), c.ID, c.Version, channels.Incoming{ID: "invalid", ChatType: "group", SenderID: "alice", Kind: "text", Text: "x"}); err == nil {
		t.Fatal("group without room accepted")
	}
}
func TestAgentOutputIsFrozenForWebChannelsAndProgress(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "key")
	c := createChannel(t, f, true)
	p := defaultAgentPolicy()
	p.Output.Thinking = true
	p.Output.Tools = "detailed"
	p.Output.Progress = false
	p.Output.Sources = false
	setTestPolicy(t, f, p)
	requireStatus(t, callback(t, f, c, "991", "member", "question"), 200)
	if err := f.server.prepareChannelReplies(context.Background(), time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	var pendingHints int
	f.store.db.QueryRow("SELECT count(*) FROM channel_outbox WHERE kind='hint'").Scan(&pendingHints)
	if pendingHints != 0 {
		t.Fatal("pending message ignored disabled progress")
	}

	if err := f.server.admitChannelInbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	run := f.runtime.requests[0]["run_id"].(string)
	if err := f.server.prepareChannelReplies(context.Background(), time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	var hints int
	f.store.db.QueryRow("SELECT count(*) FROM channel_outbox WHERE kind='hint'").Scan(&hints)
	if hints != 0 {
		t.Fatal("disabled progress emitted hint")
	}
	emit(t, f, run, "run_started", 1)
	event := func(kind string, seq int64, data map[string]any) {
		t.Helper()
		raw, _ := json.Marshal(data)
		if err := f.server.HandleEvent(agentruntime.Event{RunID: run, Type: kind, Sequence: seq, Data: raw}); err != nil {
			t.Fatal(err)
		}
	}
	event("thinking_delta", 2, map[string]any{"text": "reasoning summary"})
	event("tool_start", 3, map[string]any{"toolName": "lookup", "toolCallId": "call", "args": map[string]string{"query": "invoice"}})
	event("tool_end", 4, map[string]any{"toolName": "lookup", "toolCallId": "call", "result": "found invoice", "entry": map[string]any{"model": []map[string]bool{{"isError": false}}}})
	event("run_completed", 5, map[string]any{"answer": "final answer"})
	var chat string
	f.store.db.QueryRow("SELECT conversation_id FROM runs WHERE id=?", run).Scan(&chat)
	snapshot, err := f.server.snapshot(chat)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Runs[0].Thinking != "reasoning summary" || len(snapshot.Runs[0].Tools) != 1 || snapshot.Runs[0].Output.Tools != "detailed" {
		t.Fatal("presentation missing", snapshot.Runs)
	}
	p.Output.Thinking = false
	p.Output.Tools = "hidden"
	setTestPolicy(t, f, p)
	if err := f.server.prepareChannelReplies(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	var text string
	f.store.db.QueryRow("SELECT content FROM channel_outbox WHERE kind='final'").Scan(&text)
	if !strings.Contains(text, "reasoning summary") || !strings.Contains(text, "found invoice") || !strings.Contains(text, "invoice") {
		t.Fatal("channel lost frozen output policy", text)
	}
}

func TestAgentHiddenOutputKeepsPrivateAudit(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "key")
	p := defaultAgentPolicy()
	p.Output.Tools = "hidden"
	p.Output.Sources = false
	setTestPolicy(t, f, p)
	chat := f.conversation(t)
	run := f.start(t, chat, "question")
	emit(t, f, run, "run_started", 1)
	raw, _ := json.Marshal(map[string]any{"text": "should not be retained"})
	if err := f.server.HandleEvent(agentruntime.Event{RunID: run, Type: "thinking_delta", Sequence: 2, Data: raw}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.db.Exec("INSERT INTO tool_calls VALUES(?,?,?,?,?,?)", run, "hidden-call", "lookup", "completed", "{}", "private result"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.db.Exec("INSERT INTO run_sources VALUES(?,?,?)", run, "source", `{"source_id":"source","title":"Private source"}`); err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.server.snapshot(chat)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Runs[0].Thinking != "" || len(snapshot.Runs[0].Tools) != 0 || len(snapshot.Sources) != 0 {
		t.Fatal("hidden output leaked into chat", snapshot)
	}
	requireStatus(t, f.request(t, "GET", "/api/runs/"+run+"/tools/hidden-call", nil, true, nil), 200)
}
func TestVersionNinePolicyMigrationPreservesHistoryAndDefaults(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "prior-model", "prior-key")
	chat := f.conversation(t)
	run := f.start(t, chat, "prior question")
	emit(t, f, run, "run_started", 1)
	emit(t, f, run, "run_completed", 2)
	emit(t, f, run, "run_released", 3)
	if _, err := f.store.db.Exec("ALTER TABLE agents DROP COLUMN policy; ALTER TABLE runs DROP COLUMN thinking; DROP TABLE retrieval_traces; DROP TABLE knowledge_eval_runs; DROP TABLE knowledge_eval_questions; ALTER TABLE knowledge_chunks DROP COLUMN parent_id; ALTER TABLE knowledge_chunks DROP COLUMN block_type; ALTER TABLE knowledge_chunks DROP COLUMN section_path; ALTER TABLE knowledge_chunks DROP COLUMN clause; ALTER TABLE knowledge_chunks DROP COLUMN content_hash; ALTER TABLE runs DROP COLUMN citation_valid; ALTER TABLE runs DROP COLUMN evidence_state; PRAGMA user_version=9;"); err != nil {
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
	a, err := server.agent()
	if err != nil || a.Policy != defaultAgentPolicy() {
		t.Fatal("policy defaults not migrated", a, err)
	}
	snapshot, err := server.snapshot(chat)
	if err != nil || len(snapshot.Messages) != 2 || snapshot.Messages[0].Content != "prior question" {
		t.Fatal("migration lost history", snapshot, err)
	}
	var version int
	upgraded.db.QueryRow("PRAGMA user_version").Scan(&version)
	if version != 13 {
		t.Fatal("migration version", version)
	}
}

func TestChannelTimeUsesDurableReceiptInsteadOfDispatchTime(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "key")
	c := createChannel(t, f, true)
	requireStatus(t, callback(t, f, c, "2101", "member", "今天是哪天？"), 200)
	received := time.Date(2026, 10, 17, 16, 0, 0, 0, time.UTC)
	if _, err := f.store.db.Exec("UPDATE channel_inbox SET created=? WHERE channel_id=?", received.UnixMilli(), c.ID); err != nil {
		t.Fatal(err)
	}
	f.server.mu.Lock()
	err := f.server.admitChannelInbox(context.Background())
	f.server.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(f.runtime.requests) != 1 {
		t.Fatal("channel input not dispatched")
	}
	clock, ok := f.runtime.requests[0]["time_context"].(*TurnTime)
	if !ok || clock.ReceivedAt != received.Format(time.RFC3339Nano) || clock.Timezone != "Asia/Shanghai" {
		t.Fatal("channel receipt time was replaced", clock)
	}
	var created int64
	if err := f.store.db.QueryRow("SELECT created FROM messages WHERE role='user'").Scan(&created); err != nil || created != received.UnixMilli() {
		t.Fatal("history lost channel receive time", created, err)
	}
}

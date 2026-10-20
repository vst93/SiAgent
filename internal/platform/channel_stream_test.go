package platform

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"siagent/internal/channels"
	agentruntime "siagent/internal/runtime"
)

// streamingFixturePlugin stands in for the bundled WeCom bot: one outbound
// message can be updated in place, so ack, drafts and the final answer share it.
type streamingFixturePlugin struct {
	sent []channels.Delivery
}

func (*streamingFixturePlugin) Descriptor() channels.Descriptor {
	return channels.Descriptor{ID: "fixture-stream", Name: "Fixture stream", Transport: "websocket", Streaming: true, MaxTextBytes: 1024, IdentityFields: []string{"account"}, Fields: []channels.Field{
		{Key: "account", Label: "Account", Required: true},
		{Key: "token", Label: "Token", Secret: true, Required: true},
	}}
}
func (*streamingFixturePlugin) Validate(channels.Config) error { return nil }
func (p *streamingFixturePlugin) Open(context.Context, channels.Config, channels.Receive) (channels.Connection, error) {
	return p, nil
}
func (p *streamingFixturePlugin) Send(_ context.Context, d channels.Delivery) channels.Result {
	p.sent = append(p.sent, d)
	return channels.Result{State: "sent"}
}
func (*streamingFixturePlugin) Status() channels.Status { return channels.Status{State: "connected"} }
func (*streamingFixturePlugin) Close()                  {}

func newStreamingChannel(t *testing.T, f *fixture) (Channel, *streamingFixturePlugin) {
	t.Helper()
	plugin := &streamingFixturePlugin{}
	if err := f.server.ConfigureChannelPlugins("node", "unused", []channels.Plugin{
		func(channels.PluginOptions) (channels.Provider, error) { return plugin, nil },
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.server.closeChannelConnections)
	response := f.request(t, "POST", "/api/channels", map[string]any{
		"provider": "fixture-stream", "config": map[string]string{"account": "member"},
		"secrets": map[string]string{"token": "stream-secret"}, "agent_id": "default", "enabled": true, "confirm": true,
	}, true, nil)
	requireStatus(t, response, 201)
	var c Channel
	if err := json.Unmarshal(response.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	return c, plugin
}

func TestStreamingChannelAcknowledgesImmediatelyThenStreamsDraftsAndFinal(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "model-secret")
	c, plugin := newStreamingChannel(t, f)
	if err := f.server.receiveChannel(context.Background(), c.ID, c.Version, channels.Incoming{ID: "m1", ChatType: "single", SenderID: "member", Kind: "text", Text: "复杂问题", ReplyContext: map[string]string{"req_id": "req-1", "received_at": "1"}}); err != nil {
		t.Fatal(err)
	}
	// The acknowledgement exists before any agent work is admitted.
	var kind, content string
	if err := f.store.db.QueryRow("SELECT kind,content FROM channel_outbox WHERE kind='ack'").Scan(&kind, &content); err != nil {
		t.Fatal("no immediate acknowledgement", err)
	}
	if !strings.Contains(content, "已收到") {
		t.Fatalf("unexpected acknowledgement text: %q", content)
	}
	tickChannel(t, f, time.Now())
	if len(plugin.sent) != 1 || plugin.sent[0].Kind != channelKindAck {
		t.Fatalf("acknowledgement was not delivered first: %+v", plugin.sent)
	}
	if err := f.server.admitChannelInbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	run := f.runtime.requests[0]["run_id"].(string)
	emit(t, f, run, "run_started", 1)
	answer := "<think>hidden trace</think>第一段回答"
	raw, _ := json.Marshal(map[string]string{"text": answer})
	if err := f.server.HandleEvent(agentruntime.Event{Type: "message_delta", RunID: run, Sequence: 2, Data: raw}); err != nil {
		t.Fatal(err)
	}
	tickChannel(t, f, time.Now())
	if len(plugin.sent) != 2 || plugin.sent[1].Kind != channelKindDraft {
		t.Fatalf("draft was not delivered: %+v", plugin.sent)
	}
	if strings.Contains(plugin.sent[1].Text, "hidden trace") {
		t.Fatal("draft leaked inline reasoning")
	}
	// A processing notice must not overwrite newer streamed text.
	if err := f.server.prepareChannelReplies(context.Background(), time.Now().Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := f.server.prepareChannelReplies(context.Background(), time.Now().Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	for _, delivery := range plugin.sent {
		if delivery.Kind == channelKindHint {
			t.Fatal("processing hint replaced streamed progress")
		}
	}
	final, _ := json.Marshal(map[string]string{"answer": answer})
	if err := f.server.HandleEvent(agentruntime.Event{Type: "run_completed", RunID: run, Sequence: 3, Data: final}); err != nil {
		t.Fatal(err)
	}
	tickChannel(t, f, time.Now())
	var stored string
	if err := f.store.db.QueryRow("SELECT content FROM messages WHERE run_id=? AND role='assistant'", run).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, "hidden trace") || stored != "第一段回答" {
		t.Fatalf("stored answer kept reasoning: %q", stored)
	}
	last := plugin.sent[len(plugin.sent)-1]
	if last.Kind != channelKindFinal || len(last.Text) > 1024-32 {
		t.Fatalf("final delivery is wrong: %+v", last)
	}
	var pending int
	if err := f.store.db.QueryRow("SELECT count(*) FROM channel_outbox WHERE kind IN ('ack','draft') AND state IN ('pending','retry')").Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("stale stream messages were not cancelled: %d %v", pending, err)
	}
	var total int
	if err := f.store.db.QueryRow("SELECT count(*) FROM channel_outbox WHERE kind='ack' OR kind='draft'").Scan(&total); err != nil || total == 0 {
		t.Fatalf("stream rows are missing: %d %v", total, err)
	}
}

func TestStreamingDisabledKeepsSingleFinalReply(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "model-secret")
	c, plugin := newStreamingChannel(t, f)
	p := defaultAgentPolicy()
	p.Output.Acknowledge = false
	p.Output.Stream = false
	setTestPolicy(t, f, p)
	if err := f.server.receiveChannel(context.Background(), c.ID, c.Version, channels.Incoming{ID: "m2", ChatType: "single", SenderID: "member", Kind: "text", Text: "question"}); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := f.store.db.QueryRow("SELECT count(*) FROM channel_outbox").Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("disabled acknowledgement queued a message: %d %v", rows, err)
	}
	if err := f.server.admitChannelInbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	run := f.runtime.requests[0]["run_id"].(string)
	emit(t, f, run, "run_started", 1)
	raw, _ := json.Marshal(map[string]string{"text": "partial answer"})
	if err := f.server.HandleEvent(agentruntime.Event{Type: "message_delta", RunID: run, Sequence: 2, Data: raw}); err != nil {
		t.Fatal(err)
	}
	tickChannel(t, f, time.Now())
	if len(plugin.sent) != 0 {
		t.Fatalf("disabled stream still delivered: %+v", plugin.sent)
	}
	rawAnswer, _ := json.Marshal(map[string]string{"answer": "final answer"})
	if err := f.server.HandleEvent(agentruntime.Event{Type: "run_completed", RunID: run, Sequence: 3, Data: rawAnswer}); err != nil {
		t.Fatal(err)
	}
	tickChannel(t, f, time.Now())
	if len(plugin.sent) != 1 || plugin.sent[0].Kind != channelKindFinal || plugin.sent[0].Text != "final answer" {
		t.Fatalf("expected only the final reply: %+v", plugin.sent)
	}
}

func TestAgentEnvironmentIsEncryptedFrozenAndDelivered(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "model-secret")
	a, err := f.server.agent()
	if err != nil {
		t.Fatal(err)
	}
	response := f.request(t, "PUT", "/api/agents/default", map[string]any{
		"name": a.Name, "instructions": a.Instructions, "enabled": a.Enabled, "model_id": a.ModelID,
		"env": map[string]string{"API_TOKEN": "agent-secret", "REGION": "cn"},
	}, true, nil)
	requireStatus(t, response, 200)
	if strings.Contains(response.Body.String(), "agent-secret") {
		t.Fatal("environment value was returned by the API")
	}
	var saved Agent
	if err := json.Unmarshal(response.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if strings.Join(saved.EnvKeys, ",") != "API_TOKEN,REGION" {
		t.Fatalf("environment names are missing: %+v", saved.EnvKeys)
	}
	var stored string
	if err := f.store.db.QueryRow("SELECT environment FROM agents WHERE id='default'").Scan(&stored); err != nil || stored == "" || strings.Contains(stored, "agent-secret") {
		t.Fatalf("environment was not encrypted: %q %v", stored, err)
	}
	// A blank value keeps the stored secret, and env_remove deletes a name.
	requireStatus(t, f.request(t, "PUT", "/api/agents/default", map[string]any{
		"name": a.Name, "instructions": a.Instructions, "enabled": a.Enabled, "model_id": a.ModelID,
		"env": map[string]string{"API_TOKEN": ""}, "env_remove": []string{"REGION"},
	}, true, nil), 200)
	values, err := f.store.agentEnvironment("default")
	if err != nil || values["API_TOKEN"] != "agent-secret" || len(values) != 1 {
		t.Fatalf("blank value did not preserve the secret: %+v %v", values, err)
	}
	for _, bad := range []map[string]any{{"env": map[string]string{"1BAD": "x"}}, {"env": map[string]string{"A B": "x"}}, {"env": map[string]string{"OK": strings.Repeat("x", 9000)}}} {
		body := map[string]any{"name": a.Name, "instructions": a.Instructions, "enabled": a.Enabled, "model_id": a.ModelID}
		for key, value := range bad {
			body[key] = value
		}
		requireStatus(t, f.request(t, "PUT", "/api/agents/default", body, true, nil), 400)
	}
	chat := f.conversation(t)
	f.start(t, chat, "question")
	request := f.runtime.requests[len(f.runtime.requests)-1]
	env, ok := request["env"].(map[string]string)
	if !ok || env["API_TOKEN"] != "agent-secret" {
		t.Fatalf("run did not receive the frozen environment: %#v", request["env"])
	}
	var config string
	if err := f.store.db.QueryRow("SELECT config FROM runs WHERE conversation_id=?", chat).Scan(&config); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(config, "agent-secret") {
		t.Fatal("environment value leaked into the run snapshot")
	}
	var audit int
	if err := f.store.db.QueryRow("SELECT count(*) FROM audit_log WHERE action='agent.environment'").Scan(&audit); err != nil || audit == 0 {
		t.Fatalf("environment change was not audited: %d %v", audit, err)
	}
}

func TestChannelReplyHidesInlineReasoningAndBoundsSummary(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "model-secret")
	c, plugin := newStreamingChannel(t, f)
	p := defaultAgentPolicy()
	p.Output.ThinkTags = "strip"
	p.Output.Thinking = true
	p.Output.ThinkingMaxChars = 200
	setTestPolicy(t, f, p)
	reasoning := strings.Repeat("思考", 400)
	if err := f.server.receiveChannel(context.Background(), c.ID, c.Version, channels.Incoming{ID: "m3", ChatType: "single", SenderID: "member", Kind: "text", Text: "question"}); err != nil {
		t.Fatal(err)
	}
	if err := f.server.admitChannelInbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	run := f.runtime.requests[0]["run_id"].(string)
	emit(t, f, run, "run_started", 1)
	// The Runtime publishes inline reasoning as reasoning and stores the cleaned
	// answer, which is what a channel reply is built from.
	rawThinking, _ := json.Marshal(map[string]string{"text": reasoning})
	if err := f.server.HandleEvent(agentruntime.Event{Type: "thinking_delta", RunID: run, Sequence: 2, Data: rawThinking}); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]string{"answer": "真正的回答"})
	if err := f.server.HandleEvent(agentruntime.Event{Type: "run_completed", RunID: run, Sequence: 3, Data: raw}); err != nil {
		t.Fatal(err)
	}
	tickChannel(t, f, time.Now())
	var final string
	for _, delivery := range plugin.sent {
		if delivery.Kind == channelKindFinal {
			final = delivery.Text
		}
	}
	if !strings.Contains(final, "真正的回答") {
		t.Fatalf("answer is missing: %q", final)
	}
	if strings.Contains(final, reasoning) {
		t.Fatal("reasoning text was sent in full")
	}
	if !strings.Contains(final, "思考摘要已截断") {
		t.Fatalf("summary was not bounded: %d bytes", len(final))
	}
	if len(final) > 1024 {
		t.Fatalf("final reply exceeded the adapter limit: %d", len(final))
	}
}

func TestLegacyAdapterKeepsThresholdNoticeOnly(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "model-secret")
	c := createChannel(t, f, true)
	if err := f.server.receiveChannel(context.Background(), c.ID, c.Version, channels.Incoming{ID: "m4", ChatType: "single", SenderID: "member", Kind: "text", Text: "question"}); err != nil {
		t.Fatal(err)
	}
	var acks int
	if err := f.store.db.QueryRow("SELECT count(*) FROM channel_outbox WHERE kind='ack'").Scan(&acks); err != nil || acks != 0 {
		t.Fatalf("legacy adapter received an immediate acknowledgement: %d %v", acks, err)
	}
	requireStatus(t, f.request(t, "GET", "/api/channels", nil, true, nil), 200)
	if _, ok := f.server.channelProviders.Get("wecom-app"); !ok {
		t.Fatal("application plugin missing")
	}
	_ = http.StatusOK
}

package platform

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"siagent/internal/channels"
)

func TestGenericChannelCatalogCredentialsAndExplicitAdmission(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "model-secret")
	requireStatus(t, f.request(t, "GET", "/api/channel-providers", nil, false, nil), 401)
	response := f.request(t, "GET", "/api/channel-providers", nil, true, nil)
	requireStatus(t, response, 200)
	if !strings.Contains(response.Body.String(), "wecom-bot") || !strings.Contains(response.Body.String(), "wecom-app") {
		t.Fatal("built-in adapters missing")
	}
	input := map[string]any{"name": "Support bot", "provider": "wecom-bot", "config": map[string]string{"bot_id": "support-bot"}, "secrets": map[string]string{"secret": "bot-private-key"}, "agent_id": "default"}
	response = f.request(t, "POST", "/api/channels", input, true, nil)
	requireStatus(t, response, 201)
	var c Channel
	json.Unmarshal(response.Body.Bytes(), &c)
	if c.CallbackPath != "" || c.Provider != "wecom-bot" || !c.HasSecrets || strings.Contains(response.Body.String(), "bot-private-key") {
		t.Fatal("incorrect bot descriptor or leaked credentials")
	}
	if err := f.server.receiveChannel(context.Background(), c.ID, c.Version, channels.Incoming{ID: "message-1", Session: "member", Kind: "text", Text: "question"}); err == nil {
		t.Fatal("disabled channel admitted a message")
	}
	input["enabled"] = true
	requireStatus(t, f.request(t, "PUT", "/api/channels/"+c.ID, input, true, nil), 400)
	input["confirm"] = true
	input["secrets"] = map[string]string{"secret": ""}
	response = f.request(t, "PUT", "/api/channels/"+c.ID, input, true, nil)
	requireStatus(t, response, 200)
	json.Unmarshal(response.Body.Bytes(), &c)
	stored, secrets, err := f.server.loadChannel(context.Background(), c.ID)
	if err != nil || secrets["secret"] != "bot-private-key" || !stored.Enabled {
		t.Fatal("blank secret was not retained", err)
	}
	input["name"] = "Renamed bot"
	response = f.request(t, "PUT", "/api/channels/"+c.ID, input, true, nil)
	requireStatus(t, response, 200)
	var renamed Channel
	json.Unmarshal(response.Body.Bytes(), &renamed)
	if renamed.Version != c.Version {
		t.Fatal("display-only edit would restart the connection")
	}
	input["secrets"] = map[string]string{"secret": "rotated-key"}
	requireStatus(t, f.request(t, "PUT", "/api/channels/"+c.ID, input, true, nil), 409)
	input["secrets"] = map[string]string{"secret": ""}
	in := channels.Incoming{ID: "message-1", Session: `["group","room-a","member"]`, Kind: "text", Text: "question", ReplyContext: map[string]string{"req_id": "original", "sender": "member"}}
	if err := f.server.receiveChannel(context.Background(), c.ID, c.Version, in); err != nil {
		t.Fatal(err)
	}
	in.ReplyContext["req_id"] = "retry-request"
	if err := f.server.receiveChannel(context.Background(), c.ID, c.Version, in); err != nil {
		t.Fatal("transport request ID changed message identity", err)
	}
	var replyRaw string
	f.store.db.QueryRow("SELECT reply_context FROM channel_inbox WHERE channel_id=?", c.ID).Scan(&replyRaw)
	if !strings.Contains(replyRaw, "retry-request") {
		t.Fatal("fresh unsent reply context was not retained")
	}
	in.Text = "conflicting payload"
	if err := f.server.receiveChannel(context.Background(), c.ID, c.Version, in); err == nil {
		t.Fatal("conflicting duplicate accepted")
	}
	var count int
	if err := f.store.db.QueryRow("SELECT count(*) FROM channel_inbox WHERE channel_id=?", c.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("dedupe failed", err)
	}
	requireStatus(t, f.request(t, "DELETE", "/api/agents/default", nil, true, nil), 409)
	input["secrets"] = map[string]string{"secret": "bot-private-key"}
	requireStatus(t, f.request(t, "POST", "/api/channels", input, true, nil), 409)
	input["config"] = map[string]string{"bot_id": "different"}
	requireStatus(t, f.request(t, "PUT", "/api/channels/"+c.ID, input, true, nil), 409)
	var encrypted string
	f.store.db.QueryRow("SELECT secrets FROM channels WHERE id=?", c.ID).Scan(&encrypted)
	if strings.Contains(encrypted, "bot-private-key") {
		t.Fatal("plaintext channel credential")
	}
	input["config"] = map[string]string{"bot_id": "support-bot"}
	input["enabled"] = false
	requireStatus(t, f.request(t, "PUT", "/api/channels/"+c.ID, input, true, nil), 200)
	requireStatus(t, f.request(t, "DELETE", "/api/channels/"+c.ID, nil, true, nil), 200)
	response = f.request(t, "POST", "/api/channels", input, true, nil)
	requireStatus(t, response, 201)
	var replacement Channel
	json.Unmarshal(response.Body.Bytes(), &replacement)
	if replacement.ID == c.ID {
		t.Fatal("recreated channel reused archived ownership")
	}
}
func TestInstalledProcessAdapterUsesCoreWithoutVendorSpecificRoutes(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node required")
	}
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "key")
	directory := filepath.Join(f.directory, "channel-adapters", "fixture")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{"v": 1, "entry": "worker.mjs", "provider": channels.Descriptor{ID: "fixture-im", Name: "Fixture IM", Description: "Local fixture", Transport: "polling", MaxTextBytes: 1024, IdentityFields: []string{"account"}, Fields: []channels.Field{{Key: "account", Label: "Account", Required: true}, {Key: "token", Label: "Token", Required: true, Secret: true}}}}
	raw, _ := json.Marshal(manifest)
	os.WriteFile(filepath.Join(directory, "adapter.json"), raw, 0600)
	worker := `import{createInterface}from'node:readline';const output=x=>process.stdout.write(JSON.stringify({v:1,...x})+'\n');const lines=createInterface({input:process.stdin});lines.on('line',line=>{const f=JSON.parse(line);if(f.type==='configure'){output({type:'status',state:'connected'});for(const [id,session,text] of [['first','member-a','first question'],['second','member-b','second question']])output({type:'incoming',incoming:{id,session,kind:'text',text,reply_context:{opaque:'fixture'}}});}if(f.type==='send')output({type:'result',id:f.id,result:{state:'sent',code:0,message:'private-token should be sanitized'}});});lines.on('close',()=>process.stdin.destroy());`
	os.WriteFile(filepath.Join(directory, "worker.mjs"), []byte(worker), 0600)
	if err := f.server.ConfigureChannelAdapters(node, "runtime/dist/channel-wecom.js"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer f.server.closeChannelConnections()
	response := f.request(t, "POST", "/api/channels", map[string]any{"provider": "fixture-im", "config": map[string]string{"account": "one"}, "secrets": map[string]string{"token": "private-token"}, "agent_id": "default", "enabled": true, "confirm": true}, true, nil)
	requireStatus(t, response, 201)
	var c Channel
	json.Unmarshal(response.Body.Bytes(), &c)
	for deadline := time.Now().Add(5 * time.Second); len(f.runtime.requests) < 2 && time.Now().Before(deadline); {
		if err := f.server.ChannelTick(ctx, time.Now()); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(f.runtime.requests) != 2 || f.runtime.requests[0]["conversation_id"] == f.runtime.requests[1]["conversation_id"] {
		t.Fatal("adapter did not use isolated core sessions")
	}
	run := f.runtime.requests[0]["run_id"].(string)
	completeChannelRun(t, f, run, "adapter reply")
	if err := f.server.ChannelTick(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	var state, message string
	if err := f.store.db.QueryRow("SELECT state,error FROM channel_outbox WHERE channel_id=? AND kind='final'", c.ID).Scan(&state, &message); err != nil || state != "sent" || strings.Contains(message, "private-token") {
		t.Fatal("process adapter did not confirm delivery safely", state, err)
	}
	requireStatus(t, f.request(t, "PUT", "/api/channels/"+c.ID, map[string]any{"provider": "fixture-im", "config": map[string]string{"account": "one"}, "agent_id": "default", "enabled": false}, true, nil), 200)
	if err := f.server.ChannelTick(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if status := f.server.channelConnectionStatus(c.ID); status.State != "stopped" {
		t.Fatal("disabled adapter did not stop", status)
	}
}
func TestVersionEightChannelMigrationPreservesDeliveryAndSessionOwnership(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "key")
	c := createChannel(t, f, true)
	requireStatus(t, callback(t, f, c, "809", "member", "prior message"), 200)
	tickChannel(t, f, time.Now())
	run := f.runtime.requests[0]["run_id"].(string)
	completeChannelRun(t, f, run, "prior reply")
	if err := f.server.prepareChannelReplies(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	var conversation, inbox string
	f.store.db.QueryRow("SELECT id,conversation_id FROM channel_inbox WHERE channel_id=?", c.ID).Scan(&inbox, &conversation)
	downgradeChannelFixture(t, f.store)
	if _, err := f.store.db.Exec("DROP TABLE retrieval_traces; DROP TABLE knowledge_eval_runs; DROP TABLE knowledge_eval_questions; PRAGMA user_version=8"); err != nil {
		t.Fatal(err)
	}
	f.store.Close()
	upgraded, err := Open(f.directory)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	server, err := New(upgraded, &fakeRuntime{}, func() bool { return true }, "")
	if err != nil {
		t.Fatal(err)
	}
	migrated, secrets, err := server.loadChannel(context.Background(), c.ID)
	if err != nil || migrated.Provider != "wecom-app" || secrets["secret"] != "channel-secret" {
		t.Fatal("legacy channel migration failed", err)
	}
	var migratedInbox, migratedConversation string
	if err := upgraded.db.QueryRow("SELECT id,conversation_id FROM channel_inbox WHERE channel_id=?", c.ID).Scan(&migratedInbox, &migratedConversation); err != nil || migratedInbox != inbox || migratedConversation != conversation {
		t.Fatal("migration changed message/session identities", err)
	}
	var count int
	upgraded.db.QueryRow("SELECT count(*) FROM channel_outbox WHERE channel_id=? AND state='pending'", c.ID).Scan(&count)
	if count != 1 {
		t.Fatal("migration lost pending reply")
	}
	rows, err := upgraded.db.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("migration broke foreign keys")
	}
}

func TestBrokenOptionalAdapterDoesNotDisableBuiltins(t *testing.T) {
	f := newFixture(t)
	directory := filepath.Join(f.directory, "channel-adapters", "broken")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "adapter.json"), []byte("invalid JSON"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := f.server.ConfigureChannelAdapters("node", "runtime/dist/channel-wecom.js"); err != nil {
		t.Fatal("optional adapter prevented core startup", err)
	}
	if _, ok := f.server.channelProviders.Get("wecom-bot"); !ok {
		t.Fatal("broken extension removed built-in provider")
	}
	if _, ok := f.server.channelProviders.Get("wecom-app"); !ok {
		t.Fatal("broken extension removed compatible provider")
	}
}

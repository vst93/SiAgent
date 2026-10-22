package platform

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"siagent/internal/channels"
)

// The conversation list must order by the latest exchange, not by creation: an
// active channel conversation would otherwise sink below a stale Web chat.
func TestConversationListOrdersByLastActivity(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "secret")
	older := f.conversation(t)
	newer := f.conversation(t)
	// The newer conversation is created last but goes quiet first, so ordering by
	// creation and ordering by activity disagree.
	second := f.start(t, newer, "较晚创建但很久没动")
	emit(t, f, second, "run_started", 1)
	emit(t, f, second, "run_completed", 2)
	emit(t, f, second, "run_released", 3)
	time.Sleep(20 * time.Millisecond)
	first := f.start(t, older, "较早创建但最近提问")
	emit(t, f, first, "run_started", 1)
	emit(t, f, first, "run_completed", 2)
	emit(t, f, first, "run_released", 3)
	response := f.request(t, "GET", "/api/conversations", nil, true, nil)
	requireStatus(t, response, 200)
	var values []Conversation
	if err := json.Unmarshal(response.Body.Bytes(), &values); err != nil {
		t.Fatal(err)
	}
	if len(values) < 2 {
		t.Fatalf("expected both conversations: %+v", values)
	}
	if values[0].ID != older {
		t.Fatalf("latest exchange was not listed first: %+v", values)
	}
	if values[0].LastActivity == 0 || values[0].TurnCount != 1 {
		t.Fatalf("activity or turn count missing: %+v", values[0])
	}
}

// The detail view pages: the newest page first, then earlier pages by cursor.
func TestConversationDetailPagesNewestFirst(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "secret")
	conversation := f.conversation(t)
	total := detailMessageLimit + 20
	for i := 0; i < total/2; i++ {
		run := f.start(t, conversation, "问题 "+strconv.Itoa(i))
		emit(t, f, run, "run_started", 1)
		emit(t, f, run, "run_completed", 2)
		emit(t, f, run, "run_released", 3)
	}
	response := f.request(t, "GET", "/api/conversations/"+conversation, nil, true, nil)
	requireStatus(t, response, 200)
	var page Snapshot
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != detailMessageLimit {
		t.Fatalf("expected one bounded page, got %d", len(page.Messages))
	}
	if !page.HasMore {
		t.Fatal("older messages were not reported")
	}
	if page.OldestID != page.Messages[0].ID {
		t.Fatalf("oldest cursor does not match the first message: %d %d", page.OldestID, page.Messages[0].ID)
	}
	// The page must be the newest messages, in reading order.
	last := page.Messages[len(page.Messages)-1]
	var newest int64
	if err := f.store.db.QueryRow("SELECT max(id) FROM messages WHERE conversation_id=?", conversation).Scan(&newest); err != nil {
		t.Fatal(err)
	}
	if last.ID != newest {
		t.Fatalf("newest message missing from the first page: %d %d", last.ID, newest)
	}
	for i := 1; i < len(page.Messages); i++ {
		if page.Messages[i-1].ID >= page.Messages[i].ID {
			t.Fatal("page is not in reading order")
		}
	}
	// The next page continues before the cursor and eventually ends.
	next := f.request(t, "GET", "/api/conversations/"+conversation+"?before="+strconv.FormatInt(page.OldestID, 10), nil, true, nil)
	requireStatus(t, next, 200)
	var earlier Snapshot
	if err := json.Unmarshal(next.Body.Bytes(), &earlier); err != nil {
		t.Fatal(err)
	}
	if len(earlier.Messages) == 0 || earlier.Messages[len(earlier.Messages)-1].ID >= page.OldestID {
		t.Fatalf("earlier page is not before the cursor: %+v", earlier.Messages)
	}
	if earlier.HasMore {
		t.Fatalf("final page still reports older messages: %d", len(earlier.Messages))
	}
	// A page must carry the runs of its own messages so status and thinking render.
	if len(earlier.Runs) == 0 {
		t.Fatal("earlier page lost its runs")
	}
	requireStatus(t, f.request(t, "GET", "/api/conversations/"+conversation+"?before=abc", nil, true, nil), 400)
	requireStatus(t, f.request(t, "GET", "/api/conversations/"+conversation+"?limit=0", nil, true, nil), 400)
}

// Channel contacts are labelled by the administrator because the vendor protocol
// has no names; the label must reach both the list and the diagnostics.
func TestChannelContactLabelsResolveInLists(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "secret")
	c, _ := newImageChannel(t, f)
	group := `["group","room-1","member-1"]`
	if err := f.server.receiveChannel(context.Background(), c.ID, c.Version, channels.Incoming{ID: "g1", ChatType: "group", SenderID: "member-1", GroupID: "room-1", Kind: "text", Text: "群里的问题"}); err != nil {
		t.Fatal(err)
	}
	if err := f.server.admitChannelInbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Without a label the list falls back to the vendor IDs.
	response := f.request(t, "GET", "/api/conversations", nil, true, nil)
	requireStatus(t, response, 200)
	var values []Conversation
	if err := json.Unmarshal(response.Body.Bytes(), &values); err != nil {
		t.Fatal(err)
	}
	if len(values) != 1 || values[0].ChannelGroup != "room-1" || values[0].GroupName != "" {
		t.Fatalf("unexpected unlabelled conversation: %+v", values)
	}
	if values[0].ChannelName == "" {
		t.Fatal("channel display name missing")
	}
	// Labels are validated.
	requireStatus(t, f.request(t, "PUT", "/api/channel-contacts", map[string]any{"channel_id": c.ID, "kind": "group", "target": "room-1", "name": "产品讨论群"}, true, nil), 200)
	requireStatus(t, f.request(t, "PUT", "/api/channel-contacts", map[string]any{"channel_id": c.ID, "kind": "sender", "target": "member-1", "name": "张三"}, true, nil), 200)
	requireStatus(t, f.request(t, "PUT", "/api/channel-contacts", map[string]any{"channel_id": c.ID, "kind": "room", "target": "room-1", "name": "x"}, true, nil), 400)
	requireStatus(t, f.request(t, "PUT", "/api/channel-contacts", map[string]any{"channel_id": c.ID, "kind": "group", "target": "room-1", "name": string(make([]byte, 200))}, true, nil), 400)
	requireStatus(t, f.request(t, "PUT", "/api/channel-contacts", map[string]any{"channel_id": "missing", "kind": "group", "target": "room-1", "name": "x"}, true, nil), 404)
	response = f.request(t, "GET", "/api/conversations", nil, true, nil)
	requireStatus(t, response, 200)
	if err := json.Unmarshal(response.Body.Bytes(), &values); err != nil {
		t.Fatal(err)
	}
	if values[0].GroupName != "产品讨论群" || values[0].SenderName != "张三" {
		t.Fatalf("labels did not reach the list: %+v", values[0])
	}
	// The detail snapshot carries them too, so the header shows a readable name.
	response = f.request(t, "GET", "/api/conversations/"+values[0].ID, nil, true, nil)
	requireStatus(t, response, 200)
	var page Snapshot
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Conversation.GroupName != "产品讨论群" || page.Conversation.SenderName != "张三" {
		t.Fatalf("snapshot lost the labels: %+v", page.Conversation)
	}
	// Diagnostics show who sent each inbound record.
	response = f.request(t, "GET", "/api/channels/"+c.ID+"/diagnostics", nil, true, nil)
	requireStatus(t, response, 200)
	var diagnostics struct {
		Inbox []map[string]any `json:"inbox"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &diagnostics); err != nil {
		t.Fatal(err)
	}
	if len(diagnostics.Inbox) == 0 || diagnostics.Inbox[0]["group_name"] != "产品讨论群" || diagnostics.Inbox[0]["sender_name"] != "张三" {
		t.Fatalf("diagnostics lost the labels: %+v", diagnostics.Inbox)
	}
	// Clearing the name removes the label and falls back to the raw ID. The slice
	// is reset first: omitted empty fields would otherwise keep the previous value.
	requireStatus(t, f.request(t, "PUT", "/api/channel-contacts", map[string]any{"channel_id": c.ID, "kind": "group", "target": "room-1", "name": ""}, true, nil), 200)
	response = f.request(t, "GET", "/api/conversations", nil, true, nil)
	requireStatus(t, response, 200)
	values = nil
	if err := json.Unmarshal(response.Body.Bytes(), &values); err != nil {
		t.Fatal(err)
	}
	if values[0].GroupName != "" || values[0].SenderName != "张三" {
		t.Fatalf("clearing one label affected another: %+v", values[0])
	}
	_ = group
}

// Two sessions with the same member must be separate rows so the UI can draw a
// session divider between them.
func TestChannelSessionsGroupUnderOneContact(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "secret")
	c, _ := newImageChannel(t, f)
	for index, id := range []string{"s1", "s2"} {
		// A reset opens the next session, exactly as /new does between messages.
		if index > 0 {
			if _, err := f.server.channelConversation(context.Background(), c.ID, `["single","","member-9"]`, "default", true); err != nil {
				t.Fatal(err)
			}
		}
		if err := f.server.receiveChannel(context.Background(), c.ID, c.Version, channels.Incoming{ID: id, ChatType: "single", SenderID: "member-9", Kind: "text", Text: "问题 " + id}); err != nil {
			t.Fatal(err)
		}
		if err := f.server.admitChannelInbox(context.Background()); err != nil {
			t.Fatal(err)
		}
		run := f.runtime.requests[len(f.runtime.requests)-1]["run_id"].(string)
		emit(t, f, run, "run_started", 1)
		emit(t, f, run, "run_completed", 2)
		emit(t, f, run, "run_released", 3)
	}
	response := f.request(t, "GET", "/api/conversations", nil, true, nil)
	requireStatus(t, response, 200)
	var values []Conversation
	if err := json.Unmarshal(response.Body.Bytes(), &values); err != nil {
		t.Fatal(err)
	}
	if len(values) != 2 {
		t.Fatalf("expected two sessions for one member: %d", len(values))
	}
	for _, value := range values {
		if value.ChannelSender != "member-9" || value.ChannelKind != "single" {
			t.Fatalf("session identity missing: %+v", value)
		}
		if value.LastActivity == 0 || value.TurnCount != 1 {
			t.Fatalf("session counters missing: %+v", value)
		}
	}
}

// Members can name themselves and their group because the vendor protocol has no
// names. The command must be answered without creating a run.
func TestChannelNamingCommandsLabelWithoutRunning(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "secret")
	c, _ := newImageChannel(t, f)
	send := func(id, text, sender, group string) {
		t.Helper()
		in := channels.Incoming{ID: id, ChatType: "group", SenderID: sender, GroupID: group, Kind: "text", Text: text}
		if group == "" {
			in = channels.Incoming{ID: id, ChatType: "single", SenderID: sender, Kind: "text", Text: text}
		}
		if err := f.server.receiveChannel(context.Background(), c.ID, c.Version, in); err != nil {
			t.Fatal(err)
		}
	}
	send("cmd-1", "/user 张三", "member-1", "room-1")
	send("cmd-2", "/group 产品讨论群", "member-1", "room-1")
	var state, reason string
	if err := f.store.db.QueryRow("SELECT state,error FROM channel_inbox WHERE msg_id='cmd-1'").Scan(&state, &reason); err != nil {
		t.Fatal(err)
	}
	if state != "rejected" || !strings.Contains(reason, "张三") {
		t.Fatalf("user command was not acknowledged: %s %q", state, reason)
	}
	if len(f.runtime.requests) != 0 {
		t.Fatal("naming command created a run")
	}
	// Repeated commands keep the last value.
	send("cmd-3", "/user 张三丰", "member-1", "room-1")
	if err := f.server.admitChannelInbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.runtime.requests) != 0 {
		t.Fatal("naming commands reached the model")
	}
	var name, source string
	if err := f.store.db.QueryRow("SELECT name,source FROM channel_contacts WHERE channel_id=? AND kind='sender' AND target='member-1'", c.ID).Scan(&name, &source); err != nil {
		t.Fatal(err)
	}
	if name != "张三丰" || source != "member" {
		t.Fatalf("member label was not replaced: %q %q", name, source)
	}
	// The label reaches the console list.
	response := f.request(t, "GET", "/api/conversations", nil, true, nil)
	requireStatus(t, response, 200)
	var values []Conversation
	if err := json.Unmarshal(response.Body.Bytes(), &values); err != nil {
		t.Fatal(err)
	}
	if len(values) != 0 {
		t.Fatalf("naming commands must not create conversations: %+v", values)
	}
	// A real question then appears with both labels resolved.
	send("q-1", "群里的问题", "member-1", "room-1")
	if err := f.server.admitChannelInbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	response = f.request(t, "GET", "/api/conversations", nil, true, nil)
	requireStatus(t, response, 200)
	values = nil
	if err := json.Unmarshal(response.Body.Bytes(), &values); err != nil {
		t.Fatal(err)
	}
	if len(values) != 1 {
		t.Fatalf("expected one conversation: %+v", values)
	}
	if values[0].GroupName != "产品讨论群" || values[0].SenderName != "张三丰" || values[0].LastSenderName != "张三丰" {
		t.Fatalf("member labels did not resolve: %+v", values[0])
	}
	if values[0].LabelSource != "member" {
		t.Fatalf("label provenance missing: %+v", values[0])
	}
	// An administrator override wins and records its own provenance.
	requireStatus(t, f.request(t, "PUT", "/api/channel-contacts", map[string]any{"channel_id": c.ID, "kind": "group", "target": "room-1", "name": "客服群"}, true, nil), 200)
	if err := f.store.db.QueryRow("SELECT source FROM channel_contacts WHERE channel_id=? AND kind='group' AND target='room-1'", c.ID).Scan(&source); err != nil || source != "admin" {
		t.Fatalf("admin override did not win: %q %v", source, err)
	}
	// The Agent switch turns the commands off.
	policy := defaultAgentPolicy()
	policy.Session.AllowNaming = false
	setTestPolicy(t, f, policy)
	send("cmd-4", "/user 不应保存", "member-2", "room-1")
	if err := f.store.db.QueryRow("SELECT error FROM channel_inbox WHERE msg_id='cmd-4'").Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reason, "关闭") {
		t.Fatalf("disabled naming was not reported: %q", reason)
	}
	var stored int
	if err := f.store.db.QueryRow("SELECT count(*) FROM channel_contacts WHERE target='member-2'").Scan(&stored); err != nil || stored != 0 {
		t.Fatalf("disabled naming still wrote a label: %d %v", stored, err)
	}
}

func TestChannelNamingCommandUsageAndMentions(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "secret")
	c, _ := newImageChannel(t, f)
	reply := func(id, text, chatType, sender, group string) string {
		t.Helper()
		in := channels.Incoming{ID: id, ChatType: chatType, SenderID: sender, GroupID: group, Kind: "text", Text: text}
		if err := f.server.receiveChannel(context.Background(), c.ID, c.Version, in); err != nil {
			t.Fatal(err)
		}
		var reason string
		if err := f.store.db.QueryRow("SELECT error FROM channel_inbox WHERE msg_id=?", id).Scan(&reason); err != nil {
			t.Fatal(err)
		}
		return reason
	}
	// A missing argument explains the syntax instead of writing a label.
	if got := reply("u1", "/user", "single", "member-1", ""); !strings.Contains(got, "用法") {
		t.Fatalf("missing argument was accepted: %q", got)
	}
	// /group is meaningless in a private chat.
	if got := reply("g1", "/group 群名", "single", "member-1", ""); !strings.Contains(got, "仅群聊") {
		t.Fatalf("private group rename was accepted: %q", got)
	}
	// Too long names are refused.
	if got := reply("u2", "/user "+strings.Repeat("名", 80), "single", "member-1", ""); !strings.Contains(got, "64") {
		t.Fatalf("over-long name was accepted: %q", got)
	}
	// A leading mention is tolerated, because WeCom keeps it in the body.
	if got := reply("u3", "@机器人 /user 李四", "single", "member-1", ""); !strings.Contains(got, "李四") {
		t.Fatalf("mention-prefixed command was ignored: %q", got)
	}
	// Ordinary text that merely starts with a slash is not a command.
	if got := reply("t1", "/userland 不是命令", "single", "member-1", ""); !strings.Contains(got, "文字") && got != "" {
		t.Fatalf("non-command was intercepted: %q", got)
	}
	var name string
	if err := f.store.db.QueryRow("SELECT name FROM channel_contacts WHERE target='member-1'").Scan(&name); err != nil || name != "李四" {
		t.Fatalf("label not stored: %q %v", name, err)
	}
}

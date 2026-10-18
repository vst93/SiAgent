package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	agentruntime "siagent/internal/runtime"
)

type maintenanceRuntime func(any) error

func (fn maintenanceRuntime) Send(value any) error { return fn(value) }
func acknowledgeCleanup(t *testing.T, s *Server, kind string) {
	t.Helper()
	s.runtime = maintenanceRuntime(func(value any) error {
		req := value.(map[string]any)
		return s.HandleEvent(agentruntime.Event{Type: kind, RequestID: req["request_id"].(string), ConversationID: req["conversation_id"].(string)})
	})
}
func TestConversationDeletionRequiresConfirmationQuiescenceAndRuntimeAck(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "secret")
	conversation := f.conversation(t)
	path := "/api/conversations/" + conversation
	requireStatus(t, f.request(t, "DELETE", path, map[string]bool{"confirm": true}, false, nil), 401)
	requireStatus(t, f.request(t, "DELETE", path, map[string]bool{"confirm": false}, true, nil), 400)
	run := f.start(t, conversation, "erase this")
	requireStatus(t, f.request(t, "DELETE", path, map[string]bool{"confirm": true}, true, nil), 409)
	emit(t, f, run, "run_completed", 1)
	if _, err := f.store.db.Exec("INSERT INTO answer_feedback(run_id,rating,created,updated) VALUES(?,?,?,?)", run, "helpful", time.Now().UnixMilli(), time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, f.request(t, "DELETE", path, map[string]bool{"confirm": true}, true, nil), 409)
	emit(t, f, run, "run_released", 2)
	requireStatus(t, f.request(t, "DELETE", path, map[string]bool{"confirm": true}, true, nil), 202)
	requireStatus(t, f.request(t, "GET", path, nil, true, nil), 404)
	requireStatus(t, f.request(t, "POST", path+"/runs", map[string]string{"input": "blocked"}, true, nil), 404)
	acknowledgeCleanup(t, f.server, "error")
	if err := f.server.MaintenanceTick(context.Background()); err != nil {
		t.Fatal(err)
	}
	var records int
	f.store.db.QueryRow("SELECT count(*) FROM messages WHERE conversation_id=?", conversation).Scan(&records)
	if records != 2 {
		t.Fatal("records erased without Runtime acknowledgement")
	}
	// Reopening the server leaves the durable request available for retry.
	restarted, err := New(f.store, f.runtime, func() bool { return true }, "")
	if err != nil {
		t.Fatal(err)
	}
	f.server = restarted
	acknowledgeCleanup(t, f.server, "conversation_deleted")
	if err := f.server.MaintenanceTick(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"messages", "runs", "conversations", "conversation_cleanup"} {
		if err := f.store.db.QueryRow("SELECT count(*) FROM " + table).Scan(&records); err != nil || records != 0 {
			t.Fatalf("%s retained deleted records", table)
		}
	}
	requireStatus(t, f.request(t, "GET", "/api/agent", nil, true, nil), 200)
}
func TestStorageCountsFilesWithoutFollowingSymlinks(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	requireStatus(t, f.request(t, "GET", "/api/storage", nil, false, nil), 401)
	workspace := filepath.Join(f.directory, "workspaces")
	os.Mkdir(workspace, 0700)
	if err := os.WriteFile(filepath.Join(workspace, "visible"), []byte("12345"), 0600); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "outside"), make([]byte, 4096), 0600)
	if err := os.Symlink(outside, filepath.Join(workspace, "link")); err != nil {
		t.Fatal(err)
	}
	response := f.request(t, "GET", "/api/storage", nil, true, nil)
	requireStatus(t, response, 200)
	var report storageReport
	if err := json.Unmarshal(response.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if !report.Complete || report.Groups["workspaces"] != 5 || report.Total == 0 || report.Available > report.Total {
		t.Fatalf("incorrect disk report: %+v", report)
	}
}

func TestChannelHistoryCleanupRetainsDedupAndStartsFreshMemberContext(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "key")
	channel := createChannel(t, f, true)
	_, _ = wecomAPI(t, f)
	raw := fmt.Sprintf("<xml><ToUserName>%s</ToUserName><AgentID>%d</AgentID><FromUserName>member</FromUserName><CreateTime>1</CreateTime><MsgType>text</MsgType><Content>private text</Content><MsgId>400</MsgId></xml>", channel.CorpID, channel.AppID)
	requireStatus(t, callbackXML(f, channel, raw, "callback-token"), 200)
	tickChannel(t, f, time.Now())
	run := f.runtime.requests[0]["run_id"].(string)
	conversation := f.runtime.requests[0]["conversation_id"].(string)
	completeChannelRun(t, f, run, "private answer")
	tickChannel(t, f, time.Now())
	requireStatus(t, f.request(t, "DELETE", "/api/conversations/"+conversation, map[string]bool{"confirm": true}, true, nil), 202)
	acknowledgeCleanup(t, f.server, "conversation_deleted")
	if err := f.server.MaintenanceTick(context.Background()); err != nil {
		t.Fatal(err)
	}
	var member, content string
	if err := f.store.db.QueryRow("SELECT member,content FROM channel_inbox WHERE msg_id='400'").Scan(&member, &content); err != nil || member != "" || content != "" {
		t.Fatal("channel history content was retained")
	}
	f.server.runtime = f.runtime
	requireStatus(t, callbackXML(f, channel, raw, "callback-token"), 200)
	tickChannel(t, f, time.Now())
	if len(f.runtime.requests) != 1 {
		t.Fatal("cleaned message replayed on duplicate callback")
	}
	requireStatus(t, callback(t, f, channel, "401", "member", "fresh message"), 200)
	tickChannel(t, f, time.Now())
	if len(f.runtime.requests) != 2 || f.runtime.requests[1]["conversation_id"] == conversation {
		t.Fatal("member reused deleted context")
	}
}

func TestCleanupFailureDoesNotStarveOtherRequests(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "key")
	first, second := f.conversation(t), f.conversation(t)
	for _, conversation := range []string{first, second} {
		requireStatus(t, f.request(t, "DELETE", "/api/conversations/"+conversation, map[string]bool{"confirm": true}, true, nil), 202)
	}
	seen := map[string]bool{}
	f.server.runtime = maintenanceRuntime(func(value any) error {
		request := value.(map[string]any)
		seen[request["conversation_id"].(string)] = true
		return f.server.HandleEvent(agentruntime.Event{Type: "error", RequestID: request["request_id"].(string)})
	})
	for range 2 {
		if err := f.server.MaintenanceTick(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 2 {
		t.Fatal("failed cleanup blocked another conversation")
	}
}

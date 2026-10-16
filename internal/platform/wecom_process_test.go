package platform

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentruntime "siagent/internal/runtime"
	"siagent/internal/wecom"
)

func TestWecomCompleteLocalRuntimePath(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is required for real Runtime validation")
	}
	script, err := filepath.Abs("../../runtime/dist/index.js")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(script); err != nil {
		t.Skip("build Runtime first with make build or make test")
	}
	f := newFixture(t)
	f.setup(t)
	modelCalls := make(chan map[string]any, 4)
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer model-secret" {
			t.Error("runtime used incorrect model credential")
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		modelCalls <- body
		w.Header().Set("Content-Type", "text/event-stream")
		for _, choice := range []map[string]any{{"delta": map[string]string{"role": "assistant", "content": "wecom runtime reply"}, "finish_reason": nil}, {"delta": map[string]string{}, "finish_reason": "stop"}} {
			payload, _ := json.Marshal(map[string]any{"id": "wecom-model", "object": "chat.completion.chunk", "created": 1, "model": "fixture", "choices": []any{map[string]any{"index": 0, "delta": choice["delta"], "finish_reason": choice["finish_reason"]}}})
			fmt.Fprintf(w, "data: %s\n\n", payload)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer model.Close()
	requireStatus(t, f.request(t, "PUT", "/api/model", map[string]string{"base_url": model.URL + "/v1", "name": "fixture", "api_key": "model-secret"}, true, nil), 200)
	role := saveTestAgent(t, f, Agent{Name: "Channel role", Instructions: "Use the channel role only.", Enabled: true, ModelID: 1})
	c := createChannel(t, f, false)
	requireStatus(t, f.request(t, "PUT", "/api/wecom/channels/"+c.ID, map[string]any{"corp_id": c.CorpID, "app_id": c.AppID, "agent_id": role.ID, "enabled": true, "confirm": true}, true, nil), 200)
	_, sent := wecomAPI(t, f)
	supervisor := agentruntime.New(node, script, f.directory)
	f.server.runtime = supervisor
	f.server.ready = supervisor.Ready.Load
	ctx, cancel := context.WithCancel(context.Background())
	runtimeDone := make(chan error, 1)
	eventsDone := make(chan error, 1)
	channelsDone := make(chan error, 1)
	schedulerDone := make(chan error, 1)
	go func() { runtimeDone <- supervisor.Run(ctx) }()
	go func() {
		for event := range supervisor.Events {
			if err := f.server.HandleEvent(event); err != nil {
				eventsDone <- err
				return
			}
		}
		eventsDone <- nil
	}()
	go func() { channelsDone <- f.server.RunChannels(ctx) }()
	go func() { schedulerDone <- f.server.RunScheduler(ctx) }()
	t.Cleanup(func() {
		cancel()
		for _, done := range []chan error{runtimeDone, eventsDone, channelsDone, schedulerDone} {
			select {
			case err := <-done:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(10 * time.Second):
				t.Error("channel/runtime did not stop")
			}
		}
		f.server.StopKnowledge()
	})
	for attempt := 0; !supervisor.Ready.Load(); attempt++ {
		if attempt > 300 {
			t.Fatal("Runtime failed to become ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	message := wecom.Message{CorpID: c.CorpID, AppID: c.AppID, Member: "member-a", ID: "10000", Type: "text", Content: "from member-a", Created: 10}
	raw, _ := xml.Marshal(message)
	requireStatus(t, callbackXML(f, c, string(raw), "callback-token"), 200)
	requireStatus(t, callbackXML(f, c, string(raw), "callback-token"), 200)
	select {
	case body := <-sent:
		if body["touser"] != "member-a" || body["text"].(map[string]any)["content"] != "wecom runtime reply" {
			t.Fatal("wrong callback-to-outbox result", body)
		}
	case <-time.After(12 * time.Second):
		t.Fatal("signed callback did not complete a real Runtime round trip")
	}
	if len(modelCalls) != 1 {
		t.Fatal("duplicate callback repeated model execution")
	}
	body := <-modelCalls
	encoded, _ := json.Marshal(body["messages"])
	if !strings.Contains(string(encoded), role.Instructions) || !strings.Contains(string(encoded), "from member-a") {
		t.Fatal("wrong model scope or context")
	}
	var count int
	var conversation string
	if err := f.store.db.QueryRow("SELECT count(*),max(conversation_id) FROM runs").Scan(&count, &conversation); err != nil || count != 1 {
		t.Fatal("duplicate created another task")
	}
	f.server.mu.Lock()
	snapshot, err := f.server.snapshot(conversation)
	f.server.mu.Unlock()
	if err != nil || snapshot.Conversation.AgentID != role.ID || snapshot.Conversation.ChannelID != c.ID || snapshot.Runs[0].Status != "completed" {
		t.Fatal("channel execution lost its owner or result")
	}
}

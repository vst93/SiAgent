package platform

import (
	"testing"
	"time"
)

// A failed interruption during an expected restart used to leave the in-memory
// restart flag set while the runtime was serving again, rejecting every new
// message with "对话服务正在启动或恢复". Readiness is the authoritative gate,
// and the scheduler heals a stale flag on its next tick.
func TestStaleRestartFlagDoesNotBlockAReadyRuntime(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "model-secret")
	conversation := f.conversation(t)

	f.server.mu.Lock()
	f.server.restarting = true
	f.server.mu.Unlock()
	if err := f.server.Tick(time.Now()); err != nil {
		t.Fatal(err)
	}
	f.server.mu.Lock()
	stale := f.server.restarting
	f.server.mu.Unlock()
	if stale {
		t.Fatal("a ready runtime did not clear the stale restart flag")
	}
	if run := f.start(t, conversation, "after recovery"); run == "" {
		t.Fatal("run was not created after recovery")
	}

	// Admission no longer depends on the flag at all: even if it is set again
	// without an actual restart, a ready runtime still accepts work.
	f.server.mu.Lock()
	f.server.restarting = true
	f.server.mu.Unlock()
	if run := f.start(t, conversation, "still ready"); run == "" {
		t.Fatal("ready runtime rejected a message because of a stale flag")
	}
}

func TestNotReadyRuntimeStillRejectsNewMessages(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "model-secret")
	conversation := f.conversation(t)
	// The fixture's readiness callback is fixed at construction; flip the
	// supervisor-facing state the same way a stopped runtime would.
	f.server.ready = func() bool { return false }
	requireStatus(t, f.request(t, "POST", "/api/conversations/"+conversation+"/runs", map[string]string{"input": "hello"}, true, nil), 503)
}

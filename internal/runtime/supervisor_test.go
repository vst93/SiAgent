package runtime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSupervisorRestartAndShutdown(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js not installed")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "runtime.cjs")
	err = os.WriteFile(script, []byte(`
const readline = require('node:readline');
const send = e => console.log(JSON.stringify({v:1,...e}));
send({type:'ready'});
readline.createInterface({input:process.stdin}).on('line', line => {
 const req = JSON.parse(line);
 if (req.type === 'crash') process.exit(9);
 send({type:'status',request_id:req.request_id,data:{status:'completed'}});
});
`), 0600)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	supervisor := New(node, script, dir)
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(8 * time.Second):
			t.Error("shutdown timed out")
		}
	})
	wait := func(kind string) Event {
		t.Helper()
		select {
		case e := <-supervisor.Events:
			if e.Type != kind {
				t.Fatalf("want %s, got %s", kind, e.Type)
			}
			return e
		case <-time.After(5 * time.Second):
			t.Fatalf("waiting for %s", kind)
			return Event{}
		}
	}
	wait("ready")
	if !supervisor.Ready.Load() {
		t.Fatal("expected ready")
	}
	if err := supervisor.Send(map[string]any{"v": 1, "type": "status", "request_id": "one"}); err != nil {
		t.Fatal(err)
	}
	if e := wait("status"); e.RequestID != "one" {
		t.Fatalf("lost request correlation: %+v", e)
	}
	if err := supervisor.Send(map[string]any{"v": 1, "type": "crash"}); err != nil {
		t.Fatal(err)
	}
	wait("runtime_unavailable")
	wait("ready")
	if !supervisor.Ready.Load() {
		t.Fatal("expected ready after restart")
	}
}

func TestSendBeforeReady(t *testing.T) {
	if err := New("node", "missing", "unused").Send(map[string]any{"v": 1}); err == nil {
		t.Fatal("expected not ready error")
	}
}

func TestUnresponsiveRuntimeInputHasBoundedWrite(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js not installed")
	}
	directory := t.TempDir()
	script := filepath.Join(directory, "blocked.cjs")
	if err := os.WriteFile(script, []byte(`console.log(JSON.stringify({v:1,type:'ready'}));setInterval(()=>{},1000);`), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	supervisor := New(node, script, directory)
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(8 * time.Second):
			t.Error("blocked peer did not shut down")
		}
	})
	select {
	case <-supervisor.Events:
	case <-time.After(5 * time.Second):
		t.Fatal("peer did not become ready")
	}
	started := time.Now()
	if err := supervisor.Send(map[string]string{"payload": strings.Repeat("x", 1<<20)}); err == nil {
		t.Fatal("expected blocked input deadline")
	}
	if time.Since(started) > 3*time.Second {
		t.Fatal("input blocked cancellation watchdog")
	}
}

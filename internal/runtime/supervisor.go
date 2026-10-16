// Package runtime supervises the private Node.js NDJSON adapter.
package runtime

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

type Event struct {
	Version        int             `json:"v"`
	Type           string          `json:"type"`
	RequestID      string          `json:"request_id,omitempty"`
	RunID          string          `json:"run_id,omitempty"`
	ConversationID string          `json:"conversation_id,omitempty"`
	Sequence       int64           `json:"seq,omitempty"`
	Data           json.RawMessage `json:"data,omitempty"`
	Code           string          `json:"code,omitempty"`
}

type Supervisor struct {
	Node, Script, DataDir string
	Events                chan Event
	Ready                 atomic.Bool
	mu                    sync.Mutex
	writeMu               sync.Mutex
	process               *os.Process
	stdin                 io.WriteCloser
}

func New(node, script, dataDir string) *Supervisor {
	return &Supervisor{Node: node, Script: script, DataDir: dataDir, Events: make(chan Event, 256)}
}

func (s *Supervisor) Send(request any) error {
	data, err := json.Marshal(request)
	if err != nil {
		return err
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.mu.Lock()
	stdin := s.stdin
	s.mu.Unlock()
	if !s.Ready.Load() || stdin == nil {
		return errors.New("runtime not ready")
	}
	deadline, ok := stdin.(interface{ SetWriteDeadline(time.Time) error })
	if !ok {
		return errors.New("runtime input does not support write deadlines")
	}
	if err := deadline.SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return err
	}
	_, err = stdin.Write(append(data, '\n'))
	return err

}

// Run allows three bounded restarts, and never resubmits requests after a crash.
func (s *Supervisor) Run(ctx context.Context) error {
	defer close(s.Events)
	for attempt := 0; attempt < 4; attempt++ {
		err := s.runOnce(ctx)
		if ctx.Err() != nil {
			return nil
		}
		select {
		case s.Events <- Event{Version: 1, Type: "runtime_unavailable"}:
		case <-ctx.Done():
			return nil
		}
		slog.Warn("runtime exited", "attempt", attempt+1, "error", err)
		if attempt == 3 {
			return fmt.Errorf("runtime restart budget exhausted: %w", err)
		}
		timer := time.NewTimer(time.Duration(1<<attempt) * 250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
	return nil
}

func (s *Supervisor) runOnce(ctx context.Context) error {
	cmd := exec.Command(s.Node, s.Script)
	cmd.Env = append(os.Environ(), "SIAGENT_DATA_DIR="+s.DataDir)
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		return err
	}
	s.mu.Lock()
	s.stdin = stdin
	s.process = cmd.Process
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.Ready.Store(false)
		s.stdin = nil
		s.process = nil
		stdin.Close()
		s.mu.Unlock()
	}()
	finished := make(chan error, 1)
	becameReady := make(chan struct{}, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
		var protocolErr error
		for scanner.Scan() {
			var event Event
			if err := json.Unmarshal(scanner.Bytes(), &event); err != nil || event.Version != 1 || event.Type == "" {
				protocolErr = errors.New("invalid runtime protocol frame")
				cmd.Process.Kill()
				break
			}
			if event.Type == "ready" {
				s.Ready.Store(true)
				select {
				case becameReady <- struct{}{}:
				default:
				}
			}
			select {
			case s.Events <- event:
			case <-ctx.Done():
			}
		}
		if scanner.Err() != nil {
			protocolErr = scanner.Err()
			cmd.Process.Kill()
		}
		s.Ready.Store(false)
		_ = cmd.Process.Kill() // EOF also means loss of the private control channel.
		waitErr := cmd.Wait()
		s.Ready.Store(false)
		if protocolErr != nil {
			finished <- protocolErr
		} else if waitErr != nil {
			finished <- waitErr
		} else {
			finished <- errors.New("runtime closed its output")
		}
	}()
	startup := time.NewTimer(15 * time.Second)
	defer startup.Stop()
	for {
		select {
		case err := <-finished:
			return err
		case <-becameReady:
			startup.Stop()
		case <-startup.C:
			cmd.Process.Kill()
			<-finished
			return errors.New("runtime readiness timed out")
		case <-ctx.Done():
			cmd.Process.Signal(os.Interrupt)
			timer := time.NewTimer(5 * time.Second)
			defer timer.Stop()
			select {
			case <-finished:
			case <-timer.C:
				cmd.Process.Kill()
				<-finished
			}
			return nil
		}
	}
}

// Restart forces a peer that did not acknowledge cancellation to exit. The
// normal supervisor path observes the exit and applies its bounded restart policy.
func (s *Supervisor) Restart() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Ready.Store(false)
	if s.process == nil {
		return nil
	}
	err := s.process.Kill()
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}

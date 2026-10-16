package channels

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// ProcessProvider hosts trusted adapters over bounded versioned NDJSON, without
// linking their platform dependencies into the Agent runtime.
type ProcessProvider struct {
	Spec      Descriptor
	Node      string
	Script    string
	Directory string
	Args      []string
}

func (p *ProcessProvider) Descriptor() Descriptor  { return p.Spec }
func (p *ProcessProvider) Validate(c Config) error { _, err := ValidateFields(p.Spec, c); return err }
func (p *ProcessProvider) Open(ctx context.Context, c Config, receive Receive) (Connection, error) {
	ctx, cancel := context.WithCancel(ctx)
	args := append([]string{p.Script}, p.Args...)
	cmd := exec.CommandContext(ctx, p.Node, args...)
	cmd.Dir = p.Directory
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = time.Second
	// Credentials travel only over stdin; never arguments, inherited credentials or logs.
	cmd.Env = []string{"NODE_ENV=production"}
	cmd.Stderr = io.Discard
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	worker := &processConnection{cancel: cancel, input: stdin, pending: map[string]chan Result{}, status: Status{"connecting", "正在建立渠道连接。"}, done: make(chan struct{})}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, errors.New("无法启动渠道适配器，请检查安装文件。")
	}
	go func() {
		defer close(worker.done)
		defer cancel()
		defer func() {
			worker.mu.Lock()
			worker.status = Status{"disconnected", "渠道适配器已停止；重新启用可重建连接。"}
			for key, ch := range worker.pending {
				ch <- Result{"unknown", 0, "适配器在发送确认前退出，请核对渠道，不自动重发。"}
				delete(worker.pending, key)
			}
			worker.mu.Unlock()
		}()
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 4096), 262144)
		for scanner.Scan() {
			var frame struct {
				V        int      `json:"v"`
				Type     string   `json:"type"`
				ID       string   `json:"id"`
				State    string   `json:"state"`
				Incoming Incoming `json:"incoming"`
				Result   Result   `json:"result"`
			}
			if json.Unmarshal(scanner.Bytes(), &frame) != nil || frame.V != 1 {
				break
			}
			switch frame.Type {
			case "status":
				messages := map[string]string{"connecting": "正在连接渠道。", "connected": "渠道连接和认证成功。", "reconnecting": "连接断开，正在退避重连。", "disconnected": "渠道连接已断开。", "failed": "渠道认证或连接失败，请检查配置。"}
				message, ok := messages[frame.State]
				if !ok {
					break
				}
				worker.mu.Lock()
				worker.status = Status{frame.State, message}
				worker.mu.Unlock()
			case "incoming":
				accepted := false
				if receive != nil {
					receiveCtx, stop := context.WithTimeout(ctx, 3*time.Second)
					accepted = receive(receiveCtx, frame.Incoming) == nil
					stop()
				}
				if worker.write(map[string]any{"v": 1, "type": "receipt", "id": frame.Incoming.ID, "accepted": accepted}) != nil {
					cancel()
				}
			case "result":
				worker.mu.Lock()
				ch, ok := worker.pending[frame.ID]
				if ok {
					delete(worker.pending, frame.ID)
					switch frame.Result.State {
					case "sent", "failed", "retry", "unknown":
					default:
						frame.Result = Result{"unknown", 0, "适配器返回了无效的投递状态，请人工核对。"}
					}
					messages := map[string]string{"sent": "渠道已接受。", "failed": "渠道明确拒绝消息，请检查配置和发送限制。", "retry": "渠道未接受消息，稍后有限重试。", "unknown": "未收到投递确认，请人工核对，不自动重发。"}
					frame.Result.Message = messages[frame.Result.State]
					ch <- frame.Result
				}
				worker.mu.Unlock()
			default:
				cancel()
			}
		}
		cancel()
		_ = cmd.Wait()
	}()
	if err := worker.write(map[string]any{"v": 1, "type": "configure", "config": c}); err != nil {
		worker.Close()
		return nil, errors.New("无法初始化渠道适配器。")
	}
	return worker, nil
}

type processConnection struct {
	cancel  context.CancelFunc
	input   io.WriteCloser
	writeMu sync.Mutex
	mu      sync.Mutex
	status  Status
	pending map[string]chan Result
	done    chan struct{}
}

func (p *processConnection) write(value any) error {
	data, err := json.Marshal(value)
	if err != nil || len(data) > 262143 {
		return errors.New("channel protocol frame too large")
	}
	data = append(data, '\n')
	done := make(chan error, 1)
	go func() { p.writeMu.Lock(); defer p.writeMu.Unlock(); _, err := p.input.Write(data); done <- err }()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		p.cancel()
		return errors.New("channel protocol write timed out")
	}
}

func (p *processConnection) Status() Status { p.mu.Lock(); defer p.mu.Unlock(); return p.status }
func (p *processConnection) Send(ctx context.Context, d Delivery) Result {
	p.mu.Lock()
	if p.status.State != "connected" {
		p.mu.Unlock()
		return Result{"retry", 0, "渠道尚未连接，未发送业务消息。"}
	}
	ch := make(chan Result, 1)
	p.pending[d.ID] = ch
	p.mu.Unlock()
	if err := p.write(map[string]any{"v": 1, "type": "send", "id": d.ID, "delivery": d}); err != nil {
		p.mu.Lock()
		delete(p.pending, d.ID)
		p.mu.Unlock()
		return Result{"unknown", 0, "无法确认请求是否已交给适配器，请人工核对。"}
	}
	select {
	case result := <-ch:
		return result
	case <-ctx.Done():
		p.mu.Lock()
		delete(p.pending, d.ID)
		p.mu.Unlock()
		return Result{"unknown", 0, "投递确认超时，请核对渠道，不自动重发。"}
	}
}
func (p *processConnection) Close() {
	p.cancel()
	_ = p.input.Close()
	select {
	case <-p.done:
	case <-time.After(2 * time.Second):
	}
}

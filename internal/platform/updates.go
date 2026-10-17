package platform

import (
	"context"
	"errors"
	"net/http"
	"os"
	"siagent/internal/selfupdate"
	"strings"
	"time"
)

func (s *Server) ConfigureUpdates(manager *selfupdate.Manager, stop func()) {
	s.updater = manager
	s.updateStop = stop
	if os.Getenv("SIAGENT_UPDATE_BOOT") != "" && manager.Status().Phase == "restarting" {
		s.updating.Store(true)
		go func() {
			timer := time.NewTimer(2 * time.Minute)
			defer timer.Stop()
			ticker := time.NewTicker(200 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-timer.C:
					return
				case <-ticker.C:
					phase := manager.Status().Phase
					if phase == "completed" || phase == "rolled_back" {
						s.updating.Store(false)
						return
					}
				}
			}
		}()
	}
}
func (s *Server) updateStatus(w http.ResponseWriter, r *http.Request) {
	if s.updater == nil {
		respond(w, 200, selfupdate.Status{Mode: "source", Current: "dev", Phase: "idle", Message: "源码部署请重新构建并重启。"})
		return
	}
	respond(w, 200, s.updater.Status())
}
func (s *Server) checkUpdate(w http.ResponseWriter, r *http.Request) {
	if s.updater == nil {
		fail(w, 409, "此实例未配置更新检查。")
		return
	}
	// Downloads and version checks are asynchronous so the normal HTTP request
	// deadline never truncates a mirror fallback sequence.
	if err := s.updater.BeginCheck(); err != nil {
		fail(w, 409, err.Error())
		return
	}
	respond(w, 202, s.updater.Status())
}

// startUpdate is a single confirmation for download, drain, backup and restart.
func (s *Server) startUpdate(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Confirm bool `json:"confirm"`
	}
	if !decode(w, r, &input) {
		return
	}
	if !input.Confirm {
		fail(w, 400, "请确认下载更新并自动备份、重启。")
		return
	}
	if s.updater == nil || s.updateStop == nil {
		fail(w, 409, "当前部署不支持应用内更新。")
		return
	}
	if err := s.updater.BeginUpdate(); err != nil {
		fail(w, 409, err.Error())
		return
	}
	respond(w, 202, s.updater.Status())
}

// RunUpdates owns automatic apply; it must not depend on an open browser.
func (s *Server) RunUpdates(ctx context.Context) error {
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			if err := s.UpdateTick(); err != nil {
				return err
			}
		}
	}
}

var errUpdateBusy = errors.New("仍有运行、排队、渠道投递、后台处理或会话清理，请等待完成。")

func (s *Server) UpdateTick() error {
	if s.updater == nil || s.updateStop == nil {
		return nil
	}
	status := s.updater.Status()
	if !status.Automatic || (status.Phase != "ready" && status.Phase != "waiting") {
		if status.Phase != "restarting" {
			s.updateDraining.Store(false)
		}
		return nil
	}
	s.updateDraining.Store(true)
	if err := s.startReadyUpdate(); errors.Is(err, errUpdateBusy) {
		s.updater.WaitForIdle()
	} else if err != nil {
		s.updater.Fail(err)
		s.updateDraining.Store(false)
	}
	return nil
}

func (s *Server) prepareUpdate(w http.ResponseWriter, r *http.Request) {
	if s.updater == nil {
		fail(w, 409, "当前部署不支持应用内更新。")
		return
	}
	if err := s.updater.Prepare(); err != nil {
		fail(w, 409, err.Error())
		return
	}
	respond(w, 202, s.updater.Status())
}
func (s *Server) cancelUpdate(w http.ResponseWriter, r *http.Request) {
	if s.updater == nil {
		fail(w, 409, "没有准备好的更新。")
		return
	}
	if err := s.updater.Cancel(); err != nil {
		fail(w, 409, err.Error())
		return
	}
	s.updateDraining.Store(false)
	respond(w, 200, s.updater.Status())
}
func (s *Server) applyUpdate(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Confirm bool `json:"confirm"`
	}
	if !decode(w, r, &input) {
		return
	}
	if !input.Confirm {
		fail(w, 400, "请确认停机更新及完整数据备份。")
		return
	}
	if s.updater == nil || s.updateStop == nil {
		fail(w, 409, "当前部署不支持应用内更新。")
		return
	}
	if err := s.startReadyUpdate(); err != nil {
		fail(w, 409, err.Error())
		return
	}
	respond(w, 202, s.updater.Status())
}

func (s *Server) startReadyUpdate() error {
	s.updateGate.Lock()
	defer s.updateGate.Unlock()
	s.channelWork.Lock()
	defer s.channelWork.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping {
		return errors.New("服务正在停止。")
	}
	var active int
	err := s.store.db.QueryRow(`SELECT (SELECT count(*) FROM runs WHERE released=0 OR status='queued')+(SELECT count(*) FROM channel_inbox WHERE finalized=0)+(SELECT count(*) FROM channel_outbox WHERE state IN ('pending','retry','sending'))+(SELECT count(*) FROM conversation_cleanup)+(SELECT count(*) FROM knowledge_versions WHERE state IN ('queued','processing'))`).Scan(&active)
	if err != nil {
		return err
	}
	if active > 0 || len(s.knowledgePending) > 0 || len(s.pendingTests) > 0 {
		return errUpdateBusy
	}
	if err := s.updater.StartWorker(); err != nil {
		return err
	}
	s.updating.Store(true)
	s.stopping = true
	go func() { time.Sleep(500 * time.Millisecond); s.updateStop() }()
	return nil
}

// updateBarrier drains management writes before stopping, and rejects late ones.
func (s *Server) updateBarrier(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" || r.Method == "HEAD" || r.URL.Path == "/api/updates/apply" {
			next.ServeHTTP(w, r)
			return
		}
		s.updateGate.RLock()
		defer s.updateGate.RUnlock()
		canDrain := r.URL.Path == "/api/updates/cancel" || (strings.HasPrefix(r.URL.Path, "/api/runs/") && strings.HasSuffix(r.URL.Path, "/cancel"))
		if s.updating.Load() || (s.updateDraining.Load() && !canDrain) {
			fail(w, 503, "正在更新并重启，请等待重新连接。")
			return
		}
		next.ServeHTTP(w, r)
	})
}

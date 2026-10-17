package platform

import (
	"net/http"
	"os"
	"siagent/internal/selfupdate"
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
	s.updateGate.Lock()
	defer s.updateGate.Unlock()
	s.channelWork.Lock()
	defer s.channelWork.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping {
		fail(w, 409, "服务正在停止。")
		return
	}
	var active int
	err := s.store.db.QueryRow(`SELECT (SELECT count(*) FROM runs WHERE released=0 OR status='queued')+(SELECT count(*) FROM channel_inbox WHERE finalized=0)+(SELECT count(*) FROM channel_outbox WHERE state IN ('pending','retry','sending'))+(SELECT count(*) FROM conversation_cleanup)+(SELECT count(*) FROM knowledge_versions WHERE state IN ('queued','processing'))`).Scan(&active)
	if err != nil {
		internal(w)
		return
	}
	if active > 0 || len(s.knowledgePending) > 0 {
		fail(w, 409, "仍有运行、排队、渠道投递或会话清理。请等待完成，必要时停用渠道后重试。")
		return
	}
	// A schema migration must never overlap the running platform. The worker
	// independently waits for process exit and the exclusive data lock.
	if err := s.updater.StartWorker(); err != nil {
		fail(w, 409, err.Error())
		return
	}
	s.updating.Store(true)
	s.stopping = true
	respond(w, 202, s.updater.Status())
	go func() { time.Sleep(500 * time.Millisecond); s.updateStop() }()
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
		if s.updating.Load() {
			fail(w, 503, "正在更新并重启，请等待重新连接。")
			return
		}
		next.ServeHTTP(w, r)
	})
}

package platform

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	agentruntime "siagent/internal/runtime"
	"strings"
	"syscall"
	"time"
)

// A durable deletion request blocks new admission until Runtime has erased its
// own storage. Losing an acknowledgement is safe: deletion is idempotent.
func (s *Server) deleteConversation(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Confirm bool `json:"confirm"`
	}
	if !decode(w, r, &input) {
		return
	}
	if !input.Confirm {
		fail(w, 400, "请确认永久删除会话历史、工具记录及模型上下文；已发生的外部操作不会撤销。")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	conversation := r.PathValue("id")
	var exists int
	if err := s.store.db.QueryRow("SELECT count(*) FROM conversations WHERE id=?", conversation).Scan(&exists); err != nil {
		internal(w)
		return
	}
	if exists == 0 {
		fail(w, 404, "对话不存在。")
		return
	}
	var busy int
	if err := s.store.db.QueryRow(`SELECT (SELECT count(*) FROM runs WHERE conversation_id=? AND (released=0 OR status IN ('queued','running','cancelling'))) + (SELECT count(*) FROM channel_inbox WHERE conversation_id=? AND finalized=0) + (SELECT count(*) FROM channel_outbox o JOIN channel_inbox i ON i.id=o.inbox_id WHERE i.conversation_id=? AND o.state IN ('pending','retry','sending'))`, conversation, conversation, conversation).Scan(&busy); err != nil {
		internal(w)
		return
	}
	if busy > 0 {
		fail(w, 409, "会话仍有排队/执行/取消中的任务或待发送回复，请先取消或等待结束及资源释放，再删除。")
		return
	}
	tx, err := s.store.db.Begin()
	if err != nil {
		internal(w)
		return
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT OR IGNORE INTO conversation_cleanup(conversation_id,created) VALUES(?,?)", conversation, time.Now().UnixMilli()); err == nil {
		_, err = tx.Exec("DELETE FROM channel_sessions WHERE conversation_id=?", conversation)
	}
	if err != nil || tx.Commit() != nil {
		internal(w)
		return
	}
	respond(w, 202, map[string]string{"state": "cleanup_pending", "message": "会话已移出列表，后台将清理平台记录与模型上下文；服务不可用时保留请求并在恢复后继续。"})
}

func (s *Server) MaintenanceTick(ctx context.Context) error {
	s.maintenanceWork.Lock()
	defer s.maintenanceWork.Unlock()
	s.cleanupOrphanAttachments(ctx)
	s.mu.Lock()
	if s.stopping || s.restarting || !s.ready() || len(s.pendingTests) >= 4 {
		s.mu.Unlock()
		return nil
	}
	var conversation string
	err := s.store.db.QueryRowContext(ctx, "SELECT conversation_id FROM conversation_cleanup ORDER BY attempted,created LIMIT 1").Scan(&conversation)
	if err == sql.ErrNoRows {
		s.mu.Unlock()
		return nil
	}
	if err != nil {
		s.mu.Unlock()
		return err
	}
	if _, err := s.store.db.ExecContext(ctx, "UPDATE conversation_cleanup SET attempted=? WHERE conversation_id=?", time.Now().UnixMilli(), conversation); err != nil {
		s.mu.Unlock()
		return err
	}
	requestID := "cleanup-" + id()
	result := make(chan agentruntime.Event, 1)
	s.pendingTests[requestID] = result
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.pendingTests, requestID); s.mu.Unlock() }()
	if err := s.runtime.Send(map[string]any{"v": 1, "type": "conversation_delete", "request_id": requestID, "conversation_id": conversation}); err != nil {
		return nil
	}
	wait, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	select {
	case <-wait.Done():
		return nil
	case event := <-result:
		if event.Type != "conversation_deleted" || event.ConversationID != conversation {
			return nil
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	attachmentRows, attachmentErr := s.store.db.QueryContext(ctx, "SELECT path FROM attachments WHERE conversation_id=?", conversation)
	attachmentPaths := []string{}
	if attachmentErr == nil {
		for attachmentRows.Next() {
			var path string
			if attachmentRows.Scan(&path) == nil {
				attachmentPaths = append(attachmentPaths, path)
			}
		}
		attachmentRows.Close()
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, table := range []string{"run_sources", "run_events", "tool_calls", "run_usage"} {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE run_id IN (SELECT id FROM runs WHERE conversation_id=?)", conversation); err != nil {
			return err
		}
	}
	for _, query := range []string{
		"DELETE FROM messages WHERE conversation_id=?",
		"DELETE FROM runs WHERE conversation_id=?",
		"DELETE FROM channel_outbox WHERE inbox_id IN (SELECT id FROM channel_inbox WHERE conversation_id=?)",
		"UPDATE channel_inbox SET content='',member='',reply_context='{}',agent_id='',run_id='',conversation_id='',error='管理员已清理会话历史，去重标识保留。',state='rejected',finalized=1,hinted=1 WHERE conversation_id=?",
		"DELETE FROM channel_sessions WHERE conversation_id=?",
		"DELETE FROM attachments WHERE conversation_id=?",
		"DELETE FROM conversations WHERE id=?",
		"DELETE FROM conversation_cleanup WHERE conversation_id=?",
	} {
		if _, err := tx.ExecContext(ctx, query, conversation); err != nil {
			return fmt.Errorf("cleanup conversation %s: %w", query, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for _, relative := range attachmentPaths {
		_ = os.Remove(filepath.Join(s.store.directory, filepath.Clean(relative)))
	}
	return nil
}

func (s *Server) cleanupOrphanAttachments(ctx context.Context) {
	cutoff := time.Now().Add(-time.Duration(s.attachmentLimits.OrphanTTLHours) * time.Hour).UnixMilli()
	rows, err := s.store.db.QueryContext(ctx, "SELECT id,path FROM attachments WHERE linked=0 AND created<?", cutoff)
	if err != nil {
		return
	}
	paths := []string{}
	ids := []string{}
	for rows.Next() {
		var id, path string
		if rows.Scan(&id, &path) == nil {
			ids = append(ids, id)
			paths = append(paths, path)
		}
	}
	rows.Close()
	if len(ids) == 0 {
		return
	}
	for _, id := range ids {
		if _, err := s.store.db.ExecContext(ctx, "DELETE FROM attachments WHERE id=? AND linked=0", id); err != nil {
			return
		}
	}
	for _, relative := range paths {
		_ = os.Remove(filepath.Join(s.store.directory, filepath.Clean(relative)))
	}
}

func (s *Server) RunMaintenance(ctx context.Context) error {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := s.MaintenanceTick(ctx); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
		}
	}
}

type storageReport struct {
	Total     uint64           `json:"disk_total_bytes"`
	Available uint64           `json:"disk_available_bytes"`
	Used      int64            `json:"data_bytes"`
	Files     int              `json:"files"`
	Complete  bool             `json:"complete"`
	Groups    map[string]int64 `json:"groups"`
	Pending   int              `json:"cleanup_pending"`
	Warning   string           `json:"warning"`
}

func (s *Server) storage(w http.ResponseWriter, r *http.Request) {
	report := storageReport{Complete: true, Groups: map[string]int64{}}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(s.store.directory, &stat); err != nil {
		internal(w)
		return
	}
	report.Total = stat.Blocks * uint64(stat.Bsize)
	report.Available = stat.Bavail * uint64(stat.Bsize)
	if err := s.store.db.QueryRowContext(r.Context(), "SELECT count(*) FROM conversation_cleanup").Scan(&report.Pending); err != nil {
		internal(w)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	entries := 0
	root, resolveErr := filepath.EvalSymlinks(s.store.directory)
	if resolveErr != nil {
		internal(w)
		return
	}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		entries++
		if ctx.Err() != nil || entries > 200000 {
			report.Complete = false
			return fs.SkipAll
		}
		if err != nil {
			report.Complete = false
			return nil
		}
		if entry.IsDir() || entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			report.Complete = false
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		group := strings.SplitN(filepath.ToSlash(relative), "/", 2)[0]
		switch group {
		case "runtime", "knowledge", "skills", "workspaces", "files", "attachments":
		default:
			group = "platform"
		}
		report.Used += info.Size()
		report.Files++
		report.Groups[group] += info.Size()
		return nil
	})
	if err != nil {
		report.Complete = false
	}
	if report.Available < 1<<30 || float64(report.Available) < float64(report.Total)*0.1 {
		report.Warning = "所在磁盘剩余空间不足 1 GiB 或低于 10%，请备份并检查磁盘；不会自动删除业务数据。"
	}
	respond(w, 200, report)
}

package platform

import (
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Purging an archived Agent is the only irreversible delete in the product, so
// it is a separate endpoint from archiving and refuses to run until the Agent is
// archived. Conversation history is queued through the same conversation_cleanup
// path an explicit history deletion uses, so the Runtime is told to drop its
// context before the platform rows go away.
func (s *Server) purgeAgent(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Confirm string `json:"confirm"`
	}
	if !decode(w, r, &input) {
		return
	}
	agentID := agentPath(r)
	s.mu.Lock()
	defer s.mu.Unlock()
	a, err := s.agentByID(agentID)
	if err == sql.ErrNoRows {
		fail(w, 404, "助手不存在。")
		return
	}
	if err != nil {
		internal(w)
		return
	}
	if !a.Archived {
		fail(w, 409, "只能彻底删除已归档的助手；请先归档。")
		return
	}
	// Typing the name back is the confirmation. It is cheap for an operator and
	// makes an accidental click on a destructive dialog impossible.
	if input.Confirm != a.Name {
		fail(w, 400, fmt.Sprintf("请输入助手名称“%s”以确认彻底删除。", a.Name))
		return
	}
	var busy int
	if err := s.store.db.QueryRow(`SELECT (SELECT count(*) FROM runs r JOIN conversations c ON c.id=r.conversation_id WHERE c.agent_id=? AND (r.released=0 OR r.status IN ('queued','running','cancelling'))) + (SELECT count(*) FROM channel_inbox WHERE agent_id=? AND finalized=0)`, agentID, agentID).Scan(&busy); err != nil {
		internal(w)
		return
	}
	if busy > 0 {
		fail(w, 409, "此助手仍有排队或执行中的任务，请等待结束或先取消，再彻底删除。")
		return
	}
	tx, err := s.store.db.Begin()
	if err != nil {
		internal(w)
		return
	}
	defer tx.Rollback()
	// Queue every conversation for the normal cleanup path so the Runtime drops
	// its own context instead of leaving an orphaned session behind.
	if _, err := tx.Exec(`INSERT OR IGNORE INTO conversation_cleanup(conversation_id,created) SELECT id,? FROM conversations WHERE agent_id=?`, time.Now().UnixMilli(), agentID); err != nil {
		internal(w)
		return
	}
	for _, query := range []string{
		"DELETE FROM agent_capabilities WHERE agent_id=?",
		"DELETE FROM knowledge_bindings WHERE agent_id=?",
		"DELETE FROM channel_sessions WHERE agent_id=?",
		"DELETE FROM agents WHERE id=?",
	} {
		if _, err := tx.Exec(query, agentID); err != nil {
			internal(w)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		internal(w)
		return
	}
	// The workspace is scratch space for local tools, so removing it completes
	// the delete rather than leaving an orphan directory behind.
	_ = os.RemoveAll(filepath.Join(s.store.directory, "workspaces", agentID))
	s.audit("agent.purge", agentID, "name="+a.Name)
	respond(w, 200, map[string]any{
		"purged":  true,
		"message": "助手已彻底删除，会话记录将在后台清理。",
	})
}

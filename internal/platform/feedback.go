package platform

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// FeedbackSchema stores one rating per run. It never copies the answer, the
// sources or any credential, so exporting feedback cannot leak content.
const FeedbackSchema = `
 CREATE TABLE answer_feedback(run_id TEXT PRIMARY KEY REFERENCES runs(id),rating TEXT NOT NULL,reason TEXT NOT NULL DEFAULT '',comment TEXT NOT NULL DEFAULT '',created INTEGER NOT NULL,updated INTEGER NOT NULL);
`

var feedbackReasons = map[string]bool{
	"":             true,
	"wrong_answer": true,
	"no_evidence":  true,
	"irrelevant":   true,
	"outdated":     true,
	"off_topic":    true,
	"too_long":     true,
	"too_short":    true,
	"other":        true,
}

type Feedback struct {
	Rating  string `json:"rating"`
	Reason  string `json:"reason"`
	Comment string `json:"comment"`
}

// saveFeedback upserts one rating for a finished run.
func (s *Server) saveFeedback(w http.ResponseWriter, r *http.Request) {
	var input Feedback
	if !decode(w, r, &input) {
		return
	}
	if input.Rating != "up" && input.Rating != "down" {
		fail(w, 400, "请选择赞或踩。")
		return
	}
	if !feedbackReasons[input.Reason] {
		fail(w, 400, "反馈原因无效。")
		return
	}
	input.Comment = strings.TrimSpace(input.Comment)
	if len(input.Comment) > 1000 {
		fail(w, 400, "补充说明最多 1000 字节。")
		return
	}
	runID := r.PathValue("id")
	var status string
	err := s.store.db.QueryRow("SELECT status FROM runs WHERE id=?", runID).Scan(&status)
	if err == sql.ErrNoRows {
		fail(w, 404, "任务不存在。")
		return
	}
	if err != nil {
		internal(w)
		return
	}
	if status == "queued" || status == "running" || status == "cancelling" {
		fail(w, 409, "任务尚未结束，暂时不能评价。")
		return
	}
	now := time.Now().UnixMilli()
	if _, err := s.store.db.Exec(`INSERT INTO answer_feedback(run_id,rating,reason,comment,created,updated) VALUES(?,?,?,?,?,?)
  ON CONFLICT(run_id) DO UPDATE SET rating=excluded.rating,reason=excluded.reason,comment=excluded.comment,updated=excluded.updated`, runID, input.Rating, input.Reason, input.Comment, now, now); err != nil {
		internal(w)
		return
	}
	respond(w, 200, input)
}

func (s *Server) deleteFeedback(w http.ResponseWriter, r *http.Request) {
	if _, err := s.store.db.Exec("DELETE FROM answer_feedback WHERE run_id=?", r.PathValue("id")); err != nil {
		internal(w)
		return
	}
	respond(w, 200, map[string]bool{"deleted": true})
}

// feedbackStats is a small aggregate for the maintenance page.
func (s *Server) feedbackStats(w http.ResponseWriter, r *http.Request) {
	var up, down int
	if err := s.store.db.QueryRow("SELECT count(*) FROM answer_feedback WHERE rating='up'").Scan(&up); err != nil {
		internal(w)
		return
	}
	if err := s.store.db.QueryRow("SELECT count(*) FROM answer_feedback WHERE rating='down'").Scan(&down); err != nil {
		internal(w)
		return
	}
	reasons := map[string]int{}
	rows, err := s.store.db.Query("SELECT reason,count(*) FROM answer_feedback WHERE rating='down' AND reason<>'' GROUP BY reason ORDER BY count(*) DESC")
	if err != nil {
		internal(w)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var reason string
		var count int
		if err := rows.Scan(&reason, &count); err != nil {
			internal(w)
			return
		}
		reasons[reason] = count
	}
	respond(w, 200, map[string]any{"up": up, "down": down, "reasons": reasons})
}

func (s *Server) listGaps(w http.ResponseWriter, r *http.Request) {
	values, err := s.knowledge.Gaps(r.Context(), r.URL.Query().Get("knowledge_id"), r.URL.Query().Get("status"), 100)
	if err != nil {
		knowledgeError(w, err)
		return
	}
	respond(w, 200, values)
}

func (s *Server) setGapStatus(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Status string `json:"status"`
	}
	if !decode(w, r, &input) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		fail(w, 400, "缺口标识无效。")
		return
	}
	value, err := s.knowledge.SetGapStatus(r.Context(), id, input.Status)
	if err != nil {
		knowledgeError(w, err)
		return
	}
	respond(w, 200, value)
}

func (s *Server) deleteGap(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		fail(w, 400, "缺口标识无效。")
		return
	}
	if err := s.knowledge.DeleteGap(r.Context(), id); err != nil {
		knowledgeError(w, err)
		return
	}
	respond(w, 200, map[string]bool{"deleted": true})
}

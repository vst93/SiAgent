package platform

import (
	"net/http"
	"strconv"
)

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

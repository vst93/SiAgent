package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"siagent/internal/knowledge"
	agentruntime "siagent/internal/runtime"
)

func knowledgeError(w http.ResponseWriter, err error) {
	var fault *knowledge.Fault
	if errors.As(err, &fault) {
		status := map[string]int{"invalid": 400, "forbidden": 403, "not_found": 404, "conflict": 409, "unavailable": 503}[fault.Kind]
		if status == 0 {
			status = 500
		}
		fail(w, status, fault.Message)
		return
	}
	internal(w)
}
func (s *Server) listKnowledge(w http.ResponseWriter, r *http.Request) {
	values, err := s.knowledge.List(r.Context())
	if err != nil {
		knowledgeError(w, err)
		return
	}
	respond(w, 200, values)
}
func (s *Server) saveKnowledge(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if !decode(w, r, &input) {
		return
	}
	value, err := s.knowledge.SaveBase(r.Context(), r.PathValue("id"), input.Name, input.Description)
	if err != nil {
		knowledgeError(w, err)
		return
	}
	status := 200
	if r.Method == "POST" {
		status = 201
	}
	respond(w, status, value)
}
func (s *Server) deleteKnowledge(w http.ResponseWriter, r *http.Request) {
	if err := s.knowledge.DeleteBase(r.Context(), r.PathValue("id")); err != nil {
		knowledgeError(w, err)
		return
	}
	respond(w, 200, map[string]bool{"deleted": true})
}
func (s *Server) knowledgeStatus(w http.ResponseWriter, r *http.Request) {
	respond(w, 200, s.knowledge.DescribeDependencies())
}
func (s *Server) listDocuments(w http.ResponseWriter, r *http.Request) {
	values, err := s.knowledge.Documents(r.Context(), r.PathValue("id"))
	if err != nil {
		knowledgeError(w, err)
		return
	}
	respond(w, 200, values)
}
func (s *Server) uploadDocument(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, knowledge.MaxUpload+(1<<20))
	reader, err := r.MultipartReader()
	if err != nil {
		fail(w, 400, "请选择要上传的文档。")
		return
	}
	part, err := reader.NextPart()
	if err != nil || part.FormName() != "file" || part.FileName() == "" {
		fail(w, 400, "上传请求必须包含一个 file 文档。")
		return
	}
	defer part.Close()
	if !s.uploadExtensionSupported(part.FileName()) {
		fail(w, 400, "该文件格式需要先在“扩展”中安装并启用解析扩展。")
		return
	}
	if r.PathValue("document") != "" && r.URL.Query().Get("force") != "1" && s.evalGateBlocked(r.Context(), r.PathValue("id")) {
		fail(w, 409, "最近一次检索评测出现退化，已阻止替换。请先修复或使用 force=1 明确覆盖。")
		return
	}
	value, err := s.knowledge.Upload(r.Context(), r.PathValue("id"), r.PathValue("document"), part.FileName(), io.MultiReader(part, uploadEnd{reader}))
	if err != nil {
		var maxBytes *http.MaxBytesError
		if errors.As(err, &maxBytes) {
			fail(w, 413, "文件超过 20 MiB 上限。")
			return
		}
		knowledgeError(w, err)
		return
	}
	// Only the first file is accepted; the endpoint never silently merges documents.
	respond(w, 202, value)
}

// uploadExtensionSupported reports whether a built-in parser handles the file,
// or an enabled parser extension declares its extension.
func (s *Server) uploadExtensionSupported(filename string) bool {
	extension := strings.ToLower(filepath.Ext(filename))
	switch extension {
	case ".txt", ".md", ".markdown", ".pdf", ".docx":
		return true
	}
	if extension == "" {
		return false
	}
	_, ok := s.plugins.ParserFor(extension)
	return ok
}

func (s *Server) downloadDocument(w http.ResponseWriter, r *http.Request) {
	file, document, err := s.knowledge.Download(r.Context(), r.PathValue("document"))
	if err != nil {
		knowledgeError(w, err)
		return
	}
	defer file.Close()
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": document.Filename}))
	http.ServeContent(w, r, document.Filename, time.UnixMilli(document.Updated), file)
}

func (s *Server) deleteDocument(w http.ResponseWriter, r *http.Request) {
	if err := s.knowledge.DeleteDocument(r.Context(), r.PathValue("document")); err != nil {
		knowledgeError(w, err)
		return
	}
	respond(w, 200, map[string]bool{"deleted": true})
}
func (s *Server) retryDocument(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("force") != "1" {
		if value, err := s.knowledge.Document(r.Context(), r.PathValue("document")); err == nil && s.evalGateBlocked(r.Context(), value.KnowledgeID) {
			fail(w, 409, "最近一次检索评测出现退化，已阻止重新索引。请先修复或使用 force=1 明确覆盖。")
			return
		}
	}
	value, err := s.knowledge.Retry(r.Context(), r.PathValue("document"))
	if err != nil {
		knowledgeError(w, err)
		return
	}
	respond(w, 202, value)
}
func (s *Server) searchKnowledge(w http.ResponseWriter, r *http.Request) {
	var request knowledge.SearchRequest
	if !decode(w, r, &request) {
		return
	}
	scope, err := s.knowledge.Scope(r.Context(), false)
	if err != nil {
		knowledgeError(w, err)
		return
	}
	request.Compact = true
	// Optional model query expansion is opt-in per knowledge base; failure is
	// ignored so a slow model never blocks keyword retrieval.
	if len(request.KnowledgeIDs) == 1 && !request.Baseline {
		if cfg, err := s.knowledge.BaseConfig(r.Context(), request.KnowledgeIDs[0]); err == nil && cfg.LLMExpand {
			if reranker, ok := s.knowledge.Reranker().(*modelReranker); ok && reranker != nil {
				ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
				if extra, err := reranker.ExpandQueries(ctx, request.Query); err == nil {
					request.ExtraQueries = extra
				}
				cancel()
			}
		}
	}
	result, err := s.knowledge.Search(r.Context(), scope, request)
	if err != nil {
		knowledgeError(w, err)
		return
	}
	target := ""
	if len(request.KnowledgeIDs) == 1 {
		target = request.KnowledgeIDs[0]
	}
	if request.Trace {
		s.knowledge.RecordTrace(r.Context(), target, result.Trace)
	}
	if len(result.Hits) == 0 {
		s.knowledge.RecordGap(r.Context(), target, request.Query)
	}
	respond(w, 200, result)
}

func (s *Server) getBaseConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.knowledge.BaseConfig(r.Context(), r.PathValue("id"))
	if err != nil {
		knowledgeError(w, err)
		return
	}
	respond(w, 200, cfg)
}

func (s *Server) saveBaseConfig(w http.ResponseWriter, r *http.Request) {
	var input knowledge.BaseConfig
	if !decode(w, r, &input) {
		return
	}
	cfg, err := s.knowledge.SaveBaseConfig(r.Context(), r.PathValue("id"), input)
	if err != nil {
		knowledgeError(w, err)
		return
	}
	s.audit("knowledge.config", r.PathValue("id"), "")
	respond(w, 200, cfg)
}

func (s *Server) listVocabulary(w http.ResponseWriter, r *http.Request) {
	values, err := s.knowledge.Vocabulary(r.Context(), r.PathValue("id"))
	if err != nil {
		knowledgeError(w, err)
		return
	}
	respond(w, 200, values)
}

func (s *Server) saveVocabulary(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Term      string `json:"term"`
		Expansion string `json:"expansion"`
	}
	if !decode(w, r, &input) {
		return
	}
	if err := s.knowledge.SaveVocabulary(r.Context(), r.PathValue("id"), input.Term, input.Expansion); err != nil {
		knowledgeError(w, err)
		return
	}
	values, err := s.knowledge.Vocabulary(r.Context(), r.PathValue("id"))
	if err != nil {
		knowledgeError(w, err)
		return
	}
	respond(w, 200, values)
}

func (s *Server) deleteVocabulary(w http.ResponseWriter, r *http.Request) {
	if err := s.knowledge.DeleteVocabulary(r.Context(), r.PathValue("id"), r.URL.Query().Get("term")); err != nil {
		knowledgeError(w, err)
		return
	}
	respond(w, 200, map[string]bool{"deleted": true})
}

func (s *Server) detectConflicts(w http.ResponseWriter, r *http.Request) {
	found, err := s.knowledge.DetectConflicts(r.Context(), r.PathValue("id"))
	if err != nil {
		knowledgeError(w, err)
		return
	}
	s.audit("knowledge.conflicts.detect", r.PathValue("id"), fmt.Sprintf("%d found", found))
	respond(w, 200, map[string]any{"found": found})
}

func (s *Server) listConflicts(w http.ResponseWriter, r *http.Request) {
	values, err := s.knowledge.Conflicts(r.Context(), r.PathValue("id"), r.URL.Query().Get("status"))
	if err != nil {
		knowledgeError(w, err)
		return
	}
	respond(w, 200, values)
}

func (s *Server) setConflictStatus(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Status string `json:"status"`
	}
	if !decode(w, r, &input) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		fail(w, 400, "冲突标识无效。")
		return
	}
	if err := s.knowledge.SetConflictStatus(r.Context(), id, input.Status); err != nil {
		knowledgeError(w, err)
		return
	}
	respond(w, 200, map[string]bool{"updated": true})
}

func (s *Server) listOutdatedSources(w http.ResponseWriter, r *http.Request) {
	values, err := s.knowledge.OutdatedSources(r.Context(), r.URL.Query().Get("knowledge_id"), 50)
	if err != nil {
		knowledgeError(w, err)
		return
	}
	respond(w, 200, values)
}

func (s *Server) listVocabCandidates(w http.ResponseWriter, r *http.Request) {
	values, err := s.knowledge.VocabCandidates(r.Context(), r.PathValue("id"))
	if err != nil {
		knowledgeError(w, err)
		return
	}
	respond(w, 200, values)
}

// evalGateBlocked reports whether the base enabled the regression gate and the
// latest retrieval evaluation regressed.
func (s *Server) evalGateBlocked(ctx context.Context, baseID string) bool {
	cfg, err := s.knowledge.BaseConfig(ctx, baseID)
	if err != nil || !cfg.Gate {
		return false
	}
	regressed, err := s.knowledge.LastEvalRegressed(ctx, baseID)
	return err == nil && regressed
}

func (s *Server) listEvalQuestions(w http.ResponseWriter, r *http.Request) {
	values, err := s.knowledge.EvalQuestions(r.Context(), r.PathValue("id"))
	if err != nil {
		knowledgeError(w, err)
		return
	}
	respond(w, 200, values)
}

func (s *Server) saveEvalQuestion(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Question            string   `json:"question"`
		ExpectedDocumentIDs []string `json:"expected_document_ids"`
		Tags                []string `json:"tags"`
		Answerable          *bool    `json:"answerable"`
		ExpectedAnswer      string   `json:"expected_answer"`
	}
	if !decode(w, r, &input) {
		return
	}
	answerable := true
	if input.Answerable != nil {
		answerable = *input.Answerable
	}
	value, err := s.knowledge.SaveEvalQuestionFull(r.Context(), r.PathValue("question"), r.PathValue("id"), input.Question, input.ExpectedDocumentIDs, input.Tags, answerable, input.ExpectedAnswer)
	if err != nil {
		knowledgeError(w, err)
		return
	}
	status := 200
	if r.PathValue("question") == "" {
		status = 201
	}
	respond(w, status, value)
}

func (s *Server) deleteEvalQuestion(w http.ResponseWriter, r *http.Request) {
	if err := s.knowledge.DeleteEvalQuestion(r.Context(), r.PathValue("question")); err != nil {
		knowledgeError(w, err)
		return
	}
	respond(w, 200, map[string]bool{"deleted": true})
}

// importEvalQuestions accepts CSV or JSON. Valid rows are stored even when
// other rows fail, and failures are reported per row.
func (s *Server) importEvalQuestions(w http.ResponseWriter, r *http.Request) {
	body := http.MaxBytesReader(w, r.Body, 1<<20)
	raw, err := io.ReadAll(body)
	if err != nil {
		fail(w, 400, "评测集文件过大或无法读取。")
		return
	}
	var rows []knowledge.EvalQuestionInput
	contentType := strings.ToLower(strings.Split(r.Header.Get("Content-Type"), ";")[0])
	switch contentType {
	case "text/csv", "application/csv", "text/plain":
		rows, err = knowledge.ParseEvalCSV(bytes.NewReader(raw))
	case "application/json", "":
		rows, err = knowledge.ParseEvalJSON(raw)
	default:
		fail(w, 415, "请使用 CSV 或 JSON 导入评测集。")
		return
	}
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	imported, failures := s.knowledge.ImportEvalQuestions(r.Context(), r.PathValue("id"), rows)
	s.audit("knowledge.eval.import", r.PathValue("id"), fmt.Sprintf("%d rows", imported))
	respond(w, 200, map[string]any{"imported": imported, "failures": failures})
}

func (s *Server) exportEvalQuestions(w http.ResponseWriter, r *http.Request) {
	values, err := s.knowledge.EvalQuestions(r.Context(), r.PathValue("id"))
	if err != nil {
		knowledgeError(w, err)
		return
	}
	if strings.EqualFold(r.URL.Query().Get("format"), "csv") {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=\"eval-questions.csv\"")
		_, _ = io.WriteString(w, knowledge.FormatEvalCSV(values))
		return
	}
	respond(w, 200, values)
}

func (s *Server) runEval(w http.ResponseWriter, r *http.Request) {
	run, err := s.knowledge.RunEval(r.Context(), r.PathValue("id"))
	if err != nil {
		knowledgeError(w, err)
		return
	}
	respond(w, 201, run)
}

func (s *Server) listEvalRuns(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	values, err := s.knowledge.EvalRuns(r.Context(), r.PathValue("id"), limit)
	if err != nil {
		knowledgeError(w, err)
		return
	}
	respond(w, 200, values)
}

func (s *Server) getEvalRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.knowledge.EvalRun(r.Context(), r.PathValue("run"))
	if err != nil {
		knowledgeError(w, err)
		return
	}
	respond(w, 200, run)
}
func (s *Server) readKnowledgeSource(w http.ResponseWriter, r *http.Request) {
	request := knowledge.ReadRequest{SourceID: r.PathValue("source")}
	var err error
	if value := r.URL.Query().Get("offset"); value != "" {
		request.Offset, err = strconv.Atoi(value)
		if err != nil {
			fail(w, 400, "原文起点无效。")
			return
		}
	}
	if value := r.URL.Query().Get("limit"); value != "" {
		request.Limit, err = strconv.Atoi(value)
		if err != nil {
			fail(w, 400, "原文读取数量无效。")
			return
		}
	}
	scope, err := s.knowledge.Scope(r.Context(), true)
	if err != nil {
		knowledgeError(w, err)
		return
	}
	value, err := s.knowledge.Read(r.Context(), scope, request)
	if err != nil {
		knowledgeError(w, err)
		return
	}
	respond(w, 200, value)
}
func (s *Server) getKnowledgeBindings(w http.ResponseWriter, r *http.Request) {
	a, ok := s.capabilityAgent(w, r, false)
	if !ok {
		return
	}
	ids, err := s.knowledge.Bindings(r.Context(), a.ID)
	if err != nil {
		knowledgeError(w, err)
		return
	}
	respond(w, 200, map[string]any{"knowledge_ids": ids})
}
func (s *Server) saveKnowledgeBindings(w http.ResponseWriter, r *http.Request) {
	var input struct {
		IDs     []string `json:"knowledge_ids"`
		Confirm bool     `json:"confirm"`
	}
	if !decode(w, r, &input) {
		return
	}
	if len(input.IDs) > 0 && !input.Confirm {
		fail(w, 400, "请确认所选文档内容可发送给当前模型服务，再保存关联。")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.capabilityAgent(w, r, true)
	if !ok {
		return
	}
	if err := s.knowledge.Bind(r.Context(), a.ID, input.IDs); err != nil {
		knowledgeError(w, err)
		return
	}
	ids, err := s.knowledge.Bindings(r.Context(), a.ID)
	if err != nil {
		knowledgeError(w, err)
		return
	}
	respond(w, 200, map[string]any{"knowledge_ids": ids})
}
func (s *Server) RunKnowledge(ctx context.Context) error { return s.knowledge.Run(ctx) }
func (s *Server) StopKnowledge() {
	s.mu.Lock()
	s.stopping = true
	for _, cancel := range s.knowledgePending {
		cancel()
	}
	s.mu.Unlock()
	s.knowledgeWork.Wait()
}
func (s *Server) knowledgeRequest(event agentruntime.Event) error {
	s.mu.Lock()
	if event.Type == "knowledge_cancel" {
		if cancel := s.knowledgePending[event.RequestID]; cancel != nil {
			cancel()
		}
		s.mu.Unlock()
		return nil
	}
	reply := func(result any, message string) {
		_ = s.runtime.Send(map[string]any{"v": 1, "type": "knowledge_result", "request_id": event.RequestID, "data": map[string]any{"ok": message == "", "result": result, "error": message}})
	}
	if s.stopping || len(s.knowledgePending) >= 32 || s.knowledgePending[event.RequestID] != nil {
		s.mu.Unlock()
		reply(nil, "知识服务正在停止或繁忙，请稍后重试。")
		return nil
	}
	var status, raw string
	err := s.store.db.QueryRow("SELECT status,config FROM runs WHERE id=?", event.RunID).Scan(&status, &raw)
	var snapshot runConfig
	if err != nil || status != "running" || json.Unmarshal([]byte(raw), &snapshot) != nil || len(snapshot.KnowledgeIDs) == 0 {
		s.mu.Unlock()
		reply(nil, "当前运行没有可用的知识库授权。")
		return nil
	}
	ctx, cancel := context.WithTimeout(s.schedulerCtx, 10*time.Second)
	s.knowledgePending[event.RequestID] = cancel
	s.knowledgeWork.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.knowledgeWork.Done()
		defer cancel()
		defer func() { s.mu.Lock(); delete(s.knowledgePending, event.RequestID); s.mu.Unlock() }()
		var request struct {
			Operation string          `json:"operation"`
			Input     json.RawMessage `json:"input"`
		}
		if json.Unmarshal(event.Data, &request) != nil {
			reply(nil, "知识请求格式无效。")
			return
		}
		scope := knowledge.Scope{KnowledgeIDs: snapshot.KnowledgeIDs}
		var result any
		var err error
		references := []knowledge.Hit{}
		switch request.Operation {
		case "search":
			var input knowledge.SearchRequest
			if json.Unmarshal(request.Input, &input) != nil || input.Limit < 0 || input.Limit > 5 {
				reply(nil, "检索请求无效，每次最多返回 5 个片段。")
				return
			}
			if input.Limit == 0 {
				input.Limit = 3
			}
			// Model reranking is a management diagnostic; Agent runs keep the
			// deterministic fused order and never pay an extra model call.
			input.Rerank = false
			var search knowledge.SearchResult
			search, err = s.knowledge.Search(ctx, scope, input)
			result = search
			references = search.Hits
			if err == nil && len(search.Hits) == 0 {
				gapTarget := ""
				if len(scope.KnowledgeIDs) == 1 {
					gapTarget = scope.KnowledgeIDs[0]
				}
				s.knowledge.RecordGap(ctx, gapTarget, input.Query)
			}
		case "read":
			var input knowledge.ReadRequest
			if json.Unmarshal(request.Input, &input) != nil || input.Limit < 0 || input.Limit > 4000 {
				reply(nil, "来源读取范围无效，每次最多读取 4000 个字符。")
				return
			}
			if input.Limit == 0 {
				input.Limit = 2000
			}
			var source knowledge.Source
			source, err = s.knowledge.Read(ctx, scope, input)
			result = source
			if err == nil && !source.Deleted {
				references = append(references, source.Hit)
			}
		default:
			reply(nil, "不支持的知识操作。")
			return
		}
		if err != nil {
			message := "知识服务暂不可用，请稍后重试；未生成检索结果。"
			var fault *knowledge.Fault
			if errors.As(err, &fault) {
				message = fault.Message
			}
			reply(nil, message)
			return
		}
		if len(references) > 0 {
			tx, err := s.store.db.BeginTx(ctx, nil)
			if err != nil {
				reply(nil, "无法保存来源记录，请稍后重试。")
				return
			}
			defer tx.Rollback()
			for _, hit := range references {
				reference := hit
				reference.Text = ""
				reference.Score = nil
				encoded, _ := json.Marshal(reference)
				if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO run_sources VALUES(?,?,?)", event.RunID, hit.SourceID, string(encoded)); err != nil {
					reply(nil, "无法保存来源记录，请稍后重试。")
					return
				}
			}
			if err := tx.Commit(); err != nil {
				reply(nil, "无法保存来源记录，请稍后重试。")
				return
			}
		}
		reply(result, "")
	}()
	return nil
}

type uploadEnd struct{ reader *multipart.Reader }

func (r uploadEnd) Read(_ []byte) (int, error) {
	_, err := r.reader.NextPart()
	if err == io.EOF {
		return 0, io.EOF
	}
	return 0, &knowledge.Fault{Kind: "invalid", Message: "每次只能上传一个文档，请分开提交。"}
}

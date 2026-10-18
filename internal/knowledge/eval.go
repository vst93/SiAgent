package knowledge

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"
)

// EvalSchema is additive. Evaluation data never rewrites documents or indexes.
const EvalSchema = `
 CREATE TABLE knowledge_eval_questions(id TEXT PRIMARY KEY,knowledge_id TEXT NOT NULL REFERENCES knowledge_bases(id),question TEXT NOT NULL,expected TEXT NOT NULL,tags TEXT NOT NULL DEFAULT '',created INTEGER NOT NULL);
 CREATE INDEX knowledge_eval_questions_base ON knowledge_eval_questions(knowledge_id,created);
 CREATE TABLE knowledge_eval_runs(id TEXT PRIMARY KEY,knowledge_id TEXT NOT NULL REFERENCES knowledge_bases(id),created INTEGER NOT NULL,metrics TEXT NOT NULL,cases TEXT NOT NULL);
 CREATE INDEX knowledge_eval_runs_base ON knowledge_eval_runs(knowledge_id,created DESC);
 CREATE TABLE retrieval_traces(id INTEGER PRIMARY KEY AUTOINCREMENT,run_id TEXT NOT NULL DEFAULT '',knowledge_id TEXT NOT NULL DEFAULT '',query TEXT NOT NULL,trace TEXT NOT NULL,created INTEGER NOT NULL);
 CREATE INDEX retrieval_traces_created ON retrieval_traces(created DESC);
`

// EvalQuestion is one golden question. Expected documents are the ground truth
// used for retrieval scoring; no model output is judged here.
type EvalQuestion struct {
	ID                  string   `json:"id"`
	KnowledgeID         string   `json:"knowledge_id"`
	Question            string   `json:"question"`
	ExpectedDocumentIDs []string `json:"expected_document_ids"`
	Tags                []string `json:"tags"`
	Answerable          bool     `json:"answerable"`
	ExpectedAnswer      string   `json:"expected_answer,omitempty"`
	Created             int64    `json:"created"`
}

// EvalMetrics is the reproducible retrieval baseline for one run.
type EvalMetrics struct {
	Questions         int     `json:"questions"`
	RecallAt1         float64 `json:"recall_at_1"`
	RecallAt3         float64 `json:"recall_at_3"`
	RecallAt5         float64 `json:"recall_at_5"`
	MRR               float64 `json:"mrr"`
	NoResultRate      float64 `json:"no_result_rate"`
	AbstentionAcc     float64 `json:"abstention_accuracy"`
	CitationPrecision float64 `json:"citation_precision,omitempty"`
	Faithfulness      float64 `json:"faithfulness,omitempty"`
	PreviousRecallAt5 float64 `json:"previous_recall_at_5,omitempty"`
	Regressed         bool    `json:"regressed,omitempty"`
	AvgLatencyMS      float64 `json:"avg_latency_ms"`
	BaselineRecallAt5 float64 `json:"baseline_recall_at_5"`
	BaselineMRR       float64 `json:"baseline_mrr"`
}

// EvalCaseResult keeps the ranked document ids so a regression can be read
// without re-running retrieval.
type EvalCaseResult struct {
	QuestionID string   `json:"question_id"`
	Question   string   `json:"question"`
	Expected   []string `json:"expected_document_ids"`
	Ranked     []string `json:"ranked_document_ids"`
	FirstRank  int      `json:"first_rank"`
	Pass       bool     `json:"pass"`
}

// EvalRun is one evaluation of a knowledge base.
type EvalRun struct {
	ID          string           `json:"id"`
	KnowledgeID string           `json:"knowledge_id"`
	Kind        string           `json:"kind"`
	Created     int64            `json:"created"`
	Metrics     EvalMetrics      `json:"metrics"`
	Cases       []EvalCaseResult `json:"cases"`
}

// EvalRunSummary omits per-case detail for list responses.
type EvalRunSummary struct {
	ID          string      `json:"id"`
	KnowledgeID string      `json:"knowledge_id"`
	Kind        string      `json:"kind"`
	Created     int64       `json:"created"`
	Metrics     EvalMetrics `json:"metrics"`
}

func (c *Core) EvalQuestions(ctx context.Context, baseID string) ([]EvalQuestion, error) {
	rows, err := c.db.QueryContext(ctx, "SELECT id,knowledge_id,question,expected,tags,answerable,expected_answer,created FROM knowledge_eval_questions WHERE knowledge_id=? ORDER BY created,rowid", baseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []EvalQuestion{}
	for rows.Next() {
		var v EvalQuestion
		var expected, tags string
		var answerable int
		if err := rows.Scan(&v.ID, &v.KnowledgeID, &v.Question, &expected, &tags, &answerable, &v.ExpectedAnswer, &v.Created); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(expected), &v.ExpectedDocumentIDs); err != nil {
			return nil, err
		}
		if v.ExpectedDocumentIDs == nil {
			v.ExpectedDocumentIDs = []string{}
		}
		if err := json.Unmarshal([]byte(tags), &v.Tags); err != nil {
			return nil, err
		}
		if v.Tags == nil {
			v.Tags = []string{}
		}
		v.Answerable = answerable == 1
		values = append(values, v)
	}
	return values, rows.Err()
}

// SaveEvalQuestion creates or updates one question. Expected documents must
// belong to the same knowledge base, so a stale id cannot silently score zero.
func (c *Core) SaveEvalQuestion(ctx context.Context, id, baseID, question string, expected []string, tags []string) (EvalQuestion, error) {
	return c.SaveEvalQuestionFull(ctx, id, baseID, question, expected, tags, true, "")
}

// SaveEvalQuestionFull also stores whether the question is answerable from the
// corpus and an optional expected answer. Unanswerable questions measure
// abstention instead of recall.
func (c *Core) SaveEvalQuestionFull(ctx context.Context, id, baseID, question string, expected []string, tags []string, answerable bool, expectedAnswer string) (EvalQuestion, error) {
	question = strings.TrimSpace(question)
	if question == "" || len(question) > 2000 {
		return EvalQuestion{}, invalid("请填写评测问题（最多 2000 字节）。")
	}
	if answerable && (len(expected) == 0 || len(expected) > 10) {
		return EvalQuestion{}, invalid("可回答问题需要选择 1–10 份期望来源文档。")
	}
	if !answerable && len(expected) > 10 {
		return EvalQuestion{}, invalid("期望来源文档最多 10 份。")
	}
	if len(expectedAnswer) > 2000 {
		return EvalQuestion{}, invalid("期望答案最多 2000 字节。")
	}
	var exists int
	if err := c.db.QueryRowContext(ctx, "SELECT count(*) FROM knowledge_bases WHERE id=? AND deleted=0", baseID).Scan(&exists); err != nil {
		return EvalQuestion{}, err
	}
	if exists == 0 {
		return EvalQuestion{}, missing()
	}
	seen := map[string]bool{}
	for _, documentID := range expected {
		if seen[documentID] {
			return EvalQuestion{}, invalid("期望来源文档不能重复。")
		}
		seen[documentID] = true
		var count int
		if err := c.db.QueryRowContext(ctx, "SELECT count(*) FROM knowledge_documents WHERE id=? AND knowledge_id=? AND deleted=0", documentID, baseID).Scan(&count); err != nil {
			return EvalQuestion{}, err
		}
		if count == 0 {
			return EvalQuestion{}, invalid("期望来源文档不属于该知识库或已删除。")
		}
	}
	if len(tags) > 16 {
		return EvalQuestion{}, invalid("标签最多 16 个。")
	}
	clean := []string{}
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		if len(tag) > 40 {
			return EvalQuestion{}, invalid("单个标签最多 40 字节。")
		}
		clean = append(clean, tag)
	}
	rawExpected, _ := json.Marshal(expected)
	rawTags, _ := json.Marshal(clean)
	value := EvalQuestion{KnowledgeID: baseID, Question: question, ExpectedDocumentIDs: expected, Tags: clean, Answerable: answerable, ExpectedAnswer: strings.TrimSpace(expectedAnswer), Created: time.Now().UnixMilli()}
	if id == "" {
		value.ID = rand.Text()
		_, err := c.db.ExecContext(ctx, "INSERT INTO knowledge_eval_questions(id,knowledge_id,question,expected,tags,answerable,expected_answer,created) VALUES(?,?,?,?,?,?,?,?)", value.ID, baseID, question, string(rawExpected), string(rawTags), boolFlag(answerable), value.ExpectedAnswer, value.Created)
		return value, err
	}
	result, err := c.db.ExecContext(ctx, "UPDATE knowledge_eval_questions SET question=?,expected=?,tags=?,answerable=?,expected_answer=? WHERE id=? AND knowledge_id=?", question, string(rawExpected), string(rawTags), boolFlag(answerable), value.ExpectedAnswer, id, baseID)
	if err != nil {
		return value, err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return value, missing()
	}
	value.ID = id
	if err := c.db.QueryRowContext(ctx, "SELECT created FROM knowledge_eval_questions WHERE id=?", id).Scan(&value.Created); err != nil {
		return value, err
	}
	return value, nil
}

func (c *Core) DeleteEvalQuestion(ctx context.Context, id string) error {
	result, err := c.db.ExecContext(ctx, "DELETE FROM knowledge_eval_questions WHERE id=?", id)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return missing()
	}
	return nil
}

// RunEval scores the current index against the stored questions. It never calls
// a model and never writes to documents, so it is safe to run repeatedly.
func (c *Core) RunEval(ctx context.Context, baseID string) (EvalRun, error) {
	questions, err := c.EvalQuestions(ctx, baseID)
	if err != nil {
		return EvalRun{}, err
	}
	if len(questions) == 0 {
		return EvalRun{}, invalid("请先添加至少一个评测问题。")
	}
	scope := Scope{KnowledgeIDs: []string{baseID}}
	metrics, cases, err := c.scoreQuestions(ctx, scope, questions, false)
	if err != nil {
		return EvalRun{}, err
	}
	// The baseline replays single-query BM25 without expansion or weighting so
	// an improvement is measured against the pre-K2 behaviour, not assumed.
	baseline, _, err := c.scoreQuestions(ctx, scope, questions, true)
	if err != nil {
		return EvalRun{}, err
	}
	metrics.BaselineRecallAt5 = baseline.RecallAt5
	metrics.BaselineMRR = baseline.MRR
	// Compare with the previous retrieval run to flag obvious regressions.
	var previous string
	if err := c.db.QueryRowContext(ctx, "SELECT metrics FROM knowledge_eval_runs WHERE knowledge_id=? AND kind='retrieval' ORDER BY created DESC,rowid DESC LIMIT 1", baseID).Scan(&previous); err == nil {
		var prior EvalMetrics
		if json.Unmarshal([]byte(previous), &prior) == nil {
			metrics.PreviousRecallAt5 = prior.RecallAt5
			if prior.RecallAt5-metrics.RecallAt5 >= 0.05 {
				metrics.Regressed = true
			}
		}
	}
	run := EvalRun{ID: rand.Text(), KnowledgeID: baseID, Kind: "retrieval", Created: time.Now().UnixMilli(), Metrics: metrics, Cases: cases}
	rawMetrics, _ := json.Marshal(run.Metrics)
	rawCases, _ := json.Marshal(run.Cases)
	if _, err := c.db.ExecContext(ctx, "INSERT INTO knowledge_eval_runs(id,knowledge_id,created,metrics,cases,kind) VALUES(?,?,?,?,?,?)", run.ID, baseID, run.Created, string(rawMetrics), string(rawCases), run.Kind); err != nil {
		return EvalRun{}, err
	}
	return run, nil
}

// SaveAnswerEvalRun stores an answer-quality evaluation (model answers scored
// for citation precision, faithfulness and abstention).
func (c *Core) SaveAnswerEvalRun(ctx context.Context, baseID string, metrics EvalMetrics, cases any) (EvalRun, error) {
	rawMetrics, _ := json.Marshal(metrics)
	rawCases, _ := json.Marshal(cases)
	run := EvalRun{ID: rand.Text(), KnowledgeID: baseID, Kind: "answer", Created: time.Now().UnixMilli(), Metrics: metrics}
	if _, err := c.db.ExecContext(ctx, "INSERT INTO knowledge_eval_runs(id,knowledge_id,created,metrics,cases,kind) VALUES(?,?,?,?,?,'answer')", run.ID, baseID, run.Created, string(rawMetrics), string(rawCases)); err != nil {
		return EvalRun{}, err
	}
	return run, nil
}

// scoreQuestions runs one evaluation pass. baseline disables expansion and
// structural weighting; everything else stays identical.
func (c *Core) scoreQuestions(ctx context.Context, scope Scope, questions []EvalQuestion, baseline bool) (EvalMetrics, []EvalCaseResult, error) {
	cases := make([]EvalCaseResult, 0, len(questions))
	var recall1, recall3, recall5, mrr, empty, latency, abstention float64
	for _, question := range questions {
		started := time.Now()
		result, err := c.Search(ctx, scope, SearchRequest{Query: question.Question, Limit: 5, Baseline: baseline})
		latency += float64(time.Since(started).Microseconds()) / 1000
		if err != nil {
			var fault *Fault
			if errors.As(err, &fault) && fault.Kind == "invalid" {
				cases = append(cases, EvalCaseResult{QuestionID: question.ID, Question: question.Question, Expected: question.ExpectedDocumentIDs, Ranked: []string{}, FirstRank: 0, Pass: false})
				continue
			}
			return EvalMetrics{}, nil, err
		}
		ranked := make([]string, 0, len(result.Hits))
		firstRank := 0
		for index, hit := range result.Hits {
			ranked = append(ranked, hit.DocumentID)
			if firstRank == 0 && containsString(question.ExpectedDocumentIDs, hit.DocumentID) {
				firstRank = index + 1
			}
		}
		if len(ranked) == 0 {
			empty++
		}
		if question.Answerable && len(ranked) > 0 {
			abstention++
		}
		if firstRank > 0 {
			mrr += 1 / float64(firstRank)
			if firstRank <= 1 {
				recall1++
			}
			if firstRank <= 3 {
				recall3++
			}
			if firstRank <= 5 {
				recall5++
			}
		}
		if !question.Answerable {
			// An unanswerable question passes when retrieval finds nothing; it is
			// not scored for recall.
			cases = append(cases, EvalCaseResult{QuestionID: question.ID, Question: question.Question, Expected: question.ExpectedDocumentIDs, Ranked: ranked, FirstRank: 0, Pass: len(ranked) == 0})
			if len(ranked) == 0 {
				abstention++
			}
			continue
		}
		cases = append(cases, EvalCaseResult{QuestionID: question.ID, Question: question.Question, Expected: question.ExpectedDocumentIDs, Ranked: ranked, FirstRank: firstRank, Pass: firstRank > 0 && firstRank <= 5})
	}
	total := float64(len(questions))
	return EvalMetrics{
		Questions:     len(questions),
		RecallAt1:     round(recall1 / total),
		RecallAt3:     round(recall3 / total),
		RecallAt5:     round(recall5 / total),
		MRR:           round(mrr / total),
		NoResultRate:  round(empty / total),
		AbstentionAcc: round(abstention / total),
		AvgLatencyMS:  round(latency / total),
	}, cases, nil
}

// EvalRuns lists recent runs, newest first, without per-case payloads.
func (c *Core) EvalRuns(ctx context.Context, baseID string, limit int) ([]EvalRunSummary, error) {
	if limit < 1 || limit > 50 {
		limit = 10
	}
	rows, err := c.db.QueryContext(ctx, "SELECT id,knowledge_id,created,metrics,kind FROM knowledge_eval_runs WHERE knowledge_id=? ORDER BY created DESC,rowid DESC LIMIT ?", baseID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []EvalRunSummary{}
	for rows.Next() {
		var v EvalRunSummary
		var metrics string
		if err := rows.Scan(&v.ID, &v.KnowledgeID, &v.Created, &metrics, &v.Kind); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(metrics), &v.Metrics); err != nil {
			return nil, err
		}
		values = append(values, v)
	}
	return values, rows.Err()
}

// EvalRun loads one stored run including its per-case results.
func (c *Core) EvalRun(ctx context.Context, id string) (EvalRun, error) {
	var run EvalRun
	var metrics, cases string
	err := c.db.QueryRowContext(ctx, "SELECT id,knowledge_id,created,metrics,cases,kind FROM knowledge_eval_runs WHERE id=?", id).Scan(&run.ID, &run.KnowledgeID, &run.Created, &metrics, &cases, &run.Kind)
	if err == sql.ErrNoRows {
		return run, missing()
	}
	if err != nil {
		return run, err
	}
	if err := json.Unmarshal([]byte(metrics), &run.Metrics); err != nil {
		return run, err
	}
	if err := json.Unmarshal([]byte(cases), &run.Cases); err != nil {
		return run, err
	}
	if run.Cases == nil {
		run.Cases = []EvalCaseResult{}
	}
	return run, nil
}

// RecordTrace persists one management retrieval trace. Traces are bounded and
// expire with the rest of the retention window; failures are non-fatal because
// a trace must never block a search.
func (c *Core) RecordTrace(ctx context.Context, knowledgeID string, trace *Trace) {
	if trace == nil {
		return
	}
	raw, err := json.Marshal(trace)
	if err != nil || len(raw) > 64*1024 {
		return
	}
	_, _ = c.db.ExecContext(ctx, "INSERT INTO retrieval_traces(run_id,knowledge_id,query,trace,created) VALUES('',?,?,?,?)", knowledgeID, trace.Query, string(raw), time.Now().UnixMilli())
}

// PruneTraces keeps the trace table bounded without touching business records.
func (c *Core) PruneTraces(ctx context.Context, keep int) {
	if keep < 0 {
		return
	}
	_, _ = c.db.ExecContext(ctx, "DELETE FROM retrieval_traces WHERE id NOT IN (SELECT id FROM retrieval_traces ORDER BY id DESC LIMIT ?)", keep)
}

func boolFlag(value bool) int {
	if value {
		return 1
	}
	return 0
}

func containsString(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

func round(value float64) float64 {
	return math.Round(value*10000) / 10000
}

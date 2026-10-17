package knowledge

import "time"

// Trace explains one retrieval request. It records the real query terms, the
// per-term match attempts, candidate scores and stage timings so an
// administrator can see why a chunk was (or was not) returned.
//
// Traces are diagnostics, not business records: they carry document titles and
// scores, never full document text.
type Trace struct {
	Query       string        `json:"query"`
	QueryType   string        `json:"query_type"`
	Terms       []string      `json:"terms"`
	MatchMode   string        `json:"match_mode"`
	KnowledgeID []string      `json:"knowledge_ids"`
	Stages      []TraceStage  `json:"stages"`
	Candidates  []TraceHit    `json:"candidates"`
	Selected    []TraceHit    `json:"selected"`
	Rerank      *RerankTrace  `json:"rerank,omitempty"`
	Total       int           `json:"total_candidates"`
	Duration    time.Duration `json:"duration_ns"`
}

// TraceStage is one bounded step of the retrieval pipeline with its own timing.
type TraceStage struct {
	Name     string        `json:"name"`
	Detail   string        `json:"detail,omitempty"`
	Hits     int           `json:"hits"`
	Duration time.Duration `json:"duration_ns"`
}

// RerankTrace records the optional LLM rerank so an administrator can see what
// changed and whether the model call fell back.
type RerankTrace struct {
	Mode     string   `json:"mode"`
	Before   []string `json:"before"`
	After    []string `json:"after"`
	Fallback string   `json:"fallback,omitempty"`
}

// TraceHit is one scored chunk as seen by the pipeline. Rank is 1-based and
// only set for the selected result set.
type TraceHit struct {
	SourceID    string   `json:"source_id"`
	DocumentID  string   `json:"document_id"`
	Title       string   `json:"title"`
	Position    int      `json:"position"`
	Score       float64  `json:"score"`
	Rank        int      `json:"rank,omitempty"`
	MatchedTerm []string `json:"matched_terms,omitempty"`
}

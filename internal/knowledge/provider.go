// Package knowledge owns native knowledge lifecycle and an engine-independent
// retrieval boundary. It has no dependency on Runtime or MCP.
package knowledge

import (
	"context"
	"fmt"
)

type Fault struct{ Kind, Message string }

func (e *Fault) Error() string         { return e.Message }
func invalid(message string) error     { return &Fault{"invalid", message} }
func missing() error                   { return &Fault{"not_found", "知识库、文档或来源不存在。"} }
func conflict(message string) error    { return &Fault{"conflict", message} }
func tooLarge(message string) error    { return &Fault{"too_large", message} }
func unavailable(message string) error { return &Fault{"unavailable", message} }

type Location struct {
	Page      int `json:"page,omitempty"`
	Paragraph int `json:"paragraph,omitempty"`
	Start     int `json:"start,omitempty"`
	End       int `json:"end,omitempty"`
}
type SearchRequest struct {
	Compact      bool                `json:"-"` // Management preview only; Agent retrieval retains its context budget.
	Trace        bool                `json:"trace,omitempty"`
	Rerank       bool                `json:"rerank,omitempty"`
	Baseline     bool                `json:"baseline,omitempty"`
	Expand       *bool               `json:"expand,omitempty"`
	ExtraQueries []string            `json:"-"`
	Synonyms     map[string][]string `json:"-"`
	Query        string              `json:"query"`
	KnowledgeIDs []string            `json:"knowledge_ids,omitempty"`
	Limit        int                 `json:"limit,omitempty"`
}
type Scope struct{ KnowledgeIDs []string }
type Hit struct {
	SourceID    string   `json:"source_id"`
	KnowledgeID string   `json:"knowledge_id"`
	DocumentID  string   `json:"document_id"`
	Version     int      `json:"version"`
	Title       string   `json:"title"`
	Text        string   `json:"text"`
	SourceURL   string   `json:"source_url"`
	Location    Location `json:"location,omitzero"`
	Score       *float64 `json:"score,omitempty"`
}
type SearchResult struct {
	Hits      []Hit  `json:"hits"`
	MatchMode string `json:"match_mode"`
	Trace     *Trace `json:"trace,omitempty"`
}
type ReadRequest struct {
	SourceID string `json:"source_id"`
	Offset   int    `json:"offset,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}
type Source struct {
	Hit
	Deleted         bool   `json:"deleted"`
	Historical      bool   `json:"historical"`
	DocumentText    string `json:"document_text,omitempty"`
	Offset          int    `json:"offset"`
	NextOffset      *int   `json:"next_offset,omitempty"`
	TotalCharacters int    `json:"total_characters,omitempty"`
}

// Providers consume an already authorized scope. Core remains responsible for
// admission checks; providers must also constrain every read/query to this scope.
// An MCP adapter can implement these two operations for a particular service;
// it need not implement local document upload, parsing or index lifecycle.
// Reranker optionally reorders the top candidates with a model. Core treats it
// as best-effort: any error or timeout keeps the fused order.
type Reranker interface {
	Rerank(context.Context, string, []Hit) ([]string, error)
}

type Provider interface {
	Search(context.Context, Scope, SearchRequest) (SearchResult, error)
	Read(context.Context, Scope, ReadRequest) (Source, error)
}

func sourceID(documentID string, version, position int) string {
	return fmt.Sprintf("%s.%d.%d", documentID, version, position)
}
func sourceURL(id string) string { return "?source=" + id }

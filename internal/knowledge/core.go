package knowledge

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Schema belongs to the platform database; only Go writes these tables.
const Schema = `
 CREATE TABLE knowledge_bases(id TEXT PRIMARY KEY,name TEXT NOT NULL,description TEXT NOT NULL,deleted INTEGER NOT NULL DEFAULT 0,created INTEGER NOT NULL);
 CREATE TABLE knowledge_documents(id TEXT PRIMARY KEY,knowledge_id TEXT NOT NULL REFERENCES knowledge_bases(id),title TEXT NOT NULL,active_version INTEGER NOT NULL DEFAULT 0,deleted INTEGER NOT NULL DEFAULT 0,cleanup_pending INTEGER NOT NULL DEFAULT 0,created INTEGER NOT NULL);
 CREATE TABLE knowledge_versions(document_id TEXT NOT NULL REFERENCES knowledge_documents(id),version INTEGER NOT NULL,filename TEXT NOT NULL,kind TEXT NOT NULL,size INTEGER NOT NULL,state TEXT NOT NULL,error TEXT NOT NULL DEFAULT '',characters INTEGER NOT NULL DEFAULT 0,chunks INTEGER NOT NULL DEFAULT 0,created INTEGER NOT NULL,PRIMARY KEY(document_id,version));
 CREATE INDEX knowledge_jobs ON knowledge_versions(state,created);
 CREATE TABLE knowledge_chunks(id TEXT PRIMARY KEY,document_id TEXT NOT NULL REFERENCES knowledge_documents(id),knowledge_id TEXT NOT NULL REFERENCES knowledge_bases(id),version INTEGER NOT NULL,position INTEGER NOT NULL,text TEXT NOT NULL,location TEXT NOT NULL);
 CREATE INDEX knowledge_document_chunks ON knowledge_chunks(document_id,version);
 CREATE VIRTUAL TABLE knowledge_fts USING fts5(chunk_id UNINDEXED,title_tokens,body_tokens,tokenize='unicode61');
 CREATE TABLE knowledge_bindings(agent_id TEXT NOT NULL,knowledge_id TEXT NOT NULL REFERENCES knowledge_bases(id),PRIMARY KEY(agent_id,knowledge_id));
 CREATE TABLE run_sources(run_id TEXT NOT NULL REFERENCES runs(id),source_id TEXT NOT NULL,reference TEXT NOT NULL,PRIMARY KEY(run_id,source_id));
`

type Base struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Documents   int    `json:"documents"`
	Created     int64  `json:"created"`
}
type Document struct {
	ID             string `json:"id"`
	KnowledgeID    string `json:"knowledge_id"`
	Title          string `json:"title"`
	ActiveVersion  int    `json:"active_version"`
	Version        int    `json:"version"`
	Filename       string `json:"filename"`
	Kind           string `json:"kind"`
	Size           int64  `json:"size"`
	State          string `json:"state"`
	Error          string `json:"error"`
	Characters     int    `json:"characters"`
	Chunks         int    `json:"chunks"`
	Deleted        bool   `json:"deleted"`
	CleanupPending bool   `json:"cleanup_pending"`
	Created        int64  `json:"created"`
}
type Core struct {
	db       *sql.DB
	root     string
	provider Provider
	reranker Reranker
	external func(context.Context, string, string) (string, error)
	wake     chan struct{}
}

func New(db *sql.DB, dataDirectory string) (*Core, error) {
	root := filepath.Join(dataDirectory, "knowledge")
	if err := os.MkdirAll(filepath.Join(root, "staging"), 0700); err != nil {
		return nil, err
	}
	core := &Core{db: db, root: root, wake: make(chan struct{}, 1)}
	core.provider = &LocalProvider{db: db, root: root}
	// Startup runs before HTTP admission. Temporary files never represent ready data.
	if err := os.RemoveAll(filepath.Join(root, "staging")); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(root, "staging"), 0700); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.Name() == "staging" || !entry.IsDir() {
			continue
		}
		versions, err := os.ReadDir(filepath.Join(root, entry.Name()))
		if err != nil {
			return nil, err
		}
		for _, version := range versions {
			number, err := strconv.Atoi(version.Name())
			if err != nil || !version.IsDir() {
				continue
			}
			var count int
			if err := db.QueryRow("SELECT count(*) FROM knowledge_versions WHERE document_id=? AND version=?", entry.Name(), number).Scan(&count); err != nil {
				return nil, err
			}
			if count == 0 {
				if err := os.RemoveAll(filepath.Join(root, entry.Name(), version.Name())); err != nil {
					return nil, err
				}
			}
		}
	}
	return core, nil
}
func (c *Core) notify() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}
func (c *Core) List(ctx context.Context) ([]Base, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT k.id,k.name,k.description,count(d.id),k.created FROM knowledge_bases k LEFT JOIN knowledge_documents d ON d.knowledge_id=k.id AND d.deleted=0 WHERE k.deleted=0 GROUP BY k.id ORDER BY k.created,k.rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []Base{}
	for rows.Next() {
		var b Base
		if err := rows.Scan(&b.ID, &b.Name, &b.Description, &b.Documents, &b.Created); err != nil {
			return nil, err
		}
		values = append(values, b)
	}
	return values, rows.Err()
}
func (c *Core) SaveBase(ctx context.Context, id, name, description string) (Base, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 200 || len(description) > 4000 {
		return Base{}, invalid("请填写知识库名称（最多 200 字节），说明最多 4000 字节。")
	}
	b := Base{ID: id, Name: name, Description: description, Created: time.Now().UnixMilli()}
	if id == "" {
		b.ID = rand.Text()
		_, err := c.db.ExecContext(ctx, "INSERT INTO knowledge_bases(id,name,description,created) VALUES(?,?,?,?)", b.ID, b.Name, b.Description, b.Created)
		return b, err
	}
	result, err := c.db.ExecContext(ctx, "UPDATE knowledge_bases SET name=?,description=? WHERE id=? AND deleted=0", name, description, id)
	if err != nil {
		return b, err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return b, missing()
	}
	return b, nil
}
func (c *Core) Scope(ctx context.Context, includeDeleted bool) (Scope, error) {
	query := "SELECT id FROM knowledge_bases"
	if !includeDeleted {
		query += " WHERE deleted=0"
	}
	rows, err := c.db.QueryContext(ctx, query)
	if err != nil {
		return Scope{}, err
	}
	defer rows.Close()
	scope := Scope{KnowledgeIDs: []string{}}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return scope, err
		}
		scope.KnowledgeIDs = append(scope.KnowledgeIDs, id)
	}
	return scope, rows.Err()
}
func (c *Core) Bindings(ctx context.Context, agentID string) ([]string, error) {
	rows, err := c.db.QueryContext(ctx, "SELECT b.knowledge_id FROM knowledge_bindings b JOIN knowledge_bases k ON k.id=b.knowledge_id WHERE b.agent_id=? AND k.deleted=0 ORDER BY k.created,k.id", agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
func (c *Core) Bind(ctx context.Context, agentID string, ids []string) error {
	if len(ids) > 16 {
		return invalid("每个助手最多关联 16 个知识库。")
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	seen := map[string]bool{}
	for _, id := range ids {
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM knowledge_bases WHERE id=? AND deleted=0", id).Scan(&count); err != nil {
			return err
		}
		if count != 1 || seen[id] {
			return invalid("知识库不存在、已删除或重复选择。")
		}
		seen[id] = true
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM knowledge_bindings WHERE agent_id=?", agentID); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, "INSERT INTO knowledge_bindings VALUES(?,?)", agentID, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (c *Core) Search(ctx context.Context, scope Scope, request SearchRequest) (SearchResult, error) {
	if !utf8.ValidString(request.Query) || strings.TrimSpace(request.Query) == "" || utf8.RuneCountInString(request.Query) > 500 || strings.ContainsRune(request.Query, 0) {
		return SearchResult{}, invalid("检索词须为有效文本，最多 500 个字符。")
	}
	if request.Limit == 0 {
		request.Limit = 5
	}
	if request.Limit < 1 || request.Limit > 10 {
		return SearchResult{}, invalid("每次检索最多返回 10 个片段。")
	}
	if len(request.KnowledgeIDs) > 0 {
		for _, id := range request.KnowledgeIDs {
			if !slices.Contains(scope.KnowledgeIDs, id) {
				return SearchResult{}, &Fault{"forbidden", "不能检索未授权的知识库。"}
			}
		}
		scope.KnowledgeIDs = request.KnowledgeIDs
	}
	if len(scope.KnowledgeIDs) == 0 {
		return SearchResult{Hits: []Hit{}, MatchMode: "no_accessible_bases"}, nil
	}
	// Per-base configuration and vocabulary apply only when the scope is one
	// knowledge base; a multi-base search keeps the platform defaults.
	if len(scope.KnowledgeIDs) == 1 && !request.Baseline {
		baseID := scope.KnowledgeIDs[0]
		if cfg, err := c.BaseConfig(ctx, baseID); err == nil && !cfg.Expand {
			disabled := false
			request.Expand = &disabled
		}
		if entries, err := c.Vocabulary(ctx, baseID); err == nil && len(entries) > 0 {
			request.Synonyms = map[string][]string{}
			for _, entry := range entries {
				request.Synonyms[entry.Term] = append(request.Synonyms[entry.Term], entry.Expansion)
			}
		}
	}
	result, err := c.provider.Search(ctx, scope, request)
	if err != nil {
		return result, err
	}
	if request.Rerank {
		return c.rerank(ctx, request, result), nil
	}
	return result, nil
}

// SetExternalParser installs an extension-backed parser. It receives the
// extension (without the dot) and the stored file path, and returns text.
func (c *Core) SetExternalParser(parser func(context.Context, string, string) (string, error)) {
	c.external = parser
}

// Reranker exposes the installed reranker so the platform can reuse its model
// for optional query expansion.
func (c *Core) Reranker() Reranker { return c.reranker }

// SetReranker installs the optional model reranker. Passing nil keeps heuristic
// ordering only.
func (c *Core) SetReranker(reranker Reranker) { c.reranker = reranker }

// rerankLimit bounds the model call: only the fused top candidates are sent.
const rerankLimit = 20

// rerank reorders the first candidates by model preference. Any failure keeps
// the heuristic order and is recorded in the trace instead of failing the
// search.
func (c *Core) rerank(ctx context.Context, request SearchRequest, result SearchResult) SearchResult {
	before := make([]string, 0, len(result.Hits))
	for _, hit := range result.Hits {
		before = append(before, hit.SourceID)
	}
	record := func(mode, fallback string, after []string) {
		if result.Trace != nil {
			result.Trace.Rerank = &RerankTrace{Mode: mode, Before: before, After: after, Fallback: fallback}
		}
	}
	if c.reranker == nil || len(result.Hits) < 2 {
		record("none", "unavailable", before)
		return result
	}
	candidates := result.Hits
	if len(candidates) > rerankLimit {
		candidates = candidates[:rerankLimit]
	}
	ids, err := c.reranker.Rerank(ctx, request.Query, candidates)
	if err != nil {
		record("llm", "heuristic", before)
		return result
	}
	ordered := reorder(result.Hits, ids)
	after := make([]string, 0, len(ordered))
	for _, hit := range ordered {
		after = append(after, hit.SourceID)
	}
	record("llm", "", after)
	result.Hits = ordered
	if result.Trace != nil {
		result.Trace.Selected = result.Trace.Selected[:0]
		for index, hit := range ordered {
			entry := TraceHit{SourceID: hit.SourceID, DocumentID: hit.DocumentID, Title: hit.Title, Rank: index + 1}
			if hit.Score != nil {
				entry.Score = *hit.Score
			}
			result.Trace.Selected = append(result.Trace.Selected, entry)
		}
	}
	return result
}

// reorder applies the model's order, keeping any candidate the model omitted at
// the end in its original position.
func reorder(hits []Hit, ids []string) []Hit {
	position := map[string]int{}
	for index, hit := range hits {
		position[hit.SourceID] = index
	}
	used := map[string]bool{}
	ordered := make([]Hit, 0, len(hits))
	for _, id := range ids {
		index, ok := position[id]
		if !ok || used[id] {
			continue
		}
		used[id] = true
		ordered = append(ordered, hits[index])
	}
	for _, hit := range hits {
		if !used[hit.SourceID] {
			ordered = append(ordered, hit)
		}
	}
	return ordered
}
func (c *Core) Read(ctx context.Context, scope Scope, request ReadRequest) (Source, error) {
	return c.provider.Read(ctx, scope, request)
}

const documentSelect = `SELECT d.id,d.knowledge_id,d.title,d.active_version,v.version,v.filename,v.kind,v.size,v.state,v.error,v.characters,v.chunks,d.deleted,d.cleanup_pending,d.created FROM knowledge_documents d JOIN knowledge_versions v ON v.document_id=d.id AND v.version=(SELECT max(version) FROM knowledge_versions WHERE document_id=d.id)`

func scanDocument(row interface{ Scan(...any) error }) (Document, error) {
	var d Document
	err := row.Scan(&d.ID, &d.KnowledgeID, &d.Title, &d.ActiveVersion, &d.Version, &d.Filename, &d.Kind, &d.Size, &d.State, &d.Error, &d.Characters, &d.Chunks, &d.Deleted, &d.CleanupPending, &d.Created)
	return d, err
}
func (c *Core) Documents(ctx context.Context, baseID string) ([]Document, error) {
	var exists int
	if err := c.db.QueryRowContext(ctx, "SELECT count(*) FROM knowledge_bases WHERE id=? AND deleted=0", baseID).Scan(&exists); err != nil {
		return nil, err
	}
	if exists == 0 {
		return nil, missing()
	}
	rows, err := c.db.QueryContext(ctx, documentSelect+" WHERE d.knowledge_id=? AND (d.deleted=0 OR d.cleanup_pending=1) ORDER BY d.created DESC,d.id", baseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	documents := []Document{}
	for rows.Next() {
		d, err := scanDocument(rows)
		if err != nil {
			return nil, err
		}
		documents = append(documents, d)
	}
	return documents, rows.Err()
}
func (c *Core) Document(ctx context.Context, id string) (Document, error) {
	d, err := scanDocument(c.db.QueryRowContext(ctx, documentSelect+" WHERE d.id=?", id))
	if err == sql.ErrNoRows {
		return d, missing()
	}
	return d, err
}
func (c *Core) Upload(ctx context.Context, baseID, documentID, filename string, reader io.Reader) (Document, error) {
	filename = filepath.Base(strings.ReplaceAll(filename, "\\", "/"))
	if filename == "." || filename == "" || len(filename) > 240 {
		return Document{}, invalid("文件名无效，最多 240 字节。")
	}
	kind := strings.ToLower(filepath.Ext(filename))
	switch kind {
	case ".txt":
		kind = "txt"
	case ".md", ".markdown":
		kind = "markdown"
	case ".pdf":
		kind = "pdf"
	case ".docx":
		kind = "docx"
	default:
		if c.external == nil || len(kind) < 2 || len(kind) > 17 {
			return Document{}, invalid("仅支持 UTF-8 TXT、Markdown、DOCX 和可提取文本的 PDF；其他格式需要安装并启用解析扩展。")
		}
		kind = strings.TrimPrefix(kind, ".")
	}
	staging := filepath.Join(c.root, "staging", rand.Text())
	if err := os.Mkdir(staging, 0700); err != nil {
		return Document{}, err
	}
	defer os.RemoveAll(staging)
	file, err := os.OpenFile(filepath.Join(staging, "original"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return Document{}, err
	}
	size, err := io.Copy(file, io.LimitReader(reader, MaxUpload+1))
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return Document{}, err
	}
	if closeErr != nil {
		return Document{}, closeErr
	}
	if size > MaxUpload {
		return Document{}, invalid("单个文件不能超过 20 MiB。")
	}
	if size == 0 {
		return Document{}, invalid("文件为空，请选择包含内容的文档。")
	}
	if kind == "docx" {
		file, err := os.Open(filepath.Join(staging, "original"))
		if err != nil {
			return Document{}, err
		}
		var magic [2]byte
		_, err = io.ReadFull(file, magic[:])
		file.Close()
		if err != nil || string(magic[:]) != "PK" {
			return Document{}, invalid("文件内容不是有效的 DOCX。")
		}
	}
	if kind == "pdf" {
		file, err := os.Open(filepath.Join(staging, "original"))
		if err != nil {
			return Document{}, err
		}
		var magic [5]byte
		_, err = io.ReadFull(file, magic[:])
		file.Close()
		if err != nil || string(magic[:]) != "%PDF-" {
			return Document{}, invalid("文件内容不是有效的 PDF。")
		}
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return Document{}, err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM knowledge_bases WHERE id=? AND deleted=0", baseID).Scan(&exists); err != nil {
		return Document{}, err
	}
	if exists == 0 {
		return Document{}, missing()
	}
	version := 1
	created := time.Now().UnixMilli()
	if documentID == "" {
		documentID = rand.Text()
		if _, err := tx.ExecContext(ctx, "INSERT INTO knowledge_documents(id,knowledge_id,title,created) VALUES(?,?,?,?)", documentID, baseID, filename, created); err != nil {
			return Document{}, err
		}
	} else {
		var state string
		var deleted bool
		var savedBase string
		err := tx.QueryRowContext(ctx, `SELECT d.knowledge_id,d.deleted,v.version,v.state FROM knowledge_documents d JOIN knowledge_versions v ON v.document_id=d.id WHERE d.id=? ORDER BY v.version DESC LIMIT 1`, documentID).Scan(&savedBase, &deleted, &version, &state)
		if err == sql.ErrNoRows || deleted || savedBase != baseID {
			return Document{}, missing()
		}
		if err != nil {
			return Document{}, err
		}
		if state == "queued" || state == "processing" {
			return Document{}, conflict("文档正在处理中，请完成后再更新。")
		}
		version++
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO knowledge_versions(document_id,version,filename,kind,size,state,created) VALUES(?,?,?,?,?,'queued',?)", documentID, version, filename, kind, size, created); err != nil {
		return Document{}, err
	}
	parent := filepath.Join(c.root, documentID)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return Document{}, err
	}
	destination := filepath.Join(parent, strconv.Itoa(version))
	if err := os.Rename(staging, destination); err != nil {
		return Document{}, err
	}
	committed := false
	defer func() {
		if !committed {
			os.RemoveAll(destination)
		}
	}()
	if err := tx.Commit(); err != nil {
		return Document{}, err
	}
	committed = true
	c.notify()
	return c.Document(ctx, documentID)
}
func (c *Core) Retry(ctx context.Context, id string) (Document, error) {
	d, err := c.Document(ctx, id)
	if err != nil {
		return d, err
	}
	if d.Deleted {
		return d, missing()
	}
	if d.State == "failed" {
		result, err := c.db.ExecContext(ctx, "UPDATE knowledge_versions SET state='queued',error='' WHERE document_id=? AND version=? AND state='failed' AND EXISTS(SELECT 1 FROM knowledge_documents WHERE id=? AND deleted=0)", id, d.Version, id)
		if err != nil {
			return d, err
		}
		count, _ := result.RowsAffected()
		if count == 0 {
			return d, conflict("文档状态已变化，请刷新后重试。")
		}
		c.notify()
		return c.Document(ctx, id)
	}
	if d.State != "ready" {
		return d, conflict("文档正在处理中，请稍后重试。")
	}
	file, err := os.Open(filepath.Join(c.root, id, strconv.Itoa(d.Version), "original"))
	if err != nil {
		return d, unavailable("原文件不可读取，请重新上传文档。")
	}
	defer file.Close()
	return c.Upload(ctx, d.KnowledgeID, id, d.Filename, file)
}
func (c *Core) DeleteDocument(ctx context.Context, id string) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, "UPDATE knowledge_documents SET deleted=1,cleanup_pending=1 WHERE id=?", id)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return missing()
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM knowledge_fts WHERE chunk_id IN(SELECT id FROM knowledge_chunks WHERE document_id=?)", id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM knowledge_chunks WHERE document_id=?", id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE knowledge_versions SET state='deleted',error='' WHERE document_id=?", id); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	c.notify()
	return c.cleanupDocument(ctx, id)
}
func (c *Core) cleanupDocument(ctx context.Context, id string) error {
	if err := os.RemoveAll(filepath.Join(c.root, id)); err != nil {
		return unavailable("已从检索中删除，原文件清理待重试；后台会继续处理。")
	}
	_, err := c.db.ExecContext(ctx, "UPDATE knowledge_documents SET cleanup_pending=0 WHERE id=? AND deleted=1", id)
	return err
}
func (c *Core) DeleteBase(ctx context.Context, id string) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, "UPDATE knowledge_bases SET deleted=1 WHERE id=? AND deleted=0", id)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return missing()
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM knowledge_bindings WHERE knowledge_id=?", id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE knowledge_documents SET deleted=1,cleanup_pending=1 WHERE knowledge_id=?", id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM knowledge_fts WHERE chunk_id IN(SELECT id FROM knowledge_chunks WHERE knowledge_id=?)", id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM knowledge_chunks WHERE knowledge_id=?", id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE knowledge_versions SET state='deleted',error='' WHERE document_id IN(SELECT id FROM knowledge_documents WHERE knowledge_id=?)", id); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	c.notify()
	return nil
}
func (c *Core) Run(ctx context.Context) error {
	// Indexing is local and transactional, so interrupted local work is safe to rebuild.
	if _, err := c.db.ExecContext(ctx, "UPDATE knowledge_versions SET state='queued',error='' WHERE state='processing'"); err != nil {
		return err
	}
	ticker := time.NewTicker(400 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := c.cleanup(ctx); err != nil && ctx.Err() == nil {
			return err
		}
		for {
			worked, err := c.processOne(ctx)
			if ctx.Err() != nil {
				return nil
			}
			if err != nil {
				return err
			}
			if !worked {
				break
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		case <-c.wake:
		}
	}
}
func (c *Core) cleanup(ctx context.Context) error {
	rows, err := c.db.QueryContext(ctx, "SELECT id FROM knowledge_documents WHERE deleted=1 AND cleanup_pending=1")
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := c.cleanupDocument(ctx, id); err != nil {
			var fault *Fault
			if !errors.As(err, &fault) {
				return err
			}
		}
	}
	return nil
}
func (c *Core) processOne(ctx context.Context) (bool, error) {
	var id string
	var version int
	err := c.db.QueryRowContext(ctx, `SELECT v.document_id,v.version FROM knowledge_versions v JOIN knowledge_documents d ON d.id=v.document_id JOIN knowledge_bases k ON k.id=d.knowledge_id WHERE v.state='queued' AND d.deleted=0 AND k.deleted=0 ORDER BY v.created,v.document_id LIMIT 1`).Scan(&id, &version)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	result, err := c.db.ExecContext(ctx, "UPDATE knowledge_versions SET state='processing',error='' WHERE document_id=? AND version=? AND state='queued'", id, version)
	if err != nil {
		return false, err
	}
	claimed, _ := result.RowsAffected()
	if claimed == 0 {
		return true, nil
	}
	jobCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	err = c.build(jobCtx, id, version)
	cancel()
	if ctx.Err() != nil {
		resetCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, resetErr := c.db.ExecContext(resetCtx, "UPDATE knowledge_versions SET state='queued' WHERE document_id=? AND version=? AND state='processing'", id, version)
		return true, resetErr
	}
	if err != nil {
		message := "文档处理失败，请检查文件内容、磁盘空间和解析依赖后重试。"
		var fault *Fault
		if errors.As(err, &fault) {
			message = fault.Message
		}
		if errors.Is(err, context.DeadlineExceeded) {
			message = "文档处理超时，请拆分文件后重试。"
		}
		_, err = c.db.ExecContext(ctx, "UPDATE knowledge_versions SET state='failed',error=? WHERE document_id=? AND version=? AND state='processing'", message, id, version)
		return true, err
	}
	return true, nil
}

type chunk struct {
	ID          string
	Text        string
	Location    Location
	Position    int
	Tokens      string
	ParentID    string
	BlockType   string
	SectionPath string
	Clause      string
	ContentHash string
}

func (c *Core) build(ctx context.Context, id string, version int) error {
	var kind, title, baseID string
	if err := c.db.QueryRowContext(ctx, "SELECT v.kind,d.title,d.knowledge_id FROM knowledge_versions v JOIN knowledge_documents d ON d.id=v.document_id WHERE v.document_id=? AND v.version=? AND d.deleted=0", id, version).Scan(&kind, &title, &baseID); err != nil {
		return err
	}
	pages, blocks, err := c.parseDocument(ctx, filepath.Join(c.root, id, strconv.Itoa(version), "original"), kind)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	chunks, characters, err := chunkBlocks(id, version, blocks)
	if err != nil {
		return err
	}
	// Structure terms live in the weighted title column; body terms stay in the
	// body column. This gives headings and clause labels a stable boost without
	// duplicating the body text in the index.
	structureTokens := make([]string, len(chunks))
	for index := range chunks {
		if err := ctx.Err(); err != nil {
			return err
		}
		chunks[index].Tokens, err = tokenize(chunks[index].Text)
		if err != nil {
			return err
		}
		structureTokens[index], err = tokenize(strings.Join([]string{title, chunks[index].SectionPath, chunks[index].Clause}, "\n"))
		if err != nil {
			return err
		}
	}
	raw, err := json.Marshal(pages)
	if err != nil {
		return err
	}
	textPath := filepath.Join(c.root, id, strconv.Itoa(version), "text.json")
	if err := os.WriteFile(textPath+".tmp", raw, 0600); err != nil {
		return err
	}
	if err := os.Rename(textPath+".tmp", textPath); err != nil {
		return err
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var deleted bool
	if err := tx.QueryRowContext(ctx, "SELECT d.deleted OR k.deleted FROM knowledge_documents d JOIN knowledge_bases k ON k.id=d.knowledge_id WHERE d.id=?", id).Scan(&deleted); err != nil {
		return err
	}
	if deleted {
		return nil
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM knowledge_fts WHERE chunk_id IN(SELECT id FROM knowledge_chunks WHERE document_id=?)", id); err != nil {
		return err
	}
	insertChunk, err := tx.PrepareContext(ctx, "INSERT INTO knowledge_chunks(id,document_id,knowledge_id,version,position,text,location,parent_id,block_type,section_path,clause,content_hash) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)")
	if err != nil {
		return err
	}
	defer insertChunk.Close()
	insertIndex, err := tx.PrepareContext(ctx, "INSERT INTO knowledge_fts(chunk_id,title_tokens,body_tokens) VALUES(?,?,?)")
	if err != nil {
		return err
	}
	defer insertIndex.Close()
	for index, chunk := range chunks {
		location, _ := json.Marshal(chunk.Location)
		if _, err := insertChunk.ExecContext(ctx, chunk.ID, id, baseID, version, chunk.Position, chunk.Text, string(location), chunk.ParentID, chunk.BlockType, chunk.SectionPath, chunk.Clause, chunk.ContentHash); err != nil {
			return err
		}
		if _, err := insertIndex.ExecContext(ctx, chunk.ID, structureTokens[index], chunk.Tokens); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE knowledge_documents SET active_version=? WHERE id=?", version, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE knowledge_versions SET state='ready',error='',characters=?,chunks=? WHERE document_id=? AND version=? AND state='processing'", characters, len(chunks), id, version); err != nil {
		return err
	}
	return tx.Commit()
}

// DescribeDependencies does not expose filesystem paths or document content.
func (c *Core) DescribeDependencies() map[string]any {
	return map[string]any{"provider": "sqlite-fts5", "tokenizer": "gse@1.1.0", "pdf_available": pdfAvailable(), "max_file_bytes": MaxUpload, "max_characters": MaxCharacters, "max_chunks": MaxChunks, "max_pdf_pages": 1000}
}

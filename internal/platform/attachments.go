package platform

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"siagent/internal/knowledge"
)

type Attachment struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	MIME           string `json:"mime"`
	Kind           string `json:"kind"`
	Size           int64  `json:"size"`
	Created        int64  `json:"created"`
	ConversationID string `json:"conversation_id,omitempty"`
}

type storedAttachment struct {
	Attachment
	Path      string
	Extracted string
	Linked    bool
}

var imageTypes = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".webp": "image/webp",
	".gif":  "image/gif",
}

var fileTypes = map[string]string{
	".txt":      "text/plain",
	".md":       "text/markdown",
	".markdown": "text/markdown",
	".csv":      "text/csv",
	".json":     "application/json",
	".pdf":      "application/pdf",
	".docx":     "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
}

func attachmentDir(directory string) string { return filepath.Join(directory, "attachments") }

func (s *Server) uploadAttachment(w http.ResponseWriter, r *http.Request) {
	conversationID := r.PathValue("id")
	var exists int
	if err := s.store.db.QueryRow("SELECT count(*) FROM conversations WHERE id=? AND channel_id='' AND NOT EXISTS(SELECT 1 FROM conversation_cleanup WHERE conversation_id=conversations.id)", conversationID).Scan(&exists); err != nil {
		internal(w)
		return
	}
	if exists == 0 {
		fail(w, 404, "对话不存在，或渠道会话不能添加附件。")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.attachmentLimits.MaxFileBytes+1<<20)
	file, header, err := r.FormFile("file")
	if err != nil {
		fail(w, 400, "请选择要上传的图片或文件。")
		return
	}
	defer file.Close()
	name := strings.TrimSpace(filepath.Base(header.Filename))
	if name == "" || name == "." || len(name) > 200 {
		fail(w, 400, "文件名无效或过长。")
		return
	}
	ext := strings.ToLower(filepath.Ext(name))
	mimeType := ""
	kind := ""
	if value, ok := imageTypes[ext]; ok {
		mimeType, kind = value, "image"
	} else if value, ok := fileTypes[ext]; ok {
		mimeType, kind = value, "file"
	} else {
		fail(w, 415, "仅支持 PNG/JPEG/WebP/GIF 图片，以及 TXT、Markdown、CSV、JSON、PDF、DOCX 文件。")
		return
	}
	var existingCount int
	var existingBytes int64
	if err := s.store.db.QueryRow("SELECT count(*),coalesce(sum(size),0) FROM attachments WHERE conversation_id=?", conversationID).Scan(&existingCount, &existingBytes); err != nil {
		internal(w)
		return
	}
	if existingCount >= s.attachmentLimits.MaxConversationCount || existingBytes+int64(header.Size) > s.attachmentLimits.MaxConversationBytes {
		fail(w, 413, "每个对话最多保留 40 个附件、总计 100 MiB；请删除未发送附件或新建对话。")
		return
	}
	data, err := io.ReadAll(io.LimitReader(file, s.attachmentLimits.MaxFileBytes+1))
	if err != nil || int64(len(data)) > s.attachmentLimits.MaxFileBytes {
		fail(w, 413, "单个附件不能超过 10 MiB。")
		return
	}
	if len(data) == 0 {
		fail(w, 400, "附件为空。")
		return
	}
	if kind == "image" {
		if int64(len(data)) > s.attachmentLimits.MaxImageBytes {
			fail(w, 413, "单张图片不能超过 4 MiB，请压缩后重试。")
			return
		}
		detected := http.DetectContentType(data)
		if !strings.HasPrefix(detected, "image/") {
			fail(w, 415, "图片内容与扩展名不匹配。")
			return
		}
		mimeType = detected
	}
	id := id()
	if err := os.MkdirAll(attachmentDir(s.store.directory), 0700); err != nil {
		internal(w)
		return
	}
	relative := filepath.Join("attachments", id+ext)
	path := filepath.Join(s.store.directory, relative)
	if err := os.WriteFile(path, data, 0600); err != nil {
		internal(w)
		return
	}
	extracted := ""
	if kind == "file" {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		extracted, err = knowledge.ExtractText(ctx, path, strings.TrimPrefix(ext, "."))
		if err != nil {
			_ = os.Remove(path)
			fail(w, 422, "文件解析失败："+err.Error())
			return
		}
		extracted = strings.TrimSpace(extracted)
		if len([]rune(extracted)) > s.attachmentLimits.MaxExtractedRunes {
			_ = os.Remove(path)
			fail(w, 413, "文件提取文本超过 300,000 字符，请拆分后重试。")
			return
		}
		if extracted == "" {
			_ = os.Remove(path)
			fail(w, 422, "文件没有可提取的文本。")
			return
		}
	}
	created := time.Now().UnixMilli()
	_, err = s.store.db.Exec("INSERT INTO attachments(id,conversation_id,name,mime,kind,size,path,extracted,created) VALUES(?,?,?,?,?,?,?,?,?)", id, conversationID, name, mimeType, kind, len(data), relative, extracted, created)
	if err != nil {
		_ = os.Remove(path)
		internal(w)
		return
	}
	respond(w, 201, Attachment{ID: id, Name: name, MIME: mimeType, Kind: kind, Size: int64(len(data)), Created: created, ConversationID: conversationID})
}

func (s *Server) deleteAttachment(w http.ResponseWriter, r *http.Request) {
	var path string
	var linked int
	err := s.store.db.QueryRow("SELECT path,linked FROM attachments WHERE id=?", r.PathValue("id")).Scan(&path, &linked)
	if err == sql.ErrNoRows {
		respond(w, 204, nil)
		return
	}
	if err != nil {
		internal(w)
		return
	}
	if linked != 0 {
		fail(w, 409, "附件已发送，不能单独删除；删除会话时统一清理。")
		return
	}
	if _, err := s.store.db.Exec("DELETE FROM attachments WHERE id=? AND linked=0", r.PathValue("id")); err != nil {
		internal(w)
		return
	}
	_ = os.Remove(filepath.Join(s.store.directory, filepath.Clean(path)))
	respond(w, 204, nil)
}

func (s *Server) serveAttachment(w http.ResponseWriter, r *http.Request) {
	var item storedAttachment
	err := s.store.db.QueryRow("SELECT id,conversation_id,name,mime,kind,size,path,created FROM attachments WHERE id=?", r.PathValue("id")).Scan(&item.ID, &item.ConversationID, &item.Name, &item.MIME, &item.Kind, &item.Size, &item.Path, &item.Created)
	if err == sql.ErrNoRows {
		fail(w, 404, "附件不存在。")
		return
	}
	if err != nil {
		internal(w)
		return
	}
	path := filepath.Join(s.store.directory, filepath.Clean(item.Path))
	if !strings.HasPrefix(path, attachmentDir(s.store.directory)+string(os.PathSeparator)) {
		fail(w, 404, "附件不存在。")
		return
	}
	if _, err := os.Stat(path); err != nil {
		fail(w, 404, "附件文件已清理。")
		return
	}
	w.Header().Set("Content-Type", item.MIME)
	w.Header().Set("Cache-Control", "private, max-age=300")
	if item.Kind == "file" {
		w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+urlQueryEscape(item.Name))
	} else {
		w.Header().Set("Content-Disposition", "inline; filename*=UTF-8''"+urlQueryEscape(item.Name))
	}
	http.ServeFile(w, r, path)
}

func urlQueryEscape(value string) string {
	// net/url.QueryEscape uses '+' for spaces; filename* requires %20.
	return strings.ReplaceAll(strings.ReplaceAll(urlEncode(value), "+", "%20"), "%2F", "/")
}

func urlEncode(value string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for _, c := range []byte(value) {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' {
			b.WriteByte(c)
		} else {
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&15])
		}
	}
	return b.String()
}

func supportsModelInput(model Model, input string) bool {
	for _, value := range model.Options.Input {
		if value == input {
			return true
		}
	}
	return false
}

func (s *Server) loadAttachments(conversationID string, ids []string) ([]storedAttachment, error) {
	if len(ids) == 0 {
		return []storedAttachment{}, nil
	}
	if len(ids) > s.attachmentLimits.MaxMessageCount {
		return nil, fmt.Errorf("单条消息最多 %d 个附件。", s.attachmentLimits.MaxMessageCount)
	}
	seen := map[string]bool{}
	values := make([]storedAttachment, 0, len(ids))
	var total, imageBytes int64
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		var item storedAttachment
		var linked int
		err := s.store.db.QueryRow("SELECT id,conversation_id,name,mime,kind,size,path,extracted,linked,created FROM attachments WHERE id=? AND conversation_id=?", id, conversationID).Scan(&item.ID, &item.ConversationID, &item.Name, &item.MIME, &item.Kind, &item.Size, &item.Path, &item.Extracted, &linked, &item.Created)
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("附件不存在或不属于当前对话。")
		}
		if err != nil {
			return nil, err
		}
		if linked != 0 {
			return nil, fmt.Errorf("附件已被其他消息使用。")
		}
		total += item.Size
		if item.Kind == "image" {
			imageBytes += item.Size
		}
		item.Linked = true
		values = append(values, item)
	}
	if total > s.attachmentLimits.MaxMessageBytes {
		return nil, fmt.Errorf("单条消息附件总大小不能超过 40 MiB。")
	}
	if imageBytes > s.attachmentLimits.MaxMessageImageBytes {
		return nil, fmt.Errorf("单条消息图片总大小不能超过 5 MiB，请减少或压缩图片。")
	}
	return values, nil
}

func (s *Server) linkAttachments(tx *sql.Tx, runID string, items []storedAttachment) error {
	for _, item := range items {
		result, err := tx.Exec("UPDATE attachments SET run_id=?,linked=1 WHERE id=? AND linked=0", runID, item.ID)
		if err != nil {
			return err
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return errors.New("附件已被其他消息使用。")
		}
	}
	return nil
}

func (s *Server) attachmentsForRun(runID string) ([]storedAttachment, error) {
	rows, err := s.store.db.Query("SELECT id,conversation_id,name,mime,kind,size,path,extracted,created FROM attachments WHERE run_id=?", runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []storedAttachment{}
	for rows.Next() {
		var item storedAttachment
		if err := rows.Scan(&item.ID, &item.ConversationID, &item.Name, &item.MIME, &item.Kind, &item.Size, &item.Path, &item.Extracted, &item.Created); err != nil {
			return nil, err
		}
		values = append(values, item)
	}
	return values, rows.Err()
}

func (s *Server) runtimeAttachments(items []storedAttachment) ([]map[string]any, error) {
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		value := map[string]any{"kind": item.Kind, "name": item.Name, "mime": item.MIME}
		if item.Kind == "image" {
			data, err := os.ReadFile(filepath.Join(s.store.directory, filepath.Clean(item.Path)))
			if err != nil {
				return nil, err
			}
			value["data"] = base64.StdEncoding.EncodeToString(data)
		} else {
			value["text"] = item.Extracted
		}
		result = append(result, value)
	}
	return result, nil
}

// AttachmentLimits are runtime values loaded from the external system config.
// Zero fields fall back to the defaults documented in docs/chat-attachments.md.
type AttachmentLimits struct {
	MaxFileBytes         int64
	MaxImageBytes        int64
	MaxMessageImageBytes int64
	MaxExtractedRunes    int
	MaxMessageCount      int
	MaxMessageBytes      int64
	MaxConversationCount int
	MaxConversationBytes int64
	OrphanTTLHours       int
}

type ServerOptions struct {
	Attachments           AttachmentLimits
	Knowledge             *knowledge.Limits
	Limits                *Limits
	AdvancedToolsDisabled *bool
}

func defaultAttachmentLimits() AttachmentLimits {
	return AttachmentLimits{
		MaxFileBytes:         10 << 20,
		MaxImageBytes:        4 << 20,
		MaxMessageImageBytes: 5 << 20,
		MaxExtractedRunes:    300000,
		MaxMessageCount:      8,
		MaxMessageBytes:      40 << 20,
		MaxConversationCount: 40,
		MaxConversationBytes: 100 << 20,
		OrphanTTLHours:       24,
	}
}

func mergeAttachmentLimits(value AttachmentLimits) AttachmentLimits {
	defaults := defaultAttachmentLimits()
	if value.MaxFileBytes > 0 {
		defaults.MaxFileBytes = value.MaxFileBytes
	}
	if value.MaxImageBytes > 0 {
		defaults.MaxImageBytes = value.MaxImageBytes
	}
	if value.MaxMessageImageBytes > 0 {
		defaults.MaxMessageImageBytes = value.MaxMessageImageBytes
	}
	if value.MaxExtractedRunes > 0 {
		defaults.MaxExtractedRunes = value.MaxExtractedRunes
	}
	if value.MaxMessageCount > 0 {
		defaults.MaxMessageCount = value.MaxMessageCount
	}
	if value.MaxMessageBytes > 0 {
		defaults.MaxMessageBytes = value.MaxMessageBytes
	}
	if value.MaxConversationCount > 0 {
		defaults.MaxConversationCount = value.MaxConversationCount
	}
	if value.MaxConversationBytes > 0 {
		defaults.MaxConversationBytes = value.MaxConversationBytes
	}
	if value.OrphanTTLHours > 0 {
		defaults.OrphanTTLHours = value.OrphanTTLHours
	}
	return defaults
}

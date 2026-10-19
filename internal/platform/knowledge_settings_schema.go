package platform

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"siagent/internal/knowledge"
)

// Knowledge upload limits are deployment settings but administrators change
// them from the maintenance page. They live in their own row so saving the
// execution limits never overwrites them.
const knowledgeSettingsSchema = `
 CREATE TABLE IF NOT EXISTS knowledge_settings(id INTEGER PRIMARY KEY CHECK(id=1),config TEXT NOT NULL);
 PRAGMA user_version=25;
`

type knowledgeLimitSettings struct {
	MaxFileBytes      int64    `json:"max_file_bytes"`
	MaxCharacters     int      `json:"max_characters"`
	MaxChunks         int      `json:"max_chunks"`
	MaxPDFPages       int      `json:"max_pdf_pages"`
	AllowedExtensions []string `json:"allowed_extensions"`
	ExtendWithParsers bool     `json:"extend_with_parsers"`
}

func (v knowledgeLimitSettings) Limits() knowledge.Limits {
	return knowledge.Limits{
		MaxUploadBytes:    v.MaxFileBytes,
		MaxCharacters:     v.MaxCharacters,
		MaxChunks:         v.MaxChunks,
		MaxPDFPages:       v.MaxPDFPages,
		Extensions:        v.AllowedExtensions,
		ExtendWithParsers: v.ExtendWithParsers,
	}
}

func knowledgeSettingsFrom(limits knowledge.Limits) knowledgeLimitSettings {
	return knowledgeLimitSettings{
		MaxFileBytes:      limits.MaxUploadBytes,
		MaxCharacters:     limits.MaxCharacters,
		MaxChunks:         limits.MaxChunks,
		MaxPDFPages:       limits.MaxPDFPages,
		AllowedExtensions: limits.Extensions,
		ExtendWithParsers: limits.ExtendWithParsers,
	}
}

func (s *Store) migrateKnowledgeSettings() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(knowledgeSettingsSchema); err != nil {
		return err
	}
	return tx.Commit()
}

// loadKnowledgeLimits prefers a saved administrator setting over the deployment
// defaults. An invalid saved row stops startup instead of silently changing the
// accepted formats.
func (s *Server) loadKnowledgeLimits(defaults knowledge.Limits) (knowledge.Limits, error) {
	var raw string
	err := s.store.db.QueryRow("SELECT config FROM knowledge_settings WHERE id=1").Scan(&raw)
	if err == sql.ErrNoRows {
		return defaults.Normalize(), nil
	}
	if err != nil {
		return defaults, err
	}
	var stored knowledgeLimitSettings
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return defaults, fmt.Errorf("invalid saved knowledge limits: %w", err)
	}
	limits := stored.Limits()
	if err := limits.Validate(); err != nil {
		return defaults, fmt.Errorf("invalid saved knowledge limits: %w", err)
	}
	return limits.Normalize(), nil
}

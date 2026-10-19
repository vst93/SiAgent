// Package config loads the optional JSON system configuration. The file lives
// under the OS user config directory in a project-specific subdirectory.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const projectDir = "SiAgent"

type File struct {
	DataDir               string      `json:"data_dir"`
	Listen                string      `json:"listen"`
	PublicURL             string      `json:"public_url"`
	BasePath              string      `json:"base_path"`
	Runtime               string      `json:"runtime"`
	Node                  string      `json:"node"`
	ChannelWorker         string      `json:"channel_worker"`
	Attachments           Attachments `json:"attachments"`
	Limits                *Limits     `json:"limits,omitempty"`
	AdvancedToolsDisabled *bool       `json:"advanced_tools_disabled,omitempty"`
}

type Attachments struct {
	MaxFileBytes         int64 `json:"max_file_bytes"`
	MaxImageBytes        int64 `json:"max_image_bytes"`
	MaxMessageImageBytes int64 `json:"max_message_image_bytes"`
	MaxExtractedRunes    int   `json:"max_extracted_runes"`
	MaxMessageCount      int   `json:"max_message_count"`
	MaxMessageBytes      int64 `json:"max_message_bytes"`
	MaxConversationCount int   `json:"max_conversation_count"`
	MaxConversationBytes int64 `json:"max_conversation_bytes"`
	OrphanTTLHours       int   `json:"orphan_ttl_hours"`
}

type Limits struct {
	Concurrent        int `json:"concurrent"`
	ConversationQueue int `json:"conversation_queue"`
	GlobalQueue       int `json:"global_queue"`
	RunTimeout        int `json:"run_timeout_seconds"`
	ToolTimeout       int `json:"tool_timeout_seconds"`
	ToolRounds        int `json:"tool_rounds"`
}

func DefaultPath() string {
	if value := strings.TrimSpace(os.Getenv("SIAGENT_CONFIG")); value != "" {
		return value
	}
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, projectDir, "config.json")
}

// Load returns the parsed file and the resolved path. A missing default file is
// not an error; a missing file explicitly selected by SIAGENT_CONFIG is.
func Load(path string) (File, string, error) {
	explicit := strings.TrimSpace(path) != "" || strings.TrimSpace(os.Getenv("SIAGENT_CONFIG")) != ""
	if strings.TrimSpace(path) == "" {
		path = DefaultPath()
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if explicit {
			return File{}, path, fmt.Errorf("config file does not exist: %s", path)
		}
		return File{}, path, nil
	}
	if err != nil {
		return File{}, path, fmt.Errorf("read config file: %w", err)
	}
	var value File
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return File{}, path, fmt.Errorf("parse config file: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return File{}, path, errors.New("parse config file: trailing data")
	}
	if err := value.Validate(); err != nil {
		return File{}, path, err
	}
	return value, path, nil
}

func (f File) Validate() error {
	a := f.Attachments
	for name, value := range map[string]int64{
		"attachments.max_file_bytes":          a.MaxFileBytes,
		"attachments.max_image_bytes":         a.MaxImageBytes,
		"attachments.max_message_image_bytes": a.MaxMessageImageBytes,
		"attachments.max_message_bytes":       a.MaxMessageBytes,
		"attachments.max_conversation_bytes":  a.MaxConversationBytes,
	} {
		if value < 0 {
			return fmt.Errorf("%s cannot be negative", name)
		}
	}
	for name, value := range map[string]int{
		"attachments.max_extracted_runes":    a.MaxExtractedRunes,
		"attachments.max_message_count":      a.MaxMessageCount,
		"attachments.max_conversation_count": a.MaxConversationCount,
		"attachments.orphan_ttl_hours":       a.OrphanTTLHours,
	} {
		if value < 0 {
			return fmt.Errorf("%s cannot be negative", name)
		}
	}
	if f.Limits != nil {
		l := *f.Limits
		if l.Concurrent < 1 || l.Concurrent > 8 ||
			l.ConversationQueue < 1 || l.ConversationQueue > 1000 ||
			l.GlobalQueue < l.ConversationQueue || l.GlobalQueue > 10000 ||
			l.RunTimeout < 1 || l.RunTimeout > 3600 ||
			l.ToolTimeout < 1 || l.ToolTimeout > 600 ||
			l.ToolRounds < 1 || l.ToolRounds > 100 {
			return errors.New("limits are out of range")
		}
	}
	return nil
}

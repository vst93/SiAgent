package platform

import (
	"database/sql"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// The WeCom long-connection protocol returns only opaque member and chat IDs; it
// has no API for names. Administrators therefore label a group or member once,
// and every conversation with that ID shows the label. Labels are per channel
// and per vendor ID, so a rename never rewrites conversation history.

// listChannelContacts returns the labels of one channel, or of every channel
// when no filter is given.
func (s *Server) listChannelContacts(w http.ResponseWriter, r *http.Request) {
	channelID := strings.TrimSpace(r.URL.Query().Get("channel_id"))
	query := "SELECT channel_id,kind,target,name,updated FROM channel_contacts"
	args := []any{}
	if channelID != "" {
		query += " WHERE channel_id=?"
		args = append(args, channelID)
	}
	query += " ORDER BY updated DESC,rowid"
	rows, err := s.store.db.Query(query, args...)
	if err != nil {
		internal(w)
		return
	}
	defer rows.Close()
	values := []ChannelContact{}
	for rows.Next() {
		var v ChannelContact
		if err := rows.Scan(&v.ChannelID, &v.Kind, &v.Target, &v.Name, &v.Updated); err != nil {
			internal(w)
			return
		}
		values = append(values, v)
	}
	if rows.Err() != nil {
		internal(w)
		return
	}
	respond(w, 200, values)
}

type channelContactInput struct {
	ChannelID string `json:"channel_id"`
	Kind      string `json:"kind"`
	Target    string `json:"target"`
	Name      string `json:"name"`
}

// validateChannelContact checks a label against the same bounds as the store.
func validateChannelContact(input channelContactInput) (channelContactInput, string) {
	input.ChannelID = strings.TrimSpace(input.ChannelID)
	input.Kind = strings.TrimSpace(input.Kind)
	input.Target = strings.TrimSpace(input.Target)
	input.Name = strings.TrimSpace(input.Name)
	if input.ChannelID == "" || input.Target == "" || len(input.Target) > 256 {
		return input, "请选择渠道并填写联系人标识。"
	}
	if input.Kind != "group" && input.Kind != "sender" {
		return input, "联系人类型须为群聊或成员。"
	}
	if !utf8.ValidString(input.Name) || utf8.RuneCountInString(input.Name) > 64 || strings.ContainsAny(input.Name, "\x00\r\n") {
		return input, "名称须为不超过 64 字的单行文本。"
	}
	return input, ""
}

// saveChannelContact creates or renames one label. An empty name removes it.
func (s *Server) saveChannelContact(w http.ResponseWriter, r *http.Request) {
	var input channelContactInput
	if !decode(w, r, &input) {
		return
	}
	input, reason := validateChannelContact(input)
	if reason != "" {
		fail(w, 400, reason)
		return
	}
	var exists int
	if err := s.store.db.QueryRow("SELECT count(*) FROM channels WHERE id=?", input.ChannelID).Scan(&exists); err != nil {
		internal(w)
		return
	}
	if exists == 0 {
		fail(w, 404, "渠道不存在。")
		return
	}
	if input.Name == "" {
		s.removeChannelContact(input.ChannelID, input.Kind, input.Target)
		respond(w, 200, ChannelContact{ChannelID: input.ChannelID, Kind: input.Kind, Target: input.Target})
		return
	}
	updated := time.Now().UnixMilli()
	if _, err := s.store.db.Exec("INSERT INTO channel_contacts(channel_id,kind,target,name,updated) VALUES(?,?,?,?,?) ON CONFLICT(channel_id,kind,target) DO UPDATE SET name=excluded.name,updated=excluded.updated", input.ChannelID, input.Kind, input.Target, input.Name, updated); err != nil {
		internal(w)
		return
	}
	respond(w, 200, ChannelContact{ChannelID: input.ChannelID, Kind: input.Kind, Target: input.Target, Name: input.Name, Updated: updated})
}

func (s *Server) deleteChannelContact(w http.ResponseWriter, r *http.Request) {
	var input channelContactInput
	if !decode(w, r, &input) {
		return
	}
	input, reason := validateChannelContact(channelContactInput{ChannelID: input.ChannelID, Kind: input.Kind, Target: input.Target})
	if reason != "" {
		fail(w, 400, reason)
		return
	}
	s.removeChannelContact(input.ChannelID, input.Kind, input.Target)
	respond(w, 204, nil)
}

func (s *Server) removeChannelContact(channelID, kind, target string) {
	_, _ = s.store.db.Exec("DELETE FROM channel_contacts WHERE channel_id=? AND kind=? AND target=?", channelID, kind, target)
}

// channelLabelMap loads every label of one channel, keyed by "<kind>\x1f<target>".
// The store uses a single connection, so callers must not query per row while a
// result set is open; one map avoids that entirely.
func (s *Server) channelLabelMap(channelID string) map[string]string {
	names := map[string]string{}
	rows, err := s.store.db.Query("SELECT kind,target,name FROM channel_contacts WHERE channel_id=?", channelID)
	if err != nil {
		return names
	}
	defer rows.Close()
	for rows.Next() {
		var kind, target, name string
		if rows.Scan(&kind, &target, &name) == nil {
			names[kind+"\x1f"+target] = name
		}
	}
	return names
}

// channelName resolves one label, returning "" when none is set.
func (s *Server) channelName(channelID, kind, target string) string {
	if channelID == "" || target == "" {
		return ""
	}
	var name string
	if err := s.store.db.QueryRow("SELECT name FROM channel_contacts WHERE channel_id=? AND kind=? AND target=?", channelID, kind, target).Scan(&name); err != nil {
		if err != sql.ErrNoRows {
			return ""
		}
		return ""
	}
	return name
}

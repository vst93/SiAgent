package platform

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"siagent/internal/channels"
	"strings"
	"time"
	"unicode/utf8"
)

// Structured identities preserve the original transport target. A shared group
// changes only the context lookup key, never the delivery recipient.
func channelIdentity(session string) (kind, group, sender string) {
	var parts []string
	if json.Unmarshal([]byte(session), &parts) == nil && len(parts) == 3 && (parts[0] == "single" || parts[0] == "group") {
		return parts[0], parts[1], parts[2]
	}
	return "single", "", session
}
func normalizeIncoming(in *channels.Incoming) error {
	if in.ChatType == "" && in.SenderID == "" && in.GroupID == "" {
		return nil
	}
	if in.ChatType != "single" && in.ChatType != "group" {
		return errors.New("invalid conversation type")
	}
	if in.SenderID == "" || (in.ChatType == "group" && in.GroupID == "") || (in.ChatType == "single" && in.GroupID != "") {
		return errors.New("invalid conversation identity")
	}
	for _, v := range []string{in.SenderID, in.GroupID} {
		if len(v) > 256 || !utf8.ValidString(v) || strings.ContainsAny(v, "\x00\r\n") {
			return errors.New("invalid conversation identity")
		}
	}
	raw, _ := json.Marshal([]string{in.ChatType, in.GroupID, in.SenderID})
	in.Session = string(raw)
	return nil
}
func sessionAdmission(policy SessionPolicy, member string, reset bool) error {
	kind, _, _ := channelIdentity(member)
	if (kind == "group" && policy.Types == "private") || (kind != "group" && policy.Types == "group") {
		return &admissionError{403, "此助手不接收该类型会话，请联系管理员。"}
	}
	if reset && !policy.AllowReset {
		return &admissionError{403, "管理员已关闭用户 /new 重置权限。"}
	}
	return nil
}
func (s *Server) channelConversation(ctx context.Context, channel, member, agent string, reset bool) (string, error) {
	a, err := s.agentByID(agent)
	if err != nil {
		return "", err
	}
	if !a.Enabled || a.Archived {
		return "", &admissionError{409, "助手已停用或归档，请联系管理员。"}
	}
	if err := sessionAdmission(a.Policy.Session, member, reset); err != nil {
		return "", err
	}
	key := member
	kind, group, _ := channelIdentity(member)
	if kind == "group" && a.Policy.Session.GroupScope == "shared" {
		raw, _ := json.Marshal([]string{"group", group})
		key = string(raw)
	}
	var conversation string
	var last int64
	err = s.store.db.QueryRowContext(ctx, `SELECT cs.conversation_id,max(coalesce((SELECT max(created) FROM runs WHERE conversation_id=cs.conversation_id),0),coalesce((SELECT max(e.created) FROM run_events e JOIN runs r ON r.id=e.run_id WHERE r.conversation_id=cs.conversation_id),0),c.created) FROM channel_sessions cs JOIN conversations c ON c.id=cs.conversation_id WHERE cs.channel_id=? AND cs.member=? AND cs.agent_id=?`, channel, key, agent).Scan(&conversation, &last)
	if err == nil && !reset {
		expired := a.Policy.Session.IdleMinutes > 0 && time.Now().UnixMilli()-last >= int64(a.Policy.Session.IdleMinutes)*60000
		if !expired {
			return conversation, nil
		}
		var pending int
		if err := s.store.db.QueryRowContext(ctx, "SELECT count(*) FROM runs WHERE conversation_id=? AND (released=0 OR status='queued')", conversation).Scan(&pending); err != nil {
			return "", err
		}
		if pending > 0 {
			return conversation, nil
		}
	}
	if err != nil && err != sql.ErrNoRows {
		return "", err
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	conversation = id()
	if _, err := tx.ExecContext(ctx, "INSERT INTO conversations(id,title,created,agent_id,channel_id) VALUES(?,'新对话',?,?,?)", conversation, time.Now().UnixMilli(), agent, channel); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO channel_sessions VALUES(?,?,?,?) ON CONFLICT(channel_id,member,agent_id) DO UPDATE SET conversation_id=excluded.conversation_id", channel, key, agent, conversation); err != nil {
		return "", err
	}
	return conversation, tx.Commit()
}

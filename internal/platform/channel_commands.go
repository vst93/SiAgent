package platform

import (
	"strings"
	"unicode/utf8"
)

// Members can name themselves and their group, because the WeCom long-connection
// protocol returns opaque IDs and has no API for names. The command is handled
// before a run is created: it never reaches the model, and the answer is the
// confirmation notice.
//
//	/user 张三       label the sender
//	/group 产品讨论群   label the group (group chats only)
//
// A repeated command replaces the previous value. The administrator can always
// override the label from the console.

// channelCommand is a member-issued instruction recognised before admission.
type channelCommand struct {
	Name     string
	Argument string
}

// parseChannelCommand reads a command, tolerating one leading @mention so the
// command still works when WeCom keeps the mention in the message body. Only the
// exact commands are recognised, so ordinary text is never mistaken for one.
func parseChannelCommand(text string) (channelCommand, bool) {
	text = strings.TrimSpace(text)
	if mention := strings.IndexByte(text, ' '); mention > 0 && strings.HasPrefix(text, "@") {
		text = strings.TrimSpace(text[mention+1:])
	}
	for _, name := range []string{"new", "user", "group"} {
		prefix := "/" + name
		if text == prefix {
			return channelCommand{Name: name}, true
		}
		if strings.HasPrefix(text, prefix+" ") || strings.HasPrefix(text, prefix+"\t") {
			return channelCommand{Name: name, Argument: strings.TrimSpace(text[len(prefix):])}, true
		}
	}
	return channelCommand{}, false
}

// validChannelLabel bounds a member-supplied name the same way the console does.
func validChannelLabel(name string) bool {
	return name != "" && utf8.ValidString(name) && utf8.RuneCountInString(name) <= 64 && !strings.ContainsAny(name, "\x00\r\n")
}

// applyNamingCommand stores a member-supplied label and returns the notice the
// member receives. An empty label or a group command in a private chat returns
// usage instead of writing anything.
func (s *Server) applyNamingCommand(channelID string, in incomingIdentity, command channelCommand) string {
	switch command.Name {
	case "user":
		if !validChannelLabel(command.Argument) {
			return "用法：/user 你的名字（不超过 64 字，单行）"
		}
		if err := s.saveMemberLabel(channelID, "sender", in.sender, command.Argument, in.sender); err != nil {
			return "名称保存失败，请稍后重试。"
		}
		return "已记录你的名称：" + command.Argument + "。管理员会在对话记录中看到这个名称。"
	case "group":
		if in.kind != "group" || in.group == "" {
			return "用法：/group 群名称（仅群聊可用）"
		}
		if !validChannelLabel(command.Argument) {
			return "用法：/group 群名称（不超过 64 字，单行）"
		}
		if err := s.saveMemberLabel(channelID, "group", in.group, command.Argument, in.sender); err != nil {
			return "群名称保存失败，请稍后重试。"
		}
		return "已记录本群名称：" + command.Argument + "。"
	}
	return ""
}

// incomingIdentity is the parsed sender scope of one inbound message.
type incomingIdentity struct {
	kind   string
	group  string
	sender string
}

// saveMemberLabel upserts a label supplied by a member.
func (s *Server) saveMemberLabel(channelID, kind, target, name, setBy string) error {
	_, err := s.store.db.Exec("INSERT INTO channel_contacts(channel_id,kind,target,name,updated,source,set_by) VALUES(?,?,?,?,strftime('%s','now')*1000,'member',?) ON CONFLICT(channel_id,kind,target) DO UPDATE SET name=excluded.name,updated=excluded.updated,source=excluded.source,set_by=excluded.set_by", channelID, kind, target, name, setBy)
	return err
}

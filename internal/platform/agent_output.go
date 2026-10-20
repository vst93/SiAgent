package platform

import (
	"fmt"
	"strings"
)

func outputExcerpt(text string, limit int) string {
	chars := []rune(text)
	if len(chars) > limit {
		return string(chars[:limit]) + "\n[展示已截断，完整记录可在管理后台查看]"
	}
	return text
}
func (s *Server) channelRunOutput(runID, text, thinking string, snapshot runConfig) (string, error) {
	output := snapshot.Agent.effectivePolicy().Output
	// Inline reasoning must never reach a chat member. Old runs and runtimes that
	// predate the filter are cleaned here as well as in the Runtime.
	if output.ThinkTags != "keep" {
		summary := ""
		if output.Thinking {
			summary = reasoningSummary(thinking, text, output.ThinkingMaxChars)
		}
		text = sanitizeAnswer(text)
		if strings.TrimSpace(text) == "" {
			text = "模型只返回了思考过程，没有产生可发送的回答。请换个问法或检查模型输出配置。"
		}
		if summary != "" {
			text = "思考摘要：\n" + summary + "\n\n回答：\n" + text
		}
	} else if output.Thinking && thinking != "" {
		text = "思考摘要：\n" + outputExcerpt(thinking, output.ThinkingMaxChars) + "\n\n回答：\n" + text
	}
	if output.Tools == "hidden" {
		return text, nil
	}
	rows, err := s.store.db.Query("SELECT name,status,arguments,result FROM tool_calls WHERE run_id=? ORDER BY rowid LIMIT 51", runID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	tools := []string{}
	for rows.Next() {
		var name, status, args, result string
		if err := rows.Scan(&name, &status, &args, &result); err != nil {
			return "", err
		}
		if len(tools) == 50 {
			tools = append(tools, "其余调用请在管理后台查看。")
			break
		}
		labels := map[string]string{"completed": "完成", "running": "执行中", "failed": "失败", "interrupted": "结果不明，请核对"}
		label := labels[status]
		if label == "" {
			label = status
		}
		line := fmt.Sprintf("%s：%s", toolDisplayName(snapshot, name), label)
		if output.Tools == "detailed" {
			if args != "" {
				line += "\n参数：" + outputExcerpt(args, 1000)
			}
			if result != "" {
				line += "\n结果：" + outputExcerpt(result, 2000)
			}
		}
		tools = append(tools, line)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(tools) > 0 {
		text += "\n\n工具调用：\n" + strings.Join(tools, "\n\n")
	}
	return text, nil
}

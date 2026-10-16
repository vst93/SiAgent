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
	if output.Thinking && thinking != "" {
		text = "思考摘要：\n" + outputExcerpt(thinking, 8000) + "\n\n回答：\n" + text
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

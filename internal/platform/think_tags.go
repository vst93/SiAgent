package platform

import (
	"regexp"
	"strings"
)

// Some OpenAI-compatible models inline their reasoning in the answer text
// instead of returning a separate reasoning block, for example
// "<think>...</think>answer". The platform keeps those models usable without
// leaking the trace into a chat reply: the tags are removed from the visible
// answer, and when the Agent shows a reasoning summary the removed text becomes
// that summary. Supported names are the ones seen in practice: think, thinking,
// thought, reasoning and reflection, optionally with attributes and any case.
var thinkTagNames = []string{"thinking", "think", "thought", "reasoning", "reflection"}

var thinkOpenTag = regexp.MustCompile(`(?is)^<\s*(think|thinking|thought|reasoning|reflection)\b[^>]*>`)

var thinkCloseTags = func() map[string]*regexp.Regexp {
	tags := map[string]*regexp.Regexp{}
	for _, name := range thinkTagNames {
		tags[name] = regexp.MustCompile(`(?is)</\s*` + name + `\s*>`)
	}
	return tags
}()

// partialThinkTag reports whether text ends inside a possible reasoning tag, so
// streaming never renders half a tag as visible text. Ordinary text such as
// "a < b" is not held back.
func partialThinkTag(text string) bool {
	if !strings.HasPrefix(text, "<") || strings.Contains(text, ">") {
		return false
	}
	rest := strings.ToLower(strings.TrimLeft(text[1:], " \t"))
	if strings.HasPrefix(rest, "/") {
		rest = strings.TrimLeft(rest[1:], " \t")
	}
	if rest == "" {
		return true
	}
	for _, name := range thinkTagNames {
		if strings.HasPrefix(name, rest) || strings.HasPrefix(rest, name) {
			return true
		}
	}
	return false
}

// splitInlineThinking separates inline reasoning tags from visible answer text.
// The visible result is monotonic while streaming: an unclosed tag hides
// everything after it, and visible text only grows as more text arrives.
func splitInlineThinking(text string) (visible, thought string) {
	var visibleBuilder, thoughtBuilder strings.Builder
	for len(text) > 0 {
		index := strings.IndexByte(text, '<')
		if index < 0 {
			visibleBuilder.WriteString(text)
			break
		}
		visibleBuilder.WriteString(text[:index])
		rest := text[index:]
		open := thinkOpenTag.FindStringSubmatch(rest)
		if open == nil {
			if partialThinkTag(rest) {
				break
			}
			visibleBuilder.WriteByte('<')
			text = rest[1:]
			continue
		}
		name := strings.ToLower(open[1])
		body := rest[len(open[0]):]
		close := thinkCloseTags[name].FindStringIndex(body)
		if close == nil {
			thoughtBuilder.WriteString(body)
			break
		}
		thoughtBuilder.WriteString(body[:close[0]])
		text = body[close[1]:]
	}
	return visibleBuilder.String(), thoughtBuilder.String()
}

// sanitizeAnswer removes inline reasoning from a stored or delivered answer.
func sanitizeAnswer(text string) string {
	visible, _ := splitInlineThinking(text)
	return visible
}

// reasoningSummary returns the reasoning worth returning with a channel reply:
// the model's separate reasoning text plus any reasoning extracted from inline
// tags, bounded by the Agent's configured character limit.
func reasoningSummary(thinking, answer string, limit int) string {
	_, inline := splitInlineThinking(answer)
	parts := []string{}
	if strings.TrimSpace(thinking) != "" {
		parts = append(parts, thinking)
	}
	if strings.TrimSpace(inline) != "" {
		parts = append(parts, inline)
	}
	summary := strings.TrimSpace(strings.Join(parts, "\n\n"))
	if limit <= 0 || len([]rune(summary)) <= limit {
		return summary
	}
	return string([]rune(summary)[:limit]) + "\n[思考摘要已截断，完整内容可在管理后台查看]"
}

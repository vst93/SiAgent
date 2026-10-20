package platform

import (
	"strings"
	"testing"
)

func TestSplitInlineThinkingKeepsVisibleTextMonotonic(t *testing.T) {
	full := "前言<THINK type=\"x\">隐藏推理</THINK>可见回答"
	var lastVisible string
	for size := 0; size <= len(full); size++ {
		visible, _ := splitInlineThinking(full[:size])
		if !strings.HasPrefix(visible, lastVisible) {
			t.Fatalf("visible text is not monotonic at %d: %q then %q", size, lastVisible, visible)
		}
		lastVisible = visible
	}
	if visible, thought := splitInlineThinking(full); visible != "前言可见回答" || thought != "隐藏推理" {
		t.Fatalf("unexpected split: %q %q", visible, thought)
	}
	if visible, thought := splitInlineThinking("a < b and c > d"); visible != "a < b and c > d" || thought != "" {
		t.Fatalf("ordinary text was altered: %q %q", visible, thought)
	}
	if visible, thought := splitInlineThinking("answer<thinking>unclosed"); visible != "answer" || thought != "unclosed" {
		t.Fatalf("unclosed tag not hidden: %q %q", visible, thought)
	}
	if visible := sanitizeAnswer("<reasoning>r</reasoning>done"); visible != "done" {
		t.Fatalf("reasoning tag not removed: %q", visible)
	}
	if got := reasoningSummary("native", "<think>inline</think>answer", 4); got != "nati\n[思考摘要已截断，完整内容可在管理后台查看]" {
		t.Fatalf("summary limit not applied: %q", got)
	}
}

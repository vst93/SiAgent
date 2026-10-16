package channels

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestMessageSplitUTF8BoundedAndTruncationExplicit(t *testing.T) {
	text := strings.Repeat("中文🙂", 2000)
	parts := SplitText(text, 1800)
	if strings.Join(parts, "") != text {
		t.Fatal("split lost text")
	}
	for _, part := range parts {
		if len(part) > 1800 || !utf8.ValidString(part) {
			t.Fatal("invalid split")
		}
	}
	huge := SplitText(strings.Repeat("x", 200000), 1800)
	if len(huge) != 64 || !strings.Contains(huge[63], "剩余内容未发送") {
		t.Fatal("unbounded or silent truncation")
	}
}

package knowledge

import (
	"context"
	"os"
	"strings"
)

// ExtractText returns plain text for a chat attachment. It reuses the same
// local parsers as knowledge documents and never calls a model.
func ExtractText(ctx context.Context, path, kind string) (string, error) {
	if kind == "docx" {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		_, text, err := blocksFromDOCX(data)
		return text, err
	}
	pages, err := parse(ctx, path, kind, DefaultLimits())
	if err != nil {
		return "", err
	}
	parts := make([]string, 0, len(pages))
	for _, page := range pages {
		parts = append(parts, page.Text)
	}
	return strings.Join(parts, "\n\n"), nil
}

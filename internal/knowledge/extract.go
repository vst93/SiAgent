package knowledge

import (
	"bytes"
	"context"
	"os"
	"strings"
)

// ExtractText returns plain text for a chat attachment. It reuses the same
// local parsers as knowledge documents and never calls a model.
func ExtractText(ctx context.Context, path, kind string) (string, error) {
	switch kind {
	case "txt", "md", "markdown", "csv", "json", "pdf", "docx", "pptx", "xlsx", "doc", "ppt", "xls":
	default:
		return "", invalid("不支持此附件格式，请提供普通文本或 DOC/DOCX/PPT/PPTX/XLS/XLSX/PDF 文件。")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if bytes.HasPrefix(data, []byte("MZ")) || bytes.HasPrefix(data, []byte{0x7f, 'E', 'L', 'F'}) {
		return "", invalid("不接收可执行文件，文件内容与文档格式不符。")
	}

	if kind == "doc" || kind == "ppt" || kind == "xls" {
		return extractLegacyOffice(ctx, data, kind)
	}
	if kind == "docx" || kind == "pptx" || kind == "xlsx" {
		files, err := readOffice(data)
		if err != nil {
			return "", err
		}
		if kind != "docx" {
			return extractOffice(ctx, files, kind)
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

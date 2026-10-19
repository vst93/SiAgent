package knowledge

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// block is one structural unit of an extracted document. Chunking works on
// blocks instead of raw text so headings, clauses, steps and tables survive.
type block struct {
	Type        string // heading, paragraph, list, clause, code, table
	Level       int
	Text        string
	Page        int
	Paragraph   int
	SectionPath []string
	Clause      string
	Start       int
	End         int
}

// ChunkStructureSchema is additive and keeps existing rows readable.
const ChunkStructureSchema = `
 ALTER TABLE knowledge_chunks ADD COLUMN parent_id TEXT NOT NULL DEFAULT '';
 ALTER TABLE knowledge_chunks ADD COLUMN block_type TEXT NOT NULL DEFAULT 'paragraph';
 ALTER TABLE knowledge_chunks ADD COLUMN section_path TEXT NOT NULL DEFAULT '';
 ALTER TABLE knowledge_chunks ADD COLUMN clause TEXT NOT NULL DEFAULT '';
 ALTER TABLE knowledge_chunks ADD COLUMN content_hash TEXT NOT NULL DEFAULT '';
`

const (
	maxChunkRunes     = 600
	chunkOverlapRunes = 80
)

const (
	blockParagraph = "paragraph"
	blockHeading   = "heading"
	blockList      = "list"
	blockClause    = "clause"
	blockCode      = "code"
	blockTable     = "table"
)

var (
	markdownHeading  = regexp.MustCompile(`^(#{1,6})[ \t]+(.+?)[ \t]*#*[ \t]*$`)
	markdownTableSep = regexp.MustCompile(`^\s*\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)+\|?\s*$`)
	markdownList     = regexp.MustCompile(`^\s*(?:[-*+]|\d+[.)])[ \t]+\S`)
	clauseLine       = regexp.MustCompile(`^第[一二三四五六七八九十百千零〇0-9]+[条章节款项]`)
	chapterLine      = regexp.MustCompile(`^第[一二三四五六七八九十百千零〇0-9]+[章节篇]`)
	numericClause    = regexp.MustCompile(`^(\d+(?:\.\d+)*)[.、]?\s+\S`)
	chineseListItem  = regexp.MustCompile(`^[一二三四五六七八九十]+、\s*\S`)
	fencedCode       = regexp.MustCompile("^\\s*(```|~~~)")
)

// assignSections walks blocks in order and fills SectionPath from headings.
func assignSections(blocks []block) {
	type frame struct {
		level int
		text  string
	}
	stack := []frame{}
	for index := range blocks {
		b := &blocks[index]
		if b.Type == blockHeading {
			for len(stack) > 0 && stack[len(stack)-1].level >= b.Level {
				stack = stack[:len(stack)-1]
			}
			stack = append(stack, frame{b.Level, strings.TrimSpace(b.Text)})
			path := make([]string, 0, len(stack))
			for _, item := range stack {
				path = append(path, item.text)
			}
			b.SectionPath = path
			continue
		}
		path := make([]string, 0, len(stack))
		for _, item := range stack {
			path = append(path, item.text)
		}
		b.SectionPath = path
	}
}

// blocksFromMarkdown understands headings, fenced code, tables, lists and
// paragraphs. Markdown is never rendered; only structure is extracted.
func blocksFromMarkdown(text string, pageNumber int) []block {
	lines := strings.Split(text, "\n")
	blocks := []block{}
	paragraph := 0
	var buffer []string
	var code []string
	bufferIsList := false
	inCode := false
	flush := func() {
		joined := strings.TrimSpace(strings.Join(buffer, "\n"))
		buffer = nil
		kind := blockParagraph
		if bufferIsList {
			kind = blockList
		}
		bufferIsList = false
		if joined == "" {
			return
		}
		paragraph++
		blocks = append(blocks, makeBlock(kind, joined, pageNumber, paragraph))
	}
	for index := 0; index < len(lines); index++ {
		line := lines[index]
		if fencedCode.MatchString(line) {
			if inCode {
				paragraph++
				blocks = append(blocks, makeBlock(blockCode, strings.Join(code, "\n"), pageNumber, paragraph))
				code = nil
				inCode = false
			} else {
				flush()
				inCode = true
			}
			continue
		}
		if inCode {
			code = append(code, line)
			continue
		}
		if match := markdownHeading.FindStringSubmatch(line); match != nil {
			flush()
			paragraph++
			b := makeBlock(blockHeading, strings.TrimSpace(match[2]), pageNumber, paragraph)
			b.Level = len(match[1])
			blocks = append(blocks, b)
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(line), "|") && index+1 < len(lines) && markdownTableSep.MatchString(lines[index+1]) {
			flush()
			rows := []string{}
			header := strings.TrimSpace(line)
			rows = append(rows, header)
			index += 2
			for ; index < len(lines); index++ {
				row := strings.TrimSpace(lines[index])
				if !strings.HasPrefix(row, "|") {
					index--
					break
				}
				rows = append(rows, row)
			}
			paragraph++
			blocks = append(blocks, makeBlock(blockTable, strings.Join(rows, "\n"), pageNumber, paragraph))
			continue
		}
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		if markdownList.MatchString(line) {
			if len(buffer) > 0 && !bufferIsList {
				flush()
			}
			bufferIsList = true
			buffer = append(buffer, strings.TrimSpace(line))
			continue
		}
		if bufferIsList {
			flush()
		}
		buffer = append(buffer, line)
	}
	if inCode && len(code) > 0 {
		paragraph++
		blocks = append(blocks, makeBlock(blockCode, strings.Join(code, "\n"), pageNumber, paragraph))
	}
	flush()
	return blocks
}

// blocksFromPlainText handles TXT and PDF page text. It recognises chapter and
// clause numbering plus list markers, and keeps layout tables when several
// consecutive lines share the same column count.
func blocksFromPlainText(text string, pageNumber int, startParagraph int) []block {
	raw := paragraphBreak.Split(text, -1)
	blocks := []block{}
	paragraph := startParagraph
	for index := 0; index < len(raw); index++ {
		value := strings.TrimSpace(raw[index])
		if value == "" {
			continue
		}
		paragraph++
		lines := strings.Split(value, "\n")
		if table, width, used := layoutTable(lines); width >= 2 && used >= 3 {
			blocks = append(blocks, makeBlock(blockTable, strings.Join(table, "\n"), pageNumber, paragraph))
			continue
		}
		if len(lines) == 1 {
			if level, ok := headingLevel(value); ok {
				b := makeBlock(blockHeading, value, pageNumber, paragraph)
				b.Level = level
				blocks = append(blocks, b)
				continue
			}
			if clauseLine.MatchString(value) {
				b := makeBlock(blockClause, value, pageNumber, paragraph)
				b.Clause = clauseLabel(value)
				blocks = append(blocks, b)
				continue
			}
			if markdownList.MatchString(value) || chineseListItem.MatchString(value) {
				blocks = append(blocks, makeBlock(blockList, value, pageNumber, paragraph))
				continue
			}
		}
		blocks = append(blocks, makeBlock(blockParagraph, value, pageNumber, paragraph))
	}
	return blocks
}

// layoutTable detects a run of lines whose columns are separated by two or more
// spaces, which is how pdftotext -layout renders simple tables.
func layoutTable(lines []string) ([]string, int, int) {
	rows := []string{}
	width := 0
	for _, line := range lines {
		columns := splitColumns(line)
		if len(columns) < 2 {
			return nil, 0, 0
		}
		if width == 0 {
			width = len(columns)
		} else if len(columns) != width {
			return nil, 0, 0
		}
		rows = append(rows, strings.Join(columns, " | "))
	}
	if width < 2 {
		return nil, 0, 0
	}
	return rows, width, len(rows)
}

func splitColumns(line string) []string {
	fields := regexp.MustCompile(`\s{2,}`).Split(strings.TrimSpace(line), -1)
	columns := []string{}
	for _, field := range fields {
		if field = strings.TrimSpace(field); field != "" {
			columns = append(columns, field)
		}
	}
	return columns
}

func headingLevel(line string) (int, bool) {
	trimmed := strings.TrimSpace(line)
	if utf8.RuneCountInString(trimmed) > 40 {
		return 0, false
	}
	if chapterLine.MatchString(trimmed) {
		return 2, true
	}
	if chineseListItem.MatchString(trimmed) {
		return 3, true
	}
	if match := numericClause.FindStringSubmatch(trimmed); match != nil && !strings.HasSuffix(trimmed, "。") {
		return min(6, 2+strings.Count(match[1], ".")), true
	}
	return 0, false
}

func clauseLabel(line string) string {
	trimmed := strings.TrimSpace(line)
	if index := strings.IndexAny(trimmed, " \t"); index > 0 {
		return trimmed[:index]
	}
	if utf8.RuneCountInString(trimmed) > 24 {
		return string([]rune(trimmed)[:24])
	}
	return trimmed
}

func makeBlock(kind, text string, page, paragraph int) block {
	runes := []rune(text)
	return block{Type: kind, Text: text, Page: page, Paragraph: paragraph, Start: 0, End: len(runes)}
}

// blocksFromDOCX reads word/document.xml directly. Headings come from paragraph
// styles, lists from numbering properties and tables from w:tbl.
func blocksFromDOCX(data []byte) ([]block, string, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, "", invalid("DOCX 文件损坏，无法读取。")
	}
	var document []byte
	for _, file := range reader.File {
		if file.Name != "word/document.xml" {
			continue
		}
		if file.UncompressedSize64 > 64<<20 {
			return nil, "", invalid("DOCX 正文超过解析上限，请拆分文档。")
		}
		handle, err := file.Open()
		if err != nil {
			return nil, "", invalid("DOCX 文件损坏，无法读取正文。")
		}
		document, err = io.ReadAll(io.LimitReader(handle, 64<<20))
		handle.Close()
		if err != nil {
			return nil, "", invalid("DOCX 正文无法读取。")
		}
		break
	}
	if len(document) == 0 {
		return nil, "", invalid("DOCX 缺少正文，无法提取文本。")
	}
	blocks, text, err := decodeDOCX(document)
	if err != nil {
		return nil, "", err
	}
	return blocks, text, nil
}

func decodeDOCX(data []byte) ([]block, string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	blocks := []block{}
	var text strings.Builder
	paragraph := 0
	inParagraph := false
	inTable := 0
	paragraphText := strings.Builder{}
	style := ""
	listItem := false
	rows := []string{}
	row := []string{}
	cell := strings.Builder{}
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, "", invalid("DOCX 正文格式无效，无法解析。")
		}
		switch value := token.(type) {
		case xml.StartElement:
			switch value.Name.Local {
			case "p":
				inParagraph = true
				paragraphText.Reset()
				style = ""
				listItem = false
			case "pStyle":
				for _, attr := range value.Attr {
					if attr.Name.Local == "val" {
						style = attr.Value
					}
				}
			case "numPr":
				listItem = true
			case "tbl":
				inTable++
				rows = nil
			case "tr":
				row = nil
			case "tc":
				cell.Reset()
			case "t":
				var content string
				if err := decoder.DecodeElement(&content, &value); err != nil {
					return nil, "", invalid("DOCX 文本节点无法解析。")
				}
				paragraphText.WriteString(content)
				cell.WriteString(content)
			case "tab":
				paragraphText.WriteString(" ")
			case "br":
				paragraphText.WriteString("\n")
			}
		case xml.EndElement:
			switch value.Name.Local {
			case "p":
				if !inParagraph {
					continue
				}
				inParagraph = false
				content := strings.TrimSpace(paragraphText.String())
				if content == "" {
					continue
				}
				if inTable > 0 {
					text.WriteString(content)
					text.WriteString(" ")
					continue
				}
				paragraph++
				if level, ok := docxHeadingLevel(style); ok {
					b := makeBlock(blockHeading, content, 1, paragraph)
					b.Level = level
					blocks = append(blocks, b)
				} else if listItem {
					blocks = append(blocks, makeBlock(blockList, content, 1, paragraph))
				} else if clauseLine.MatchString(content) {
					b := makeBlock(blockClause, content, 1, paragraph)
					b.Clause = clauseLabel(content)
					blocks = append(blocks, b)
				} else {
					blocks = append(blocks, makeBlock(blockParagraph, content, 1, paragraph))
				}
				text.WriteString(content)
				text.WriteString("\n\n")
			case "tc":
				row = append(row, strings.TrimSpace(cell.String()))
			case "tr":
				if len(row) > 0 {
					rows = append(rows, strings.Join(row, " | "))
				}
				row = nil
			case "tbl":
				inTable--
				if inTable < 0 {
					inTable = 0
				}
				if len(rows) > 0 {
					paragraph++
					blocks = append(blocks, makeBlock(blockTable, strings.Join(rows, "\n"), 1, paragraph))
					for _, item := range rows {
						text.WriteString(item)
						text.WriteString("\n")
					}
					text.WriteString("\n")
				}
				rows = nil
			}
		}
	}
	if len(blocks) == 0 {
		return nil, "", invalid("DOCX 没有可检索文本。")
	}
	return blocks, text.String(), nil
}

func docxHeadingLevel(style string) (int, bool) {
	lower := strings.ToLower(style)
	for _, prefix := range []string{"heading", "标题"} {
		if !strings.HasPrefix(lower, prefix) {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(lower, prefix))
		if number, err := strconv.Atoi(rest); err == nil && number >= 1 && number <= 6 {
			return number, true
		}
	}
	if lower == "title" {
		return 1, true
	}
	return 0, false
}

// chunkBlocks turns structured blocks into indexable chunks. A chunk never
// crosses a heading boundary; tables stay whole; long sections split into
// children that keep a pointer to their section's first chunk.
func chunkBlocks(documentID string, version int, blocks []block, limits Limits) ([]chunk, int, error) {
	chunks := []chunk{}
	characters := 0
	for _, b := range blocks {
		characters += utf8.RuneCountInString(b.Text)
		if characters > limits.MaxCharacters {
			return nil, 0, invalid(fmt.Sprintf("提取文本超过 %d 字符上限。", limits.MaxCharacters))
		}
	}
	pending := []block{}
	sectionAnchor := ""
	flush := func() error {
		if len(pending) == 0 {
			return nil
		}
		joined := strings.Join(blockTexts(pending), "\n")
		location := Location{Page: pending[0].Page, Paragraph: pending[0].Paragraph}
		section := strings.Join(pending[0].SectionPath, " / ")
		clause := pending[0].Clause
		kind := pending[0].Type
		runes := []rune(joined)
		parts := []string{joined}
		if len(runes) > maxChunkRunes {
			parts = splitRunes(runes)
		}
		for index, part := range parts {
			if len(chunks) >= limits.MaxChunks {
				return invalid(fmt.Sprintf("文档超过 %d 个片段，请合并过短段落或拆分文档后重试。", limits.MaxChunks))
			}
			position := len(chunks)
			parent := ""
			if len(parts) > 1 {
				if sectionAnchor == "" {
					sectionAnchor = sourceID(documentID, version, position)
				}
				if index > 0 {
					parent = sectionAnchor
				}
			}
			chunks = append(chunks, chunk{
				ID:          sourceID(documentID, version, position),
				Text:        part,
				Location:    location,
				Position:    position,
				ParentID:    parent,
				BlockType:   kind,
				SectionPath: section,
				Clause:      clause,
				ContentHash: hashText(part),
			})
		}
		pending = nil
		sectionAnchor = ""
		return nil
	}
	for _, b := range blocks {
		// A page boundary always closes the current chunk so page numbers stay
		// accurate for source citations.
		if len(pending) > 0 && b.Page != 0 && pending[0].Page != 0 && b.Page != pending[0].Page {
			if err := flush(); err != nil {
				return nil, 0, err
			}
		}
		if b.Type == blockHeading || b.Type == blockTable {
			if err := flush(); err != nil {
				return nil, 0, err
			}
			if b.Type == blockTable {
				pending = []block{b}
				if err := flush(); err != nil {
					return nil, 0, err
				}
			}
			continue
		}
		pending = append(pending, b)
		if runeLength(pending) >= maxChunkRunes {
			if err := flush(); err != nil {
				return nil, 0, err
			}
		}
	}
	if err := flush(); err != nil {
		return nil, 0, err
	}
	if len(chunks) == 0 {
		return nil, 0, invalid("文档没有可检索文本。")
	}
	return chunks, characters, nil
}

func blockTexts(blocks []block) []string {
	values := make([]string, 0, len(blocks))
	for _, b := range blocks {
		if strings.TrimSpace(b.Text) != "" {
			values = append(values, b.Text)
		}
	}
	return values
}

func runeLength(blocks []block) int {
	total := 0
	for _, b := range blocks {
		total += utf8.RuneCountInString(b.Text) + 1
	}
	return total
}

func hashText(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:8])
}

// splitRunes keeps the historical sentence-aware windowing for sections that
// exceed one chunk.
func splitRunes(runes []rune) []string {
	parts := []string{}
	for start := 0; start < len(runes); {
		end := chunkEnd(runes, start)
		parts = append(parts, string(runes[start:end]))
		if end == len(runes) {
			break
		}
		start = end - chunkOverlapRunes
		if start < 0 {
			start = 0
		}
	}
	return parts
}

// parseDocument returns both the pages used for source reading and the blocks
// used for indexing.
func (c *Core) parseDocument(ctx context.Context, path, kind string) ([]page, []block, error) {
	if kind != "txt" && kind != "markdown" && kind != "pdf" && kind != "docx" {
		if c.external == nil {
			return nil, nil, invalid("该格式需要安装并启用解析扩展。")
		}
		text, err := c.external(ctx, kind, path)
		if err != nil {
			return nil, nil, invalid("解析扩展未能处理该文件，请检查扩展状态后重试。")
		}
		if utf8.RuneCountInString(text) > c.limits.MaxCharacters {
			return nil, nil, invalid(fmt.Sprintf("提取文本超过 %d 字符上限，请拆分文档。", c.limits.MaxCharacters))
		}
		blocks := blocksFromPlainText(text, 1, 0)
		assignSections(blocks)
		return []page{{Text: text}}, blocks, nil
	}
	if kind == "docx" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, nil, err
		}
		blocks, text, err := blocksFromDOCX(data)
		if err != nil {
			return nil, nil, err
		}
		assignSections(blocks)
		return []page{{Text: text}}, blocks, nil
	}
	pages, err := parse(ctx, path, kind, c.limits)
	if err != nil {
		return nil, nil, err
	}
	blocks := []block{}
	paragraph := 0
	for _, item := range pages {
		var values []block
		if kind == "markdown" {
			values = blocksFromMarkdown(item.Text, item.Number)
		} else {
			values = blocksFromPlainText(item.Text, item.Number, paragraph)
		}
		if len(values) > 0 {
			paragraph = values[len(values)-1].Paragraph
		}
		blocks = append(blocks, values...)
	}
	assignSections(blocks)
	return pages, blocks, nil
}

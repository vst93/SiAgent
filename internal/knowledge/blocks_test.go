package knowledge

import (
	"archive/zip"
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestMarkdownBlocksKeepHeadingHierarchyAndTables(t *testing.T) {
	text := "# 差旅制度\n\n## 住宿标准\n\n住宿费每晚不超过 500 元。\n\n| 职级 | 标准 |\n| --- | --- |\n| P5 | 400 |\n| P6 | 500 |\n\n## 报销流程\n\n1. 提交发票\n2. 财务审核\n"
	blocks := blocksFromMarkdown(text, 1)
	assignSections(blocks)
	kinds := []string{}
	for _, b := range blocks {
		kinds = append(kinds, b.Type)
	}
	joined := strings.Join(kinds, ",")
	if !strings.Contains(joined, blockHeading) || !strings.Contains(joined, blockTable) || !strings.Contains(joined, blockList) {
		t.Fatalf("markdown structure lost: %s", joined)
	}
	table := ""
	for _, b := range blocks {
		if b.Type == blockTable {
			table = b.Text
		}
	}
	if !strings.Contains(table, "P5 | 400") || !strings.Contains(table, "P6 | 500") {
		t.Fatalf("table rows lost: %q", table)
	}
	chunks, _, err := chunkBlocks("doc", 1, blocks, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) < 3 {
		t.Fatalf("expected heading-scoped chunks, got %d", len(chunks))
	}
	found := false
	for _, c := range chunks {
		if strings.Contains(c.Text, "500 元") && strings.Contains(c.SectionPath, "住宿标准") {
			found = true
		}
	}
	if !found {
		t.Fatal("chunk did not carry its section path")
	}
}

func TestPlainTextDetectsClausesAndHeadings(t *testing.T) {
	text := "第三章 差旅管理\n\n第十二条 住宿标准如下。\n\n一、市内交通\n\n1.1 打车需附行程单。"
	blocks := blocksFromPlainText(text, 2, 0)
	assignSections(blocks)
	if blocks[0].Type != blockHeading || blocks[0].Level != 2 {
		t.Fatalf("chapter heading not detected: %+v", blocks[0])
	}
	if blocks[1].Type != blockClause || blocks[1].Clause == "" {
		t.Fatalf("clause not detected: %+v", blocks[1])
	}
	if blocks[1].Page != 2 {
		t.Fatal("page number lost")
	}
}

func TestDOCXExtractsHeadingsTablesAndLists(t *testing.T) {
	data := buildDOCX(t, `<w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>差旅制度</w:t></w:r></w:p>
<w:p><w:pPr><w:pStyle w:val="Heading2"/></w:pPr><w:r><w:t>住宿标准</w:t></w:r></w:p>
<w:p><w:r><w:t>住宿费每晚不超过 500 元。</w:t></w:r></w:p>
<w:tbl><w:tr><w:tc><w:p><w:r><w:t>职级</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>标准</w:t></w:r></w:p></w:tc></w:tr>
<w:tr><w:tc><w:p><w:r><w:t>P5</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>400</w:t></w:r></w:p></w:tc></w:tr></w:tbl>
<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/></w:numPr></w:pPr><w:r><w:t>提交发票</w:t></w:r></w:p>`)
	blocks, text, err := blocksFromDOCX(data)
	if err != nil {
		t.Fatal(err)
	}
	assignSections(blocks)
	types := map[string]int{}
	for _, b := range blocks {
		types[b.Type]++
	}
	if types[blockHeading] != 2 || types[blockTable] != 1 || types[blockList] != 1 {
		t.Fatalf("docx structure lost: %+v", types)
	}
	if !strings.Contains(text, "住宿费每晚不超过 500 元") {
		t.Fatal("docx text extraction lost content")
	}
	for _, b := range blocks {
		if b.Type == blockTable && !strings.Contains(b.Text, "P5 | 400") {
			t.Fatalf("docx table rows lost: %q", b.Text)
		}
	}
	chunks, _, err := chunkBlocks("doc", 1, blocks, DefaultLimits())
	if err != nil || len(chunks) < 3 {
		t.Fatalf("docx chunking failed: %d %v", len(chunks), err)
	}
	if chunks[1].SectionPath == "" {
		t.Fatal("docx chunk missing section path")
	}
}

func TestDOCXRejectsInvalidArchive(t *testing.T) {
	if _, _, err := blocksFromDOCX([]byte("not a zip")); err == nil {
		t.Fatal("expected invalid docx to fail")
	}
}

func TestLongSectionSplitsIntoChildrenWithParent(t *testing.T) {
	long := strings.Repeat("这是制度条款的详细说明。", 120)
	blocks := blocksFromMarkdown("# 制度\n\n"+long, 1)
	assignSections(blocks)
	chunks, _, err := chunkBlocks("doc", 1, blocks, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) < 2 {
		t.Fatalf("expected split chunks, got %d", len(chunks))
	}
	if chunks[0].ParentID != "" {
		t.Fatal("first chunk should be the section anchor")
	}
	for _, c := range chunks[1:] {
		if c.ParentID != chunks[0].ID {
			t.Fatalf("child chunk missing parent anchor: %+v", c)
		}
		if c.ContentHash == "" {
			t.Fatal("chunk missing content hash")
		}
	}
}

func TestChunksCarryStructuralMetadataIntoIndex(t *testing.T) {
	c := fixture(t)
	b := base(t, c, "structure")
	doc := upload(t, c, b.ID, "", "policy.md", "# 差旅制度\n\n## 住宿标准\n\n住宿费每晚不超过 500 元。\n")
	process(t, c)
	var section, kind string
	if err := c.db.QueryRow("SELECT section_path,block_type FROM knowledge_chunks WHERE document_id=? LIMIT 1", doc.ID).Scan(&section, &kind); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(section, "住宿标准") || kind == "" {
		t.Fatalf("structural metadata not stored: %q %q", section, kind)
	}
	// The heading is searchable even though it lives outside the body chunk.
	got := search(t, c, []string{b.ID}, "住宿标准")
	if len(got.Hits) == 0 {
		t.Fatal("section heading not indexed")
	}
}

func buildDOCX(t *testing.T, body string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	file, err := writer.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	header := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`
	if _, err := file.Write([]byte(header + body + `</w:body></w:document>`)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestUploadAcceptsDOCXAndIndexes(t *testing.T) {
	c := fixture(t)
	b := base(t, c, "docx")
	data := buildDOCX(t, `<w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>发票流程</w:t></w:r></w:p><w:p><w:r><w:t>七个工作日内办理发票。</w:t></w:r></w:p>`)
	doc, err := c.Upload(context.Background(), b.ID, "", "invoice.docx", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	process(t, c)
	state, _ := c.Document(context.Background(), doc.ID)
	if state.State != "ready" {
		t.Fatalf("docx did not index: %+v", state)
	}
	got := search(t, c, []string{b.ID}, "发票")
	if len(got.Hits) == 0 {
		t.Fatal("docx content not searchable")
	}
	if _, err := c.Upload(context.Background(), b.ID, "", "fake.docx", strings.NewReader("not a zip")); err == nil {
		t.Fatal("invalid docx accepted")
	}
}

func TestTableQueryReturnsHeaderAndMatchingRow(t *testing.T) {
	c := fixture(t)
	b := base(t, c, "table rows")
	upload(t, c, b.ID, "", "levels.md", "# 住宿标准\n\n| 职级 | 标准 |\n| --- | --- |\n| P5 | 400 |\n| P6 | 500 |\n| P7 | 600 |\n")
	process(t, c)
	got := search(t, c, []string{b.ID}, "P6 住宿标准是多少")
	if len(got.Hits) == 0 {
		t.Fatal("table query returned nothing")
	}
	text := got.Hits[0].Text
	if !strings.Contains(text, "职级 | 标准") || !strings.Contains(text, "P6 | 500") {
		t.Fatalf("table hit lost header or matching row: %q", text)
	}
	if strings.Contains(text, "P7 | 600") {
		t.Fatalf("unrelated row should be dropped: %q", text)
	}
}

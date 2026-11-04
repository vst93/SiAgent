package knowledge

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func officeArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for name, text := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Write([]byte(text)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func extractOfficeFixture(t *testing.T, kind string, files map[string]string) (string, error) {
	t.Helper()
	name := filepath.Join(t.TempDir(), "test."+kind)
	if err := os.WriteFile(name, officeArchive(t, files), 0600); err != nil {
		t.Fatal(err)
	}
	return ExtractText(context.Background(), name, kind)
}
func TestPPTXUsesPresentationOrderAndParagraphs(t *testing.T) {
	text, err := extractOfficeFixture(t, "pptx", map[string]string{
		"ppt/presentation.xml":            `<p:presentation xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><p:sldIdLst><p:sldId id="256" r:id="second"/><p:sldId id="257" r:id="first"/></p:sldIdLst></p:presentation>`,
		"ppt/_rels/presentation.xml.rels": `<Relationships><Relationship Id="first" Target="slides/slide1.xml"/><Relationship Id="second" Target="slides/slide2.xml"/></Relationships>`,
		"ppt/slides/slide1.xml":           `<slide><p><r><t>后展示</t></r></p></slide>`,
		"ppt/slides/slide2.xml":           `<slide><p><r><t>季度</t></r><r><t>总结</t></r></p><p><r><t>预算 500 万</t></r></p></slide>`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if text != "[幻灯片 1]\n季度总结\n预算 500 万\n\n[幻灯片 2]\n后展示\n\n" {
		t.Fatalf("lost order or text: %q", text)
	}
}
func TestXLSXPreservesSheetsCellsAndCachedValues(t *testing.T) {
	files := map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="预算" r:id="r1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="r1" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/sharedStrings.xml":       `<sst><si><r><t>销售</t></r><r><t>收入</t></r></si></sst>`,
		"xl/worksheets/sheet1.xml":   `<worksheet><sheetData><row r="1"><c r="A1" t="s"><v>0</v></c><c r="C1"><v>500</v></c><c r="D1"><f>C1*2</f><v>1000</v></c><c r="E1" t="inlineStr"><is><t>备注</t></is></c><c r="F1"><f>WEBSERVICE("http://must-not-fetch.invalid")</f></c></row></sheetData></worksheet>`,
	}
	text, err := extractOfficeFixture(t, "xlsx", files)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"[工作表 预算]", "A1=销售收入", "C1=500", "D1=1000", "E1=备注", "F1=[公式无缓存值"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("missing %s: %s", expected, text)
		}
	}
	if strings.Contains(text, "must-not-fetch") {
		t.Fatal("formula leaked as a value")
	}
	files["xl/worksheets/sheet1.xml"] = `<worksheet><sheetData><row><c r="A1" t="s"><v>99</v></c></row></sheetData></worksheet>`
	if _, err := extractOfficeFixture(t, "xlsx", files); err == nil {
		t.Fatal("bad shared string accepted")
	}
}
func TestOfficeRejectsMacrosEmbeddedObjectsAndDisguisedExecutables(t *testing.T) {
	for _, name := range []string{"word/vbaProject.bin", "ppt/embeddings/object.bin", "xl/activeX/activeX1.xml", "hidden.exe", "script.js"} {
		t.Run(name, func(t *testing.T) {
			if _, err := extractOfficeFixture(t, "docx", map[string]string{name: "payload", "word/document.xml": `<document><p><t>正文</t></p></document>`}); err == nil {
				t.Fatal("unsafe package accepted")
			}
		})
	}
	if _, err := extractOfficeFixture(t, "xlsx", map[string]string{"[Content_Types].xml": `<Types>application/vnd.ms-excel.sheet.macroEnabled.main+xml</Types>`}); err == nil {
		t.Fatal("macro content type accepted")
	}
	for _, kind := range []string{"txt", "docx", "pptx", "xlsx"} {
		name := filepath.Join(t.TempDir(), "bad."+kind)
		os.WriteFile(name, []byte("MZ executable"), 0600)
		if _, err := ExtractText(context.Background(), name, kind); err == nil {
			t.Fatal("disguised executable accepted")
		}
	}
}

func TestOfficeRejectsOversizedExpandedPart(t *testing.T) {
	if _, err := extractOfficeFixture(t, "pptx", map[string]string{"ppt/slides/slide1.xml": strings.Repeat("x", (16<<20)+1)}); err == nil {
		t.Fatal("oversized expansion accepted")
	}
}

func TestOfficeDOCXRemainsReadable(t *testing.T) {
	text, err := extractOfficeFixture(t, "docx", map[string]string{
		"word/document.xml": `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>项目计划：下月交付。</w:t></w:r></w:p></w:body></w:document>`,
	})
	if err != nil || !strings.Contains(text, "下月交付") {
		t.Fatalf("DOCX failed: %q %v", text, err)
	}
}

package knowledge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestLegacyOfficeRealDocuments(t *testing.T) {
	for _, item := range []struct{ name, kind, expected string }{
		{"simple.doc", "doc", "This is a simple file"},
		{"simple.xls", "xls", "A1=replaceMe"},
		{"multicell.xls", "xls", "[工作表"},
		{"formulas.xls", "xls", "[工作表"},
		{"basic.ppt", "ppt", "[幻灯片"},
	} {
		t.Run(item.name, func(t *testing.T) {
			text, err := ExtractText(context.Background(), filepath.Join("testdata", "legacy", item.name), item.kind)
			if err != nil {
				t.Fatal(err)
			}

			if !strings.Contains(text, item.expected) {
				t.Fatalf("missing text %q", item.expected)
			}
		})
	}
}

func TestLegacyOfficeRejectsRealMacros(t *testing.T) {
	for _, kind := range []string{"doc", "xls"} {
		_, err := ExtractText(context.Background(), filepath.Join("testdata", "legacy", "macro."+kind), kind)
		if err == nil || !strings.Contains(err.Error(), "宏") {
			t.Fatalf("macro %s not rejected: %v", kind, err)
		}
	}
}

func legacyUnicode(text string) []byte {
	units := utf16.Encode([]rune(text))
	data := make([]byte, len(units)*2)
	for i, u := range units {
		little.PutUint16(data[i*2:], u)
	}
	return data
}
func wordStreams(text string) map[string][]byte {
	raw := legacyUnicode(text)
	doc := make([]byte, 512+len(raw))
	copy(doc[512:], raw)
	little.PutUint16(doc, 0xa5ec)
	little.PutUint16(doc[2:], 0xc1)
	little.PutUint16(doc[10:], 0x200)
	little.PutUint32(doc[76:], uint32(len(raw)/2))
	little.PutUint32(doc[422:], 21)
	table := make([]byte, 21)
	table[0] = 2
	little.PutUint32(table[1:], 16)
	little.PutUint32(table[9:], uint32(len(raw)/2))
	little.PutUint32(table[15:], 512)
	return map[string][]byte{"WordDocument": doc, "1Table": table}
}
func TestLegacyWordChineseFieldsAndEncryption(t *testing.T) {
	streams := wordStreams("季度计划\r预算\x07500 万\r\x13INCLUDETEXT hidden-file\x14显示结果\x15")
	text, err := legacyWord(context.Background(), streams)
	if err != nil || !strings.Contains(text, "预算\t500 万") || !strings.Contains(text, "显示结果") || strings.Contains(text, "hidden-file") {
		t.Fatalf("bad Word text: %q %v", text, err)
	}
	little.PutUint16(streams["WordDocument"][10:], 0x300)
	if _, err = legacyWord(context.Background(), streams); err == nil {
		t.Fatal("encrypted DOC accepted")
	}
}
func TestBIFFContinuedUnicodeAndRichText(t *testing.T) {
	// One string crosses a CONTINUE boundary and changes from compressed Unicode
	// to UTF-16. Formatting data must not become text.
	reader := biffStrings{parts: [][]byte{{4, 0, 8, 1, 0, 'A', 'B'}, append([]byte{1}, append(legacyUnicode("中文"), []byte{0, 0, 0, 0}...)...)}}
	text, err := reader.text()
	if err != nil || text != "AB中文" {
		t.Fatalf("continued string: %q %v", text, err)
	}
}
func pptTestRecord(kind, options uint16, body []byte) []byte {
	data := make([]byte, 8+len(body))
	little.PutUint16(data, options)
	little.PutUint16(data[2:], kind)
	little.PutUint32(data[4:], uint32(len(body)))
	copy(data[8:], body)
	return data
}
func TestLegacyPPTIgnoresDeletedContentAndKeepsChinese(t *testing.T) {
	data := pptTestRecord(4008, 0, []byte("DELETED CONTENT"))
	slideOffset := len(data)
	data = append(data, pptTestRecord(1006, 15, pptTestRecord(4000, 0, legacyUnicode("当前中文内容")))...)
	persistAtom := make([]byte, 20)
	little.PutUint32(persistAtom, 2)
	docOffset := len(data)
	data = append(data, pptTestRecord(1000, 15, pptTestRecord(4080, 15, pptTestRecord(1011, 0, persistAtom)))...)
	ptrOffset := len(data)
	pointers := make([]byte, 12)
	little.PutUint32(pointers, 2<<20|1)
	little.PutUint32(pointers[4:], uint32(docOffset))
	little.PutUint32(pointers[8:], uint32(slideOffset))
	data = append(data, pptTestRecord(6002, 0, pointers)...)
	editOffset := len(data)
	edit := make([]byte, 28)
	little.PutUint32(edit[12:], uint32(ptrOffset))
	little.PutUint32(edit[16:], 1)
	data = append(data, pptTestRecord(4085, 0, edit)...)
	user := make([]byte, 20)
	little.PutUint32(user[12:], 0xe391c05f)
	little.PutUint32(user[16:], uint32(editOffset))
	streams := map[string][]byte{"PowerPoint Document": data, "Current User": user}
	text, err := legacyPowerPoint(context.Background(), streams)
	if err != nil || !strings.Contains(text, "当前中文内容") || strings.Contains(text, "DELETED") {
		t.Fatalf("PPT included wrong version: %q %v", text, err)
	}
	// A cyclic edit chain is rejected without hanging.
	little.PutUint32(data[editOffset+8+8:], uint32(editOffset))
	if _, err = legacyPowerPoint(context.Background(), streams); err == nil {
		t.Fatal("cyclic edit chain accepted")
	}
}
func TestLegacyOfficeTruncatedFiles(t *testing.T) {
	for _, item := range []struct{ name, kind string }{{"simple.doc", "doc"}, {"simple.xls", "xls"}, {"basic.ppt", "ppt"}} {
		data, err := os.ReadFile(filepath.Join("testdata", "legacy", item.name))
		if err != nil {
			t.Fatal(err)
		}
		for _, size := range []int{0, 7, 511, 512, len(data) / 2} {
			if _, err := extractLegacyOffice(context.Background(), data[:size], item.kind); err == nil {
				t.Fatalf("truncated %s accepted at %d", item.name, size)
			}
		}
	}
}
func FuzzLegacyOffice(f *testing.F) {
	for _, name := range []string{"simple.doc", "simple.xls", "basic.ppt"} {
		data, err := os.ReadFile(filepath.Join("testdata", "legacy", name))
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data, strings.TrimPrefix(filepath.Ext(name), "."))
	}
	f.Fuzz(func(t *testing.T, data []byte, kind string) {
		if len(data) > 65536 {
			return
		}
		if kind != "doc" && kind != "xls" && kind != "ppt" {
			return
		}
		_, _ = extractLegacyOffice(context.Background(), data, kind)
	})
}

package knowledge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func fixture(t testing.TB) *Core {
	t.Helper()
	root := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(root, "platform.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec("PRAGMA foreign_keys=ON;" + Schema + EvalSchema + ChunkStructureSchema + QualitySchema20 + QualitySchema21); err != nil {
		t.Fatal(err)
	}
	core, err := New(db, root)
	if err != nil {
		t.Fatal(err)
	}
	return core
}
func base(t testing.TB, c *Core, name string) Base {
	t.Helper()
	b, err := c.SaveBase(context.Background(), "", name, "")
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func upload(t testing.TB, c *Core, baseID, id, name, text string) Document {
	t.Helper()
	d, err := c.Upload(context.Background(), baseID, id, name, strings.NewReader(text))
	if err != nil {
		t.Fatal(err)
	}
	return d
}
func process(t testing.TB, c *Core) {
	t.Helper()
	for {
		worked, err := c.processOne(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !worked {
			return
		}
	}
}
func search(t testing.TB, c *Core, ids []string, query string) SearchResult {
	t.Helper()
	result, err := c.Search(context.Background(), Scope{ids}, SearchRequest{Query: query})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func TestLocalProviderBilingualScopeAndSources(t *testing.T) {
	c := fixture(t)
	allowed := base(t, c, "Operations")
	other := base(t, c, "Private")
	d := upload(t, c, allowed.ID, "", "发票流程.md", "# 发票开具流程\n\n申请增值税发票，请提供订单编号。财务会在七个工作日内完成开具。\n\nInvoice refunds are processed within seven business days.")
	upload(t, c, other.ID, "", "private.txt", "发票开具的私人知识内容，不应越权返回。")
	if got := search(t, c, []string{allowed.ID}, "发票开具"); len(got.Hits) != 0 {
		t.Fatal("queued documents must not be searchable")
	}
	process(t, c)
	for _, query := range []string{"发票开具流程", "invoice refunds"} {
		got := search(t, c, []string{allowed.ID}, query)
		if len(got.Hits) == 0 {
			t.Fatalf("no result for %q", query)
		}
		hit := got.Hits[0]
		if hit.KnowledgeID != allowed.ID || hit.DocumentID != d.ID || hit.SourceURL != "/?source="+hit.SourceID || hit.Score == nil {
			t.Fatalf("invalid source: %+v", hit)
		}
		source, err := c.Read(context.Background(), Scope{[]string{allowed.ID}}, ReadRequest{SourceID: hit.SourceID, Limit: 20})
		if err != nil || source.Text == "" || source.DocumentText == "" || source.Location.Paragraph < 1 || source.Location.Page != 0 || source.NextOffset == nil {
			t.Fatalf("source read: %+v %v", source, err)
		}
		if _, err := c.Read(context.Background(), Scope{[]string{other.ID}}, ReadRequest{SourceID: hit.SourceID}); err == nil {
			t.Fatal("unbound document read succeeded")
		}
	}
	_, err := c.Search(context.Background(), Scope{[]string{allowed.ID}}, SearchRequest{Query: "发票", KnowledgeIDs: []string{other.ID}})
	var fault *Fault
	if !errors.As(err, &fault) || fault.Kind != "forbidden" {
		t.Fatal("request expanded the scope")
	}
	got := search(t, c, []string{allowed.ID}, `发票" OR 1=1 --`)
	for _, hit := range got.Hits {
		if hit.KnowledgeID != allowed.ID {
			t.Fatal("FTS query expanded scope")
		}
	}
	if got := search(t, c, []string{allowed.ID}, "quasarrandommissingtoken"); len(got.Hits) != 0 {
		t.Fatal("fabricated search result")
	}
}
func TestAtomicUpdateFailureRetryAndDelete(t *testing.T) {
	c := fixture(t)
	b := base(t, c, "Policies")
	d := upload(t, c, b.ID, "", "policy.txt", "Original invoice policy uses seven business days.")
	process(t, c)
	original := search(t, c, []string{b.ID}, "seven").Hits[0]
	update := upload(t, c, b.ID, d.ID, "replacement.txt", string([]byte{0xff, 0xfe, 0x00}))
	process(t, c)
	failed, err := c.Document(context.Background(), d.ID)
	if err != nil || failed.State != "failed" || failed.ActiveVersion != 1 || failed.Version != 2 {
		t.Fatalf("failed update lost active version: %+v %v", failed, err)
	}
	if len(search(t, c, []string{b.ID}, "seven").Hits) == 0 {
		t.Fatal("failed update destroyed old index")
	}
	if _, err := c.Retry(context.Background(), d.ID); err != nil {
		t.Fatal(err)
	}
	process(t, c)
	if status, _ := c.Document(context.Background(), d.ID); status.State != "failed" {
		t.Fatal("invalid document was marked ready")
	}
	update = upload(t, c, b.ID, d.ID, "replacement.txt", "Replacement invoice policy uses fourteen calendar days.")
	if len(search(t, c, []string{b.ID}, "seven").Hits) == 0 {
		t.Fatal("queued update hid old index")
	}
	process(t, c)
	if got := search(t, c, []string{b.ID}, "fourteen"); len(got.Hits) == 0 || got.Hits[0].Version != update.Version {
		t.Fatal("new index not published")
	}
	if got := search(t, c, []string{b.ID}, "seven"); len(got.Hits) != 0 {
		t.Fatal("old index still searchable")
	}
	source, err := c.Read(context.Background(), Scope{[]string{b.ID}}, ReadRequest{SourceID: original.SourceID})
	if err != nil || !source.Historical || !strings.Contains(source.Text, "seven") {
		t.Fatal("historical reference changed")
	}
	if err := c.DeleteDocument(context.Background(), d.ID); err != nil {
		t.Fatal(err)
	}
	if len(search(t, c, []string{b.ID}, "invoice").Hits) != 0 {
		t.Fatal("deleted document remained searchable")
	}
	source, err = c.Read(context.Background(), Scope{[]string{b.ID}}, ReadRequest{SourceID: original.SourceID})
	if err != nil || !source.Deleted || source.Text != "" || source.DocumentText != "" {
		t.Fatal("deleted source exposed text or lost its tombstone")
	}
	if _, err := os.Stat(filepath.Join(c.root, d.ID)); !os.IsNotExist(err) {
		t.Fatal("deleted raw files remain")
	}
	if len(update.ID) == 0 {
		t.Fatal("missing update identifier")
	}
}
func TestBindingDeletionAndInterruptedLocalIndexRecovery(t *testing.T) {
	c := fixture(t)
	b := base(t, c, "Bound")
	d := upload(t, c, b.ID, "", "restart.txt", "Recovery searchable instructions.")
	if err := c.Bind(context.Background(), "default", []string{b.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.db.Exec("UPDATE knowledge_versions SET state='processing'"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	// Wait by querying a meaningful persisted condition, then shut down the worker.
	for i := 0; i < 1000; i++ {
		state, err := c.Document(context.Background(), d.ID)
		if err != nil {
			t.Fatal(err)
		}
		if state.State == "ready" {
			break
		}
		if i == 999 {
			t.Fatal("interrupted index did not recover")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteBase(context.Background(), b.ID); err != nil {
		t.Fatal(err)
	}
	ids, err := c.Bindings(context.Background(), "default")
	if err != nil || len(ids) != 0 {
		t.Fatal("deleted base stayed bound")
	}
	if len(search(t, c, []string{b.ID}, "recovery").Hits) != 0 {
		t.Fatal("deleted base remained searchable")
	}
	if err := c.cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func TestDocumentLimitsAndReindexVersion(t *testing.T) {
	c := fixture(t)
	b := base(t, c, "Validation")
	if _, err := c.Upload(context.Background(), b.ID, "", "bad.docx", strings.NewReader("Office")); err == nil {
		t.Fatal("unsupported file accepted")
	}
	if _, err := c.Upload(context.Background(), b.ID, "", "bad.pdf", strings.NewReader("not a PDF")); err == nil {
		t.Fatal("PDF content type mismatch accepted")
	}
	d := upload(t, c, b.ID, "", "large.txt", strings.Repeat("x", MaxCharacters+1))
	process(t, c)
	state, _ := c.Document(context.Background(), d.ID)
	if state.State != "failed" || state.ActiveVersion != 0 {
		t.Fatal("oversized extraction published")
	}
	d = upload(t, c, b.ID, "", "normal.txt", "Reindex preserves published source references.")
	process(t, c)
	old := search(t, c, []string{b.ID}, "reindex").Hits[0]
	next, err := c.Retry(context.Background(), d.ID)
	if err != nil || next.Version != 2 {
		t.Fatal("reindex did not create a new immutable version")
	}
	process(t, c)
	source, err := c.Read(context.Background(), Scope{[]string{b.ID}}, ReadRequest{SourceID: old.SourceID})
	if err != nil || !source.Historical {
		t.Fatal("reindex changed historical source")
	}
}
func TestPDFTextPageLocationsAndScans(t *testing.T) {
	if !pdfAvailable() {
		t.Skip("Poppler not installed; PDF integration requires pdftotext/pdfinfo")
	}
	c := fixture(t)
	b := base(t, c, "PDF")
	d := upload(t, c, b.ID, "", "policy.pdf", testPDF("First invoice page.", "Second refund page."))
	process(t, c)
	state, _ := c.Document(context.Background(), d.ID)
	if state.State != "ready" {
		t.Fatalf("PDF failed: %+v", state)
	}
	got := search(t, c, []string{b.ID}, "refund")
	if len(got.Hits) == 0 || got.Hits[0].Location.Page != 2 {
		t.Fatalf("PDF page mapping incorrect: %+v", got)
	}
	d = upload(t, c, b.ID, "", "scan.pdf", testPDF("", ""))
	process(t, c)
	state, _ = c.Document(context.Background(), d.ID)
	if state.State != "failed" || !strings.Contains(state.Error, "扫描") {
		t.Fatalf("empty PDF should fail: %+v", state)
	}
}
func testPDF(first, second string) string {
	objects := []string{"<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [3 0 R 4 0 R] /Count 2 >>", "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 5 0 R >> >> /Contents 6 0 R >>", "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 5 0 R >> >> /Contents 7 0 R >>", "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"}
	for _, text := range []string{first, second} {
		stream := "BT /F1 12 Tf 72 720 Td (" + text + ") Tj ET\n"
		objects = append(objects, fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(stream), stream))
	}
	var pdf strings.Builder
	pdf.WriteString("%PDF-1.4\n")
	offsets := []int{0}
	for index, object := range objects {
		offsets = append(offsets, pdf.Len())
		fmt.Fprintf(&pdf, "%d 0 obj\n%s\nendobj\n", index+1, object)
	}
	xref := pdf.Len()
	fmt.Fprintf(&pdf, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&pdf, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&pdf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return pdf.String()
}

func TestOversizeTextFailsBeforeIndexPublication(t *testing.T) {
	c := fixture(t)
	base := base(t, c, "Bounded parsing")
	document := upload(t, c, base.ID, "", "too-large.txt", strings.Repeat("x", MaxCharacters+1))
	process(t, c)
	failed, err := c.Document(context.Background(), document.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.State != "failed" || failed.ActiveVersion != 0 || !strings.Contains(failed.Error, "100 万") {
		t.Fatal("size limit did not fail clearly", failed)
	}
	var chunks int
	if err := c.db.QueryRow("SELECT count(*) FROM knowledge_chunks WHERE document_id=?", document.ID).Scan(&chunks); err != nil || chunks != 0 {
		t.Fatal("failed processing published partial chunks")
	}
}

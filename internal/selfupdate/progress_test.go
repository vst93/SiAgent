package selfupdate

import "testing"

func TestDownloadProgressParsesTransfer(t *testing.T) {
	w := &downloadProgress{}
	w.Write([]byte("Installing SiAgent v0.0.29\nprogress: 1024 8192\n"))
	if current, total := w.transfer(); current != 1024 || total != 8192 {
		t.Fatalf("got %d/%d", current, total)
	}
	w.Write([]byte("progress: 4096 0\n"))
	if current, total := w.transfer(); current != 4096 || total != 0 {
		t.Fatalf("unknown total: got %d/%d", current, total)
	}
	w.Write([]byte("noise\n"))
	if current, total := w.transfer(); current != 4096 || total != 0 {
		t.Fatalf("noise changed the last sample: %d/%d", current, total)
	}
}

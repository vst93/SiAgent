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

func TestDownloadProgressNeverGoesBackwards(t *testing.T) {
	w := &downloadProgress{}
	w.Write([]byte("progress: 3000 4000\n"))
	if current, total := w.transfer(); current != 3000 || total != 4000 {
		t.Fatalf("first sample: %d/%d", current, total)
	}
	// An installer retry truncates the archive and starts again at zero; the bar
	// must keep its position instead of jumping back to the start.
	w.Write([]byte("progress: 0 4000\n"))
	if current, total := w.transfer(); current != 3000 || total != 4000 {
		t.Fatalf("restart went backwards: %d/%d", current, total)
	}
	w.Write([]byte("progress: 500 4000\n"))
	if current, total := w.transfer(); current != 3000 || total != 4000 {
		t.Fatalf("smaller sample moved the bar back: %d/%d", current, total)
	}
	w.Write([]byte("progress: 3500 4000\n"))
	if current, total := w.transfer(); current != 3500 || total != 4000 {
		t.Fatalf("growth was ignored: %d/%d", current, total)
	}
	// A wrong total (for example a stale probe) falls back to indeterminate.
	w.Write([]byte("progress: 9000 4000\n"))
	if current, total := w.transfer(); current != 9000 || total != 0 {
		t.Fatalf("impossible total was kept: %d/%d", current, total)
	}
}

package web

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestEmbTemplatesMatchDisk: the embedded template set is exactly web/templates/*.html of the
// checkout, so a template added outside the embed pattern (a subdirectory, another extension)
// fails here instead of at the first page render.
func TestEmbTemplatesMatchDisk(t *testing.T) {
	disk, err := filepath.Glob(filepath.Join(w0RepoRoot, "web", "templates", "*"))
	if err != nil {
		t.Fatal(err)
	}
	onDisk := map[string]bool{}
	for _, p := range disk {
		onDisk[filepath.Base(p)] = true
	}
	entries, err := os.ReadDir(filepath.Join(w0RepoRoot, "web", "templates"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("no files in web/templates")
	}
	for _, e := range entries {
		if _, err := readTemplateFile(e.Name()); err != nil {
			t.Errorf("web/templates/%s is on disk but not embedded: %v", e.Name(), err)
		}
	}
}

// TestEmbRenderWithoutDisk: from a working directory without web/, a fresh template set, the
// favicon and the tree demo page all come from the binary; with PUGLEAF_DEV_TEMPLATES=1 the
// same set must come from disk and therefore fail there.
func TestEmbRenderWithoutDisk(t *testing.T) {
	if devTemplates() {
		t.Skip("PUGLEAF_DEV_TEMPLATES=1 reads templates from disk")
	}
	t.Chdir(t.TempDir())
	if _, err := os.Stat("web"); err == nil {
		t.Fatal("temp working directory unexpectedly has a web/ directory")
	}

	name := "emb-fresh-" + time.Now().Format("150405.000000000")
	if _, err := loadTemplates(name, nil, "base.html", "help.html"); err != nil {
		t.Fatalf("loadTemplates without web/ on disk: %v (templates not embedded?)", err)
	}
	page, err := readTemplateFile("thread-tree-demo.html")
	if err != nil || !strings.Contains(string(page), "<html") {
		t.Fatalf("readTemplateFile(thread-tree-demo.html) = %d bytes, %v", len(page), err)
	}

	rec := w0Do(t, w0Req{Path: "/favicon.ico"})
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/x-icon" || rec.Body.Len() == 0 {
		t.Errorf("GET /favicon.ico: status %d, Content-Type %q, %d bytes; want 200 image/x-icon from the binary", rec.Code, rec.Header().Get("Content-Type"), rec.Body.Len())
	}
	rec = w0Do(t, w0Req{Method: http.MethodHead, Path: "/favicon.ico"})
	if rec.Code != http.StatusOK {
		t.Errorf("HEAD /favicon.ico: status %d, want 200", rec.Code)
	}
	rec = w0Do(t, w0Req{Path: "/demo/thread-tree"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<html") {
		t.Errorf("GET /demo/thread-tree: status %d, want 200 with the demo page from the binary", rec.Code)
	}

	t.Setenv("PUGLEAF_DEV_TEMPLATES", "1")
	if _, err := loadTemplates(name+"-dev", nil, "base.html", "help.html"); err == nil {
		t.Error("PUGLEAF_DEV_TEMPLATES=1 without web/ on disk parsed a template set: dev mode does not read from disk")
	}
}

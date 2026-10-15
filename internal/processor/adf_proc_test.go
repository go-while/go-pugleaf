package processor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// OPS-1b: NewProcessorE returns the hostname validation error instead of exiting.
// With a non-empty LocalNNTPHostname the SetHostname(db) lookup is skipped and
// validateHostname rejects "localhost" before db is touched, so a nil db is safe.
func TestAdfProcNewProcessorEInvalidHost(t *testing.T) {
	saved := LocalNNTPHostname
	t.Cleanup(func() { LocalNNTPHostname = saved })
	LocalNNTPHostname = "localhost"

	proc, err := NewProcessorE(nil, nil, 7)
	if err == nil {
		t.Fatalf("NewProcessorE with hostname %q: want error, got processor %v", LocalNNTPHostname, proc)
	}
	if proc != nil {
		t.Fatalf("NewProcessorE returned a processor together with error %v", err)
	}
	if !strings.Contains(err.Error(), "hostname validation") {
		t.Fatalf("error %q does not mention hostname validation", err)
	}
}

// OPS-1b: printAnalysisResults returns the export error instead of exiting.
// A regular file named "out" in the cwd makes out/<group>.json unwritable.
func TestAdfProcPrintAnalysisResultsError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "out"), []byte("not a directory\n"), 0o600); err != nil {
		t.Fatalf("create out file: %v", err)
	}
	t.Chdir(dir) // restored automatically when the test ends

	analysis := &GroupAnalysis{GroupName: "adf.proc.fatal", ProviderName: "test"}
	err := printAnalysisResults(analysis, "json")
	if err == nil {
		t.Fatal("printAnalysisResults with a file named out: want error, got nil")
	}
	if !strings.Contains(err.Error(), "failed to write JSON file") {
		t.Fatalf("error %q does not mention the JSON write", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "out", "adf.proc.fatal.json")); statErr == nil {
		t.Fatal("JSON file was written below the out file")
	}
}

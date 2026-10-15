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
// A directory named <group>.json in the output directory makes the JSON file unwritable.
func TestAdfProcPrintAnalysisResultsError(t *testing.T) {
	outDir := filepath.Join(t.TempDir(), "out")
	blocker := filepath.Join(outDir, "adf.proc.fatal.json")
	if err := os.MkdirAll(blocker, 0o750); err != nil {
		t.Fatalf("create blocking directory: %v", err)
	}

	analysis := &GroupAnalysis{GroupName: "adf.proc.fatal", ProviderName: "test"}
	err := printAnalysisResults(analysis, "json", outDir)
	if err == nil {
		t.Fatal("printAnalysisResults with a directory in place of the JSON file: want error, got nil")
	}
	if !strings.Contains(err.Error(), "failed to write JSON file") {
		t.Fatalf("error %q does not mention the JSON write", err)
	}
	if info, statErr := os.Stat(blocker); statErr != nil || !info.IsDir() {
		t.Fatalf("blocking directory was replaced: info=%v err=%v", info, statErr)
	}
}

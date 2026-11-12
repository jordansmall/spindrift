package github

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An unreadable job log (expired, no access) leaves the check in the detail
// without an excerpt rather than failing the read.
func TestFailureDetailSurvivesAnUnreadableJobLog(t *testing.T) {
	h := newPRForgeHarness(t)
	url := h.SeedOpenPR("211")
	h.SeedFailingCheckLog(url, "build", "unused")
	if err := os.Remove(filepath.Join(h.stateDir, "joblogs", "7001")); err != nil {
		t.Fatal(err)
	}
	got, err := h.Forge().FailureDetail(url)
	if err != nil {
		t.Fatalf("FailureDetail: %v", err)
	}
	if !strings.Contains(got, "build: FAILURE") || strings.Contains(got, "log excerpt") {
		t.Errorf("FailureDetail = %q, want the check without a log excerpt", got)
	}
}

package forgejo_test

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgejo"
	"spindrift.dev/launcher/internal/forge/forgetest"
)

// prforgeHarness is a forgetest.PRForgeHarness backed by fakeForgejo, an
// in-memory stand-in for the Forgejo REST API's pull/commit-status/repo
// endpoints.
type prforgeHarness struct {
	*fakeForgejo
	cf forge.CodeForge
}

func newPRForgeHarness(t *testing.T) *prforgeHarness {
	t.Helper()
	f := newFakeForgejo(t)
	cf := forgejo.NewForgejoCodeForgeForTest(forgejo.ForgejoCodeForgeConfig{
		BaseURL:    f.URL(),
		Repo:       "owner/repo",
		Token:      "tok",
		BaseBranch: "main",
	}, nil, "unused")
	return &prforgeHarness{fakeForgejo: f, cf: cf}
}

func (h *prforgeHarness) Forge() forge.PRForge       { return h.cf.(forge.PRForge) }
func (h *prforgeHarness) CodeForge() forge.CodeForge { return h.cf }

func (h *prforgeHarness) BranchPrefix() string { return "agent/issue-" }

func TestForgejoCodeForge_PRForgeContract(t *testing.T) {
	forgetest.RunPRForgeContract(t, newPRForgeHarness(t))
}

// The fake's job-log support backs the failure-detail log excerpt: the status
// carries the job page and the web /logs route serves the raw log.
func TestForgejoFakeServesFailingCheckLog(t *testing.T) {
	h := newPRForgeHarness(t)
	prURL := h.SeedOpenPR("42")
	h.SeedFailingCheckLog(prURL, "ci/build", "step 3\nFAIL TestFake: kaboom\n")
	got, err := h.Forge().FailureDetail(prURL)
	if err != nil {
		t.Fatalf("FailureDetail(...) unexpected error: %v", err)
	}
	if !strings.Contains(got, "FAIL TestFake: kaboom") {
		t.Fatalf("FailureDetail(...) = %q, want the job log", got)
	}
}

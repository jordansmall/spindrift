package forgejo_test

import (
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
		BaseURL:      f.URL(),
		Repo:         "owner/repo",
		Token:        "tok",
		BaseBranch:   "main",
		BranchPrefix: "agent/issue-",
	}, nil, "unused")
	return &prforgeHarness{fakeForgejo: f, cf: cf}
}

func (h *prforgeHarness) Forge() forge.PRForge       { return h.cf.(forge.PRForge) }
func (h *prforgeHarness) CodeForge() forge.CodeForge { return h.cf }

func TestForgejoCodeForge_PRForgeContract(t *testing.T) {
	forgetest.RunPRForgeContract(t, newPRForgeHarness(t))
}

// TestFakeForgejo_SeedDraftPR_DraftDerivedFromTitle verifies the fake
// derives the served "draft" field from the pull's title, mirroring real
// Forgejo (services/convert/pull.go: Draft is pr.IsWorkInProgress(ctx),
// never an independently-settable flag) rather than an independent
// fakePull.Draft bool disconnected from the title. SeedDraftPR must seed a
// WIP-prefixed title so IsDraftTitle reports true, and MarkReady — which
// PATCHes the title with the WIP prefix stripped — must flip a subsequent
// read's draft field to false. Read via fakeForgejo.IsDraftTitle rather
// than the adapter's OpenPRForBranch, which no longer surfaces draft status
// on the returned forge.PR; the adoption behavior itself (found regardless
// of draft) is covered separately by the PRForge contract and
// TestOpenPRForBranch_AdoptsDraftPR.
func TestFakeForgejo_SeedDraftPR_DraftDerivedFromTitle(t *testing.T) {
	h := newPRForgeHarness(t)
	url := h.SeedDraftPR("300")

	if _, ok, err := h.Forge().OpenPRForBranch("agent/issue-300"); err != nil {
		t.Fatalf("OpenPRForBranch: %v", err)
	} else if !ok {
		t.Fatalf("OpenPRForBranch(%q): not found", "agent/issue-300")
	}
	if !h.IsDraftTitle("300") {
		t.Fatalf("IsDraftTitle(%q) = false, want true (fake's draft field must derive from its WIP-prefixed title)", "300")
	}

	if err := h.Forge().MarkReady(url); err != nil {
		t.Fatalf("MarkReady(%q): %v", url, err)
	}

	if _, ok, err := h.Forge().OpenPRForBranch("agent/issue-300"); err != nil {
		t.Fatalf("OpenPRForBranch after MarkReady: %v", err)
	} else if !ok {
		t.Fatalf("OpenPRForBranch(%q) after MarkReady: not found", "agent/issue-300")
	}
	if h.IsDraftTitle("300") {
		t.Fatalf("IsDraftTitle(%q) = true after MarkReady, want false (fake's draft field must track the WIP-stripped title)", "300")
	}
}

package connectorforgetest

import (
	"strconv"
	"strings"

	"testing"

	"spindrift.dev/launcher/internal/forge"
)

const (
	// PRWasm is the fixture PR forge Connector declaring every optional
	// capability.
	PRWasm = "forge-pr.wasm"
	// MinimalPRWasm declares none of the optional PR capabilities.
	MinimalPRWasm = "forge-pr-minimal.wasm"
	// UndeclaredMergeWasm merges through a path its Route table does not declare.
	UndeclaredMergeWasm = "forge-pr-undeclared-merge.wasm"
)

// PRHarness implements forgetest.PRForgeHarness over the composed PR-capable
// Connector forge.
type PRHarness struct{ *base }

// NewPR loads the full-capability fixture PR Connector.
func NewPR(t *testing.T) *PRHarness { return NewPRVariant(t, PRWasm) }

// NewPRVariant loads the named fixture PR Connector wasm file.
func NewPRVariant(t *testing.T, wasm string) *PRHarness {
	t.Helper()
	return &PRHarness{newBase(t, wasm, prMethods)}
}

// Forge returns the PRForge Compose resolved.
func (h *PRHarness) Forge() forge.PRForge { return h.caps.PRForge }

// CodeForge returns the composed forge.
func (h *PRHarness) CodeForge() forge.CodeForge { return h.cf }

func (h *PRHarness) number(url string) int {
	h.t.Helper()
	n, err := strconv.Atoi(url[strings.LastIndex(url, "/")+1:])
	if err != nil {
		h.t.Fatalf("not a seeded PR URL: %q", url)
	}
	return n
}

func (h *PRHarness) seed(num string, draft bool) string {
	h.t.Helper()
	n, err := strconv.Atoi(num)
	if err != nil {
		h.t.Fatalf("PR number %q is not numeric", num)
	}
	h.Backend.AddPR(n, forge.AgentBranchName(prefix, num), "main", draft)
	return h.Backend.PRURL(n)
}

func (h *PRHarness) SeedOpenPR(num string) string  { return h.seed(num, false) }
func (h *PRHarness) SeedDraftPR(num string) string { return h.seed(num, true) }

// SeedLandablePR seeds a real agent branch and an open, ready PR for it whose
// checks are green, so a Merge lands the branch in the bare repo.
func (h *PRHarness) SeedLandablePR(num string) string {
	h.t.Helper()
	h.SeedLandable(num)
	url := h.seed(num, false)
	h.Backend.SetSteadyState(h.number(url), "success")
	return url
}

func (h *PRHarness) IsDraft(url string) bool { return h.Backend.PR(h.number(url)).Draft }

// Merged reports whether the backend merged url's PR.
func (h *PRHarness) Merged(url string) bool { return h.Backend.PR(h.number(url)).Merged }

func (h *PRHarness) SeedCheckStates(url string, states []forge.RollupState) {
	s := make([]string, len(states))
	for i, st := range states {
		s[i] = string(st)
	}
	h.Backend.SetCheckStates(h.number(url), s)
}

func (h *PRHarness) SeedFailingCheck(url, name, conclusion, summary string) {
	h.Backend.AddStatus(h.number(url), name, strings.ToLower(conclusion), summary, "")
}

func (h *PRHarness) SeedFailingCheckLog(url, check, log string) {
	id := h.Backend.AddStatus(h.number(url), check, "failure", "", "")
	h.Backend.SetJobLog(id, log)
}

func (h *PRHarness) SeedAutoMergeAllowed(allowed bool) { h.Backend.SetAutoMergeAllowed(allowed) }

func (h *PRHarness) SeedNeedsUpdate(url string, needsUpdate bool) {
	behind := 0
	if needsUpdate {
		behind = 1
	}
	h.Backend.SetBehind(h.number(url), behind)
}

func (h *PRHarness) AutoMergeEnqueued(url string) bool { return h.Backend.PR(h.number(url)).Enqueued }

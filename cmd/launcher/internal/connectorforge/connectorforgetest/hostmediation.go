package connectorforgetest

import (
	"errors"
	"sync"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
)

// HostMediationHarness implements forgetest.HostMediationHarness over a
// Connector PR forge composed for a read-only Box, so core relays the Box's
// bundle in its own git. The tracker half is a forge.Fake: it is not under test.
type HostMediationHarness struct {
	*base
	fake *forge.Fake

	mu         sync.Mutex
	relayedSHA map[string]string
	prSeq      int
}

// NewHostMediation loads the full-capability fixture PR Connector and composes
// it for a read-only Box.
func NewHostMediation(t *testing.T) *HostMediationHarness {
	t.Helper()
	return NewHostMediationVariant(t, PRWasm)
}

// NewHostMediationVariant is NewHostMediation over the named fixture wasm file.
func NewHostMediationVariant(t *testing.T, wasm string) *HostMediationHarness {
	t.Helper()
	b := newBase(t, wasm, prMethods, readOnlyBox)
	return &HostMediationHarness{base: b, fake: forge.NewFake(), relayedSHA: map[string]string{}, prSeq: 100}
}

func (h *HostMediationHarness) CodeForge() forge.CodeForge { return h.base.cf }

func (h *HostMediationHarness) SeedBundle(ref string) string {
	outbox := h.t.TempDir()
	sha := forgetest.SeedRelayBundle(h.t, h.Repo.Bare, "main", outbox, ref)
	h.mu.Lock()
	h.relayedSHA[ref] = sha
	h.mu.Unlock()
	return outbox
}

func (h *HostMediationHarness) BundleLanded(ref string) bool {
	h.mu.Lock()
	want, ok := h.relayedSHA[ref]
	h.mu.Unlock()
	return ok && forgetest.RevParse(h.t, h.Repo.Bare, "refs/heads/"+ref) == want
}

func (h *HostMediationHarness) EmptyOutbox() string { return h.t.TempDir() }

func (h *HostMediationHarness) SeedDraftPRHead(failing bool) string {
	if failing {
		const head = "agent/issue-hmfail"
		h.Backend.RefuseHead(head)
		return head
	}
	return "agent/issue-hmpr1"
}

func (h *HostMediationHarness) SeedExistingOpenPR() (head, wantURL string) {
	h.mu.Lock()
	h.prSeq++
	n := h.prSeq
	h.mu.Unlock()
	head = "agent/issue-2407-adopt"
	h.Backend.AddPR(n, head, "main", true)
	return head, h.Backend.PRURL(n)
}

func (h *HostMediationHarness) Tracker() forge.IssueTracker { return h.fake.AsIssueFiler() }

func (h *HostMediationHarness) SeedCommentTarget(failing bool) string {
	if failing {
		h.fake.CommentErr = errSimulatedComment
		return "901"
	}
	h.fake.CommentErr = nil
	return "902"
}

func (h *HostMediationHarness) CommentPosted(num, body string) bool {
	for _, c := range h.fake.CommentCalls {
		if c.Num == num && c.Body == body {
			return true
		}
	}
	return false
}

var errSimulatedComment = errors.New("simulated comment failure")

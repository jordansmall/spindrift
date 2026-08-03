package forge_test

import (
	"errors"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
)

// fakeHostMediationHarness adapts forge.Fake to forgetest.HostMediationHarness
// and forgetest.IssueFilerHarness -- the Fake models every adapter's shape at
// once here since AsGithubReadOnly()/AsIssueFiler() both wrap the same
// underlying scripted fields the github/forgejo real-backend harnesses drive
// through git/HTTP instead.
type fakeHostMediationHarness struct {
	f  *forge.Fake
	cf forge.CodeForge
	tr forge.IssueTracker
}

func newFakeHostMediationHarness() *fakeHostMediationHarness {
	f := forge.NewFake(testLabels)
	return &fakeHostMediationHarness{
		f:  f,
		cf: f.AsGithubReadOnly(),
		tr: f.AsIssueFiler(),
	}
}

func (h *fakeHostMediationHarness) CodeForge() forge.CodeForge  { return h.cf }
func (h *fakeHostMediationHarness) Tracker() forge.IssueTracker { return h.tr }

func (h *fakeHostMediationHarness) SeedBundle(ref string) (outboxDir string) {
	h.f.RelayBundleErr = nil
	return "unused-outbox"
}

func (h *fakeHostMediationHarness) BundleLanded(ref string) bool {
	for _, c := range h.f.RelayBundleCalls {
		if c.Ref == ref {
			return true
		}
	}
	return false
}

func (h *fakeHostMediationHarness) EmptyOutbox() (outboxDir string) {
	h.f.RelayBundleErr = forge.ErrBundleNotFound
	return "empty-outbox"
}

func (h *fakeHostMediationHarness) SeedDraftPRHead(failing bool) (head string) {
	if failing {
		h.f.CreateDraftPRErr = errors.New("simulated draft PR create failure")
		return "fail-head"
	}
	h.f.CreateDraftPRErr = nil
	h.f.CreateDraftPRURL = "https://github.com/owner/repo/pull/999"
	return "agent/issue-999"
}

func (h *fakeHostMediationHarness) SeedCommentTarget(failing bool) (num string) {
	if failing {
		h.f.CommentErr = errors.New("simulated comment failure")
		return "901"
	}
	h.f.CommentErr = nil
	return "902"
}

func (h *fakeHostMediationHarness) CommentPosted(num, body string) bool {
	for _, c := range h.f.CommentCalls {
		if c.Num == num && c.Body == body {
			return true
		}
	}
	return false
}

func (h *fakeHostMediationHarness) IssueFilerTracker() forge.IssueTracker { return h.tr }

func (h *fakeHostMediationHarness) SeedIssueFilerTarget(failing bool) (title, body string, labels []string) {
	if failing {
		h.f.PostIssueErr = errors.New("simulated issue file failure")
		return "fail title", "fail body", nil
	}
	h.f.PostIssueErr = nil
	h.f.PostIssueURL = "https://github.com/owner/repo/issues/999"
	return "queued finding", "filed from review", []string{"agent-review-finding"}
}

func (h *fakeHostMediationHarness) IssuePosted(title, body string, labels []string) bool {
	for _, c := range h.f.PostIssueCalls {
		if c.Title == title && c.Body == body {
			return true
		}
	}
	return false
}

func TestFake_HostMediationContract(t *testing.T) {
	forgetest.RunHostMediationContract(t, newFakeHostMediationHarness())
}

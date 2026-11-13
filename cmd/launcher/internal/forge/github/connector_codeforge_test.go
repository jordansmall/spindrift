package github

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
)

// composedCodeForgeHarness is the harness the CodeForge contract runs against:
// the composed Code Forge, whose refs are PR URLs.
type composedCodeForgeHarness struct{ *connectorHarness }

var _ forgetest.CodeForgeHarness = composedCodeForgeHarness{}

func (h composedCodeForgeHarness) Forge() forge.CodeForge { return h.cf }

// Unreachable composes against a second backend, then closes it so Probe fails.
// Compose itself reads the repository's identity, so the backend answers that
// one query first.
func (h composedCodeForgeHarness) Unreachable() forge.CodeForge {
	h.t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"data": map[string]any{"repository": map[string]any{
			"url": "https://" + r.Host + "/" + connectorRepo, "nameWithOwner": connectorRepo,
		}}})
	}))
	conn, _ := loadGithubConnector(h.t, srv.Listener.Addr().String(), connectorRepo, srv.Client(), &bytes.Buffer{})
	cf, _, err := composeForge(conn, srv.URL, connectorRepo, false)
	if err != nil {
		h.t.Fatal(err)
	}
	srv.Close()
	return cf
}

func (h composedCodeForgeHarness) SeedLandable(num string) string { return h.SeedLandablePR(num) }

func (h composedCodeForgeHarness) AdvanceBase() { h.backend.repo.AdvanceBase() }

func (h composedCodeForgeHarness) Landed(num string) bool { return h.backend.repo.Landed(num) }

func (h composedCodeForgeHarness) Rebased(num string) bool {
	return h.backend.repo.Rebased(forge.AgentBranchName(h.BranchPrefix(), num))
}

// FailNextMerge makes the head and base genuinely conflict, so the forge
// refuses the merge and the PR reports itself unmergeable.
func (h composedCodeForgeHarness) FailNextMerge(ref string) { h.backend.repo.ConflictBase(prNum(ref)) }

func (h composedCodeForgeHarness) FailNextRebase(ref string) {
	h.backend.repo.ConflictBase(prNum(ref))
}

// composedHostMediationHarness is the harness the host-mediation contract runs
// against: the Code Forge composed for a read-only Box, so core relays the Box's
// bundle in its own git. The tracker half is a forge.Fake and not under test.
type composedHostMediationHarness struct {
	*connectorHarness
	fake *forge.Fake

	mu         sync.Mutex
	relayedSHA map[string]string
	seeded     map[string]bool
	prSeq      int
}

var _ forgetest.HostMediationHarness = (*composedHostMediationHarness)(nil)

func newComposedHostMediationHarness(t *testing.T) *composedHostMediationHarness {
	return &composedHostMediationHarness{
		connectorHarness: newComposedHarness(t, true), fake: forge.NewFake(),
		relayedSHA: map[string]string{}, seeded: map[string]bool{}, prSeq: 1500,
	}
}

func (h *composedHostMediationHarness) Tracker() forge.IssueTracker { return h.fake.AsIssueFiler() }

func (h *composedHostMediationHarness) SeedBundle(ref string) string {
	outbox := h.t.TempDir()
	sha := forgetest.SeedRelayBundle(h.t, h.backend.repo.Bare, "main", outbox, ref)
	h.mu.Lock()
	h.relayedSHA[ref] = sha
	h.mu.Unlock()
	return outbox
}

func (h *composedHostMediationHarness) BundleLanded(ref string) bool {
	h.mu.Lock()
	want, ok := h.relayedSHA[ref]
	h.mu.Unlock()
	return ok && forgetest.RevParse(h.t, h.backend.repo.Bare, "refs/heads/"+ref) == want
}

func (h *composedHostMediationHarness) EmptyOutbox() string { return h.t.TempDir() }

// SeedDraftPRHead returns a head GitHub can open a PR for (a branch in the
// repository), or, when failing, one it cannot (no such branch).
func (h *composedHostMediationHarness) SeedDraftPRHead(failing bool) string {
	if failing {
		return "agent/issue-hmfail"
	}
	const head = "agent/issue-hmpr1"
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.seeded[head] {
		h.backend.repo.SeedBranch(head, "hmpr1")
		h.seeded[head] = true
	}
	return head
}

func (h *composedHostMediationHarness) SeedExistingOpenPR() (head, wantURL string) {
	h.mu.Lock()
	h.prSeq++
	n := h.prSeq
	h.mu.Unlock()
	head = "agent/issue-2407-adopt"
	h.backend.mu.Lock()
	defer h.backend.mu.Unlock()
	h.backend.prs[n] = &restPR{num: n, branch: head, base: "main", draft: true}
	return head, h.backend.prURL(n)
}

func (h *composedHostMediationHarness) SeedCommentTarget(failing bool) string {
	if failing {
		h.fake.CommentErr = errors.New("simulated comment failure")
		return "901"
	}
	h.fake.CommentErr = nil
	return "902"
}

func (h *composedHostMediationHarness) CommentPosted(num, body string) bool {
	for _, c := range h.fake.CommentCalls {
		if c.Num == num && c.Body == body {
			return true
		}
	}
	return false
}

// gitCredentialLog puts a git shim on PATH and returns the file it appends the
// GITEXEC_USERNAME:GITEXEC_PASSWORD core's git ran with to.
func gitCredentialLog(t *testing.T) string {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	shim, seen := t.TempDir(), filepath.Join(t.TempDir(), "seen")
	script := "#!/bin/sh\nprintf '%s:%s\\n' \"$GITEXEC_USERNAME\" \"$GITEXEC_PASSWORD\" >> '" + seen + "'\nexec '" + realGit + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(shim, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shim+string(os.PathListSeparator)+os.Getenv("PATH"))
	return seen
}

// A read-only Box's bundle reaches the repository through core's git with
// core's credential, never through the Connector: the relay crosses no Route and
// the Connector's audit log never carries the credential.
func TestConnector_RelayUsesCoresCredentialNotTheConnector(t *testing.T) {
	h := newComposedHostMediationHarness(t)
	seen := gitCredentialLog(t)
	const ref = "agent/issue-9002"
	outbox := h.SeedBundle(ref)
	before := h.audit.Len()

	relay, ok := forge.AsCodeForge[forge.BundleRelay](h.cf)
	if !ok {
		t.Fatal("read-only composed forge is not a BundleRelay")
	}
	if err := relay.RelayBundle(outbox, ref); err != nil {
		t.Fatalf("RelayBundle: %v", err)
	}
	if !h.BundleLanded(ref) {
		t.Fatalf("%s never reached the repository", ref)
	}
	log, err := os.ReadFile(seen)
	if err != nil {
		t.Fatalf("git shim never ran with a credential: %v", err)
	}
	if !strings.Contains(string(log), "bot:"+gitSecret) {
		t.Errorf("relay's git ran without core's credential: %q", log)
	}
	if h.audit.Len() != before {
		t.Errorf("relay crossed the Connector: audit grew to %q", h.audit.String())
	}
	if strings.Contains(h.audit.String(), gitSecret) {
		t.Error("the Connector's audit log carries core's git credential")
	}
}

// A transient merge failure is classified for core's merge loop to retry, and the
// Connector call itself is not retried; a permanent one is not retryable.
func testMergeTransientAndPermanent(t *testing.T, h *connectorHarness) {
	t.Run("Transient", func(t *testing.T) {
		url := h.SeedLandablePR("421")
		h.scriptMerge(http.StatusBadGateway)
		before := h.mergeRequests()

		err := h.cf.Merge(url)
		if !errors.Is(err, forge.ErrMergeTransient) {
			t.Fatalf("Merge on a 502 = %v, want forge.ErrMergeTransient (the class core retries)", err)
		}
		if n := h.mergeRequests() - before; n != 1 {
			t.Errorf("backend saw %d merge requests, want 1: the Connector call is not retried", n)
		}
		if h.Merged(url) || h.backend.repo.Landed("421") {
			t.Fatal("a failed merge landed")
		}

		if err := h.cf.Merge(url); err != nil {
			t.Fatalf("core's retry of the merge: %v", err)
		}
		if !h.Merged(url) || !h.backend.repo.Landed("421") {
			t.Errorf("retry: merged=%v landed=%v, want both", h.Merged(url), h.backend.repo.Landed("421"))
		}
	})
	t.Run("Permanent", func(t *testing.T) {
		url := h.SeedLandablePR("422")
		h.scriptMerge(http.StatusMethodNotAllowed)
		before := h.mergeRequests()

		err := h.cf.Merge(url)
		if err == nil || errors.Is(err, forge.ErrMergeTransient) {
			t.Fatalf("Merge on a 405 = %v, want a permanent (non-transient) error", err)
		}
		if n := h.mergeRequests() - before; n != 1 {
			t.Errorf("backend saw %d merge requests, want 1", n)
		}
		if h.Merged(url) || h.backend.repo.Landed("422") {
			t.Error("a refused merge landed")
		}
	})
}

func (h *connectorHarness) scriptMerge(statuses ...int) {
	h.backend.mu.Lock()
	defer h.backend.mu.Unlock()
	h.backend.mergeStatuses = append(h.backend.mergeStatuses, statuses...)
}

func (h *connectorHarness) mergeRequests() int {
	h.backend.mu.Lock()
	defer h.backend.mu.Unlock()
	return h.backend.merges
}

// A forge composed for a repository the Route table does not declare has its
// merge refused by the host before it is sent, with the refusal named and
// audited, never mistaken for a retryable or conflict failure.
func TestConnector_UndeclaredRepoMergeIsRefusedUnsentAndAudited(t *testing.T) {
	h := newHarnessFor(t, false, hostileRepo)
	landable := h.SeedLandablePR("431")
	url := strings.Replace(landable, "/"+connectorRepo+"/", "/"+hostileRepo+"/", 1)
	h.audit.Reset()

	err := h.cf.Merge(url)
	if !errors.Is(err, connectorhost.ErrRouteRefused) {
		t.Fatalf("Merge = %v, want ErrRouteRefused", err)
	}
	if errors.Is(err, forge.ErrMergeTransient) || errors.Is(err, forge.ErrMergeConflict) || errors.Is(err, forge.ErrMergeBlockedByChecks) {
		t.Errorf("Merge = %v, want the refusal unclassified", err)
	}
	const path = "/repos/" + hostileRepo + "/pulls/431/merge"
	if !strings.Contains(err.Error(), path) {
		t.Errorf("Merge = %v, want it to name the undeclared path %s", err, path)
	}
	if n := h.mergeRequests(); n != 0 {
		t.Errorf("backend saw %d merge requests, want none", n)
	}
	if h.Merged(landable) || h.backend.repo.Landed("431") {
		t.Error("the refused merge landed")
	}

	var refused []connectorhost.AuditEntry
	for _, e := range h.auditEntries(t) {
		if e.Decision == connectorhost.DecisionRefused {
			refused = append(refused, e)
		}
	}
	if len(refused) != 1 || refused[0].Path != path || refused[0].Method != http.MethodPut {
		t.Fatalf("refused audit entries = %+v, want one PUT for %s", refused, path)
	}
}

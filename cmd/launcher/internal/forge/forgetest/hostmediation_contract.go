package forgetest

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// HostMediationHarness lets RunHostMediationContract drive the four
// host-mediated write seams -- bundle relay, draft-PR creation, host-posted
// comment, host-posted issue filing (ADR 0034) -- against an adapter's own
// scripted or real backend, without knowing which adapter it is. github,
// forgejo, and the shared Fake each provide their own harness.
type HostMediationHarness interface {
	// CodeForge returns the read-only CodeForge under test -- it must
	// implement forge.BundleRelay and forge.DraftPRCreator.
	CodeForge() forge.CodeForge
	// Tracker returns the IssueTracker under test -- it must implement
	// forge.HostPostedCommenter.
	Tracker() forge.IssueTracker

	// SeedBundle stages ref as a relayable code-out bundle (a real git
	// bundle for a git-backed harness, scripted state for the Fake) and
	// returns the outbox directory RelayBundle should read it from.
	SeedBundle(ref string) (outboxDir string)
	// BundleLanded reports whether ref's staged content actually reached the
	// backing repo/store after a RelayBundle call.
	BundleLanded(ref string) bool
	// EmptyOutbox returns a fresh outbox directory with nothing staged --
	// the Box-never-wrote-a-bundle fault RelayBundle must reject.
	EmptyOutbox() (outboxDir string)

	// SeedDraftPRHead returns a head ref CreateDraftPR should be called
	// with: failing selects a head the backend will refuse to open a draft
	// PR for; !failing selects an ordinary head ref.
	SeedDraftPRHead(failing bool) (head string)

	// SeedExistingOpenPR pre-seeds an OPEN PR for a head ref such that a
	// subsequent CreateDraftPR(head) call hits the backend's own
	// already-exists/409 "a PR for this branch already exists" refusal (a
	// retried host-mediated create after an earlier one already succeeded),
	// and CreateDraftPR is expected to adopt it -- resolving the existing
	// open PR (draft or not) the same way forgejo's openAnyPRForBranch would
	// and returning its URL with no error, rather than surfacing the refusal
	// as a failure (issue #2407 slices 1-3). Returns the head to call
	// CreateDraftPR with and the URL it must return.
	SeedExistingOpenPR() (head, wantURL string)

	// SeedCommentTarget returns an issue number Comment should be called
	// against: failing selects a target the backend will refuse to post to;
	// !failing selects an ordinary, seeded issue.
	SeedCommentTarget(failing bool) (num string)
	// CommentPosted reports whether body was recorded against num after a
	// successful Comment call.
	CommentPosted(num, body string) bool
}

// IssueFilerHarness is implemented by harnesses whose Tracker() also
// satisfies forge.HostPostedIssueFiler -- github, forgejo, and the Fake (via
// AsIssueFiler()) all qualify. RunHostMediationContract type-asserts for it
// and no-ops the issue-filing scenario when absent, the same optional-marker
// pattern PRForgeHarness's PushOnlyCodeForgeProvider and CodeForgeHarness's
// LandingHarness already use.
type IssueFilerHarness interface {
	// IssueFilerTracker returns an IssueTracker that also implements
	// forge.HostPostedIssueFiler.
	IssueFilerTracker() forge.IssueTracker
	// SeedIssueFilerTarget returns a (title, body, labels) triple PostIssue
	// should be called with: failing selects input the backend will refuse
	// to file; !failing selects ordinary input.
	SeedIssueFilerTarget(failing bool) (title, body string, labels []string)
	// IssuePosted reports whether a filed issue matching title/body/labels
	// was recorded after a successful PostIssue call.
	IssuePosted(title, body string, labels []string) bool
}

// RunHostMediationContract runs the shared host-mediation conformance suite
// against h. Every adapter capable of BOX_FORGE_AND_ISSUE_ACCESS=read-only
// calls this from its own test file, backed by its own harness. Success is
// scenario-ordered before its seam's own fault case, since RelayBundleErr/
// CreateDraftPRErr-style scripted faults are sticky (returned by every
// subsequent call on that seam) rather than one-shot -- see each harness's
// own Seed*(failing bool) doc.
func RunHostMediationContract(t *testing.T, h HostMediationHarness) {
	t.Run("BundleRelayLandsRef", func(t *testing.T) { testBundleRelayLandsRef(t, h) })
	t.Run("BundleRelayMissingBundleErrors", func(t *testing.T) { testBundleRelayMissingBundleErrors(t, h) })
	t.Run("DraftPRCreation", func(t *testing.T) { testDraftPRCreation(t, h) })
	t.Run("DraftPRCreationFails", func(t *testing.T) { testDraftPRCreationFails(t, h) })
	t.Run("DraftPRCreationAdoptsExisting", func(t *testing.T) { testDraftPRCreationAdoptsExisting(t, h) })
	t.Run("HostPostedComment", func(t *testing.T) { testHostPostedComment(t, h) })
	t.Run("HostPostedCommentFails", func(t *testing.T) { testHostPostedCommentFails(t, h) })

	if ih, ok := h.(IssueFilerHarness); ok {
		t.Run("HostPostedIssueFiling", func(t *testing.T) { testHostPostedIssueFiling(t, ih) })
		t.Run("HostPostedIssueFilingFails", func(t *testing.T) { testHostPostedIssueFilingFails(t, ih) })
	}
}

func mustBundleRelay(t *testing.T, h HostMediationHarness) forge.BundleRelay {
	t.Helper()
	br, ok := h.CodeForge().(forge.BundleRelay)
	if !ok {
		t.Fatal("harness's CodeForge does not implement forge.BundleRelay")
	}
	return br
}

func mustDraftPRCreator(t *testing.T, h HostMediationHarness) forge.DraftPRCreator {
	t.Helper()
	dpc, ok := h.CodeForge().(forge.DraftPRCreator)
	if !ok {
		t.Fatal("harness's CodeForge does not implement forge.DraftPRCreator")
	}
	return dpc
}

func mustHostPostedCommenter(t *testing.T, h HostMediationHarness) forge.HostPostedCommenter {
	t.Helper()
	hc, ok := h.Tracker().(forge.HostPostedCommenter)
	if !ok {
		t.Fatal("harness's Tracker does not implement forge.HostPostedCommenter")
	}
	return hc
}

func mustHostPostedIssueFiler(t *testing.T, ih IssueFilerHarness) forge.HostPostedIssueFiler {
	t.Helper()
	f, ok := ih.IssueFilerTracker().(forge.HostPostedIssueFiler)
	if !ok {
		t.Fatal("harness's IssueFilerTracker does not implement forge.HostPostedIssueFiler")
	}
	return f
}

// testBundleRelayLandsRef verifies RelayBundle imports a staged bundle and
// the ref actually lands on the backing repo/store, observable through the
// harness's own BundleLanded query -- the seam settle's pre-merge
// relay-before-land step depends on (ADR 0033, ADR 0034).
func testBundleRelayLandsRef(t *testing.T, h HostMediationHarness) {
	br := mustBundleRelay(t, h)
	const ref = "agent/issue-hm101"
	outbox := h.SeedBundle(ref)

	if err := br.RelayBundle(outbox, ref); err != nil {
		t.Fatalf("RelayBundle(%q, %q): %v", outbox, ref, err)
	}
	if !h.BundleLanded(ref) {
		t.Fatalf("RelayBundle(%q, %q) reported success but %s never landed", outbox, ref, ref)
	}
}

// testBundleRelayMissingBundleErrors verifies RelayBundle rejects an outbox
// with no staged bundle rather than silently no-oping -- the Box-never-wrote
// -one fault (ADR 0033).
func testBundleRelayMissingBundleErrors(t *testing.T, h HostMediationHarness) {
	br := mustBundleRelay(t, h)
	const ref = "agent/issue-hm102"
	outbox := h.EmptyOutbox()

	if err := br.RelayBundle(outbox, ref); err == nil {
		t.Fatal("RelayBundle with no bundle staged: got nil error, want one")
	}
}

// testDraftPRCreation verifies CreateDraftPR opens a draft PR, returns a
// non-empty URL, and reports created=true -- the host-side counterpart to a
// read-only Box's own in-box `gh pr create` (issue #1919). created=true
// distinguishes this fresh-create success from the adoption path below
// (issue #2447).
func testDraftPRCreation(t *testing.T, h HostMediationHarness) {
	dpc := mustDraftPRCreator(t, h)
	head := h.SeedDraftPRHead(false)

	url, created, err := dpc.CreateDraftPR("feat: add widget", "Adds a widget.", "main", head)
	if err != nil {
		t.Fatalf("CreateDraftPR(...): %v", err)
	}
	if url == "" {
		t.Fatal("CreateDraftPR(...) returned an empty URL")
	}
	if !created {
		t.Error("CreateDraftPR(...) created = false, want true for a fresh create")
	}
}

// testDraftPRCreationFails verifies CreateDraftPR surfaces a backend refusal
// as an error rather than a blank URL.
func testDraftPRCreationFails(t *testing.T, h HostMediationHarness) {
	dpc := mustDraftPRCreator(t, h)
	head := h.SeedDraftPRHead(true)

	if _, _, err := dpc.CreateDraftPR("feat: add widget", "body", "main", head); err == nil {
		t.Fatal("CreateDraftPR with a failing backend: got nil error, want one")
	}
}

// testDraftPRCreationAdoptsExisting verifies CreateDraftPR adopts an already
// -open PR for head rather than surfacing the backend's already-exists/409
// refusal as a failure -- a retried host-mediated create for a branch an
// earlier call already opened a PR for must settle idempotently, not block
// the seam (issue #2407 slices 1-3) -- and reports created=false, since this
// call did not itself open the PR (issue #2447).
func testDraftPRCreationAdoptsExisting(t *testing.T, h HostMediationHarness) {
	dpc := mustDraftPRCreator(t, h)
	head, wantURL := h.SeedExistingOpenPR()

	url, created, err := dpc.CreateDraftPR("feat: add widget", "body", "main", head)
	if err != nil {
		t.Fatalf("CreateDraftPR(...): %v", err)
	}
	if url != wantURL {
		t.Errorf("CreateDraftPR(...) url = %q, want %q", url, wantURL)
	}
	if created {
		t.Error("CreateDraftPR(...) created = true, want false for an adopted pre-existing PR")
	}
}

// testHostPostedComment verifies Comment posts and the harness can observe
// it landed -- the read-only Box's blocked/verdict comment travels as a
// SPINDRIFT_COMMENT line for the Launcher to post host-side via this same
// call (issue #1914).
func testHostPostedComment(t *testing.T, h HostMediationHarness) {
	hc := mustHostPostedCommenter(t, h)
	num := h.SeedCommentTarget(false)
	const body = "host-mediation contract comment"

	if err := hc.Comment(num, body); err != nil {
		t.Fatalf("Comment(%q, %q): %v", num, body, err)
	}
	if !h.CommentPosted(num, body) {
		t.Fatalf("Comment(%q, %q) reported success but was not recorded", num, body)
	}
}

// testHostPostedCommentFails verifies Comment surfaces a backend refusal as
// an error.
func testHostPostedCommentFails(t *testing.T, h HostMediationHarness) {
	hc := mustHostPostedCommenter(t, h)
	num := h.SeedCommentTarget(true)

	if err := hc.Comment(num, "should not land"); err == nil {
		t.Fatal("Comment with a failing backend: got nil error, want one")
	}
}

// testHostPostedIssueFiling verifies PostIssue files an issue and returns a
// non-empty URL, observable through the harness's own IssuePosted query --
// the fourth host-mediated write channel (issue #2018, ADR 0034).
func testHostPostedIssueFiling(t *testing.T, ih IssueFilerHarness) {
	f := mustHostPostedIssueFiler(t, ih)
	title, body, labels := ih.SeedIssueFilerTarget(false)

	url, err := f.PostIssue(title, body, labels)
	if err != nil {
		t.Fatalf("PostIssue(%q, %q, %v): %v", title, body, labels, err)
	}
	if url == "" {
		t.Fatal("PostIssue(...) returned an empty URL")
	}
	if !ih.IssuePosted(title, body, labels) {
		t.Fatalf("PostIssue(%q, %q, %v) reported success but was not recorded", title, body, labels)
	}
}

// testHostPostedIssueFilingFails verifies PostIssue surfaces a backend
// refusal as an error rather than a blank URL.
func testHostPostedIssueFilingFails(t *testing.T, ih IssueFilerHarness) {
	f := mustHostPostedIssueFiler(t, ih)
	title, body, labels := ih.SeedIssueFilerTarget(true)

	if _, err := f.PostIssue(title, body, labels); err == nil {
		t.Fatal("PostIssue with a failing backend: got nil error, want one")
	}
}

package forgetest

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// HostMediationHarness lets RunHostMediationContract drive the four
// host-mediated write seams (bundle relay, draft-PR creation, host-posted
// comment, host-posted issue filing) against an adapter's own scripted or real
// backend, without knowing which adapter it is (ADR 0034).
type HostMediationHarness interface {
	// CodeForge returns the read-only CodeForge under test. It must implement
	// forge.BundleRelay and forge.DraftPRCreator.
	CodeForge() forge.CodeForge
	// Tracker returns the IssueTracker under test. It must implement
	// forge.HostPostedCommenter.
	Tracker() forge.IssueTracker

	// SeedBundle stages ref as a relayable code-out bundle and returns the
	// outbox directory RelayBundle should read it from.
	SeedBundle(ref string) (outboxDir string)
	// BundleLanded reports whether ref's staged content reached the backing
	// repo or store after a RelayBundle call.
	BundleLanded(ref string) bool
	// EmptyOutbox returns a fresh outbox directory with nothing staged, the
	// Box-never-wrote-a-bundle fault RelayBundle must reject.
	EmptyOutbox() (outboxDir string)

	// SeedDraftPRHead returns a head ref to call CreateDraftPR with: failing
	// selects a head the backend will refuse to open a draft PR for, !failing
	// an ordinary head ref.
	SeedDraftPRHead(failing bool) (head string)

	// SeedExistingOpenPR pre-seeds an open PR (draft or not) for a head ref so
	// that a later CreateDraftPR(head) hits the backend's own
	// already-exists/409 refusal. CreateDraftPR must adopt that PR, returning
	// its URL with no error, rather than surfacing the refusal as a failure
	// (issue #2407 slices 1-3). Returns that head and the URL it must return.
	SeedExistingOpenPR() (head, wantURL string)

	// SeedCommentTarget returns an issue number to call Comment against:
	// failing selects a target the backend will refuse to post to, !failing an
	// ordinary seeded issue.
	SeedCommentTarget(failing bool) (num string)
	// CommentPosted reports whether body was recorded against num after a
	// successful Comment call.
	CommentPosted(num, body string) bool
}

// IssueFilerHarness is implemented by harnesses whose Tracker() also satisfies
// forge.HostPostedIssueFiler. RunHostMediationContract type-asserts for it and
// no-ops the issue-filing scenario when it is absent.
type IssueFilerHarness interface {
	// IssueFilerTracker returns an IssueTracker that also implements
	// forge.HostPostedIssueFiler.
	IssueFilerTracker() forge.IssueTracker
	// SeedIssueFilerTarget returns a (title, body, labels) triple to call
	// PostIssue with: failing selects input the backend will refuse to file,
	// !failing ordinary input.
	SeedIssueFilerTarget(failing bool) (title, body string, labels []string)
	// IssuePosted reports whether a filed issue matching title/body/labels was
	// recorded after a successful PostIssue call.
	IssuePosted(title, body string, labels []string) bool
}

// RunHostMediationContract runs the shared host-mediation conformance suite
// against h. Every adapter capable of BOX_FORGE_AND_ISSUE_ACCESS=read-only
// calls it from its own test file. Each success scenario runs before its
// seam's fault case because scripted faults such as RelayBundleErr are sticky:
// once armed they fail every later call on that seam.
func RunHostMediationContract(t *testing.T, h HostMediationHarness) {
	t.Run("BundleRelayLandsRef", func(t *testing.T) { testBundleRelayLandsRef(t, h) })
	t.Run("BundleRelayThenDraftPRCreate", func(t *testing.T) { testBundleRelayThenDraftPRCreate(t, h) })
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

// testBundleRelayLandsRef verifies the ref RelayBundle imports actually lands
// on the backing repo or store, the relay-before-land step settle depends on
// before it merges (ADR 0033, ADR 0034).
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

// testBundleRelayThenDraftPRCreate chains RelayBundle and CreateDraftPR
// against the same just-relayed ref, the sequence settle.Mediation.Open
// drives (issue #2501). SeedDraftPRHead(false)'s head is discarded on purpose:
// the call only arms a fresh-create response on the Fake, whose CreateDraftPR
// otherwise returns an empty URL. It is a no-op for the real backends.
func testBundleRelayThenDraftPRCreate(t *testing.T, h HostMediationHarness) {
	br := mustBundleRelay(t, h)
	dpc := mustDraftPRCreator(t, h)
	h.SeedDraftPRHead(false)
	const ref = "agent/issue-hm103"
	outbox := h.SeedBundle(ref)

	if err := br.RelayBundle(outbox, ref); err != nil {
		t.Fatalf("RelayBundle(%q, %q): %v", outbox, ref, err)
	}
	if !h.BundleLanded(ref) {
		t.Fatalf("RelayBundle(%q, %q) reported success but %s never landed", outbox, ref, ref)
	}

	url, created, err := dpc.CreateDraftPR("feat: add widget", "Adds a widget.", "main", ref)
	if err != nil {
		t.Fatalf("CreateDraftPR against just-relayed ref %q: %v", ref, err)
	}
	if url == "" {
		t.Fatal("CreateDraftPR(...) returned an empty URL")
	}
	if !created {
		t.Error("CreateDraftPR(...) created = false, want true for a fresh create against a freshly-relayed ref")
	}
}

// testBundleRelayMissingBundleErrors verifies RelayBundle rejects an outbox
// with no staged bundle rather than silently no-oping (ADR 0033).
func testBundleRelayMissingBundleErrors(t *testing.T, h HostMediationHarness) {
	br := mustBundleRelay(t, h)
	const ref = "agent/issue-hm102"
	outbox := h.EmptyOutbox()

	if err := br.RelayBundle(outbox, ref); err == nil {
		t.Fatal("RelayBundle with no bundle staged: got nil error, want one")
	}
}

// testDraftPRCreation covers the host-side counterpart to a read-only Box's
// own in-box gh pr create (issue #1919). created=true separates this
// fresh-create success from the adoption path below (issue #2447).
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

func testDraftPRCreationFails(t *testing.T, h HostMediationHarness) {
	dpc := mustDraftPRCreator(t, h)
	head := h.SeedDraftPRHead(true)

	if _, _, err := dpc.CreateDraftPR("feat: add widget", "body", "main", head); err == nil {
		t.Fatal("CreateDraftPR with a failing backend: got nil error, want one")
	}
}

// testDraftPRCreationAdoptsExisting verifies a retried host-mediated create
// for a branch an earlier call already opened a PR for settles idempotently
// instead of blocking the seam on the already-exists refusal (issue #2407
// slices 1-3), and reports created=false because this call did not open the
// PR (issue #2447).
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

// testHostPostedComment covers the channel a read-only Box's blocked or
// verdict comment travels: a SPINDRIFT_COMMENT line the Launcher posts
// host-side through this call (issue #1914).
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

func testHostPostedCommentFails(t *testing.T, h HostMediationHarness) {
	hc := mustHostPostedCommenter(t, h)
	num := h.SeedCommentTarget(true)

	if err := hc.Comment(num, "should not land"); err == nil {
		t.Fatal("Comment with a failing backend: got nil error, want one")
	}
}

// testHostPostedIssueFiling covers the fourth host-mediated write channel
// (issue #2018, ADR 0034).
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

func testHostPostedIssueFilingFails(t *testing.T, ih IssueFilerHarness) {
	f := mustHostPostedIssueFiler(t, ih)
	title, body, labels := ih.SeedIssueFilerTarget(true)

	if _, err := f.PostIssue(title, body, labels); err == nil {
		t.Fatal("PostIssue with a failing backend: got nil error, want one")
	}
}

package forgejo_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgejo"
	"spindrift.dev/launcher/internal/forge/forgetest"
	"spindrift.dev/launcher/internal/seambundle"
)

// The fake Forgejo REST server only backs the Probe/PRForge plumbing the
// config wires up. RelayBundle itself never touches it.
func newReadOnlyRelayHarness(t *testing.T) (*forgetest.GitRepoFixture, forge.CodeForge) {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "Test Bot")
	t.Setenv("GIT_AUTHOR_EMAIL", "bot@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test Bot")
	t.Setenv("GIT_COMMITTER_EMAIL", "bot@example.com")

	repo := forgetest.NewGitRepoFixture(t, "main")

	// forgetest.NewGitRepoFixture's first push leaves the bare repo's HEAD
	// symref on git-init's default ("master", absent here), so a fresh clone
	// gets no local "main" to check out. CommitSubjects needs "main" itself to
	// resolve for `git log base..ref`, as it would against a real forge clone.
	if out, err := exec.Command("git", "-C", repo.Bare, "symbolic-ref", "HEAD", "refs/heads/main").CombinedOutput(); err != nil {
		t.Fatalf("set bare repo HEAD to refs/heads/main: %v: %s", err, out)
	}

	fake := newFakeForgejo(t)

	cf := forgejo.NewReadOnlyForgejoCodeForgeForTest(forgejo.ForgejoCodeForgeConfig{
		BaseURL:      fake.URL(),
		Repo:         "owner/repo",
		Token:        "tok",
		BaseBranch:   "main",
		UserName:     "Test Bot",
		UserEmail:    "bot@example.com",
		BranchPrefix: "agent/issue-",
	}, nil, repo.Bare)
	return repo, cf
}

// A read-write Box (BOX_FORGE_AND_ISSUE_ACCESS=read-write) pushes in-box, so
// if NewForgejoCodeForge satisfied forge.BundleRelay, settle's generic
// relay-before-merge (ready.go) would try to relay a bundle that Box never
// wrote and block every read-write land.
func TestNewForgejoCodeForge_DoesNotImplementBundleRelay(t *testing.T) {
	cf := forgejo.NewForgejoCodeForge(forgejo.ForgejoCodeForgeConfig{
		BaseURL: "https://codeberg.org",
		Repo:    "owner/repo",
		Token:   "tok",
	}, nil)
	if _, ok := cf.(forge.BundleRelay); ok {
		t.Error("NewForgejoCodeForge satisfies forge.BundleRelay, want it hidden for read-write")
	}
}

// A read-write Box already opens its own PR in-box, so settle must never call
// a host-side create for it.
func TestNewForgejoCodeForge_DoesNotImplementDraftPRCreator(t *testing.T) {
	cf := forgejo.NewForgejoCodeForge(forgejo.ForgejoCodeForgeConfig{
		BaseURL: "https://codeberg.org",
		Repo:    "owner/repo",
		Token:   "tok",
	}, nil)
	if _, ok := cf.(forge.DraftPRCreator); ok {
		t.Error("NewForgejoCodeForge satisfies forge.DraftPRCreator, want it hidden for read-write")
	}
}

// The read-only adapter keeps every PRForge method NewForgejoCodeForge has,
// by embedding. It opens PRs and watches CI exactly as read-write does. Only
// the finished branch's hand-off differs.
func TestNewReadOnlyForgejoCodeForge_ImplementsPRForge(t *testing.T) {
	_, cf := newReadOnlyRelayHarness(t)
	if _, ok := cf.(forge.PRForge); !ok {
		t.Error("NewReadOnlyForgejoCodeForge does not satisfy forge.PRForge")
	}
}

func TestReadOnlyForgejoCodeForge_RelayBundle_PushesRefToOrigin(t *testing.T) {
	repo, cf := newReadOnlyRelayHarness(t)
	outbox := t.TempDir()
	branch := "agent/issue-1964"
	wantSHA := forgetest.SeedRelayBundle(t, repo.Bare, "main", outbox, branch)

	br, ok := cf.(forge.BundleRelay)
	if !ok {
		t.Fatal("forgejo read-only CodeForge does not implement forge.BundleRelay")
	}

	if err := br.RelayBundle(outbox, branch); err != nil {
		t.Fatalf("RelayBundle: %v", err)
	}

	if got := forgetest.RevParse(t, repo.Bare, "refs/heads/"+branch); got != wantSHA {
		t.Errorf("refs/heads/%s = %s, want %s", branch, got, wantSHA)
	}
}

// A fix-pass retry rebuilds the bundle with a branch tip that diverged from
// what an earlier pass relayed. That push must overwrite the remote ref rather
// than be rejected as non-fast-forward.
func TestReadOnlyForgejoCodeForge_RelayBundle_ReRelayForceUpdatesRef(t *testing.T) {
	repo, cf := newReadOnlyRelayHarness(t)
	outbox := t.TempDir()
	branch := "agent/issue-1964"
	forgetest.SeedRelayBundle(t, repo.Bare, "main", outbox, branch)

	br := cf.(forge.BundleRelay)
	if err := br.RelayBundle(outbox, branch); err != nil {
		t.Fatalf("RelayBundle (first attempt): %v", err)
	}

	// Clone bare's base rather than the already-relayed ref, so the rebuilt
	// branch shares no ancestry with the commit the first relay pushed.
	work := t.TempDir()
	forgetest.Run(t, "", "clone", repo.Bare, work)
	forgetest.Run(t, work, "checkout", "main")
	forgetest.Run(t, work, "checkout", "-b", branch)
	forgetest.WriteFile(t, filepath.Join(work, "feature.txt"), "retried\n")
	forgetest.Run(t, work, "add", "feature.txt")
	forgetest.Run(t, work, "commit", "-m", "retried feature")
	wantSHA := forgetest.RevParse(t, work, branch)
	forgetest.Run(t, work, "bundle", "create", filepath.Join(outbox, seambundle.FileName), "main.."+branch)

	if err := br.RelayBundle(outbox, branch); err != nil {
		t.Fatalf("RelayBundle (retry, diverged history): %v", err)
	}

	if got := forgetest.RevParse(t, repo.Bare, "refs/heads/"+branch); got != wantSHA {
		t.Errorf("refs/heads/%s = %s, want %s (the retried bundle's tip)", branch, got, wantSHA)
	}
}

// An empty outbox means the Box never wrote a bundle. That must block the seam
// with an error, not a nil-error no-op, matching local's RelayBundle (ADR 0033).
func TestReadOnlyForgejoCodeForge_RelayBundle_MissingBundleErrors(t *testing.T) {
	_, cf := newReadOnlyRelayHarness(t)
	outbox := t.TempDir()

	br := cf.(forge.BundleRelay)
	err := br.RelayBundle(outbox, "agent/issue-1964")
	if err == nil {
		t.Fatal("RelayBundle with no bundle file present: got nil error, want one")
	}
	if !errors.Is(err, forge.ErrBundleNotFound) {
		t.Errorf("RelayBundle with no bundle file present: err = %v, want errors.Is(err, forge.ErrBundleNotFound)", err)
	}
}

// `git bundle verify` must reject the corrupt file before fetch sees it.
func TestReadOnlyForgejoCodeForge_RelayBundle_MalformedBundleErrors(t *testing.T) {
	_, cf := newReadOnlyRelayHarness(t)
	outbox := t.TempDir()
	forgetest.WriteFile(t, filepath.Join(outbox, seambundle.FileName), "not a bundle")

	br := cf.(forge.BundleRelay)
	err := br.RelayBundle(outbox, "agent/issue-1964")
	if err == nil {
		t.Fatal("RelayBundle with a malformed bundle file: got nil error, want one")
	}
	if errors.Is(err, forge.ErrBundleNotFound) {
		t.Errorf("RelayBundle with a malformed bundle file: err = %v, want a generic error, not forge.ErrBundleNotFound", err)
	}
}

// Subjects come back oldest first. Unlike RelayBundle, CommitSubjects must
// leave the real remote untouched: no ref for the branch may appear on
// repo.Bare.
func TestReadOnlyForgejoCodeForge_CommitSubjects_ReturnsSubjectsReadOnly(t *testing.T) {
	repo, cf := newReadOnlyRelayHarness(t)
	outbox := t.TempDir()
	branch := "agent/issue-1964"
	forgetest.SeedRelayBundle(t, repo.Bare, "main", outbox, branch)

	cs, ok := cf.(forge.BundleCommitSubjects)
	if !ok {
		t.Fatal("forgejo read-only CodeForge does not implement forge.BundleCommitSubjects")
	}

	subjects, err := cs.CommitSubjects(outbox, "main", branch)
	if err != nil {
		t.Fatalf("CommitSubjects: %v", err)
	}
	want := []string{"feature"}
	if len(subjects) != len(want) || subjects[0] != want[0] {
		t.Errorf("CommitSubjects = %v, want %v", subjects, want)
	}

	cmd := exec.Command("git", "-C", repo.Bare, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	if err := cmd.Run(); err == nil {
		t.Errorf("refs/heads/%s exists on the real remote after CommitSubjects, want no push side effect", branch)
	}
}

func TestReadOnlyForgejoCodeForge_CommitSubjects_MissingBundleErrors(t *testing.T) {
	_, cf := newReadOnlyRelayHarness(t)
	outbox := t.TempDir()

	cs := cf.(forge.BundleCommitSubjects)
	_, err := cs.CommitSubjects(outbox, "main", "agent/issue-1964")
	if err == nil {
		t.Fatal("CommitSubjects with no bundle file present: got nil error, want one")
	}
	if !errors.Is(err, forge.ErrBundleNotFound) {
		t.Errorf("CommitSubjects with no bundle file present: err = %v, want errors.Is(err, forge.ErrBundleNotFound)", err)
	}
}

func TestReadOnlyForgejoCodeForge_CommitSubjects_MalformedBundleErrors(t *testing.T) {
	_, cf := newReadOnlyRelayHarness(t)
	outbox := t.TempDir()
	forgetest.WriteFile(t, filepath.Join(outbox, seambundle.FileName), "not a bundle")

	cs := cf.(forge.BundleCommitSubjects)
	_, err := cs.CommitSubjects(outbox, "main", "agent/issue-1964")
	if err == nil {
		t.Fatal("CommitSubjects with a malformed bundle file: got nil error, want one")
	}
	if errors.Is(err, forge.ErrBundleNotFound) {
		t.Errorf("CommitSubjects with a malformed bundle file: err = %v, want a generic error, not forge.ErrBundleNotFound", err)
	}
}

// The ref guard must reject an empty or flag-like ref before it reaches a
// refspec or a checkout argument.
func TestReadOnlyForgejoCodeForge_RelayBundle_InvalidRefRejected(t *testing.T) {
	_, cf := newReadOnlyRelayHarness(t)
	outbox := t.TempDir()
	br := cf.(forge.BundleRelay)

	for _, ref := range []string{"", "-x"} {
		if err := br.RelayBundle(outbox, ref); err == nil {
			t.Errorf("RelayBundle(outbox, %q): got nil error, want one", ref)
		}
	}
}

// CreateDraftPR POSTs a WIP-prefixed draft PR, returns its html_url, and
// reports created=true (issue #2447).
func TestReadOnlyForgejoCodeForge_CreateDraftPR_ReturnsURL(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/repos/owner/repo/pulls" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"number":   1964,
			"html_url": "https://forge.test/owner/repo/pulls/1964",
		})
	}))
	defer srv.Close()

	cf := forgejo.NewReadOnlyForgejoCodeForge(forgejo.ForgejoCodeForgeConfig{
		BaseURL: srv.URL,
		Repo:    "owner/repo",
		Token:   "tok",
	}, nil)
	dpc, ok := cf.(forge.DraftPRCreator)
	if !ok {
		t.Fatal("forgejo read-only CodeForge does not implement forge.DraftPRCreator")
	}

	url, created, err := dpc.CreateDraftPR("feat: add widget", "Adds a widget.", "main", "agent/issue-1964")
	if err != nil {
		t.Fatalf("CreateDraftPR: %v", err)
	}
	want := "https://forge.test/owner/repo/pulls/1964"
	if url != want {
		t.Errorf("CreateDraftPR url = %q, want %q", url, want)
	}
	if !created {
		t.Error("CreateDraftPR created = false, want true for a fresh create")
	}

	wantTitle := "WIP: feat: add widget"
	if gotBody["title"] != wantTitle {
		t.Errorf("request title = %v, want %q", gotBody["title"], wantTitle)
	}
	if gotBody["head"] != "agent/issue-1964" {
		t.Errorf("request head = %v, want %q", gotBody["head"], "agent/issue-1964")
	}
	if gotBody["base"] != "main" {
		t.Errorf("request base = %v, want %q", gotBody["base"], "main")
	}
	if gotBody["body"] != "Adds a widget." {
		t.Errorf("request body = %v, want %q", gotBody["body"], "Adds a widget.")
	}
}

// A non-2xx create must produce an error, not a blank URL.
func TestReadOnlyForgejoCodeForge_CreateDraftPR_Errors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	cf := forgejo.NewReadOnlyForgejoCodeForge(forgejo.ForgejoCodeForgeConfig{
		BaseURL: srv.URL,
		Repo:    "owner/repo",
		Token:   "tok",
	}, nil)
	dpc := cf.(forge.DraftPRCreator)

	if _, _, err := dpc.CreateDraftPR("feat: add widget", "body", "main", "fail-head"); err == nil {
		t.Fatal("CreateDraftPR with a failing create: got nil error, want one")
	}
}

// On the pulls-create endpoint a 409 means "a pull request for this head
// already exists", not the "not mergeable" sense the same status carries on
// the merge endpoint (forgejoStatusMap/errMergeRefused). CreateDraftPR adopts
// the branch's open PR via OpenPRForBranch and reports created=false, as
// github's adoption does (relay.go, issue #2407 slice 1/2; issue #2447).
func TestReadOnlyForgejoCodeForge_CreateDraftPR_AdoptsExistingOnConflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/repos/owner/repo/pulls":
			w.WriteHeader(http.StatusConflict)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/owner/repo/pulls":
			if err := json.NewEncoder(w).Encode([]map[string]any{
				{
					"number":   1964,
					"html_url": "https://forge.test/owner/repo/pulls/1964",
					"draft":    false,
					"title":    "feat: add widget",
					"head":     map[string]any{"ref": "agent/issue-1964"},
				},
			}); err != nil {
				t.Fatal(err)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cf := forgejo.NewReadOnlyForgejoCodeForge(forgejo.ForgejoCodeForgeConfig{
		BaseURL: srv.URL,
		Repo:    "owner/repo",
		Token:   "tok",
	}, nil)
	dpc := cf.(forge.DraftPRCreator)

	url, created, err := dpc.CreateDraftPR("feat: add widget", "Adds a widget.", "main", "agent/issue-1964")
	if err != nil {
		t.Fatalf("CreateDraftPR: %v", err)
	}
	want := "https://forge.test/owner/repo/pulls/1964"
	if url != want {
		t.Errorf("CreateDraftPR url = %q, want %q", url, want)
	}
	if created {
		t.Error("CreateDraftPR created = true, want false for an adopted pre-existing PR")
	}
}

// CreateDraftPR always creates a draft, so the PR a retried call collides with
// on 409 is itself a draft. Only OpenPRForBranch's draft-inclusive contract
// (issue #2408) makes that adoption target resolvable (issue #2407 follow-up).
func TestReadOnlyForgejoCodeForge_CreateDraftPR_AdoptsExistingDraftOnConflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/repos/owner/repo/pulls":
			w.WriteHeader(http.StatusConflict)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/owner/repo/pulls":
			if err := json.NewEncoder(w).Encode([]map[string]any{
				{
					"number":   1964,
					"html_url": "https://forge.test/owner/repo/pulls/1964",
					"draft":    true,
					"title":    "WIP: feat: add widget",
					"head":     map[string]any{"ref": "agent/issue-1964"},
				},
			}); err != nil {
				t.Fatal(err)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cf := forgejo.NewReadOnlyForgejoCodeForge(forgejo.ForgejoCodeForgeConfig{
		BaseURL: srv.URL,
		Repo:    "owner/repo",
		Token:   "tok",
	}, nil)
	dpc := cf.(forge.DraftPRCreator)

	url, created, err := dpc.CreateDraftPR("feat: add widget", "Adds a widget.", "main", "agent/issue-1964")
	if err != nil {
		t.Fatalf("CreateDraftPR: %v", err)
	}
	want := "https://forge.test/owner/repo/pulls/1964"
	if url != want {
		t.Errorf("CreateDraftPR url = %q, want %q", url, want)
	}
	if created {
		t.Error("CreateDraftPR created = true, want false for an adopted pre-existing PR")
	}
}

// When a 409 create finds no open PR to adopt (only a closed or merged one
// exists), CreateDraftPR must return the original create error rather than
// swallow it.
func TestReadOnlyForgejoCodeForge_CreateDraftPR_ConflictWithoutOpenPRReturnsOriginalError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/repos/owner/repo/pulls":
			w.WriteHeader(http.StatusConflict)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/owner/repo/pulls":
			if err := json.NewEncoder(w).Encode([]map[string]any{}); err != nil {
				t.Fatal(err)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cf := forgejo.NewReadOnlyForgejoCodeForge(forgejo.ForgejoCodeForgeConfig{
		BaseURL: srv.URL,
		Repo:    "owner/repo",
		Token:   "tok",
	}, nil)
	dpc := cf.(forge.DraftPRCreator)

	if _, _, err := dpc.CreateDraftPR("feat: add widget", "body", "main", "agent/issue-1964"); err == nil {
		t.Fatal("CreateDraftPR with 409 and no open PR to adopt: got nil error, want the original create error")
	}
}

package forgejo_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgejo"
	"spindrift.dev/launcher/internal/forge/forgetest"
	"spindrift.dev/launcher/internal/seambundle"
)

// newReadOnlyRelayHarness sets up a real bare "remote" repo plus a fake
// Forgejo REST server (only used for Probe/PRForge plumbing the config
// wires up; RelayBundle itself never touches it), mirroring github's
// newRelayHarness.
func newReadOnlyRelayHarness(t *testing.T) (*forgetest.GitRepoFixture, forge.CodeForge) {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "Test Bot")
	t.Setenv("GIT_AUTHOR_EMAIL", "bot@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test Bot")
	t.Setenv("GIT_COMMITTER_EMAIL", "bot@example.com")

	repo := forgetest.NewGitRepoFixture(t, "main")
	fake := newFakeForgejo(t)

	cf := forgejo.NewReadOnlyForgejoCodeForge(forgejo.ForgejoCodeForgeConfig{
		BaseURL:      fake.URL(),
		Repo:         "owner/repo",
		Token:        "tok",
		BaseBranch:   "main",
		UserName:     "Test Bot",
		UserEmail:    "bot@example.com",
		BranchPrefix: "agent/issue-",
		GitRemoteURL: repo.Bare,
	})
	return repo, cf
}

// TestNewForgejoCodeForge_DoesNotImplementBundleRelay guards read-write's own
// contract: NewForgejoCodeForge (BOX_FORGE_AND_ISSUE_ACCESS=read-write, the
// Box pushes in-box) must never satisfy forge.BundleRelay, or settle's
// generic relay-before-merge (ready.go) would try to relay a bundle a
// read-write Box never wrote and block every read-write forgejo land.
func TestNewForgejoCodeForge_DoesNotImplementBundleRelay(t *testing.T) {
	cf := forgejo.NewForgejoCodeForge(forgejo.ForgejoCodeForgeConfig{
		BaseURL: "https://codeberg.org",
		Repo:    "owner/repo",
		Token:   "tok",
	})
	if _, ok := cf.(forge.BundleRelay); ok {
		t.Error("NewForgejoCodeForge satisfies forge.BundleRelay, want it hidden for read-write")
	}
}

// TestNewForgejoCodeForge_DoesNotImplementDraftPRCreator mirrors
// TestNewForgejoCodeForge_DoesNotImplementBundleRelay for
// forge.DraftPRCreator: a read-write Box already opens its own PR in-box, so
// settle must never call a host-side create for it.
func TestNewForgejoCodeForge_DoesNotImplementDraftPRCreator(t *testing.T) {
	cf := forgejo.NewForgejoCodeForge(forgejo.ForgejoCodeForgeConfig{
		BaseURL: "https://codeberg.org",
		Repo:    "owner/repo",
		Token:   "tok",
	})
	if _, ok := cf.(forge.DraftPRCreator); ok {
		t.Error("NewForgejoCodeForge satisfies forge.DraftPRCreator, want it hidden for read-write")
	}
}

// TestNewReadOnlyForgejoCodeForge_ImplementsPRForge asserts the read-only
// adapter keeps the full PRForge surface NewForgejoCodeForge has (via
// embedding) — it still opens PRs and watches CI exactly as read-write does;
// only the finished branch's hand-off differs.
func TestNewReadOnlyForgejoCodeForge_ImplementsPRForge(t *testing.T) {
	_, cf := newReadOnlyRelayHarness(t)
	if _, ok := cf.(forge.PRForge); !ok {
		t.Error("NewReadOnlyForgejoCodeForge does not satisfy forge.PRForge")
	}
}

// TestReadOnlyForgejoCodeForge_RelayBundle_PushesRefToOrigin asserts
// RelayBundle imports a Box's code-out bundle and pushes it to the real
// remote.
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

// TestReadOnlyForgejoCodeForge_RelayBundle_ReRelayForceUpdatesRef asserts a
// fix-pass retry -- a rebuilt bundle whose branch tip diverged from what an
// earlier pass already relayed -- overwrites the remote ref rather than
// being rejected as non-fast-forward.
func TestReadOnlyForgejoCodeForge_RelayBundle_ReRelayForceUpdatesRef(t *testing.T) {
	repo, cf := newReadOnlyRelayHarness(t)
	outbox := t.TempDir()
	branch := "agent/issue-1964"
	forgetest.SeedRelayBundle(t, repo.Bare, "main", outbox, branch)

	br := cf.(forge.BundleRelay)
	if err := br.RelayBundle(outbox, branch); err != nil {
		t.Fatalf("RelayBundle (first attempt): %v", err)
	}

	// Rebuild branch from a diverged history (a different marker file, same
	// name) -- a fresh clone of bare's base, not of the already-relayed ref,
	// so the new commit shares no ancestry with the one already relayed in.
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

// TestReadOnlyForgejoCodeForge_RelayBundle_MissingBundleErrors asserts an
// empty outbox (the Box never wrote a bundle) blocks the seam via an error
// rather than a nil-error no-op, mirroring local's RelayBundle (ADR 0033).
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

// TestReadOnlyForgejoCodeForge_RelayBundle_MalformedBundleErrors asserts a
// corrupt bundle file is rejected by `git bundle verify` rather than fed to
// fetch.
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

// TestReadOnlyForgejoCodeForge_RelayBundle_InvalidRefRejected asserts the
// defense-in-depth ref guard rejects an empty or flag-like ref before it
// reaches a refspec or checkout argument.
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

// TestReadOnlyForgejoCodeForge_CreateDraftPR_ReturnsURL asserts CreateDraftPR
// POSTs a WIP-prefixed draft PR to Forgejo's REST pull-create endpoint and
// returns its html_url.
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
	})
	dpc, ok := cf.(forge.DraftPRCreator)
	if !ok {
		t.Fatal("forgejo read-only CodeForge does not implement forge.DraftPRCreator")
	}

	url, err := dpc.CreateDraftPR("feat: add widget", "Adds a widget.", "main", "agent/issue-1964")
	if err != nil {
		t.Fatalf("CreateDraftPR: %v", err)
	}
	want := "https://forge.test/owner/repo/pulls/1964"
	if url != want {
		t.Errorf("CreateDraftPR url = %q, want %q", url, want)
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

// TestReadOnlyForgejoCodeForge_CreateDraftPR_Errors asserts a non-2xx
// response from Forgejo's pull-create endpoint surfaces as an error rather
// than a blank URL.
func TestReadOnlyForgejoCodeForge_CreateDraftPR_Errors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	cf := forgejo.NewReadOnlyForgejoCodeForge(forgejo.ForgejoCodeForgeConfig{
		BaseURL: srv.URL,
		Repo:    "owner/repo",
		Token:   "tok",
	})
	dpc := cf.(forge.DraftPRCreator)

	if _, err := dpc.CreateDraftPR("feat: add widget", "body", "main", "fail-head"); err == nil {
		t.Fatal("CreateDraftPR with a failing create: got nil error, want one")
	}
}

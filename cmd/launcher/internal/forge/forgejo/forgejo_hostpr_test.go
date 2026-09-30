package forgejo_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgejo"
	"spindrift.dev/launcher/internal/forge/forgetest"
)

// NewForgejoCodeForge (read-write) now issues draft-PR creates and pushes
// branches itself, on the same terms readOnlyCodeForge previously
// monopolized (issue #4071): the request reaching the fake Forgejo server is
// recorded and checked verbatim.
func TestForgejoCodeForge_CreateDraftPR_PostsAndReturnsURL(t *testing.T) {
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

	cf := forgejo.NewForgejoCodeForge(forgejo.ForgejoCodeForgeConfig{
		BaseURL: srv.URL,
		Repo:    "owner/repo",
		Token:   "tok",
	}, nil)
	dpc, ok := cf.(forge.DraftPRCreator)
	if !ok {
		t.Fatal("forgejo read-write CodeForge does not implement forge.DraftPRCreator")
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

// A refused create (422, e.g. an invalid head branch) must produce an error,
// not a blank URL. rest.Client's StatusError carries both the HTTP status
// and the forge's own message, so this checks for both.
func TestForgejoCodeForge_CreateDraftPR_RefusedErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		json.NewEncoder(w).Encode(map[string]any{"message": "head branch does not exist"})
	}))
	defer srv.Close()

	cf := forgejo.NewForgejoCodeForge(forgejo.ForgejoCodeForgeConfig{
		BaseURL: srv.URL,
		Repo:    "owner/repo",
		Token:   "tok",
	}, nil)
	dpc := cf.(forge.DraftPRCreator)

	_, _, err := dpc.CreateDraftPR("feat: add widget", "body", "main", "no-such-branch")
	if err == nil {
		t.Fatal("CreateDraftPR with a refused create: got nil error, want one")
	}
	if !strings.Contains(err.Error(), "422") {
		t.Errorf("CreateDraftPR error = %q, want it to mention the 422 status", err.Error())
	}
	if !strings.Contains(err.Error(), "head branch does not exist") {
		t.Errorf("CreateDraftPR error = %q, want it to mention the forge's message", err.Error())
	}
}

// newForgejoPushBranchHarness returns a read-write forgejo CodeForge pointed
// at a real bare git repo via the ForTest git-remote override, mirroring
// newReadOnlyRelayHarness (forgejo_readonly_test.go) but for the base adapter.
func newForgejoPushBranchHarness(t *testing.T) *forgetest.GitRepoFixture {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "Test Bot")
	t.Setenv("GIT_AUTHOR_EMAIL", "bot@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test Bot")
	t.Setenv("GIT_COMMITTER_EMAIL", "bot@example.com")

	return forgetest.NewGitRepoFixture(t, "main")
}

func newForgejoPushBranchForge(t *testing.T, bare string) forge.BranchPusher {
	t.Helper()
	cf := forgejo.NewForgejoCodeForgeForTest(forgejo.ForgejoCodeForgeConfig{
		BaseURL: "https://codeberg.org",
		Repo:    "owner/repo",
		Token:   "tok",
	}, nil, bare)
	bp, ok := cf.(forge.BranchPusher)
	if !ok {
		t.Fatal("forgejo read-write CodeForge does not implement forge.BranchPusher")
	}
	return bp
}

// PushBranch fetches localRef straight out of the local repo at srcDir, not
// out of a bundle, and pushes it onto a fresh branch on the target
// remote (issue #4071, ADR 0057).
func TestForgejoCodeForge_PushBranch_CreatesRemoteBranch(t *testing.T) {
	repo := newForgejoPushBranchHarness(t)

	src := t.TempDir()
	forgetest.Run(t, "", "clone", repo.Bare, src)
	forgetest.Run(t, src, "checkout", "-b", "work")
	forgetest.WriteFile(t, filepath.Join(src, "feature.txt"), "feature\n")
	forgetest.Run(t, src, "add", "feature.txt")
	forgetest.Run(t, src, "commit", "-m", "feature")
	wantSHA := forgetest.RevParse(t, src, "work")

	bp := newForgejoPushBranchForge(t, repo.Bare)
	branch := "agent/issue-9001"
	if err := bp.PushBranch(src, "work", branch); err != nil {
		t.Fatalf("PushBranch: %v", err)
	}

	if got := forgetest.RevParse(t, repo.Bare, "refs/heads/"+branch); got != wantSHA {
		t.Errorf("refs/heads/%s = %s, want %s", branch, got, wantSHA)
	}
}

// PushBranch is create-only (issue #4104): a second push onto a branch the
// first push already created must fail, leaving the first push's tip
// on the remote untouched.
func TestForgejoCodeForge_PushBranch_RefusesExistingBranch(t *testing.T) {
	repo := newForgejoPushBranchHarness(t)
	branch := "agent/issue-9002"
	bp := newForgejoPushBranchForge(t, repo.Bare)

	src1 := t.TempDir()
	forgetest.Run(t, "", "clone", repo.Bare, src1)
	forgetest.Run(t, src1, "checkout", "-b", "work")
	forgetest.WriteFile(t, filepath.Join(src1, "feature.txt"), "v1\n")
	forgetest.Run(t, src1, "add", "feature.txt")
	forgetest.Run(t, src1, "commit", "-m", "v1")
	if err := bp.PushBranch(src1, "work", branch); err != nil {
		t.Fatalf("PushBranch (first): %v", err)
	}
	wantSHA := forgetest.RevParse(t, repo.Bare, "refs/heads/"+branch)

	src2 := t.TempDir()
	forgetest.Run(t, "", "clone", repo.Bare, src2)
	forgetest.Run(t, src2, "checkout", "main")
	forgetest.Run(t, src2, "checkout", "-b", "work2")
	forgetest.WriteFile(t, filepath.Join(src2, "feature.txt"), "v2\n")
	forgetest.Run(t, src2, "add", "feature.txt")
	forgetest.Run(t, src2, "commit", "-m", "v2")
	if err := bp.PushBranch(src2, "work2", branch); err == nil {
		t.Fatal("PushBranch onto an already-existing branch: got nil error, want one")
	}

	if got := forgetest.RevParse(t, repo.Bare, "refs/heads/"+branch); got != wantSHA {
		t.Errorf("refs/heads/%s = %s, want %s (the first push's tip, unchanged)", branch, got, wantSHA)
	}
}

// A pre-receive hook's rejection message must reach the caller.
func TestForgejoCodeForge_PushBranch_HookRejectionSurfacesStderr(t *testing.T) {
	repo := newForgejoPushBranchHarness(t)
	hook := "#!/bin/sh\nprintf 'hook declined: protected\\n' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(repo.Bare, "hooks", "pre-receive"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}

	src := t.TempDir()
	forgetest.Run(t, "", "clone", repo.Bare, src)
	forgetest.Run(t, src, "checkout", "-b", "work")
	forgetest.WriteFile(t, filepath.Join(src, "feature.txt"), "feature\n")
	forgetest.Run(t, src, "add", "feature.txt")
	forgetest.Run(t, src, "commit", "-m", "feature")

	bp := newForgejoPushBranchForge(t, repo.Bare)
	err := bp.PushBranch(src, "work", "agent/issue-9003")
	if err == nil {
		t.Fatal("PushBranch rejected by a pre-receive hook: got nil error, want one")
	}
	if !strings.Contains(err.Error(), "hook declined: protected") {
		t.Errorf("PushBranch error = %q, want it to contain the hook's stderr", err.Error())
	}
}

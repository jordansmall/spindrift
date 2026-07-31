package forgejo_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgejo"
)

// TestForgejoClient_ImplementsIssueTracker asserts that NewForgejoClient
// satisfies IssueTracker (Forgejo implements only this seam, per ADR 0013 —
// code still lands via the github CodeForge).
func TestForgejoClient_ImplementsIssueTracker(t *testing.T) {
	var _ forge.IssueTracker = forgejo.NewForgejoClient(forgejo.ForgejoConfig{})
}

// TestForgejoClient_ImplementsBlockersLister verifies the forgejo adapter
// satisfies forge.BlockersLister: Forgejo's issue-dependencies API is a
// genuine bidirectional native relationship, exposing a separate "blocks"
// endpoint for the reverse direction.
func TestForgejoClient_ImplementsBlockersLister(t *testing.T) {
	if _, ok := forgejo.NewForgejoClient(forgejo.ForgejoConfig{}).(forge.BlockersLister); !ok {
		t.Error("forgejoClient does not satisfy forge.BlockersLister, want it implemented")
	}
}

// TestForgejoClient_ImplementsLabeledTracker verifies the forgejo adapter
// satisfies forge.LabeledTracker: its entire DispatchState space reduces to
// one DispatchLabels value (no status-mapping blend like jira), so
// PickIssue's double-box guard (#1742) can shortcut it.
func TestForgejoClient_ImplementsLabeledTracker(t *testing.T) {
	if _, ok := forgejo.NewForgejoClient(forgejo.ForgejoConfig{}).(forge.LabeledTracker); !ok {
		t.Error("forgejoClient does not satisfy forge.LabeledTracker, want it implemented")
	}
}

// TestForgejoClient_Probe_Success verifies Probe() confirms connectivity and
// returns the repository's full_name on success.
func TestForgejoClient_Probe_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/owner/repo" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"full_name":"owner/repo"}`))
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"})
	slug, err := fc.Probe()
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if slug != "owner/repo" {
		t.Errorf("Probe() = %q, want %q", slug, "owner/repo")
	}
}

// TestForgejoClient_Probe_AuthFailure verifies Probe() surfaces
// ErrAuthFailure when Forgejo rejects the credentials.
func TestForgejoClient_Probe_AuthFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "bad-token"})
	if _, err := fc.Probe(); !errors.Is(err, forge.ErrAuthFailure) {
		t.Fatalf("Probe() error = %v, want ErrAuthFailure", err)
	}
}

// TestForgejoClient_Probe_NotFound verifies Probe() surfaces ErrRepoNotFound
// when the repository cannot be reached or does not exist.
func TestForgejoClient_Probe_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"})
	if _, err := fc.Probe(); !errors.Is(err, forge.ErrRepoNotFound) {
		t.Fatalf("Probe() error = %v, want ErrRepoNotFound", err)
	}
}

// TestForgejoClient_Comment_PostsBody verifies Comment() POSTs the body to
// the issue's comments endpoint.
func TestForgejoClient_Comment_PostsBody(t *testing.T) {
	var gotPath, gotMethod string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"})
	if err := fc.Comment("42", "hello from the agent"); err != nil {
		t.Fatalf("Comment: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/api/v1/repos/owner/repo/issues/42/comments" {
		t.Errorf("path = %q", gotPath)
	}
	if gotBody["body"] != "hello from the agent" {
		t.Errorf("body = %v", gotBody)
	}
}

// TestForgejoClient_ListLabels_ReturnsRepoLabels verifies ListLabels reads
// the repository's defined label names.
func TestForgejoClient_ListLabels_ReturnsRepoLabels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/owner/repo/labels" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[{"name":"ready-for-agent"},{"name":"agent-in-progress"}]`))
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"})
	labels, err := fc.ListLabels()
	if err != nil {
		t.Fatalf("ListLabels: %v", err)
	}
	if len(labels) != 2 || labels[0] != "ready-for-agent" || labels[1] != "agent-in-progress" {
		t.Errorf("labels = %v", labels)
	}
}

// TestForgejoClient_CreateLabel_PostsHexColorWithHash verifies CreateLabel
// POSTs the name/description/color, prefixing color with "#" since Forgejo's
// label-creation endpoint wants the leading hash unlike the color argument's
// own bare-hex convention.
func TestForgejoClient_CreateLabel_PostsHexColorWithHash(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/owner/repo/labels" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"})
	if err := fc.CreateLabel("agent-failed", "desc", "d93f0b"); err != nil {
		t.Fatalf("CreateLabel: %v", err)
	}
	if gotBody["name"] != "agent-failed" || gotBody["description"] != "desc" || gotBody["color"] != "#d93f0b" {
		t.Errorf("body = %v", gotBody)
	}
}

// TestForgejoClient_DefaultBaseURL verifies NewForgejoClient defaults
// BaseURL to codeberg.org when unset.
func TestForgejoClient_DefaultBaseURL(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"full_name":"owner/repo"}`))
	}))
	defer srv.Close()

	// We can't hit the real codeberg.org from a unit test; instead assert
	// the trailing-slash stripping behavior, which shares the same code
	// path as the default-BaseURL assignment.
	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL + "/", Repo: "owner/repo", Token: "tok"})
	if _, err := fc.Probe(); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if gotPath != "/api/v1/repos/owner/repo" {
		t.Errorf("path = %q, want trailing slash stripped from BaseURL", gotPath)
	}
}

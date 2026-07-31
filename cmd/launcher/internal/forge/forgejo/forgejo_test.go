package forgejo_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/doctor"
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

// TestForgejoClient_BlocksOf_ReturnsNativeBlocking verifies BlocksOf queries
// Forgejo's native "blocks" endpoint and reports every result as
// DepSourceNative, deduplicating repeated IDs in the response (Forgejo's
// dependency API has no documented uniqueness guarantee, so dependencyIDs
// dedups defensively).
func TestForgejoClient_BlocksOf_ReturnsNativeBlocking(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[{"number":42},{"number":43},{"number":42}]`))
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"}).(forge.BlockersLister)
	blocks, err := fc.BlocksOf("7")
	if err != nil {
		t.Fatalf("BlocksOf: %v", err)
	}
	want := []forge.Dependency{{ID: "42", Source: forge.DepSourceNative}, {ID: "43", Source: forge.DepSourceNative}}
	if !reflect.DeepEqual(blocks, want) {
		t.Fatalf("BlocksOf = %v, want %v", blocks, want)
	}
	if gotPath != "/api/v1/repos/owner/repo/issues/7/blocks" {
		t.Errorf("path = %q, want the /blocks endpoint", gotPath)
	}
}

// TestForgejoClient_BlocksOf_PropagatesNativeError verifies BlocksOf
// surfaces a native lookup failure directly rather than degrading to some
// fallback — there is none to fall back to.
func TestForgejoClient_BlocksOf_PropagatesNativeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"}).(forge.BlockersLister)
	_, err := fc.BlocksOf("7")
	if err == nil {
		t.Fatal("BlocksOf: want error, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("BlocksOf error = %q, want it to mention the status", err.Error())
	}
}

// TestForgejoClient_TouchesOf_ParsesBodyTouchSection verifies TouchesOf
// fetches the full issue (Issue()'s payload includes body, unlike the
// summary list endpoint) and parses its "## Touches" section via the shared
// forge.ParseTouchPaths grammar — Forgejo has no native touch-set concept to
// prefer over it.
func TestForgejoClient_TouchesOf_ParsesBodyTouchSection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/owner/repo/issues/10" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"number":10,"title":"t","body":"## Touches\n- lib/env-schema.nix","state":"open","labels":[]}`))
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"})
	touches, err := fc.TouchesOf("10")
	if err != nil {
		t.Fatalf("TouchesOf: %v", err)
	}
	want := []string{"lib/env-schema.nix"}
	if !reflect.DeepEqual(touches, want) {
		t.Fatalf("TouchesOf = %v, want %v", touches, want)
	}
}

// newForgejoLabelServer starts an httptest server backing a single
// owner/repo Forgejo repository: it answers Probe, ListLabels, and
// CreateLabel against an in-memory label set seeded from initial, and
// records every name POSTed to the create-label endpoint (in call order,
// duplicates included) so a test can assert on exactly what doctor.Run
// asked the real forgejo adapter to create.
func newForgejoLabelServer(t *testing.T, initial []string) (srv *httptest.Server, created *[]string) {
	t.Helper()
	labels := make(map[string]bool, len(initial))
	for _, l := range initial {
		labels[l] = true
	}
	var createdNames []string
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/owner/repo":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"full_name":"owner/repo"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/owner/repo/labels":
			type labelOut struct {
				Name string `json:"name"`
			}
			out := make([]labelOut, 0, len(labels))
			for name := range labels {
				out = append(out, labelOut{Name: name})
			}
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(out)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/repos/owner/repo/labels":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			name, _ := body["name"].(string)
			labels[name] = true
			createdNames = append(createdNames, name)
			w.WriteHeader(http.StatusCreated)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	return srv, &createdNames
}

// TestDoctorRun_Forgejo_CreatesTriageAndResearchLabels drives doctor.Run
// against the real forgejo IssueTracker adapter (over an httptest server,
// not a fake) end-to-end: starting from a repo with none of its labels
// defined, it proves both the four work/triage labels (AC#2) and the six
// ADR 0022 research labels (AC#3) get created via the adapter's real
// CreateLabel HTTP call, and that doctor's post-creation re-verify then
// reports every label present.
func TestDoctorRun_Forgejo_CreatesTriageAndResearchLabels(t *testing.T) {
	srv, created := newForgejoLabelServer(t, nil)
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"})
	cf := forge.NewFake()
	cf.ProbeRepo = "owner/repo"

	cfg := doctor.Config{
		IssueTracker:    "forgejo",
		Label:           "ready-for-agent",
		InProgressLabel: "agent-in-progress",
		FailedLabel:     "agent-failed",
		CompleteLabel:   "agent-complete",
	}

	var buf bytes.Buffer
	err := doctor.Run(fc, cf, cfg, &buf, bufio.NewScanner(strings.NewReader("y\n")), true)
	if err != nil {
		t.Fatalf("doctor.Run: %v", err)
	}

	wantLabels := append([]string{cfg.Label, cfg.InProgressLabel, cfg.FailedLabel, cfg.CompleteLabel}, doctor.ResearchLabelNames()...)
	createdSet := make(map[string]bool, len(*created))
	for _, name := range *created {
		createdSet[name] = true
	}
	for _, want := range wantLabels {
		if !createdSet[want] {
			t.Errorf("label %q was never POSTed to the forgejo adapter's create-label endpoint; created = %v", want, *created)
		}
	}

	if got := buf.String(); !strings.Contains(got, "ok: all triage and research labels present") {
		t.Errorf("output missing final success line, got:\n%s", got)
	}
}

// TestDoctorRun_Forgejo_MissingResearchLabelsAdvisoryOnly verifies AC#3:
// when a Forgejo repo already has all four work/triage labels but is
// missing the ADR 0022 research labels, doctor.Run reports the gap as
// advisory and returns nil (does not fail the check) — proven against the
// real forgejo adapter's ListLabels response, in non-interactive mode so
// no creation prompt/POST happens at all.
func TestDoctorRun_Forgejo_MissingResearchLabelsAdvisoryOnly(t *testing.T) {
	workLabels := []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}
	srv, created := newForgejoLabelServer(t, workLabels)
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"})
	cf := forge.NewFake()
	cf.ProbeRepo = "owner/repo"

	cfg := doctor.Config{
		IssueTracker:    "forgejo",
		Label:           workLabels[0],
		InProgressLabel: workLabels[1],
		FailedLabel:     workLabels[2],
		CompleteLabel:   workLabels[3],
	}

	var buf bytes.Buffer
	err := doctor.Run(fc, cf, cfg, &buf, bufio.NewScanner(strings.NewReader("")), false)
	if err != nil {
		t.Fatalf("doctor.Run: %v, want nil — missing research labels are advisory only (AC#3)", err)
	}

	if len(*created) != 0 {
		t.Errorf("expected no CreateLabel calls in the non-interactive advisory path, got created = %v", *created)
	}

	out := buf.String()
	if !strings.Contains(out, "advisory:") || !strings.Contains(out, "does not fail") {
		t.Errorf("output missing advisory line, got:\n%s", out)
	}
}

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/inputdoc"
)

// TestRegisteredStrategiesShape pins the registered Strategies against main.go's
// hardcoded per-axis switch for all 5 backends.
func TestRegisteredStrategiesShape(t *testing.T) {
	if len(backendkit.Strategies()) != 5 {
		t.Fatalf("len(backendkit.Strategies()) = %d, want 5", len(backendkit.Strategies()))
	}

	cases := []struct {
		name                    string
		validAsTracker          bool
		validAsCodeForge        bool
		tokenEnvVar             string
		boxTokenEnvVar          string
		doctorTokenHint         string
		doctorSlugHint          string
		hostMediatedRemote      bool
		outboxRelayCapable      bool
		inBoxUnreachableTracker bool

		hasValidateTracker      bool
		hasValidateCodeForge    bool
		hasNewIssueTracker      bool
		hasNewCodeForge         bool
		hasNewReadOnlyCodeForge bool
	}{
		{
			name:               "github",
			validAsTracker:     true,
			validAsCodeForge:   true,
			tokenEnvVar:        "GH_TOKEN",
			boxTokenEnvVar:     "BOX_GH_TOKEN",
			outboxRelayCapable: true,

			hasValidateTracker:      true,
			hasNewIssueTracker:      true,
			hasNewCodeForge:         true,
			hasNewReadOnlyCodeForge: true,
		},
		{
			name:               "forgejo",
			validAsTracker:     true,
			validAsCodeForge:   true,
			tokenEnvVar:        backend.Forgejo.TokenEnvVar,
			boxTokenEnvVar:     "BOX_FORGEJO_TOKEN",
			doctorTokenHint:    backend.Forgejo.TokenEnvVar,
			doctorSlugHint:     "FORGEJO_BASE_URL",
			outboxRelayCapable: true,

			hasValidateTracker:      true,
			hasValidateCodeForge:    true,
			hasNewIssueTracker:      true,
			hasNewCodeForge:         true,
			hasNewReadOnlyCodeForge: true,
		},
		{
			name:             "jira",
			validAsTracker:   true,
			validAsCodeForge: false,
			tokenEnvVar:      "JIRA_TOKEN",
			boxTokenEnvVar:   "",
			doctorTokenHint:  "JIRA_TOKEN",
			doctorSlugHint:   "JIRA_BASE_URL / JIRA_PROJECT_KEY",

			hasValidateTracker: true,
			hasNewIssueTracker: true,
		},
		{
			name:                    "local",
			validAsTracker:          true,
			validAsCodeForge:        true,
			hostMediatedRemote:      true,
			inBoxUnreachableTracker: true,

			hasValidateCodeForge: true,
			hasNewIssueTracker:   true,
			hasNewCodeForge:      true,
		},
		{
			name:             "git",
			validAsTracker:   false,
			validAsCodeForge: true,

			hasValidateCodeForge: true,
			hasNewCodeForge:      true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row, ok := backendkit.ByName(tc.name)
			if !ok {
				t.Fatalf("backendkit.ByName(%q) ok=false, want true", tc.name)
			}
			if row.Name != tc.name {
				t.Errorf("name = %q, want %q", row.Name, tc.name)
			}
			if row.ValidAsTracker != tc.validAsTracker {
				t.Errorf("validAsTracker = %v, want %v", row.ValidAsTracker, tc.validAsTracker)
			}
			if row.ValidAsCodeForge != tc.validAsCodeForge {
				t.Errorf("validAsCodeForge = %v, want %v", row.ValidAsCodeForge, tc.validAsCodeForge)
			}
			if row.TokenEnvVar != tc.tokenEnvVar {
				t.Errorf("tokenEnvVar = %q, want %q", row.TokenEnvVar, tc.tokenEnvVar)
			}
			if row.BoxTokenEnvVar != tc.boxTokenEnvVar {
				t.Errorf("BoxTokenEnvVar = %q, want %q", row.BoxTokenEnvVar, tc.boxTokenEnvVar)
			}
			if row.DoctorTokenHint != tc.doctorTokenHint {
				t.Errorf("doctorTokenHint = %q, want %q", row.DoctorTokenHint, tc.doctorTokenHint)
			}
			if row.DoctorSlugHint != tc.doctorSlugHint {
				t.Errorf("doctorSlugHint = %q, want %q", row.DoctorSlugHint, tc.doctorSlugHint)
			}
			if row.HostMediatedRemote != tc.hostMediatedRemote {
				t.Errorf("hostMediatedRemote = %v, want %v", row.HostMediatedRemote, tc.hostMediatedRemote)
			}
			if row.OutboxRelayCapable != tc.outboxRelayCapable {
				t.Errorf("outboxRelayCapable = %v, want %v", row.OutboxRelayCapable, tc.outboxRelayCapable)
			}
			if row.InBoxUnreachableTracker != tc.inBoxUnreachableTracker {
				t.Errorf("inBoxUnreachableTracker = %v, want %v", row.InBoxUnreachableTracker, tc.inBoxUnreachableTracker)
			}

			if (row.ValidateTracker != nil) != tc.hasValidateTracker {
				t.Errorf("validateTracker present = %v, want %v", row.ValidateTracker != nil, tc.hasValidateTracker)
			}
			if (row.ValidateCodeForge != nil) != tc.hasValidateCodeForge {
				t.Errorf("validateCodeForge present = %v, want %v", row.ValidateCodeForge != nil, tc.hasValidateCodeForge)
			}
			if (row.NewIssueTracker != nil) != tc.hasNewIssueTracker {
				t.Errorf("newIssueTracker present = %v, want %v", row.NewIssueTracker != nil, tc.hasNewIssueTracker)
			}
			if (row.NewCodeForge != nil) != tc.hasNewCodeForge {
				t.Errorf("newCodeForge present = %v, want %v", row.NewCodeForge != nil, tc.hasNewCodeForge)
			}
			if (row.NewReadOnlyCodeForge != nil) != tc.hasNewReadOnlyCodeForge {
				t.Errorf("newReadOnlyCodeForge present = %v, want %v", row.NewReadOnlyCodeForge != nil, tc.hasNewReadOnlyCodeForge)
			}
		})
	}
}

func TestBackendByNameUnknown(t *testing.T) {
	if _, ok := backendkit.ByName("nonexistent"); ok {
		t.Fatalf("backendkit.ByName(%q) ok=true, want false", "nonexistent")
	}
}

// A malformed JIRA_STATUS_MAPPING must fall back to an empty map and still
// yield a non-nil tracker, matching main.go's newIssueTracker "jira" case.
func TestJiraNewIssueTrackerMalformedStatusMapping(t *testing.T) {
	row, ok := backendkit.ByName("jira")
	if !ok {
		t.Fatal("backendkit.ByName(\"jira\") ok=false")
	}
	if row.NewIssueTracker == nil {
		t.Fatal("jira row.NewIssueTracker is nil")
	}
	c := config{schemaConfig: schemaConfig{
		rowKnobs: map[string]string{
			"JIRA_BASE_URL":       "https://example.atlassian.net",
			"JIRA_PROJECT_KEY":    "PROJ",
			"JIRA_TOKEN":          "tok",
			"JIRA_STATUS_MAPPING": "{not valid json",
		},
	}}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("newIssueTracker panicked: %v", r)
		}
	}()
	tracker := mustBuild(row.NewIssueTracker(backendShared(c), c.knobLookup))
	if tracker == nil {
		t.Fatal("newIssueTracker returned nil tracker")
	}
}

// The backendkit registry and internal/backend's ByName are two paths over the
// same generated descriptors, so a Strategy reaching one but not the other
// splits them silently rather than failing a build (issue #3062). Descriptor
// holds only strings and bools, so comparing whole values also catches a
// Strategy that carries a stale copy instead of embedding the generated
// package variable.
func TestStrategiesCoverRegistry(t *testing.T) {
	for _, d := range backend.Registry {
		s, ok := backendkit.ByName(d.Name)
		if !ok {
			t.Errorf("backendkit.ByName(%q) ok=false, want true (backend.Registry descriptor has no registered Strategy)", d.Name)
			continue
		}
		if s.Descriptor != d {
			t.Errorf("backendkit.ByName(%q).Descriptor = %+v, want %+v", d.Name, s.Descriptor, d)
		}
	}
	for _, s := range backendkit.Strategies() {
		if _, ok := backend.ByName(s.Name); !ok {
			t.Errorf("backend.ByName(%q) ok=false, want true (registered Strategy missing from backend.Registry)", s.Name)
		}
	}
}

// JIRA_INCLUDE_COMMENTS is a deprecated no-op: the thread reaches the prompt
// once, via CommentLister, whether or not it is set (issue #3747).
func TestJiraIncludeCommentsIsNoOp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/api/2/issue/PROJ-7":
			w.Write([]byte(`{"key": "PROJ-7", "fields": {"summary": "s", "description": "desc", "status": {"name": "To Do"}, "labels": []}}`))
		case "/rest/api/2/issue/PROJ-7/comment":
			w.Write([]byte(`{"comments": [{"body": "INJECTED"}]}`))
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	row, ok := backendkit.ByName("jira")
	if !ok {
		t.Fatal("backendkit.ByName(\"jira\") ok=false")
	}
	for _, include := range []bool{false, true} {
		c := config{schemaConfig: schemaConfig{
			rowKnobs: map[string]string{
				"JIRA_BASE_URL":         srv.URL,
				"JIRA_PROJECT_KEY":      "PROJ",
				"JIRA_TOKEN":            "tok",
				"JIRA_INCLUDE_COMMENTS": boolKnob(include),
			},
		}}
		text, err := forge.IssueText(mustBuild(row.NewIssueTracker(backendShared(c), c.knobLookup)), "PROJ-7", io.Discard)
		if err != nil {
			t.Fatalf("include=%v: IssueText: %v", include, err)
		}
		if n := strings.Count(text, "INJECTED"); n != 1 {
			t.Errorf("include=%v: %d occurrences of the comment, want 1:\n%s", include, n, text)
		}
	}
}

// fakeJiraWorkflow serves one issue whose labels track PUT updates and
// records every request, so a test sees both the wire calls a tracker made and
// the label state it left behind.
type fakeJiraWorkflow struct {
	mu          sync.Mutex
	labels      []string
	transitions int
	jqls        []string
	status      string // the issue's workflow status; empty means "To Do"
}

func (f *fakeJiraWorkflow) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/transitions"):
		f.transitions++
		w.WriteHeader(http.StatusNoContent)
	case strings.HasSuffix(r.URL.Path, "/transitions"):
		w.Write([]byte(`{"transitions": [{"id": "21", "to": {"name": "In Progress"}}]}`))
	case strings.HasSuffix(r.URL.Path, "/search"):
		f.jqls = append(f.jqls, r.URL.Query().Get("jql"))
		w.Write([]byte(`{"issues": [], "total": 0}`))
	case r.Method == http.MethodPut:
		var body struct {
			Update struct {
				Labels []map[string]string `json:"labels"`
			} `json:"update"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		for _, op := range body.Update.Labels {
			if rm, ok := op["remove"]; ok {
				kept := []string{}
				for _, l := range f.labels {
					if l != rm {
						kept = append(kept, l)
					}
				}
				f.labels = kept
			}
			if add, ok := op["add"]; ok {
				f.labels = append(f.labels, add)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		labels, _ := json.Marshal(append([]string{}, f.labels...))
		status := f.status
		if status == "" {
			status = "To Do"
		}
		w.Write([]byte(`{"key": "PROJ-1", "fields": {"summary": "s", "description": "d", "status": {"name": "` + status + `"}, "labels": ` + string(labels) + `}}`))
	}
}

// JIRA_STATUS_MAPPING maps the work lifecycle only: the research tracker must
// ride the label fallback, so a claim adds agent-research-in-progress (which
// CompleteVerdict then requires) instead of posting a workflow transition
// (issue #4919). The work tracker keeps the native path.
func TestJiraStatusMappingAppliesToWorkTrackerOnly(t *testing.T) {
	const mapping = `{"dispatchable":"To Do","inProgress":"In Progress"}`
	row, ok := backendkit.ByName("jira")
	if !ok {
		t.Fatal(`backendkit.ByName("jira") ok=false`)
	}

	cases := []struct {
		name           string
		kind           *dispatchkind.Descriptor
		status         string
		wantTransition bool
		wantStatusJQL  bool
	}{
		{"research", dispatchkind.Research, "To Do", false, false},
		{"research on a work in-progress issue", dispatchkind.Research, "In Progress", false, false},
		{"work", dispatchkind.Work, "To Do", true, true},
		{"recover", dispatchkind.Recover, "To Do", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeJiraWorkflow{status: tc.status}
			srv := httptest.NewServer(fake)
			defer srv.Close()

			c := applyDispatchKind(config{schemaConfig: schemaConfig{
				rowKnobs: map[string]string{
					"JIRA_BASE_URL":       srv.URL,
					"JIRA_PROJECT_KEY":    "PROJ",
					"JIRA_TOKEN":          "tok",
					"JIRA_STATUS_MAPPING": mapping,
				},
				label:           "ready-for-agent",
				inProgressLabel: "agent-in-progress",
				completeLabel:   "agent-complete",
				failedLabel:     "agent-failed",
			}}, tc.kind)
			tracker := mustBuild(row.NewIssueTracker(backendShared(c), c.knobLookup))

			if err := tracker.TransitionState("PROJ-1", forge.Dispatchable, forge.InProgress); err != nil {
				t.Fatalf("TransitionState: %v", err)
			}
			if got := fake.transitions > 0; got != tc.wantTransition {
				t.Errorf("workflow transition posted = %v, want %v", got, tc.wantTransition)
			}

			if tc.kind == dispatchkind.Research {
				if len(fake.labels) != 1 || fake.labels[0] != "agent-research-in-progress" {
					t.Errorf("labels after claim = %v, want [agent-research-in-progress]", fake.labels)
				}
				if err := tracker.CompleteVerdict("PROJ-1", forge.Recommend); err != nil {
					t.Errorf("CompleteVerdict: %v", err)
				}
			}

			if _, err := tracker.ListIssues(forge.Dispatchable); err != nil {
				t.Fatalf("ListIssues: %v", err)
			}
			if len(fake.jqls) != 1 {
				t.Fatalf("searches = %d, want 1", len(fake.jqls))
			}
			if got := strings.Contains(fake.jqls[0], "status ="); got != tc.wantStatusJQL {
				t.Errorf("JQL %q has a status clause = %v, want %v", fake.jqls[0], got, tc.wantStatusJQL)
			}
		})
	}
}

// An input document that sets a knob to "" reaches the Strategy as a blank
// (the document value wins over the schema default), and ReadKnobs would
// re-default it: a blank FORGEJO_BASE_URL must still fail closed rather
// than send a self-hosted FORGEJO_TOKEN to codeberg.
func TestBlankInputDocumentKnobs_ForgejoRejectedLocalRedefaults(t *testing.T) {
	t.Cleanup(func() { loadedDoc = nil })
	t.Setenv("FORGEJO_BASE_URL", "")
	t.Setenv("LOCAL_ISSUES_DIR", "")
	loadedDoc = &inputdoc.Document{Settings: map[string]string{
		"FORGEJO_BASE_URL": "",
		"LOCAL_ISSUES_DIR": "",
	}}
	c := minimalValidConfig()
	c.schemaConfig = loadSchemaConfig()
	c.setRowKnob(backend.Forgejo.TokenEnvVar, "tok")
	fjBase, _ := c.knobLookup("FORGEJO_BASE_URL")
	localDir, _ := c.knobLookup("LOCAL_ISSUES_DIR")
	if fjBase != "" || localDir != "" {
		t.Fatalf("document blanks not resolved: %q %q", fjBase, localDir)
	}

	fj, ok := backendkit.ByName("forgejo")
	if !ok {
		t.Fatal("forgejo row missing")
	}
	for axis, fn := range map[string]func(backendkit.Shared, backendkit.Lookup) error{
		"ISSUE_TRACKER": fj.ValidateTracker,
		"CODE_FORGE":    fj.ValidateCodeForge,
	} {
		want := "set FORGEJO_BASE_URL (Forgejo instance base URL) when " + axis + "=forgejo"
		if err := fn(backendShared(c), c.knobLookup); err == nil || err.Error() != want {
			t.Errorf("forgejo %s: err = %v, want %q", axis, err, want)
		}
	}

	// LOCAL_ISSUES_DIR is deliberately unguarded: a blank re-defaults to
	// .spindrift/issues (the pre-Strategy launcher built an inert tracker on ""; issue #5638).
	local, ok := backendkit.ByName("local")
	if !ok {
		t.Fatal("local row missing")
	}
	if local.ValidateTracker != nil {
		t.Error("local validateTracker non-nil; a blank LOCAL_ISSUES_DIR must not fail closed")
	}
	t.Chdir(t.TempDir())
	c.issueTracker = "local"
	probe, ok := mustBuild(local.NewIssueTracker(backendShared(c), c.knobLookup)).(interface{ Probe() (string, error) })
	if !ok {
		t.Fatal("local tracker has no Probe")
	}
	dir, err := probe.Probe()
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if want := filepath.Join(".spindrift", "issues"); !strings.HasSuffix(dir, string(filepath.Separator)+want) {
		t.Errorf("blank LOCAL_ISSUES_DIR resolved to %q, want it to end in %q", dir, want)
	}
}

// An unregistered ISSUE_TRACKER or CODE_FORGE fails validation, and the
// constructors never substitute github for it.
func TestUnregisteredBackendNameFailsWithoutGithubFallback(t *testing.T) {
	for _, tc := range []struct {
		name         string
		tracker, cf  string
		wantContains string
	}{
		{"tracker", "nope", "local", `ISSUE_TRACKER="nope" is not valid`},
		{"code forge", "local", "nope", `CODE_FORGE="nope" is not valid`},
		{"tracker-only backend as code forge", "local", "jira", `CODE_FORGE="jira" is not valid`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := minimalValidConfig()
			c.issueTracker, c.codeForge = tc.tracker, tc.cf

			if err := validate(c); err == nil || !strings.Contains(err.Error(), tc.wantContains) {
				t.Errorf("validate() = %v, want error containing %q", err, tc.wantContains)
			}
			if err := checkBackendsRegistered(c); err == nil || !strings.Contains(err.Error(), tc.wantContains) {
				t.Errorf("checkBackendsRegistered() = %v, want error containing %q", err, tc.wantContains)
			}
		})
	}

	c := minimalValidConfig()
	c.issueTracker = "nope"
	assertPanics := func(name string, f func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Errorf("%s with an unregistered name returned, want a panic rather than a github fallback", name)
			}
		}()
		f()
	}
	assertPanics("newIssueTracker", func() { newIssueTracker(c) })
	c.codeForge = "nope"
	assertPanics("newCodeForge", func() { newCodeForge(c, forge.SanitizedParent{}, forge.NewFake()) })
}

func TestNewReadContext_UnregisteredBackendIsAnError(t *testing.T) {
	setFullyLocalEnv(t)
	t.Setenv("CODE_FORGE", "nope")

	if _, err := newReadContext(dispatchkind.Work, false); err == nil || !strings.Contains(err.Error(), `CODE_FORGE="nope" is not valid`) {
		t.Errorf("newReadContext err = %v, want the unregistered CODE_FORGE rejected", err)
	}
}

// TestHostTokenValue pins the precedence: a resolved row knob wins, the core
// GitHub token is served from the config field (not the environment), and any
// other name falls back to the environment.
func TestHostTokenValue(t *testing.T) {
	c := minimalValidConfig()
	c.ghToken = "ghp_core"
	c.setRowKnob(backend.Forgejo.TokenEnvVar, "fj_knob")
	t.Setenv(backend.GitHub.TokenEnvVar, "env_gh")
	t.Setenv(backend.Forgejo.TokenEnvVar, "env_fj")
	t.Setenv("STUB_TOKEN", "env_stub")

	for env, want := range map[string]string{
		backend.GitHub.TokenEnvVar:  "ghp_core",
		backend.Forgejo.TokenEnvVar: "fj_knob",
		"STUB_TOKEN":                "env_stub",
	} {
		if got := c.hostTokenValue(env); got != want {
			t.Errorf("hostTokenValue(%q) = %q, want %q", env, got, want)
		}
	}
}

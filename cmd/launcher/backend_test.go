package main

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backend/backendrows"
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
		doctorSlugHint          string
		hostMediatedRemote      bool
		hostSeededMirror        bool
		outboxRelayCapable      bool
		inBoxUnreachableTracker bool

		hasValidateTracker   bool
		hasValidateCodeForge bool
		hasNewIssueTracker   bool
		hasNewCodeForge      bool
	}{
		{
			name:               "github",
			validAsTracker:     true,
			validAsCodeForge:   true,
			tokenEnvVar:        "GH_TOKEN",
			outboxRelayCapable: true,

			hasValidateTracker: true,
			hasNewIssueTracker: true,
			hasNewCodeForge:    true,
		},
		{
			name:               "forgejo",
			validAsTracker:     true,
			validAsCodeForge:   true,
			tokenEnvVar:        backend.Forgejo.TokenEnvVar,
			doctorSlugHint:     "FORGEJO_BASE_URL",
			outboxRelayCapable: true,

			hasValidateTracker:   true,
			hasValidateCodeForge: true,
			hasNewIssueTracker:   true,
			hasNewCodeForge:      true,
		},
		{
			name:             "jira",
			validAsTracker:   true,
			validAsCodeForge: false,
			tokenEnvVar:      "JIRA_TOKEN",
			doctorSlugHint:   "JIRA_BASE_URL / JIRA_PROJECT_KEY",

			hasValidateTracker: true,
			hasNewIssueTracker: true,
		},
		{
			name:                    "local",
			validAsTracker:          true,
			validAsCodeForge:        true,
			hostMediatedRemote:      true,
			hostSeededMirror:        true,
			inBoxUnreachableTracker: true,

			hasValidateCodeForge: true,
			hasNewIssueTracker:   true,
			hasNewCodeForge:      true,
		},
		{
			name:             "git",
			validAsTracker:   false,
			validAsCodeForge: true,
			hostSeededMirror: true,

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
			if row.DoctorSlugHint != tc.doctorSlugHint {
				t.Errorf("doctorSlugHint = %q, want %q", row.DoctorSlugHint, tc.doctorSlugHint)
			}
			if row.HostMediatedRemote != tc.hostMediatedRemote {
				t.Errorf("hostMediatedRemote = %v, want %v", row.HostMediatedRemote, tc.hostMediatedRemote)
			}
			if row.HostSeededMirror != tc.hostSeededMirror {
				t.Errorf("hostSeededMirror = %v, want %v", row.HostSeededMirror, tc.hostSeededMirror)
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
		})
	}
}

func TestBackendByNameUnknown(t *testing.T) {
	if _, ok := backendkit.ByName("nonexistent"); ok {
		t.Fatalf("backendkit.ByName(%q) ok=true, want false", "nonexistent")
	}
}

// A malformed JIRA_STATUS_MAPPING is an error from the Connector-built tracker,
// not a silent fall back to the label-only lifecycle: the Connector has no
// native adapter to degrade to, and callers validate the mapping first.
func TestJiraNewIssueTrackerMalformedStatusMapping(t *testing.T) {
	row, ok := backendkit.ByName("jira")
	if !ok {
		t.Fatal("backendkit.ByName(\"jira\") ok=false")
	}
	t.Cleanup(backendrows.Snapshot())
	doc := `[{"name":"jira","module":"/nonexistent.wasm","credentialEnv":"JIRA_TOKEN",
"routes":[{"method":"GET","host":"jira.example","path":"/x"}]}]`
	if err := backendrows.Use([]byte(doc), func(string) string { return "" }); err != nil {
		t.Fatal(err)
	}
	c := config{Knobs: inputdoc.Knobs{
		RowKnobs: map[string]string{
			"JIRA_BASE_URL":       "https://example.atlassian.net",
			"JIRA_PROJECT_KEY":    "PROJ",
			"JIRA_TOKEN":          "tok",
			"JIRA_STATUS_MAPPING": "{not valid json",
		},
	}}
	if _, err := row.NewIssueTracker(backendShared(c), c.knobLookup); err == nil || !strings.Contains(err.Error(), "JIRA_STATUS_MAPPING") {
		t.Fatalf("err = %v, want one naming JIRA_STATUS_MAPPING", err)
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
		if !reflect.DeepEqual(s.Descriptor, d) {
			t.Errorf("backendkit.ByName(%q).Descriptor = %+v, want %+v", d.Name, s.Descriptor, d)
		}
	}
	for _, s := range backendkit.Strategies() {
		if _, ok := backend.ByName(s.Name); !ok {
			t.Errorf("backend.ByName(%q) ok=false, want true (registered Strategy missing from backend.Registry)", s.Name)
		}
	}
}

// An input document that sets a knob to "" reaches the Strategy as a blank
// (the document value wins over the schema default), and ReadKnobs would
// re-default it: a blank FORGEJO_BASE_URL must still fail closed rather
// than send a self-hosted FORGEJO_TOKEN to codeberg.
func TestBlankInputDocumentKnobs_ForgejoRejectedLocalRedefaults(t *testing.T) {
	setKnob(t, "FORGEJO_BASE_URL", "")
	setKnob(t, "LOCAL_ISSUES_DIR", "")
	useDoc(t, &inputdoc.Document{Settings: map[string]string{
		"FORGEJO_BASE_URL": "",
		"LOCAL_ISSUES_DIR": "",
	}})
	c := minimalValidConfig()
	c.Knobs = loadSchemaConfig()
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
	c.IssueTracker = "local"
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
			c.IssueTracker, c.CodeForge = tc.tracker, tc.cf

			if err := validate(c); err == nil || !strings.Contains(err.Error(), tc.wantContains) {
				t.Errorf("validate() = %v, want error containing %q", err, tc.wantContains)
			}
			if err := checkBackendsRegistered(c); err == nil || !strings.Contains(err.Error(), tc.wantContains) {
				t.Errorf("checkBackendsRegistered() = %v, want error containing %q", err, tc.wantContains)
			}
		})
	}

	c := minimalValidConfig()
	c.IssueTracker = "nope"
	assertPanics := func(name string, f func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Errorf("%s with an unregistered name returned, want a panic rather than a github fallback", name)
			}
		}()
		f()
	}
	assertPanics("newIssueTracker", func() { mustBuild(newIssueTracker(c)) })
	c.CodeForge = "nope"
	assertPanics("newCodeForge", func() { _, _ = newCodeForge(c, forge.SanitizedParent{}, forge.NewFake()) })
}

func TestNewReadContext_UnregisteredBackendIsAnError(t *testing.T) {
	setFullyLocalEnv(t)
	setKnob(t, "CODE_FORGE", "nope")

	if _, err := newReadContext(dispatchkind.Work, false); err == nil || !strings.Contains(err.Error(), `CODE_FORGE="nope" is not valid`) {
		t.Errorf("newReadContext err = %v, want the unregistered CODE_FORGE rejected", err)
	}
}

// setRowKnob stores a row knob's resolved value, as a test fixture would set
// it. It copies the map first so a config copied by value never shares writes
// with the original.
func (c *config) setRowKnob(env, value string) {
	m := make(map[string]string, len(c.RowKnobs)+1)
	for k, v := range c.RowKnobs {
		m[k] = v
	}
	m[env] = value
	c.RowKnobs = m
}

package inputdoc

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func withSchemaFlags(t *testing.T, rows ...FlagEntry) {
	t.Helper()
	orig := SchemaFlags
	t.Cleanup(func() { SchemaFlags = orig })
	SchemaFlags = rows
}

func ambientText(env map[string]string) string {
	if err := AmbientKnobError(env); err != nil {
		return err.Error()
	}
	return ""
}

// ADR 0020 requires the error to say where the knob belongs, so for a
// flakeOption-backed knob it names the variable, its flag, and its domain-tree
// path (the knob's derived flake path, ADR 0037 Pass 2).
func TestAmbientKnobError_NamesFlagAndSettingsEquivalent(t *testing.T) {
	withSchemaFlags(t, FlagEntry{Env: "BASE_BRANCH", Flag: "base-branch", SettingsPath: "git.baseBranch"})

	out := ambientText(map[string]string{"BASE_BRANCH": "develop"})

	want := "BASE_BRANCH=develop set in environment — knob env vars are not read; use --base-branch or git.baseBranch"
	if out != want {
		t.Errorf("error = %q, want %q", out, want)
	}
}

// A knob with no flakeOption behind it has no settings equivalent, so the
// fixture leaves settingsPath empty and the error names only the flag.
func TestAmbientKnobError_NoSettingsPath_FlagOnly(t *testing.T) {
	withSchemaFlags(t, FlagEntry{Env: "ISSUE_NUMBER", Flag: "issue-number"})

	out := ambientText(map[string]string{"ISSUE_NUMBER": "42"})

	if !strings.Contains(out, "--issue-number") {
		t.Errorf("error = %q, want it to mention --issue-number", out)
	}
	if strings.Contains(out, " or ") {
		t.Errorf("error = %q, want just the flag (no second migration target) for a non-flakeOption knob", out)
	}
}

// A launcherIgnores knob's flag is inert (see FlagEntry), so the error must
// name only the settings path (issue #3698).
func TestAmbientKnobError_LauncherIgnores_SettingsPathOnly(t *testing.T) {
	withSchemaFlags(t, FlagEntry{Env: "DAEMON_IDLE_FLOOR", Flag: "daemon-idle-floor", SettingsPath: "dispatch.daemonIdleFloor", LauncherIgnores: true})

	out := ambientText(map[string]string{"DAEMON_IDLE_FLOOR": "99h"})

	for _, want := range []string{"DAEMON_IDLE_FLOOR", "dispatch.daemonIdleFloor"} {
		if !strings.Contains(out, want) {
			t.Errorf("error = %q, want it to mention %q", out, want)
		}
	}
	if strings.Contains(out, "--daemon-idle-floor") {
		t.Errorf("error = %q, want no flag suggestion for a launcher-ignored knob", out)
	}
	if strings.Contains(out, " or ") {
		t.Errorf("error = %q, want just the settings path (no flag alternative)", out)
	}
}

func TestAmbientKnobError_UnsetKnob_Passes(t *testing.T) {
	withSchemaFlags(t, FlagEntry{Env: "MAX_PARALLEL", Flag: "max-parallel", SettingsPath: "dispatch.maxParallel"})

	if out := ambientText(map[string]string{}); out != "" {
		t.Errorf("error = %q, want empty for an unset knob", out)
	}
}

// A bool knob's explicit off is set-but-empty, and it overrides the
// document's value, so it owes the same ADR 0020 error.
func TestAmbientKnobError_SetButEmptyBoolKnob_Errors(t *testing.T) {
	withSchemaFlags(t, FlagEntry{Env: "AUTO_FORMAT", Flag: "auto-format", Kind: "bool", SettingsPath: "agents.format.enable"})

	out := ambientText(map[string]string{"AUTO_FORMAT": ""})

	for _, want := range []string{`AUTO_FORMAT=""`, "agents.format.enable"} {
		if !strings.Contains(out, want) {
			t.Errorf("error = %q, want it to mention %q", out, want)
		}
	}
}

// An emptyDisables string knob's "" is a real setting (it disables the limit),
// so it owes the ADR 0020 error like a bool knob's explicit off.
func TestAmbientKnobError_EmptyDisablesKnobSetEmpty_Errors(t *testing.T) {
	withSchemaFlags(t, FlagEntry{Env: "MEMORY_LIMIT", Flag: "memory-limit", SettingsPath: "box.memoryLimit", EmptyDisables: true})

	out := ambientText(map[string]string{"MEMORY_LIMIT": ""})

	for _, want := range []string{`MEMORY_LIMIT=""`, "box.memoryLimit"} {
		if !strings.Contains(out, want) {
			t.Errorf("error = %q, want it to mention %q", out, want)
		}
	}
}

func TestAmbientKnobError_SetButEmptyNonBoolKnob_Passes(t *testing.T) {
	withSchemaFlags(t, FlagEntry{Env: "MAX_PARALLEL", Flag: "max-parallel", SettingsPath: "dispatch.maxParallel"})

	if out := ambientText(map[string]string{"MAX_PARALLEL": ""}); out != "" {
		t.Errorf("error = %q, want empty for a set-but-empty non-bool knob", out)
	}
}

// Secrets and non-knob plumbing are not SchemaFlags rows, so they stay legal
// ambient input against the real generated table.
func TestAmbientKnobError_SecretAndPlumbing_Pass(t *testing.T) {
	env := map[string]string{"GH_TOKEN": "secret", "DRIVER": "claude", "PATH": "/usr/bin"}
	if out := ambientText(env); out != "" {
		t.Errorf("error = %q, want empty for a secret and non-knob variables", out)
	}
}

func docWith(settings map[string]string) *Document { return &Document{Settings: settings} }

func mustResolve(t *testing.T, args []string, doc *Document, env map[string]string) Resolved {
	t.Helper()
	r, err := Resolve(args, doc, env)
	if err != nil {
		t.Fatalf("Resolve(%v) = %v", args, err)
	}
	return r
}

func TestResolve_Precedence(t *testing.T) {
	cases := []struct {
		name string
		args []string
		doc  *Document
		get  func(Resolved) any
		want any
	}{
		{"string default", nil, nil,
			func(r Resolved) any { return r.Knobs.BaseBranch }, "main"},
		{"settings beat default", nil, docWith(map[string]string{"BASE_BRANCH": "trunk"}),
			func(r Resolved) any { return r.Knobs.BaseBranch }, "trunk"},
		{"flag beats settings", []string{"--base-branch", "dev"}, docWith(map[string]string{"BASE_BRANCH": "trunk"}),
			func(r Resolved) any { return r.Knobs.BaseBranch }, "dev"},
		{"empty on a plain string falls through to settings", []string{"--base-branch", ""}, docWith(map[string]string{"BASE_BRANCH": "trunk"}),
			func(r Resolved) any { return r.Knobs.BaseBranch }, "trunk"},
		{"empty on a plain string falls through to default", []string{"--base-branch", ""}, nil,
			func(r Resolved) any { return r.Knobs.BaseBranch }, "main"},
		{"deprecated alias is the flag", []string{"--box-forge-and-issue-access", "read-only"}, docWith(map[string]string{"BOX_FORGE_AND_ISSUE_ACCESS": "read-write"}),
			func(r Resolved) any { return r.Knobs.BoxForgeAndIssueAccess }, "read-only"},
		{"canonical name of an aliased flag", []string{"--box-access", "read-only"}, nil,
			func(r Resolved) any { return r.Knobs.BoxForgeAndIssueAccess }, "read-only"},
		{"bool default is off", nil, nil,
			func(r Resolved) any { return r.Knobs.ContinuousDispatch }, false},
		{"bool settings on", nil, docWith(map[string]string{"CONTINUOUS_DISPATCH": "1"}),
			func(r Resolved) any { return r.Knobs.ContinuousDispatch }, true},
		{"bare bool flag is on", []string{"--continuous-dispatch"}, nil,
			func(r Resolved) any { return r.Knobs.ContinuousDispatch }, true},
		{"bool =true is on", []string{"--continuous-dispatch=true"}, nil,
			func(r Resolved) any { return r.Knobs.ContinuousDispatch }, true},
		{"set-but-empty bool beats settings on", []string{"--continuous-dispatch=false"}, docWith(map[string]string{"CONTINUOUS_DISPATCH": "1"}),
			func(r Resolved) any { return r.Knobs.ContinuousDispatch }, false},
		{"bool = with no value is off", []string{"--continuous-dispatch="}, docWith(map[string]string{"CONTINUOUS_DISPATCH": "1"}),
			func(r Resolved) any { return r.Knobs.ContinuousDispatch }, false},
		{"empty document bool is off", nil, docWith(map[string]string{"CONTINUOUS_DISPATCH": ""}),
			func(r Resolved) any { return r.Knobs.ContinuousDispatch }, false},
		{"emptyDisables default", nil, nil,
			func(r Resolved) any { return r.Knobs.MemoryLimit }, "5g"},
		{"empty on an emptyDisables knob beats settings", []string{"--memory-limit", ""}, docWith(map[string]string{"MEMORY_LIMIT": "9g"}),
			func(r Resolved) any { return r.Knobs.MemoryLimit }, ""},
		{"empty document value on an emptyDisables knob beats default", nil, docWith(map[string]string{"MEMORY_LIMIT": ""}),
			func(r Resolved) any { return r.Knobs.MemoryLimit }, ""},
		{"positive int default", nil, nil,
			func(r Resolved) any { return r.Knobs.ButlerPatchMaxFiles }, 3},
		{"positive int settings", nil, docWith(map[string]string{"BUTLER_PATCH_MAX_FILES": "7"}),
			func(r Resolved) any { return r.Knobs.ButlerPatchMaxFiles }, 7},
		{"positive int flag beats settings", []string{"--butler-patch-max-files", "9"}, docWith(map[string]string{"BUTLER_PATCH_MAX_FILES": "7"}),
			func(r Resolved) any { return r.Knobs.ButlerPatchMaxFiles }, 9},
		{"malformed int flag falls back to settings", []string{"--butler-patch-max-files", "x"}, docWith(map[string]string{"BUTLER_PATCH_MAX_FILES": "7"}),
			func(r Resolved) any { return r.Knobs.ButlerPatchMaxFiles }, 7},
		{"zero positive int flag falls back to settings", []string{"--butler-patch-max-files", "0"}, docWith(map[string]string{"BUTLER_PATCH_MAX_FILES": "7"}),
			func(r Resolved) any { return r.Knobs.ButlerPatchMaxFiles }, 7},
		{"nonneg int flag accepts zero", []string{"--butler-max-sweeps-per-day", "0"}, docWith(map[string]string{"BUTLER_MAX_SWEEPS_PER_DAY": "5"}),
			func(r Resolved) any { return r.Knobs.ButlerMaxSweepsPerDay }, 0},
		{"nonneg int settings", nil, docWith(map[string]string{"BUTLER_MAX_SWEEPS_PER_DAY": "5"}),
			func(r Resolved) any { return r.Knobs.ButlerMaxSweepsPerDay }, 5},
		{"float flag beats settings", []string{"--max-budget-usd", "2.5"}, docWith(map[string]string{"MAX_BUDGET_USD": "1"}),
			func(r Resolved) any { return r.Knobs.MaxBudgetUSD }, 2.5},
		{"negative float flag falls back to settings", []string{"--max-budget-usd", "-1"}, docWith(map[string]string{"MAX_BUDGET_USD": "1"}),
			func(r Resolved) any { return r.Knobs.MaxBudgetUSD }, 1.0},
		{"float settings", nil, docWith(map[string]string{"MAX_BUDGET_USD": "4"}),
			func(r Resolved) any { return r.Knobs.MaxBudgetUSD }, 4.0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := mustResolve(t, tc.args, tc.doc, nil)
			if got := tc.get(r); got != tc.want {
				t.Errorf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestResolve_BoxEnvFollowsTheSameTiers(t *testing.T) {
	doc := docWith(map[string]string{"AUTO_FORMAT": "1", "BASE_BRANCH": "trunk"})
	r := mustResolve(t, []string{"--auto-format=false", "--branch-prefix", "p/"}, doc, nil)
	for name, want := range map[string]string{
		"AUTO_FORMAT":   "",
		"BASE_BRANCH":   "trunk",
		"BRANCH_PREFIX": "p/",
		"MEMORY_LIMIT":  "5g",
	} {
		if got := r.BoxEnv(name); got != want {
			t.Errorf("BoxEnv(%s) = %q, want %q", name, got, want)
		}
	}
}

func TestResolve_AmbientKnobIsConfigInvalid(t *testing.T) {
	withSchemaFlags(t, FlagEntry{Env: "BASE_BRANCH", Flag: "base-branch", SettingsPath: "git.baseBranch"})

	_, err := Resolve(nil, nil, map[string]string{"BASE_BRANCH": "develop"})

	if !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("error = %v, want it to match ErrConfigInvalid", err)
	}
	want := "BASE_BRANCH=develop set in environment — knob env vars are not read; use --base-branch or git.baseBranch"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err, want)
	}
}

func TestResolve_SecretsAndPlumbingPassThrough(t *testing.T) {
	env := map[string]string{"GH_TOKEN": "tok", "PATH": "/bin", "DRIVER": "claude"}
	r := mustResolve(t, nil, nil, env)
	if r.Knobs.GhToken != "tok" {
		t.Errorf("Knobs.GhToken = %q, want tok", r.Knobs.GhToken)
	}
	for name, want := range env {
		if got := r.BoxEnv(name); got != want {
			t.Errorf("BoxEnv(%s) = %q, want %q", name, got, want)
		}
	}
	if got := r.BoxEnv("UNSET_PLUMBING"); got != "" {
		t.Errorf("BoxEnv(unset) = %q, want empty", got)
	}
}

func TestResolve_BoxEnvNonKnobFallsBackToDocument(t *testing.T) {
	doc := docWith(map[string]string{"EXTRA": "doc", "SHADOWED": "doc"})
	r := mustResolve(t, nil, doc, map[string]string{"EXTRA": "", "SHADOWED": "env"})
	for name, want := range map[string]string{"EXTRA": "doc", "SHADOWED": "env"} {
		if got := r.BoxEnv(name); got != want {
			t.Errorf("BoxEnv(%s) = %q, want %q", name, got, want)
		}
	}
}

func TestResolve_VerbArgsAreReturnedUntouched(t *testing.T) {
	args := []string{
		"run", "--no-build", "--yes", "--base-branch", "x", "42",
		"--some-doctor-opt", "v", "--gh-token-file", "--base-branch",
		"--", "--model", "y", "--base-branch", "z",
	}
	r := mustResolve(t, args, nil, nil)
	want := []string{
		"run", "--no-build", "--yes", "42",
		"--some-doctor-opt", "v", "--gh-token-file", "--base-branch",
		"--", "--model", "y", "--base-branch", "z",
	}
	if !reflect.DeepEqual(r.Args, want) {
		t.Errorf("Args = %q, want %q", r.Args, want)
	}
	if r.Knobs.BaseBranch != "x" {
		t.Errorf("BaseBranch = %q, want x (the flag before --, not the one after)", r.Knobs.BaseBranch)
	}
}

func TestResolve_ValueFlagWithEqualsIsNotASchemaMatch(t *testing.T) {
	r := mustResolve(t, []string{"--base-branch=dev"}, nil, nil)
	if !reflect.DeepEqual(r.Args, []string{"--base-branch=dev"}) || r.Knobs.BaseBranch != "main" {
		t.Errorf("Args = %q, BaseBranch = %q; want the token passed through and the default kept", r.Args, r.Knobs.BaseBranch)
	}
}

func TestResolve_ArtifactsComeFromTheDocumentOnly(t *testing.T) {
	var arts Artifacts
	if err := json.Unmarshal([]byte(`{"RUNNER_KIND":"oci","DRIVER":"claude","IMAGE_TAG":"img:1"}`), &arts); err != nil {
		t.Fatal(err)
	}
	doc := &Document{Artifacts: arts}

	r := mustResolve(t, nil, doc, map[string]string{"DRIVER": "other", "IMAGE_TAG": "img:2"})
	if !reflect.DeepEqual(r.Artifacts, arts) {
		t.Errorf("Artifacts = %+v, want the document's %+v", r.Artifacts, arts)
	}

	r = mustResolve(t, nil, nil, map[string]string{"DRIVER": "other"})
	if !reflect.DeepEqual(r.Artifacts, Artifacts{}) {
		t.Errorf("nil document: Artifacts = %+v, want zero", r.Artifacts)
	}
	if r.Knobs.BaseBranch != "main" {
		t.Errorf("nil document: BaseBranch = %q, want the schema default", r.Knobs.BaseBranch)
	}
}

func TestResolve_FlagErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"invalid bool", []string{"--continuous-dispatch=maybe"}, `flag --continuous-dispatch: invalid boolean value "maybe"`},
		{"value flag without a value", []string{"--base-branch"}, "flag --base-branch requires a value"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Resolve(tc.args, nil, nil)
			if err == nil || err.Error() != tc.want {
				t.Errorf("error = %v, want %q", err, tc.want)
			}
			if errors.Is(err, ErrConfigInvalid) {
				t.Errorf("flag error %v matches ErrConfigInvalid, want only the ambient-knob error to", err)
			}
		})
	}
}

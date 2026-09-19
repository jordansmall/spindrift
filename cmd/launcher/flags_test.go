package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/term"

	"spindrift.dev/launcher/internal/backend"
)

// MODEL must stay sonnet-5 and not regress to opus-4-8 or an older release
// (issue #2240). The issue #2514 review found SCOUT_MODEL, REVIEW_MODEL,
// FILER_MODEL and WORKER_MODEL generated into the fixture
// (cmd/launcher/defaultmodels_gen_test.go) but asserted nowhere.
func TestSchemaFlags_DefaultModelsMatchFixture(t *testing.T) {
	for env, want := range expectedDefaultModels {
		t.Run(env, func(t *testing.T) {
			for _, e := range schemaFlags {
				if e.env == env {
					if e.dflt != want {
						t.Errorf("%s default = %q, want %q", env, e.dflt, want)
					}
					return
				}
			}
			t.Fatalf("%s entry not found in schemaFlags", env)
		})
	}
}

// Issue #2145 slice A made bwrap-unshare-net a presence-style bool, not a
// string.
func TestSchemaFlags_BwrapUnshareNetIsBool(t *testing.T) {
	for _, e := range schemaFlags {
		if e.flag == "bwrap-unshare-net" {
			if e.kind != "bool" {
				t.Errorf("bwrap-unshare-net kind = %q, want %q", e.kind, "bool")
			}
			return
		}
	}
	t.Fatal("bwrap-unshare-net entry not found in schemaFlags")
}

// The settingsPath comes from spindriftPromptDir in lib/env-schema.nix,
// which declares flakeOption = true with nixSubPath = "promptDir" (issue
// #2200 slice 1).
func TestSchemaFlags_PromptDirSettingsPath(t *testing.T) {
	for _, e := range schemaFlags {
		if e.env == "SPINDRIFT_PROMPT_DIR" {
			if e.settingsPath != "agents.promptDir" {
				t.Errorf("SPINDRIFT_PROMPT_DIR settingsPath = %q, want %q", e.settingsPath, "agents.promptDir")
			}
			return
		}
	}
	t.Fatal("SPINDRIFT_PROMPT_DIR entry not found in schemaFlags")
}

// Issue #2146 slice 1 converted these six knobs from strings to
// presence-style bools.
func TestSchemaFlags_GenericBoolsAreBool(t *testing.T) {
	envs := []string{
		"AUTO_FORMAT",
		"AUTO_LINT",
		"LOCAL_ISSUE_REFERENCE",
		"ORCHESTRATOR_ENABLED",
		"PREFLIGHT_STALE_BASE",
		"JIRA_INCLUDE_COMMENTS",
	}
	for _, env := range envs {
		env := env
		t.Run(env, func(t *testing.T) {
			for _, e := range schemaFlags {
				if e.env == env {
					if e.kind != "bool" {
						t.Errorf("%s kind = %q, want %q", env, e.kind, "bool")
					}
					return
				}
			}
			t.Fatalf("%s entry not found in schemaFlags", env)
		})
	}
}

// Issue #2147 slice 1 made CONTINUOUS_DISPATCH a presence-style bool with
// --continuous as a schema alias.
func TestSchemaFlags_ContinuousDispatchIsBoolAlias(t *testing.T) {
	for _, e := range schemaFlags {
		if e.env == "CONTINUOUS_DISPATCH" {
			if e.kind != "bool" {
				t.Errorf("CONTINUOUS_DISPATCH kind = %q, want %q", e.kind, "bool")
			}
			if e.alias != "continuous" {
				t.Errorf("CONTINUOUS_DISPATCH alias = %q, want %q", e.alias, "continuous")
			}
			return
		}
	}
	t.Fatalf("CONTINUOUS_DISPATCH entry not found in schemaFlags")
}

// A later slice of issue #2520 sources a generic Go guard from this choices
// field instead of a hand-typed value list, so the entry must carry the
// schema's enum verbatim.
func TestSchemaFlags_MergeModeChoices(t *testing.T) {
	want := []string{"immediate", "auto", "manual"}
	for _, e := range schemaFlags {
		if e.env == "MERGE_MODE" {
			if !slices.Equal(e.choices, want) {
				t.Errorf("MERGE_MODE choices = %v, want %v", e.choices, want)
			}
			return
		}
	}
	t.Fatal("MERGE_MODE entry not found in schemaFlags")
}

func TestExtractInputFlag_Present(t *testing.T) {
	path, remaining, err := extractInputFlag([]string{"--repo-slug", "o/r", "--input", "/nix/store/x.json", "dispatch"})
	if err != nil {
		t.Fatalf("extractInputFlag: %v", err)
	}
	if path != "/nix/store/x.json" {
		t.Errorf("path = %q, want /nix/store/x.json", path)
	}
	want := []string{"--repo-slug", "o/r", "dispatch"}
	if strings.Join(remaining, ",") != strings.Join(want, ",") {
		t.Errorf("remaining = %v, want %v", remaining, want)
	}
}

func TestExtractInputFlag_Absent(t *testing.T) {
	path, remaining, err := extractInputFlag([]string{"dispatch", "42"})
	if err != nil {
		t.Fatalf("extractInputFlag: %v", err)
	}
	if path != "" {
		t.Errorf("path = %q, want empty", path)
	}
	if strings.Join(remaining, ",") != "dispatch,42" {
		t.Errorf("remaining = %v, want [dispatch 42]", remaining)
	}
}

func TestExtractInputFlag_MissingValue(t *testing.T) {
	_, _, err := extractInputFlag([]string{"--input"})
	if err == nil {
		t.Fatal("want error for --input with no value")
	}
}

func TestParseFlags_SetEnv(t *testing.T) {
	t.Setenv("ISSUE_NUMBER", "")
	remaining, err := parseFlags([]string{"--issue-number", "215"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(remaining) != 0 {
		t.Errorf("expected no remaining args, got %v", remaining)
	}
	if got := os.Getenv("ISSUE_NUMBER"); got != "215" {
		t.Errorf("ISSUE_NUMBER = %q, want %q", got, "215")
	}
}

func TestParseFlags_FlagWinsOverEnv(t *testing.T) {
	t.Setenv("ISSUE_NUMBER", "1")
	_, err := parseFlags([]string{"--issue-number", "999"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := os.Getenv("ISSUE_NUMBER"); got != "999" {
		t.Errorf("ISSUE_NUMBER = %q, want %q (flag must win over env)", got, "999")
	}
}

func TestParseFlags_RepoSlugFlagWinsOverEnv(t *testing.T) {
	t.Setenv("REPO_SLUG", "env-org/env-repo")
	_, err := parseFlags([]string{"--repo-slug", "flag-org/flag-repo"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := os.Getenv("REPO_SLUG"); got != "flag-org/flag-repo" {
		t.Errorf("REPO_SLUG = %q, want %q (flag must win over env)", got, "flag-org/flag-repo")
	}
}

func TestParseFlags_EnvFallback(t *testing.T) {
	t.Setenv("MAX_JOBS", "7")
	_, err := parseFlags([]string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := os.Getenv("MAX_JOBS"); got != "7" {
		t.Errorf("MAX_JOBS = %q, want %q (env must survive when no flag given)", got, "7")
	}
}

func TestParseFlags_UnknownFlag(t *testing.T) {
	_, err := parseFlags([]string{"--not-a-schema-flag", "value"})
	if err == nil {
		t.Fatal("expected error for unknown flag, got nil")
	}
}

func TestParseFlags_PassthroughPositional(t *testing.T) {
	remaining, err := parseFlags([]string{"build", "--max-jobs", "2", "extra"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(remaining) != 2 || remaining[0] != "build" || remaining[1] != "extra" {
		t.Errorf("remaining = %v, want [build extra]", remaining)
	}
}

func TestParseFlags_DoubleDash(t *testing.T) {
	remaining, err := parseFlags([]string{"--", "--not-parsed"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(remaining) != 1 || remaining[0] != "--not-parsed" {
		t.Errorf("remaining = %v, want [--not-parsed]", remaining)
	}
}

func TestParseFlags_MissingValue(t *testing.T) {
	_, err := parseFlags([]string{"--issue-number"})
	if err == nil {
		t.Fatal("expected error when flag value is missing, got nil")
	}
}

// DEPS_POLL_SECS and DEPS_WAIT_SECS configured the in-process
// dependency-wave poll that #522/#524 deleted, so they must not survive in
// the schema-generated flag table (ADR 0019).
func TestSchemaFlags_ExcludesRemovedDepsKnobs(t *testing.T) {
	for _, removed := range []string{"DEPS_POLL_SECS", "DEPS_WAIT_SECS"} {
		for _, entry := range schemaFlags {
			if entry.env == removed {
				t.Errorf("removed knob %s must not appear in schemaFlags", removed)
			}
		}
	}
}

func TestParseFlags_SecretsExcluded(t *testing.T) {
	secrets := []string{"GH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"}
	for _, env := range secrets {
		for _, entry := range schemaFlags {
			if entry.env == env {
				t.Errorf("secret knob %s must not appear in schemaFlags (would expose secrets in ps output)", env)
			}
		}
	}
}

func TestParseFlags_MultipleFlags(t *testing.T) {
	t.Setenv("ISSUE_NUMBER", "")
	t.Setenv("MAX_JOBS", "")
	_, err := parseFlags([]string{"--issue-number", "215", "--max-jobs", "1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := os.Getenv("ISSUE_NUMBER"); got != "215" {
		t.Errorf("ISSUE_NUMBER = %q, want %q", got, "215")
	}
	if got := os.Getenv("MAX_JOBS"); got != "1" {
		t.Errorf("MAX_JOBS = %q, want %q", got, "1")
	}
}

// A bare bool-kind flag sets its env var to "1" and does not consume the
// next arg (issue #2145 slice B).
func TestParseFlags_BoolFlag_BarePresence(t *testing.T) {
	t.Setenv("BWRAP_UNSHARE_NET", "")
	remaining, err := parseFlags([]string{"dispatch", "--bwrap-unshare-net"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := os.Getenv("BWRAP_UNSHARE_NET"); got != "1" {
		t.Errorf("BWRAP_UNSHARE_NET = %q, want %q", got, "1")
	}
	if len(remaining) != 1 || remaining[0] != "dispatch" {
		t.Errorf("remaining = %v, want [dispatch]", remaining)
	}
}

// The equals form is on for "1" and "true", off for "", "0" and "false"
// (issue #2145 slice B).
func TestParseFlags_BoolFlag_EqualsForms(t *testing.T) {
	cases := []struct {
		arg  string
		want string
	}{
		{"--bwrap-unshare-net=1", "1"},
		{"--bwrap-unshare-net=true", "1"},
		{"--bwrap-unshare-net=0", ""},
		{"--bwrap-unshare-net=false", ""},
		{"--bwrap-unshare-net=", ""},
	}
	for _, c := range cases {
		t.Run(c.arg, func(t *testing.T) {
			t.Setenv("BWRAP_UNSHARE_NET", "")
			_, err := parseFlags([]string{c.arg})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := os.Getenv("BWRAP_UNSHARE_NET"); got != c.want {
				t.Errorf("BWRAP_UNSHARE_NET = %q, want %q", got, c.want)
			}
		})
	}
}

// A token following a bare bool-kind flag is never swallowed as its value.
// It survives as a normal positional arg (issue #2145 slice B).
func TestParseFlags_BoolFlag_SpaceSeparatedIsPositional(t *testing.T) {
	t.Setenv("BWRAP_UNSHARE_NET", "")
	remaining, err := parseFlags([]string{"dispatch", "--bwrap-unshare-net", "1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := os.Getenv("BWRAP_UNSHARE_NET"); got != "1" {
		t.Errorf("BWRAP_UNSHARE_NET = %q, want %q", got, "1")
	}
	found := false
	for _, r := range remaining {
		if r == "1" {
			found = true
		}
	}
	if !found {
		t.Errorf("remaining = %v, want it to contain %q (must not be swallowed as flag value)", remaining, "1")
	}
}

// An explicit off form clears an already-set env var rather than leaving the
// ambient "1" in place, because flag-over-env precedence applies to the off
// case too (ADR 0020; issue #2145 slice B).
func TestParseFlags_BoolFlag_ExplicitOffOverridesAmbient(t *testing.T) {
	for _, arg := range []string{"--bwrap-unshare-net=0", "--bwrap-unshare-net=false", "--bwrap-unshare-net="} {
		t.Run(arg, func(t *testing.T) {
			t.Setenv("BWRAP_UNSHARE_NET", "1")
			_, err := parseFlags([]string{arg})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := os.Getenv("BWRAP_UNSHARE_NET"); got != "" {
				t.Errorf("BWRAP_UNSHARE_NET = %q, want empty (explicit off must override ambient env)", got)
			}
		})
	}
}

func TestPrintVersion_Format(t *testing.T) {
	var buf bytes.Buffer
	printVersion(&buf)
	got := buf.String()
	if !strings.HasPrefix(got, "spindrift ") {
		t.Errorf("printVersion must start with 'spindrift ', got: %q", got)
	}
	if !strings.Contains(got, "(rev ") {
		t.Errorf("printVersion must contain '(rev ...)', got: %q", got)
	}
}

func TestPrintHelp_UsageLineNamesSpindrift(t *testing.T) {
	var buf bytes.Buffer
	printHelp(&buf)
	if !strings.Contains(buf.String(), "Usage: spindrift [flags] <subcommand>") {
		t.Errorf("help must contain a usage line naming spindrift, got:\n%s", buf.String())
	}
}

func TestPrintHelp_Concise_PointsToFullReference(t *testing.T) {
	var buf bytes.Buffer
	printHelp(&buf)
	out := buf.String()
	if !strings.Contains(out, "man spindrift") {
		t.Errorf("concise help must point to 'man spindrift', got:\n%s", out)
	}
	if !strings.Contains(out, "--help --all") {
		t.Errorf("concise help must point to '--help --all', got:\n%s", out)
	}
}

// --repo-slug is required unless the run is fully local (CODE_FORGE=local
// and ISSUE_TRACKER=local both set), so its help text must not print an
// unconditional "(required)" (issue #1895).
func TestPrintHelp_RepoSlugNotesLocalExemption(t *testing.T) {
	var buf bytes.Buffer
	printHelp(&buf)
	out := buf.String()
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "--repo-slug") {
			if strings.Contains(line, "(required)") {
				t.Errorf("--repo-slug help must not say unconditional '(required)', got: %q", line)
			}
			if !strings.Contains(line, "local") {
				t.Errorf("--repo-slug help must note the fully-local exemption, got: %q", line)
			}
			return
		}
	}
	t.Fatal("help output missing --repo-slug line")
}

func TestPrintHelp_Concise_OmitsRareFlags(t *testing.T) {
	var buf bytes.Buffer
	printHelp(&buf)
	out := buf.String()
	for _, rare := range []string{"--transient-backoff-secs", "--hold-jitter-secs", "--deps-poll-secs"} {
		if strings.Contains(out, rare) {
			t.Errorf("concise help should omit rare flag %s; it belongs in --help --all/man, got:\n%s", rare, out)
		}
	}
}

func TestPrintHelp_ShowsDispatchSubcommand(t *testing.T) {
	var buf bytes.Buffer
	printHelp(&buf)
	out := buf.String()
	if !strings.Contains(out, "dispatch") {
		t.Errorf("help output must show 'dispatch' subcommand, got:\n%s", out)
	}
}

// Bare `spindrift` points operators at the interactive console first, so
// console leads the listing (ADR 0023).
func TestPrintSubcommands_ConsoleFirst(t *testing.T) {
	var buf bytes.Buffer
	printSubcommands(&buf)
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("printSubcommands output too short: %q", buf.String())
	}
	if !strings.Contains(lines[1], "console") {
		t.Errorf("first subcommand line = %q, want it to mention console", lines[1])
	}
}

// --continuous is a schema-backed bool alias, so parseFlags consumes it and
// sets CONTINUOUS_DISPATCH directly instead of passing it through to the
// verb handler for hand-rolled extraction (issues #2033 and #2147).
func TestParseFlags_ContinuousAlias(t *testing.T) {
	t.Setenv("CONTINUOUS_DISPATCH", "")
	remaining, err := parseFlags([]string{"dispatch", "--continuous"})
	if err != nil {
		t.Fatalf("parseFlags with --continuous: unexpected error: %v", err)
	}
	if got := os.Getenv("CONTINUOUS_DISPATCH"); got != "1" {
		t.Errorf("CONTINUOUS_DISPATCH = %q, want %q", got, "1")
	}
	if len(remaining) != 1 || remaining[0] != "dispatch" {
		t.Errorf("remaining = %v, want [dispatch] (flag consumed, not passed through)", remaining)
	}
}

func TestParseFlags_ContinuousDispatchBareAlias(t *testing.T) {
	t.Setenv("CONTINUOUS_DISPATCH", "")
	remaining, err := parseFlags([]string{"dispatch", "--continuous-dispatch"})
	if err != nil {
		t.Fatalf("parseFlags with --continuous-dispatch: unexpected error: %v", err)
	}
	if got := os.Getenv("CONTINUOUS_DISPATCH"); got != "1" {
		t.Errorf("CONTINUOUS_DISPATCH = %q, want %q", got, "1")
	}
	if len(remaining) != 1 || remaining[0] != "dispatch" {
		t.Errorf("remaining = %v, want [dispatch] (flag consumed, not passed through)", remaining)
	}
}

// The listing is pinned byte-for-byte so a change to printSubcommands, such
// as its column-width constant, cannot silently misalign the output the way
// a hand-picked width once did (issue #1575 review).
func TestPrintSubcommands_ExactOutput(t *testing.T) {
	want := "Subcommands:\n" +
		"  console                                                  browse the open backlog interactively (read-only)\n" +
		"  dispatch [--no-build] [--yes] [--continuous] [issue...]  dispatch agents in waves; an issue list dispatches exactly those (bypasses label/barrier gates)\n" +
		"  research [--no-build] [--yes] [--continuous] [issue...]  advise-only research dispatch: drains agent-research (or an issue list) and posts a verdict comment; never merges, never promotes\n" +
		"  preview [issue...]                                       dry-run: show what dispatch would pick up, in order\n" +
		"  build                                                    realize the agent image without running any agent\n" +
		"  recover <issue>                                          run the merge gate for a single issue\n" +
		"  doctor                                                   check configuration validity, forge credentials, repository connectivity, and label presence; distinct exit code per failure class (see docs/reference.md)\n" +
		"  reconcile                                                local-tracker bookkeeping sweep: close issues whose recorded landing PR merged (no-op on github/jira)\n" +
		"  registry discover <repo-dir> <routes-file> [--force]     discover registry routes from a Target repo checkout and write the routes file (ADR 0045)\n"

	var buf bytes.Buffer
	printSubcommands(&buf)
	if got := buf.String(); got != want {
		t.Errorf("printSubcommands output =\n%s\nwant:\n%s", got, want)
	}
}

// The research dispatch kind (ADR 0022) must be discoverable beside
// dispatch.
func TestPrintHelp_ShowsResearchSubcommand(t *testing.T) {
	var buf bytes.Buffer
	printHelp(&buf)
	out := buf.String()
	if !strings.Contains(out, "research") {
		t.Errorf("help output must show 'research' subcommand, got:\n%s", out)
	}
}

func TestPrintHelpFull_ContainsLabelEntry(t *testing.T) {
	var buf bytes.Buffer
	printHelpFull(&buf)
	out := buf.String()
	if !strings.Contains(out, "--label") {
		t.Error("full help output missing --label flag")
	}
	if !strings.Contains(out, "issues carrying this label are dispatchable") {
		t.Error("full help output missing label doc string")
	}
}

// The doc strings come from lib/env-schema.nix and must spell out the
// fully-local exemption (issue #1895). Matching on each line's leading
// env/flag token, not a bare substring, keeps only GH_TOKEN's own two
// rendered lines in scope and skips docs that merely mention "GH_TOKEN" in
// prose, such as BOX_GH_TOKEN's (issue #380).
func TestPrintHelpFull_RepoSlugAndGhTokenNoteLocalExemption(t *testing.T) {
	var buf bytes.Buffer
	printHelpFull(&buf)
	out := buf.String()
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "--repo-slug":
			if !strings.Contains(line, "local") {
				t.Errorf("REPO_SLUG doc must note the fully-local exemption, got: %q", line)
			}
		case "GH_TOKEN", "--gh-token-file":
			if !strings.Contains(line, "local") {
				t.Errorf("GH_TOKEN doc must note the fully-local exemption, got: %q", line)
			}
		}
	}
}

func TestPrintHelpFull_GroupsFlags(t *testing.T) {
	var buf bytes.Buffer
	printHelpFull(&buf)
	out := buf.String()
	for _, g := range []string{"issues", "agents", "infra"} {
		if !strings.Contains(out, g) {
			t.Errorf("full help output missing group heading %q, got:\n%s", g, out)
		}
	}
}

// A knob whose group is absent from groupOrder drops out of the full
// reference silently.
func TestPrintHelpFull_CoversEverySchemaFlag(t *testing.T) {
	var buf bytes.Buffer
	printHelpFull(&buf)
	out := buf.String()
	for _, e := range schemaFlags {
		if !strings.Contains(out, "--"+e.flag) {
			t.Errorf("full help output missing flag --%s (group %q not rendered?)", e.flag, e.group)
		}
	}
}

// A presence-style bool flag takes no following value, so it renders
// labelled "bool" with no value placeholder (issue #2145).
func TestPrintHelpFull_BoolFlagNoValuePlaceholder(t *testing.T) {
	var buf bytes.Buffer
	printHelpFull(&buf)
	var line string
	for _, l := range strings.Split(buf.String(), "\n") {
		if strings.Contains(l, "--bwrap-unshare-net") {
			line = l
			break
		}
	}
	if line == "" {
		t.Fatal("full help output missing --bwrap-unshare-net")
	}
	if !strings.Contains(line, "bool") {
		t.Errorf("bool flag line should be labelled bool, got: %q", line)
	}
	if strings.ContainsAny(line, "<>") {
		t.Errorf("bool flag line must carry no <value> placeholder, got: %q", line)
	}
}

// The full help and the man page group flags by this field.
func TestSchemaFlags_AllHaveGroup(t *testing.T) {
	for _, e := range schemaFlags {
		if e.group == "" {
			t.Errorf("flag --%s has no group; add `group = ...` to its lib/env-schema.nix entry", e.flag)
		}
	}
}

// printHelpFull drops the flags of any group missing from groupOrder.
func TestGroupOrder_CoversEverySchemaGroup(t *testing.T) {
	known := map[string]bool{}
	for _, g := range groupOrder {
		known[g] = true
	}
	for _, e := range schemaFlags {
		if e.group != "" && !known[e.group] {
			t.Errorf("flag --%s has group %q missing from groupOrder", e.flag, e.group)
		}
	}
}

func TestParseFlags_AliasSetEnv(t *testing.T) {
	t.Setenv("ISSUE_NUMBER", "")
	remaining, err := parseFlags([]string{"--issue", "42"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(remaining) != 0 {
		t.Errorf("expected no remaining args, got %v", remaining)
	}
	if got := os.Getenv("ISSUE_NUMBER"); got != "42" {
		t.Errorf("ISSUE_NUMBER = %q, want %q (alias must set same env var)", got, "42")
	}
}

// A deprecated old flag name resolves to the same env var as its renamed
// canonical flag (ADR 0037 Pass 2), for both a value-taking and a
// presence-style bool knob.
func TestParseFlags_DeprecatedAliasSetsSameEnv(t *testing.T) {
	// --merge-mode is the deprecated name of the canonical --merge-policy.
	t.Setenv("MERGE_MODE", "")
	if _, err := parseFlags([]string{"--merge-mode", "auto"}); err != nil {
		t.Fatalf("parseFlags --merge-mode: %v", err)
	}
	if got := os.Getenv("MERGE_MODE"); got != "auto" {
		t.Errorf("MERGE_MODE = %q, want %q (deprecated alias must set same env var)", got, "auto")
	}
	// --orchestrator-enabled is the deprecated name of --orchestrator.
	t.Setenv("ORCHESTRATOR_ENABLED", "")
	if _, err := parseFlags([]string{"--orchestrator-enabled"}); err != nil {
		t.Fatalf("parseFlags --orchestrator-enabled: %v", err)
	}
	if got := os.Getenv("ORCHESTRATOR_ENABLED"); got != "1" {
		t.Errorf("ORCHESTRATOR_ENABLED = %q, want %q", got, "1")
	}
}

func TestParseFlags_NewCanonicalFlagResolves(t *testing.T) {
	t.Setenv("MERGE_MODE", "")
	if _, err := parseFlags([]string{"--merge-policy", "immediate"}); err != nil {
		t.Fatalf("parseFlags --merge-policy: %v", err)
	}
	if got := os.Getenv("MERGE_MODE"); got != "immediate" {
		t.Errorf("MERGE_MODE = %q, want %q", got, "immediate")
	}
}

func TestPrintHelpFull_MarksDeprecatedAlias(t *testing.T) {
	var buf bytes.Buffer
	printHelpFull(&buf)
	out := buf.String()
	if !strings.Contains(out, "--merge-policy, --merge-mode (deprecated)") {
		t.Errorf("full help must show canonical flag and mark the old name deprecated; got:\n%s", out)
	}
}

func TestPrintHelpFull_ShowsAlias(t *testing.T) {
	var buf bytes.Buffer
	printHelpFull(&buf)
	out := buf.String()
	if !strings.Contains(out, "--issue-number, --issue") {
		t.Errorf("full help output missing alias display; want --issue-number, --issue in:\n%s", out)
	}
}

func TestParseFlags_FileFlag_ReadsToken(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token.txt")
	if err := os.WriteFile(tokenFile, []byte("secret-value"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_TOKEN", "")
	_, err := parseFlags([]string{"--gh-token-file", tokenFile})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := os.Getenv("GH_TOKEN"); got != "secret-value" {
		t.Errorf("GH_TOKEN = %q, want %q", got, "secret-value")
	}
}

func TestParseFlags_FileFlag_WinsOverEnv(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token.txt")
	if err := os.WriteFile(tokenFile, []byte("file-value"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_TOKEN", "env-value")
	_, err := parseFlags([]string{"--gh-token-file", tokenFile})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := os.Getenv("GH_TOKEN"); got != "file-value" {
		t.Errorf("GH_TOKEN = %q, want %q (file flag must win over env)", got, "file-value")
	}
}

func TestParseFlags_FileFlag_MissingFile(t *testing.T) {
	_, err := parseFlags([]string{"--gh-token-file", "/nonexistent/path/token.txt"})
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
	if !strings.Contains(err.Error(), "/nonexistent/path/token.txt") {
		t.Errorf("error should mention the path, got: %v", err)
	}
}

func TestParseFlags_FileFlag_MissingValue(t *testing.T) {
	_, err := parseFlags([]string{"--gh-token-file"})
	if err == nil {
		t.Fatal("expected error when file flag has no path argument, got nil")
	}
}

func TestPrintHelpFull_SecretKnobEnvOnly(t *testing.T) {
	var buf bytes.Buffer
	printHelpFull(&buf)
	out := buf.String()
	if !strings.Contains(out, "GH_TOKEN") {
		t.Error("full help output missing GH_TOKEN env-only listing")
	}
	if !strings.Contains(out, "env-only") {
		t.Error("full help output missing 'env-only' marker for secret knobs")
	}
}

func TestParseFlags_FileFlag_StripsNewline(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token.txt")
	if err := os.WriteFile(tokenFile, []byte("stripped-value\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_TOKEN", "")
	_, err := parseFlags([]string{"--gh-token-file", tokenFile})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := os.Getenv("GH_TOKEN"); got != "stripped-value" {
		t.Errorf("GH_TOKEN = %q, want %q (trailing newline must be stripped)", got, "stripped-value")
	}
}

func TestParseFlags_CmdFlag_RunsCommand(t *testing.T) {
	orig := secretCmdRunner
	t.Cleanup(func() { secretCmdRunner = orig })
	secretCmdRunner = func(cmd string) (string, error) {
		if cmd != "rbw get spindrift-pat" {
			t.Fatalf("secretCmdRunner called with %q, want %q", cmd, "rbw get spindrift-pat")
		}
		return "cmd-value\n", nil
	}
	t.Setenv("GH_TOKEN", "")
	_, err := parseFlags([]string{"--gh-token-cmd", "rbw get spindrift-pat"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := os.Getenv("GH_TOKEN"); got != "cmd-value" {
		t.Errorf("GH_TOKEN = %q, want %q", got, "cmd-value")
	}
}

func TestParseFlags_CmdEnv_RunsCommand(t *testing.T) {
	orig := secretCmdRunner
	t.Cleanup(func() { secretCmdRunner = orig })
	secretCmdRunner = func(cmd string) (string, error) {
		if cmd != "rbw get spindrift-pat" {
			t.Fatalf("secretCmdRunner called with %q, want %q", cmd, "rbw get spindrift-pat")
		}
		return "env-cmd-value\n", nil
	}
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GH_TOKEN_CMD", "rbw get spindrift-pat")
	_, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := os.Getenv("GH_TOKEN"); got != "env-cmd-value" {
		t.Errorf("GH_TOKEN = %q, want %q", got, "env-cmd-value")
	}
}

func TestParseFlags_CmdFlag_WinsOverCmdEnv(t *testing.T) {
	orig := secretCmdRunner
	t.Cleanup(func() { secretCmdRunner = orig })
	secretCmdRunner = func(cmd string) (string, error) {
		switch cmd {
		case "flag-cmd":
			return "flag-value", nil
		case "env-cmd":
			return "env-value", nil
		}
		t.Fatalf("secretCmdRunner called with unexpected cmd %q", cmd)
		return "", nil
	}
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GH_TOKEN_CMD", "env-cmd")
	_, err := parseFlags([]string{"--gh-token-cmd", "flag-cmd"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := os.Getenv("GH_TOKEN"); got != "flag-value" {
		t.Errorf("GH_TOKEN = %q, want %q (flag must win over env)", got, "flag-value")
	}
}

func TestParseFlags_CmdEnv_WinsOverFileFlag(t *testing.T) {
	orig := secretCmdRunner
	t.Cleanup(func() { secretCmdRunner = orig })
	secretCmdRunner = func(cmd string) (string, error) {
		return "cmd-value", nil
	}
	tokenFile := filepath.Join(t.TempDir(), "token.txt")
	if err := os.WriteFile(tokenFile, []byte("file-value"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GH_TOKEN_CMD", "env-cmd")
	_, err := parseFlags([]string{"--gh-token-file", tokenFile})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := os.Getenv("GH_TOKEN"); got != "cmd-value" {
		t.Errorf("GH_TOKEN = %q, want %q (cmd env must win over file flag)", got, "cmd-value")
	}
}

func TestParseFlags_CmdFlagAndFileFlag_IsConfigError(t *testing.T) {
	orig := secretCmdRunner
	t.Cleanup(func() { secretCmdRunner = orig })
	secretCmdRunner = func(cmd string) (string, error) {
		t.Fatal("secretCmdRunner should not be called when the flags conflict")
		return "", nil
	}
	tokenFile := filepath.Join(t.TempDir(), "token.txt")
	if err := os.WriteFile(tokenFile, []byte("file-value"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_TOKEN", "")
	_, err := parseFlags([]string{"--gh-token-cmd", "some-cmd", "--gh-token-file", tokenFile})
	if err == nil {
		t.Fatal("expected error when both --gh-token-cmd and --gh-token-file are supplied, got nil")
	}
	if !strings.Contains(err.Error(), "--gh-token-cmd") || !strings.Contains(err.Error(), "--gh-token-file") {
		t.Errorf("error should name both conflicting flags, got: %v", err)
	}
}

func TestParseFlags_CmdFlag_EmptyOutputIsError(t *testing.T) {
	orig := secretCmdRunner
	t.Cleanup(func() { secretCmdRunner = orig })
	secretCmdRunner = func(cmd string) (string, error) {
		return "", nil
	}
	t.Setenv("GH_TOKEN", "")
	_, err := parseFlags([]string{"--gh-token-cmd", "some-cmd"})
	if err == nil {
		t.Fatal("expected error for empty command output, got nil")
	}
	if !strings.Contains(err.Error(), "GH_TOKEN") {
		t.Errorf("error should name GH_TOKEN, got: %v", err)
	}
}

func TestParseFlags_CmdFlag_NonZeroExitIsError(t *testing.T) {
	orig := secretCmdRunner
	t.Cleanup(func() { secretCmdRunner = orig })
	secretCmdRunner = func(cmd string) (string, error) {
		return "partial-secret-leak", errors.New("exit status 1")
	}
	t.Setenv("GH_TOKEN", "")
	_, err := parseFlags([]string{"--gh-token-cmd", "some-cmd"})
	if err == nil {
		t.Fatal("expected error for a failing command, got nil")
	}
	if !strings.Contains(err.Error(), "GH_TOKEN") {
		t.Errorf("error should name GH_TOKEN, got: %v", err)
	}
	if strings.Contains(err.Error(), "partial-secret-leak") {
		t.Errorf("error must not leak command output, got: %v", err)
	}
	if got := os.Getenv("GH_TOKEN"); got != "" {
		t.Errorf("GH_TOKEN = %q, want empty after a failing command", got)
	}
}

// The error names the knob, the real exit code and a tool-agnostic unlock
// hint, never the command string, stdout or stderr (issue #1972).
func TestResolveSecretCmd_NonZeroExit_NamesExitCodeAndUnlockHint(t *testing.T) {
	orig := secretCmdRunner
	t.Cleanup(func() { secretCmdRunner = orig })
	secretCmdRunner = func(cmd string) (string, error) {
		err := exec.Command("sh", "-c", "exit 42").Run()
		return "", err
	}
	_, err := resolveSecretCmd("GH_TOKEN", "rbw get spindrift-gh-token")
	if err == nil {
		t.Fatal("expected error for a failing command, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "GH_TOKEN") {
		t.Errorf("error should name GH_TOKEN, got: %v", msg)
	}
	if !strings.Contains(msg, "42") {
		t.Errorf("error should include the exit code 42, got: %v", msg)
	}
	if !strings.Contains(msg, "unlock") {
		t.Errorf("error should include the unlock remediation hint, got: %v", msg)
	}
}

// Issue #1972: an empty result must still name the knob and the unlock hint.
func TestResolveSecretCmd_EmptyOutput_NamesUnlockHint(t *testing.T) {
	orig := secretCmdRunner
	t.Cleanup(func() { secretCmdRunner = orig })
	secretCmdRunner = func(cmd string) (string, error) {
		return "", nil
	}
	_, err := resolveSecretCmd("GH_TOKEN", "rbw get spindrift-gh-token")
	if err == nil {
		t.Fatal("expected error for empty command output, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "GH_TOKEN") {
		t.Errorf("error should name GH_TOKEN, got: %v", msg)
	}
	if !strings.Contains(msg, "unlock") {
		t.Errorf("error should include the unlock remediation hint, got: %v", msg)
	}
}

// resolveSecretCmd has the command string and its captured stdout to hand
// when it builds the message, and must put neither in the error (issue
// #1972's exposure model).
func TestResolveSecretCmd_NonZeroExit_NeverLeaksCommandOrOutput(t *testing.T) {
	orig := secretCmdRunner
	t.Cleanup(func() { secretCmdRunner = orig })
	secretCmdRunner = func(cmd string) (string, error) {
		return "partial-secret-leak", errors.New("exit status 1: some vault diagnostic on stderr")
	}
	_, err := resolveSecretCmd("GH_TOKEN", "rbw get spindrift-gh-token --verbose")
	if err == nil {
		t.Fatal("expected error for a failing command, got nil")
	}
	msg := err.Error()
	if strings.Contains(msg, "partial-secret-leak") {
		t.Errorf("error must not leak captured stdout, got: %v", msg)
	}
	if strings.Contains(msg, "rbw get spindrift-gh-token --verbose") {
		t.Errorf("error must not leak the command string, got: %v", msg)
	}
	if strings.Contains(msg, "vault diagnostic on stderr") {
		t.Errorf("error must not leak stderr diagnostics, got: %v", msg)
	}
}

// A signal-killed command reports ExitCode() == -1, a sentinel rather than a
// real exit code. Printing it would mislead the operator (issue #1972 review
// finding).
func TestResolveSecretCmd_SignalKilled_OmitsFabricatedExitCode(t *testing.T) {
	orig := secretCmdRunner
	t.Cleanup(func() { secretCmdRunner = orig })
	secretCmdRunner = func(cmd string) (string, error) {
		err := exec.Command("sh", "-c", "kill -9 $$").Run()
		return "", err
	}
	_, err := resolveSecretCmd("GH_TOKEN", "rbw get spindrift-gh-token")
	if err == nil {
		t.Fatal("expected error for a signal-killed command, got nil")
	}
	msg := err.Error()
	if strings.Contains(msg, "-1") {
		t.Errorf("error must not print the ExitCode() sentinel -1 as a real exit code, got: %v", msg)
	}
	if !strings.Contains(msg, "GH_TOKEN") {
		t.Errorf("error should name GH_TOKEN, got: %v", msg)
	}
	if !strings.Contains(msg, "unlock") {
		t.Errorf("error should include the unlock remediation hint, got: %v", msg)
	}
}

func TestParseFlags_CmdFlag_MissingValue(t *testing.T) {
	_, err := parseFlags([]string{"--gh-token-cmd"})
	if err == nil {
		t.Fatal("expected error when cmd flag has no command argument, got nil")
	}
}

// toKebab mirrors lib/renderers.nix's toKebab, which maps an env name to its
// kebab-case vault-item form.
func TestToKebab_ReplacesUnderscoresAndLowercases(t *testing.T) {
	cases := map[string]string{
		"GH_TOKEN":                "gh-token",
		"CLAUDE_CODE_OAUTH_TOKEN": "claude-code-oauth-token",
		"ANTHROPIC_API_KEY":       "anthropic-api-key",
	}
	for env, want := range cases {
		if got := toKebab(env); got != want {
			t.Errorf("toKebab(%q) = %q, want %q", env, got, want)
		}
	}
}

// resolveGlobalSecretCmd runs the same two-step sequence main() runs.
// applySecretCmdFallback must run after loadedDoc is in place, so it is not
// folded into parseFlags. Callers that need a document must set loadedDoc,
// and t.Cleanup it back to nil, before calling this.
func resolveGlobalSecretCmd(t *testing.T, args []string) error {
	t.Helper()
	if _, err := parseFlags(args); err != nil {
		return err
	}
	return applySecretCmdFallback()
}

// --secret-cmd is a templated fallback below every per-secret form: {name}
// substitutes toKebab(env), and it fires only for a secret the run requires.
// The fixture pre-satisfies the Claude/Anthropic pair and leaves the Jira and
// Box tokens unneeded, so only GH_TOKEN should reach secretCmdRunner.
func TestParseFlags_GlobalSecretCmd_RunsTemplate(t *testing.T) {
	orig := secretCmdRunner
	t.Cleanup(func() { secretCmdRunner = orig })
	secretCmdRunner = func(cmd string) (string, error) {
		if cmd != "rbw get spindrift-gh-token" {
			t.Fatalf("secretCmdRunner called with %q, want %q", cmd, "rbw get spindrift-gh-token")
		}
		return "templated-value\n", nil
	}
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GH_TOKEN_CMD", "")
	t.Setenv("BOX_GH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "existing-value")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("JIRA_TOKEN", "")
	t.Setenv("ISSUE_TRACKER", "")
	t.Setenv("CODE_FORGE", "")
	if err := resolveGlobalSecretCmd(t, []string{"--secret-cmd", "rbw get spindrift-{name}"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := os.Getenv("GH_TOKEN"); got != "templated-value" {
		t.Errorf("GH_TOKEN = %q, want %q", got, "templated-value")
	}
}

func TestParseFlags_GlobalSecretCmd_LosesToPerSecretCmdFlag(t *testing.T) {
	orig := secretCmdRunner
	t.Cleanup(func() { secretCmdRunner = orig })
	secretCmdRunner = func(cmd string) (string, error) {
		if cmd != "per-secret-cmd" {
			t.Fatalf("secretCmdRunner called with %q, want %q", cmd, "per-secret-cmd")
		}
		return "per-secret-value", nil
	}
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GH_TOKEN_CMD", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "existing-value")
	if err := resolveGlobalSecretCmd(t, []string{"--gh-token-cmd", "per-secret-cmd", "--secret-cmd", "rbw get spindrift-{name}"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := os.Getenv("GH_TOKEN"); got != "per-secret-value" {
		t.Errorf("GH_TOKEN = %q, want %q (per-secret --gh-token-cmd must win over --secret-cmd)", got, "per-secret-value")
	}
}

func TestParseFlags_GlobalSecretCmd_LosesToCmdEnv(t *testing.T) {
	orig := secretCmdRunner
	t.Cleanup(func() { secretCmdRunner = orig })
	secretCmdRunner = func(cmd string) (string, error) {
		if cmd != "per-secret-env-cmd" {
			t.Fatalf("secretCmdRunner called with %q, want %q", cmd, "per-secret-env-cmd")
		}
		return "per-secret-env-value", nil
	}
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GH_TOKEN_CMD", "per-secret-env-cmd")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "existing-value")
	if err := resolveGlobalSecretCmd(t, []string{"--secret-cmd", "rbw get spindrift-{name}"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := os.Getenv("GH_TOKEN"); got != "per-secret-env-value" {
		t.Errorf("GH_TOKEN = %q, want %q (GH_TOKEN_CMD must win over --secret-cmd)", got, "per-secret-env-value")
	}
}

func TestParseFlags_GlobalSecretCmd_LosesToFileFlag(t *testing.T) {
	orig := secretCmdRunner
	t.Cleanup(func() { secretCmdRunner = orig })
	secretCmdRunner = func(cmd string) (string, error) {
		t.Fatal("secretCmdRunner should not be called when --gh-token-file is set")
		return "", nil
	}
	tokenFile := filepath.Join(t.TempDir(), "token.txt")
	if err := os.WriteFile(tokenFile, []byte("file-value"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GH_TOKEN_CMD", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "existing-value")
	if err := resolveGlobalSecretCmd(t, []string{"--gh-token-file", tokenFile, "--secret-cmd", "rbw get spindrift-{name}"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := os.Getenv("GH_TOKEN"); got != "file-value" {
		t.Errorf("GH_TOKEN = %q, want %q (--gh-token-file must win over --secret-cmd)", got, "file-value")
	}
}

// A direct env value is the lowest of the four per-secret forms and still
// outranks the template.
func TestParseFlags_GlobalSecretCmd_LosesToDirectEnv(t *testing.T) {
	orig := secretCmdRunner
	t.Cleanup(func() { secretCmdRunner = orig })
	secretCmdRunner = func(cmd string) (string, error) {
		t.Fatal("secretCmdRunner should not be called when GH_TOKEN is already set directly")
		return "", nil
	}
	t.Setenv("GH_TOKEN", "direct-value")
	t.Setenv("GH_TOKEN_CMD", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "existing-value")
	if err := resolveGlobalSecretCmd(t, []string{"--secret-cmd", "rbw get spindrift-{name}"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := os.Getenv("GH_TOKEN"); got != "direct-value" {
		t.Errorf("GH_TOKEN = %q, want %q (direct env must win over --secret-cmd)", got, "direct-value")
	}
}

func TestParseFlags_GlobalSecretCmdEnv_RunsTemplate(t *testing.T) {
	orig := secretCmdRunner
	t.Cleanup(func() { secretCmdRunner = orig })
	secretCmdRunner = func(cmd string) (string, error) {
		if cmd != "rbw get spindrift-gh-token" {
			t.Fatalf("secretCmdRunner called with %q, want %q", cmd, "rbw get spindrift-gh-token")
		}
		return "env-templated-value", nil
	}
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GH_TOKEN_CMD", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "existing-value")
	t.Setenv("SECRET_CMD", "rbw get spindrift-{name}")
	if err := resolveGlobalSecretCmd(t, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := os.Getenv("GH_TOKEN"); got != "env-templated-value" {
		t.Errorf("GH_TOKEN = %q, want %q", got, "env-templated-value")
	}
}

func TestParseFlags_GlobalSecretCmdFlag_WinsOverEnv(t *testing.T) {
	orig := secretCmdRunner
	t.Cleanup(func() { secretCmdRunner = orig })
	secretCmdRunner = func(cmd string) (string, error) {
		switch cmd {
		case "rbw get flag-spindrift-gh-token":
			return "flag-templated-value", nil
		case "rbw get env-spindrift-gh-token":
			t.Fatal("SECRET_CMD env template must not run when --secret-cmd flag is set")
		}
		t.Fatalf("secretCmdRunner called with unexpected cmd %q", cmd)
		return "", nil
	}
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GH_TOKEN_CMD", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "existing-value")
	t.Setenv("SECRET_CMD", "rbw get env-spindrift-{name}")
	if err := resolveGlobalSecretCmd(t, []string{"--secret-cmd", "rbw get flag-spindrift-{name}"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := os.Getenv("GH_TOKEN"); got != "flag-templated-value" {
		t.Errorf("GH_TOKEN = %q, want %q (--secret-cmd flag must win over SECRET_CMD env)", got, "flag-templated-value")
	}
}

// The template fallback is gated per knob: JIRA_TOKEN is required only when
// ISSUE_TRACKER=jira.
func TestParseFlags_GlobalSecretCmd_SkipsJiraWhenTrackerNotJira(t *testing.T) {
	orig := secretCmdRunner
	t.Cleanup(func() { secretCmdRunner = orig })
	secretCmdRunner = func(cmd string) (string, error) {
		if strings.Contains(cmd, "jira") {
			t.Fatalf("secretCmdRunner should not be called for JIRA_TOKEN when ISSUE_TRACKER is not jira, got %q", cmd)
		}
		return "value", nil
	}
	t.Setenv("JIRA_TOKEN", "")
	t.Setenv("ISSUE_TRACKER", "github")
	t.Setenv("GH_TOKEN", "already-set")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "already-set")
	if err := resolveGlobalSecretCmd(t, []string{"--secret-cmd", "rbw get spindrift-{name}"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := os.Getenv("JIRA_TOKEN"); got != "" {
		t.Errorf("JIRA_TOKEN = %q, want empty (not required when ISSUE_TRACKER != jira)", got)
	}
}

func TestParseFlags_GlobalSecretCmd_AppliesToJiraWhenTrackerIsJira(t *testing.T) {
	orig := secretCmdRunner
	t.Cleanup(func() { secretCmdRunner = orig })
	secretCmdRunner = func(cmd string) (string, error) {
		if cmd == "rbw get spindrift-jira-token" {
			return "jira-value", nil
		}
		return "value", nil
	}
	t.Setenv("JIRA_TOKEN", "")
	t.Setenv("ISSUE_TRACKER", "jira")
	t.Setenv("GH_TOKEN", "already-set")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "already-set")
	if err := resolveGlobalSecretCmd(t, []string{"--secret-cmd", "rbw get spindrift-{name}"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := os.Getenv("JIRA_TOKEN"); got != "jira-value" {
		t.Errorf("JIRA_TOKEN = %q, want %q", got, "jira-value")
	}
}

func withExtraBackendRow(t *testing.T, row backendRow) {
	t.Helper()
	original := backendRows
	backendRows = append(append([]backendRow{}, original...), row)
	t.Cleanup(func() { backendRows = original })
}

// A backend registered under another name that shares a stock TokenEnvVar,
// on either the tracker or the code forge axis, must still mark that secret
// required, or applySecretCmdFallback skips the vault fetch and validate()
// then rejects the run over a token nothing asked for. A name in no row
// yields TokenEnvVar "", which must match no knob.
func TestParseFlags_GlobalSecretCmd_RequirednessKeysOffTokenEnvVar(t *testing.T) {
	tests := []struct {
		name      string
		extraRow  *backendRow
		env       map[string]string
		secretEnv string
		want      string
	}{
		{
			name: "tracker shares JIRA_TOKEN under a different name",
			extraRow: &backendRow{Descriptor: backend.Descriptor{
				Name:           "my-jira-clone",
				ValidAsTracker: true,
				TokenEnvVar:    backend.Jira.TokenEnvVar,
			}},
			env:       map[string]string{"ISSUE_TRACKER": "my-jira-clone"},
			secretEnv: "JIRA_TOKEN",
			want:      "jira-value",
		},
		{
			name: "tracker shares FORGEJO_TOKEN under a different name",
			extraRow: &backendRow{Descriptor: backend.Descriptor{
				Name:           "my-forgejo-clone",
				ValidAsTracker: true,
				TokenEnvVar:    backend.Forgejo.TokenEnvVar,
			}},
			env:       map[string]string{"ISSUE_TRACKER": "my-forgejo-clone"},
			secretEnv: "FORGEJO_TOKEN",
			want:      "forgejo-value",
		},
		{
			name:      "code forge claims FORGEJO_TOKEN while the tracker does not",
			env:       map[string]string{"CODE_FORGE": "forgejo", "ISSUE_TRACKER": "github"},
			secretEnv: "FORGEJO_TOKEN",
			want:      "forgejo-value",
		},
		{
			name:      "unregistered tracker name matches no knob",
			env:       map[string]string{"ISSUE_TRACKER": "not-a-registered-backend"},
			secretEnv: "JIRA_TOKEN",
			want:      "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.extraRow != nil {
				withExtraBackendRow(t, *tc.extraRow)
			}
			orig := secretCmdRunner
			t.Cleanup(func() { secretCmdRunner = orig })
			secretCmdRunner = func(cmd string) (string, error) {
				switch cmd {
				case "rbw get spindrift-jira-token":
					return "jira-value", nil
				case "rbw get spindrift-forgejo-token":
					return "forgejo-value", nil
				}
				return "value", nil
			}
			t.Setenv(tc.secretEnv, "")
			t.Setenv("CODE_FORGE", "")
			t.Setenv("GH_TOKEN", "already-set")
			t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "already-set")
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if err := resolveGlobalSecretCmd(t, []string{"--secret-cmd", "rbw get spindrift-{name}"}); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := os.Getenv(tc.secretEnv); got != tc.want {
				t.Errorf("%s = %q, want %q", tc.secretEnv, got, tc.want)
			}
		})
	}
}

// CODE_FORGE and ISSUE_TRACKER may be set only through the Consumer flake's
// settings (ADR 0020), which reach the launcher as loadedDoc rather than env
// or flag. A fallback reading only the ambient default misses a secret the
// run needs, JIRA_TOKEN here.
func TestApplySecretCmdFallback_UsesDocumentSettings(t *testing.T) {
	t.Cleanup(func() { loadedDoc = nil })
	loadedDoc = &inputDocument{Settings: map[string]string{"ISSUE_TRACKER": "jira"}}

	orig := secretCmdRunner
	t.Cleanup(func() { secretCmdRunner = orig })
	secretCmdRunner = func(cmd string) (string, error) {
		if cmd == "rbw get spindrift-jira-token" {
			return "jira-value", nil
		}
		return "value", nil
	}
	t.Setenv("JIRA_TOKEN", "")
	t.Setenv("ISSUE_TRACKER", "")
	t.Setenv("GH_TOKEN", "already-set")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "already-set")
	if err := resolveGlobalSecretCmd(t, []string{"--secret-cmd", "rbw get spindrift-{name}"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := os.Getenv("JIRA_TOKEN"); got != "jira-value" {
		t.Errorf("JIRA_TOKEN = %q, want %q (ISSUE_TRACKER=jira set only via the document)", got, "jira-value")
	}
}

// A fully-local pairing set only through the document must not force a
// GH_TOKEN vault lookup. Artifacts carries the FULLY_LOCAL bit nix bakes
// alongside that Settings pairing, so the fixture models a real document:
// secretRequiredThisRun trusts the forwarded artifact only when Settings
// still matches the resolved pairing (ADR 0020; issue #2527 review).
func TestApplySecretCmdFallback_SkipsGhTokenWhenDocumentIsFullyLocal(t *testing.T) {
	t.Cleanup(func() { loadedDoc = nil })
	loadedDoc = &inputDocument{
		Settings:  map[string]string{"CODE_FORGE": "local", "ISSUE_TRACKER": "local"},
		Artifacts: map[string]string{"FULLY_LOCAL": "true"},
	}

	orig := secretCmdRunner
	t.Cleanup(func() { secretCmdRunner = orig })
	secretCmdRunner = func(cmd string) (string, error) {
		if strings.Contains(cmd, "gh-token") {
			t.Fatalf("secretCmdRunner should not be called for GH_TOKEN in a fully-local run, got %q", cmd)
		}
		return "value", nil
	}
	t.Setenv("GH_TOKEN", "")
	t.Setenv("CODE_FORGE", "")
	t.Setenv("ISSUE_TRACKER", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "already-set")
	if err := resolveGlobalSecretCmd(t, []string{"--secret-cmd", "rbw get spindrift-{name}"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := os.Getenv("GH_TOKEN"); got != "" {
		t.Errorf("GH_TOKEN = %q, want empty (not required when CODE_FORGE and ISSUE_TRACKER are both local)", got)
	}
}

// BOX_GH_TOKEN (ADR 0016 two-actor separation) is opt-in with no
// requiredness signal of its own, so the template must never auto-source it.
func TestParseFlags_GlobalSecretCmd_SkipsBoxGhToken(t *testing.T) {
	orig := secretCmdRunner
	t.Cleanup(func() { secretCmdRunner = orig })
	secretCmdRunner = func(cmd string) (string, error) {
		if strings.Contains(cmd, "box-gh-token") {
			t.Fatalf("secretCmdRunner should not be called for BOX_GH_TOKEN, got %q", cmd)
		}
		return "value", nil
	}
	t.Setenv("BOX_GH_TOKEN", "")
	t.Setenv("GH_TOKEN", "already-set")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "already-set")
	if err := resolveGlobalSecretCmd(t, []string{"--secret-cmd", "rbw get spindrift-{name}"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := os.Getenv("BOX_GH_TOKEN"); got != "" {
		t.Errorf("BOX_GH_TOKEN = %q, want empty (never auto-sourced by the template)", got)
	}
}

// ANTHROPIC_API_KEY sorts before CLAUDE_CODE_OAUTH_TOKEN in secretKnobs, so
// the fallback pass must see every per-secret resolution parseFlags already
// made before it decides the pair is unsatisfied.
func TestParseFlags_GlobalSecretCmd_ExplicitClaudeCmdPreemptsAnthropicFallback(t *testing.T) {
	orig := secretCmdRunner
	t.Cleanup(func() { secretCmdRunner = orig })
	secretCmdRunner = func(cmd string) (string, error) {
		if cmd == "claude-cmd" {
			return "claude-value", nil
		}
		if strings.Contains(cmd, "anthropic-api-key") {
			t.Fatalf("secretCmdRunner should not be called for ANTHROPIC_API_KEY when --claude-code-oauth-token-cmd already satisfies the pair, got %q", cmd)
		}
		return "value", nil
	}
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GH_TOKEN", "already-set")
	args := []string{"--claude-code-oauth-token-cmd", "claude-cmd", "--secret-cmd", "rbw get spindrift-{name}"}
	if err := resolveGlobalSecretCmd(t, args); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := os.Getenv("CLAUDE_CODE_OAUTH_TOKEN"); got != "claude-value" {
		t.Errorf("CLAUDE_CODE_OAUTH_TOKEN = %q, want %q", got, "claude-value")
	}
	if got := os.Getenv("ANTHROPIC_API_KEY"); got != "" {
		t.Errorf("ANTHROPIC_API_KEY = %q, want empty (CLAUDE_CODE_OAUTH_TOKEN already satisfies the pair)", got)
	}
}

func TestParseFlags_GlobalSecretCmd_FailureIsError(t *testing.T) {
	orig := secretCmdRunner
	t.Cleanup(func() { secretCmdRunner = orig })
	secretCmdRunner = func(cmd string) (string, error) {
		return "partial-secret-leak", errors.New("exit status 1")
	}
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GH_TOKEN_CMD", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "existing-value")
	err := resolveGlobalSecretCmd(t, []string{"--secret-cmd", "rbw get spindrift-{name}"})
	if err == nil {
		t.Fatal("expected error for a failing templated command, got nil")
	}
	if !strings.Contains(err.Error(), "GH_TOKEN") {
		t.Errorf("error should name GH_TOKEN, got: %v", err)
	}
	if strings.Contains(err.Error(), "partial-secret-leak") {
		t.Errorf("error must not leak command output, got: %v", err)
	}
}

func TestParseFlags_GlobalSecretCmd_MissingValue(t *testing.T) {
	_, err := parseFlags([]string{"--secret-cmd"})
	if err == nil {
		t.Fatal("expected error when --secret-cmd has no command argument, got nil")
	}
}

func TestParseFlags_NoCmdOrFile_LeavesDirectEnv(t *testing.T) {
	t.Setenv("GH_TOKEN", "direct-value")
	t.Setenv("GH_TOKEN_CMD", "")
	_, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := os.Getenv("GH_TOKEN"); got != "direct-value" {
		t.Errorf("GH_TOKEN = %q, want %q (direct env must be left untouched)", got, "direct-value")
	}
}

// Both seams are package vars, so all four combinations run here without a
// real TTY (issue #2559).
func TestIsInteractiveTTY_Composition(t *testing.T) {
	origStdin, origStderr := isStdinTTY, isStderrTTY
	t.Cleanup(func() { isStdinTTY, isStderrTTY = origStdin, origStderr })

	for _, tc := range []struct {
		stdin, stderr, want bool
	}{
		{false, false, false},
		{false, true, false},
		{true, false, false},
		{true, true, true},
	} {
		isStdinTTY = func() bool { return tc.stdin }
		isStderrTTY = func() bool { return tc.stderr }
		if got := isInteractiveTTY(); got != tc.want {
			t.Errorf("isInteractiveTTY() with stdin=%v stderr=%v = %v, want %v", tc.stdin, tc.stderr, got, tc.want)
		}
	}
}

// /dev/null is a character device, so the old os.ModeCharDevice check
// wrongly treated it as an interactive TTY (issue #2559). This runs the real
// call isStdinTTY and isStderrTTY wrap against a real non-tty descriptor;
// faking those package vars would prove nothing about the regression.
func TestTermIsTerminal_DevNull_NotATTY(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer f.Close()
	if term.IsTerminal(f.Fd()) {
		t.Error("want /dev/null to not be reported as a TTY (regression: os.ModeCharDevice used to wrongly treat it as one)")
	}
}

// Every other test here fakes secretCmdRunner, so this one runs the
// production implementation to prove the seam is wired to something real.
func TestSecretCmdRunner_Default_RunsRealCommand(t *testing.T) {
	out, err := secretCmdRunner("printf real-value")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "real-value" {
		t.Errorf("secretCmdRunner(%q) = %q, want %q", "printf real-value", out, "real-value")
	}
}

// When the launcher's own stdin and stderr are TTYs the secret command
// inherits them as raw file descriptors, so a vault tool's prompt and stdin
// read both work while stdout is still captured as the secret (issue #1971).
func TestSecretCmdRunner_Interactive_PassesStdinAndStderr(t *testing.T) {
	origInteractive := isInteractiveTTY
	isInteractiveTTY = func() bool { return true }
	t.Cleanup(func() { isInteractiveTTY = origInteractive })

	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stdinW.WriteString("unlock-me\n"); err != nil {
		t.Fatal(err)
	}
	stdinW.Close()
	origStdin := os.Stdin
	os.Stdin = stdinR
	t.Cleanup(func() { os.Stdin = origStdin })

	stderrFile, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	origStderr := os.Stderr
	os.Stderr = stderrFile
	t.Cleanup(func() { os.Stderr = origStderr })

	out, err := secretCmdRunner(`read line; printf "vault prompt\n" >&2; printf "secret-for-%s" "$line"`)
	stderrFile.Close()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "secret-for-unlock-me" {
		t.Errorf("secretCmdRunner output = %q, want %q", out, "secret-for-unlock-me")
	}

	stderrOut, err := os.ReadFile(stderrFile.Name())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stderrOut), "vault prompt") {
		t.Errorf("stderr passthrough missing prompt, got %q", stderrOut)
	}
	if strings.Contains(string(stderrOut), "secret-for") {
		t.Errorf("secret leaked into passed-through stderr, got %q", stderrOut)
	}
}

// Without a TTY, a command that reads stdin gets an immediate EOF instead of
// blocking the run, and its stderr never reaches the launcher's own stderr.
// Behaviour must stay as it was before the TTY gate existed (issue #1971).
func TestSecretCmdRunner_NonInteractive_NoStdinAttached_NoHang(t *testing.T) {
	origInteractive := isInteractiveTTY
	isInteractiveTTY = func() bool { return false }
	t.Cleanup(func() { isInteractiveTTY = origInteractive })

	stderrFile, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	origStderr := os.Stderr
	os.Stderr = stderrFile
	t.Cleanup(func() { os.Stderr = origStderr })

	done := make(chan struct{})
	var out string
	var runErr error
	go func() {
		out, runErr = secretCmdRunner(`read line; printf "vault diagnostic\n" >&2; printf "value"`)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("secretCmdRunner hung waiting on stdin in non-interactive mode")
	}
	stderrFile.Close()

	if runErr != nil {
		t.Fatalf("unexpected error: %v", runErr)
	}
	if out != "value" {
		t.Errorf("secretCmdRunner output = %q, want %q", out, "value")
	}
	stderrOut, err := os.ReadFile(stderrFile.Name())
	if err != nil {
		t.Fatal(err)
	}
	if len(stderrOut) != 0 {
		t.Errorf("command stderr leaked to launcher stderr, got %q", stderrOut)
	}
}

func TestPrintHelpFull_ShowsSecretFileFlags(t *testing.T) {
	var buf bytes.Buffer
	printHelpFull(&buf)
	out := buf.String()
	for _, want := range []string{"--gh-token-file", "--anthropic-api-key-file", "--claude-code-oauth-token-file"} {
		if !strings.Contains(out, want) {
			t.Errorf("help output missing %s", want)
		}
	}
}

func TestPrintHelpFull_ShowsSecretCmdFlags(t *testing.T) {
	var buf bytes.Buffer
	printHelpFull(&buf)
	out := buf.String()
	for _, want := range []string{"--gh-token-cmd", "--anthropic-api-key-cmd", "--claude-code-oauth-token-cmd"} {
		if !strings.Contains(out, want) {
			t.Errorf("help output missing %s", want)
		}
	}
}

func TestPrintHelpFull_ShowsGlobalSecretCmd(t *testing.T) {
	var buf bytes.Buffer
	printHelpFull(&buf)
	out := buf.String()
	for _, want := range []string{"--secret-cmd", "SECRET_CMD", "{name}"} {
		if !strings.Contains(out, want) {
			t.Errorf("help output missing %s, got:\n%s", want, out)
		}
	}
}

func TestParseFlags_NoBuildPassthrough(t *testing.T) {
	remaining, err := parseFlags([]string{"dispatch", "--no-build"})
	if err != nil {
		t.Fatalf("parseFlags with --no-build: unexpected error: %v", err)
	}
	if len(remaining) != 2 || remaining[0] != "dispatch" || remaining[1] != "--no-build" {
		t.Errorf("remaining = %v, want [dispatch --no-build]", remaining)
	}
}

func TestParseFlags_NoBuildWithIssue(t *testing.T) {
	remaining, err := parseFlags([]string{"dispatch", "--no-build", "42"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(remaining) != 3 || remaining[1] != "--no-build" || remaining[2] != "42" {
		t.Errorf("remaining = %v, want [dispatch --no-build 42]", remaining)
	}
}

func TestDispatchNoBuildArgs(t *testing.T) {
	noBuild, rest := dispatchNoBuildArgs([]string{"--no-build", "123"})
	if !noBuild {
		t.Error("want noBuild=true, got false")
	}
	if len(rest) != 1 || rest[0] != "123" {
		t.Errorf("rest = %v, want [123]", rest)
	}
}

func TestDispatchNoBuildArgs_AbsentFlag(t *testing.T) {
	noBuild, rest := dispatchNoBuildArgs([]string{"42"})
	if noBuild {
		t.Error("want noBuild=false, got true")
	}
	if len(rest) != 1 || rest[0] != "42" {
		t.Errorf("rest = %v, want [42]", rest)
	}
}

func TestDispatchYesArgs_YesFlag(t *testing.T) {
	yes, rest := dispatchYesArgs([]string{"--yes", "42"})
	if !yes {
		t.Error("want yes=true, got false")
	}
	if len(rest) != 1 || rest[0] != "42" {
		t.Errorf("rest = %v, want [42]", rest)
	}
}

func TestDispatchYesArgs_ForceAlias(t *testing.T) {
	yes, _ := dispatchYesArgs([]string{"--force"})
	if !yes {
		t.Error("--force must set yes=true")
	}
}

func TestDispatchYesArgs_Absent(t *testing.T) {
	yes, rest := dispatchYesArgs([]string{"42"})
	if yes {
		t.Error("want yes=false, got true")
	}
	if len(rest) != 1 || rest[0] != "42" {
		t.Errorf("rest = %v, want [42]", rest)
	}
}

// Issue #2202 added --self-contained.
func TestDispatchSelfContainedArgs(t *testing.T) {
	selfContained, rest := dispatchSelfContainedArgs([]string{"--self-contained", "42"})
	if !selfContained {
		t.Error("want selfContained=true, got false")
	}
	if len(rest) != 1 || rest[0] != "42" {
		t.Errorf("rest = %v, want [42]", rest)
	}
}

func TestDispatchSelfContainedArgs_Absent(t *testing.T) {
	selfContained, rest := dispatchSelfContainedArgs([]string{"42"})
	if selfContained {
		t.Error("want selfContained=false, got true")
	}
	if len(rest) != 1 || rest[0] != "42" {
		t.Errorf("rest = %v, want [42]", rest)
	}
}

// Issue #3054: all three dispatch-family booleans and a numeric issue ID
// must resolve in one pass.
func TestParseIssuePositionals_AllBoolsPlusID(t *testing.T) {
	parsed := parseIssuePositionals([]string{"--no-build", "--yes", "--self-contained", "42"})
	if !parsed.noBuild || !parsed.yes || !parsed.selfContained {
		t.Errorf("noBuild=%v yes=%v selfContained=%v, want all true", parsed.noBuild, parsed.yes, parsed.selfContained)
	}
	if len(parsed.remaining) != 1 || parsed.remaining[0] != "42" {
		t.Errorf("remaining = %v, want [42]", parsed.remaining)
	}
}

func TestParseIssuePositionals_NoBoolsJustID(t *testing.T) {
	parsed := parseIssuePositionals([]string{"42"})
	if parsed.noBuild || parsed.yes || parsed.selfContained {
		t.Errorf("noBuild=%v yes=%v selfContained=%v, want all false", parsed.noBuild, parsed.yes, parsed.selfContained)
	}
	if len(parsed.remaining) != 1 || parsed.remaining[0] != "42" {
		t.Errorf("remaining = %v, want [42]", parsed.remaining)
	}
}

func TestParseIssuePositionals_BoolBeforeID(t *testing.T) {
	parsed := parseIssuePositionals([]string{"--yes", "42"})
	if !parsed.yes {
		t.Error("want yes=true, got false")
	}
	if len(parsed.remaining) != 1 || parsed.remaining[0] != "42" {
		t.Errorf("remaining = %v, want [42]", parsed.remaining)
	}
}

// ID before the bool is the exact `spindrift recover --yes 42` shape, where
// "--yes" was once mistaken for the issue ID (issue #3054).
func TestParseIssuePositionals_IDBeforeBool(t *testing.T) {
	parsed := parseIssuePositionals([]string{"42", "--yes"})
	if !parsed.yes {
		t.Error("want yes=true, got false")
	}
	if len(parsed.remaining) != 1 || parsed.remaining[0] != "42" {
		t.Errorf("remaining = %v, want [42]", parsed.remaining)
	}
}

// Non-numeric positionals pass through unfiltered; parseIssuePositionals's
// doc comment in flags.go says why (issue #3054). A flag-shaped positional,
// such as a slug ID surviving a "--" separator, must also survive verbatim,
// the case a prior version silently dropped (issue #3055), because every
// issue-taking verb now uses remaining with no further filtering.
func TestParseIssuePositionals_NonNumericPassedThrough(t *testing.T) {
	parsed := parseIssuePositionals([]string{"--no-build", "foo", "--yes", "42", "bar", "--odd-slug"})
	if !parsed.noBuild || !parsed.yes || parsed.selfContained {
		t.Errorf("noBuild=%v yes=%v selfContained=%v, want noBuild=true yes=true selfContained=false", parsed.noBuild, parsed.yes, parsed.selfContained)
	}
	want := []string{"foo", "42", "bar", "--odd-slug"}
	if len(parsed.remaining) != len(want) {
		t.Fatalf("remaining = %v, want %v", parsed.remaining, want)
	}
	for i, w := range want {
		if parsed.remaining[i] != w {
			t.Errorf("pos %d: got %q, want %q", i, parsed.remaining[i], w)
		}
	}
}

func TestParseFlags_YesPassthrough(t *testing.T) {
	remaining, err := parseFlags([]string{"dispatch", "--yes", "42"})
	if err != nil {
		t.Fatalf("parseFlags with --yes: unexpected error: %v", err)
	}
	if len(remaining) != 3 || remaining[1] != "--yes" || remaining[2] != "42" {
		t.Errorf("remaining = %v, want [dispatch --yes 42]", remaining)
	}
}

func TestParseFlags_ForcePassthrough(t *testing.T) {
	remaining, err := parseFlags([]string{"dispatch", "--force"})
	if err != nil {
		t.Fatalf("parseFlags with --force: unexpected error: %v", err)
	}
	if len(remaining) != 2 || remaining[1] != "--force" {
		t.Errorf("remaining = %v, want [dispatch --force]", remaining)
	}
}

func TestPrintHelp_ShowsNoBuildFlag(t *testing.T) {
	var buf bytes.Buffer
	printHelp(&buf)
	if !strings.Contains(buf.String(), "--no-build") {
		t.Error("help output missing --no-build flag")
	}
}

// Issue #2033 added --continuous as the bare-flag alias for
// --continuous-dispatch 1.
func TestPrintHelpFull_ShowsContinuousFlag(t *testing.T) {
	var buf bytes.Buffer
	printHelpFull(&buf)
	out := buf.String()
	if !strings.Contains(out, "--continuous ") {
		t.Error("full help output missing --continuous flag")
	}
	if !strings.Contains(out, "--continuous-dispatch") {
		t.Error("full help output's --continuous doc missing a pointer to --continuous-dispatch")
	}
}

// Issue #2520 slice 2 added the generic choice-knob guard.
func TestValidateChoice(t *testing.T) {
	t.Run("valid value is a no-op", func(t *testing.T) {
		if err := validateChoice("MERGE_MODE", "auto"); err != nil {
			t.Errorf("validateChoice(MERGE_MODE, auto) = %v, want nil", err)
		}
	})

	t.Run("invalid value names flag, value, and choices", func(t *testing.T) {
		err := validateChoice("MERGE_MODE", "bogus")
		if err == nil {
			t.Fatal("validateChoice(MERGE_MODE, bogus) = nil, want error")
		}
		msg := err.Error()
		for _, want := range []string{"MERGE_MODE", "bogus", "immediate", "auto", "manual"} {
			if !strings.Contains(msg, want) {
				t.Errorf("error %q missing %q", msg, want)
			}
		}
	})

	t.Run("unknown env is a no-op", func(t *testing.T) {
		if err := validateChoice("NOT_A_REAL_ENV", "whatever"); err != nil {
			t.Errorf("validateChoice(NOT_A_REAL_ENV, whatever) = %v, want nil", err)
		}
	})

	t.Run("non-choice env is a no-op", func(t *testing.T) {
		// MODEL has no declared choices, so any value is accepted.
		if err := validateChoice("MODEL", "whatever"); err != nil {
			t.Errorf("validateChoice(MODEL, whatever) = %v, want nil", err)
		}
	})
}

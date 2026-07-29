package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestModelDefault_IsOpus48 asserts that the main/coordinator session model
// default is claude-opus-4-8, not an older release or the worker tier's
// claude-sonnet-5 (issue #2055).
func TestModelDefault_IsOpus48(t *testing.T) {
	const want = "claude-opus-4-8"
	for _, e := range schemaFlags {
		if e.env == "MODEL" {
			if e.dflt != want {
				t.Errorf("MODEL default = %q, want %q", e.dflt, want)
			}
			return
		}
	}
	t.Fatal("MODEL entry not found in schemaFlags")
}

// TestSchemaFlags_BwrapUnshareNetIsBool asserts the bwrap-unshare-net entry
// is a presence-style bool flag, not a string (issue #2145 slice A).
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

// TestSchemaFlags_GenericBoolsAreBool asserts each of the six generic
// boolean knobs converted in issue #2146 slice 1 renders as a presence-style
// bool flag, not a string.
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

// TestExtractInputFlag_Present extracts the document path and strips both
// tokens from the remaining args.
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

// TestExtractInputFlag_Absent leaves args untouched and returns an empty path.
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

// TestExtractInputFlag_MissingValue errors instead of silently swallowing a
// trailing --input.
func TestExtractInputFlag_MissingValue(t *testing.T) {
	_, _, err := extractInputFlag([]string{"--input"})
	if err == nil {
		t.Fatal("want error for --input with no value")
	}
}

// TestParseFlags_SetEnv: a recognized flag is injected into the environment.
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

// TestParseFlags_FlagWinsOverEnv: flag > env precedence.
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

// TestParseFlags_RepoSlugFlagWinsOverEnv: CLI flag wins over env for
// REPO_SLUG, confirming the promoted identity knob honours flag > env
// precedence even when a settings-baked default is in play at runtime.
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

// TestParseFlags_EnvFallback: env is used when no flag is supplied.
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

// TestParseFlags_UnknownFlag: unrecognised --flag returns an error.
func TestParseFlags_UnknownFlag(t *testing.T) {
	_, err := parseFlags([]string{"--not-a-schema-flag", "value"})
	if err == nil {
		t.Fatal("expected error for unknown flag, got nil")
	}
}

// TestParseFlags_PassthroughPositional: positional args are returned unchanged.
func TestParseFlags_PassthroughPositional(t *testing.T) {
	remaining, err := parseFlags([]string{"build", "--max-jobs", "2", "extra"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(remaining) != 2 || remaining[0] != "build" || remaining[1] != "extra" {
		t.Errorf("remaining = %v, want [build extra]", remaining)
	}
}

// TestParseFlags_DoubleDash: args after "--" are passed through unchanged.
func TestParseFlags_DoubleDash(t *testing.T) {
	remaining, err := parseFlags([]string{"--", "--not-parsed"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(remaining) != 1 || remaining[0] != "--not-parsed" {
		t.Errorf("remaining = %v, want [--not-parsed]", remaining)
	}
}

// TestParseFlags_MissingValue: flag with no value returns an error.
func TestParseFlags_MissingValue(t *testing.T) {
	_, err := parseFlags([]string{"--issue-number"})
	if err == nil {
		t.Fatal("expected error when flag value is missing, got nil")
	}
}

// TestSchemaFlags_ExcludesRemovedDepsKnobs: DEPS_POLL_SECS/DEPS_WAIT_SECS
// configured the in-process dependency-wave poll, deleted by #522/#524; the
// knobs must not survive in the schema-generated flag table (ADR 0019).
func TestSchemaFlags_ExcludesRemovedDepsKnobs(t *testing.T) {
	for _, removed := range []string{"DEPS_POLL_SECS", "DEPS_WAIT_SECS"} {
		for _, entry := range schemaFlags {
			if entry.env == removed {
				t.Errorf("removed knob %s must not appear in schemaFlags", removed)
			}
		}
	}
}

// TestParseFlags_SecretsExcluded: secret knobs must not appear in schemaFlags.
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

// TestParseFlags_MultipleFlags: multiple flags are all injected.
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

// TestParseFlags_BoolFlag_BarePresence: a bare bool-kind flag (no value
// token) sets its env var to "1" and does not consume the next arg (issue
// #2145 slice B).
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

// TestParseFlags_BoolFlag_EqualsForms: the --flag=<value> equals form is ON
// for "1"/"true" and OFF for ""/"0"/"false" (issue #2145 slice B).
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

// TestParseFlags_BoolFlag_SpaceSeparatedIsPositional: a token following a
// bare bool-kind flag is never swallowed as its value — it survives as a
// normal positional arg in remaining (issue #2145 slice B).
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

// TestParseFlags_BoolFlag_ExplicitOffOverridesAmbient: an explicit
// --flag=0/--flag=false/--flag= clears an already-set env var to "" rather
// than leaving the ambient "1" in place — flag-over-env precedence applies
// to the off case too (ADR 0020; issue #2145 slice B).
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

// TestDispatchIssueArg_Numeric: a numeric arg is returned as the issue number.
func TestDispatchIssueArg_Numeric(t *testing.T) {
	got := dispatchIssueArg([]string{"123"})
	if got != "123" {
		t.Errorf("dispatchIssueArg([\"123\"]) = %q, want %q", got, "123")
	}
}

// TestDispatchIssueArg_Empty: empty args return empty string.
func TestDispatchIssueArg_Empty(t *testing.T) {
	got := dispatchIssueArg([]string{})
	if got != "" {
		t.Errorf("dispatchIssueArg([]) = %q, want %q", got, "")
	}
}

// TestDispatchIssueArg_NonNumeric: non-numeric first arg returns empty string.
func TestDispatchIssueArg_NonNumeric(t *testing.T) {
	got := dispatchIssueArg([]string{"not-an-issue"})
	if got != "" {
		t.Errorf("dispatchIssueArg([\"not-an-issue\"]) = %q, want empty (non-numeric ignored)", got)
	}
}

// TestPrintVersion_Format: version output starts with "spindrift" and includes a rev.
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

// TestPrintHelp_UsageLineNamesSpindrift: the concise help carries a usage line naming the binary.
func TestPrintHelp_UsageLineNamesSpindrift(t *testing.T) {
	var buf bytes.Buffer
	printHelp(&buf)
	if !strings.Contains(buf.String(), "Usage: spindrift [flags] <subcommand>") {
		t.Errorf("help must contain a usage line naming spindrift, got:\n%s", buf.String())
	}
}

// TestPrintHelp_Concise_PointsToFullReference: the concise help must route users
// to the full reference (man page and --help --all) rather than dumping every flag.
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

// TestPrintHelp_RepoSlugNotesLocalExemption: --repo-slug's help text must
// flag that it's required unless the run is fully local (CODE_FORGE=local
// and ISSUE_TRACKER=local both set), not an unconditional "(required)"
// (issue #1895).
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

// TestPrintHelp_Concise_OmitsRareFlags: the concise help stays concise — it must
// NOT enumerate the long tail of tuning knobs (those live in --help --all / man).
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

// TestPrintHelp_ShowsDispatchSubcommand: help output names dispatch as a subcommand (not just a flag doc).
func TestPrintHelp_ShowsDispatchSubcommand(t *testing.T) {
	var buf bytes.Buffer
	printHelp(&buf)
	out := buf.String()
	// "dispatch" must appear as a standalone subcommand entry, not buried in a flag doc.
	if !strings.Contains(out, "dispatch") {
		t.Errorf("help output must show 'dispatch' subcommand, got:\n%s", out)
	}
}

// TestPrintSubcommands_ConsoleFirst verifies console is the first
// subcommand line advertised — bare `spindrift` now points operators at the
// interactive console first (ADR 0023's "bare invocation keeps printing
// help, now pointing at console").
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

// TestParseFlags_ContinuousPassthrough: --continuous passes through like
// --no-build, rather than erroring as an unknown flag (issue #2033).
func TestParseFlags_ContinuousPassthrough(t *testing.T) {
	remaining, err := parseFlags([]string{"dispatch", "--continuous"})
	if err != nil {
		t.Fatalf("parseFlags with --continuous: unexpected error: %v", err)
	}
	if len(remaining) != 2 || remaining[0] != "dispatch" || remaining[1] != "--continuous" {
		t.Errorf("remaining = %v, want [dispatch --continuous]", remaining)
	}
}

// TestPrintSubcommands_ExactOutput pins the rendered subcommand listing
// byte-for-byte so a future subcommandRegistry-vs-format change (e.g. the
// column-width constant in printSubcommands) can't silently misalign the
// output the way a hand-picked width once did (issue #1575 review).
func TestPrintSubcommands_ExactOutput(t *testing.T) {
	want := "Subcommands:\n" +
		"  console                                                  browse the open backlog interactively (read-only)\n" +
		"  dispatch [--no-build] [--yes] [--continuous] [issue...]  dispatch agents in waves; an issue list dispatches exactly those (bypasses label/barrier gates)\n" +
		"  research [--no-build] [--yes] [--continuous] [issue...]  advise-only research dispatch: drains agent-research (or an issue list) and posts a verdict comment; never merges, never promotes\n" +
		"  preview [issue...]                                       dry-run: show what dispatch would pick up, in order\n" +
		"  build                                                    realize the agent image without running any agent\n" +
		"  recover <issue>                                          run the merge gate for a single issue\n" +
		"  doctor                                                   check forge credentials, repository connectivity, and label presence (triage fatal, research advisory)\n" +
		"  reconcile                                                local-tracker bookkeeping sweep: close issues whose recorded landing PR merged (no-op on github/jira)\n"

	var buf bytes.Buffer
	printSubcommands(&buf)
	if got := buf.String(); got != want {
		t.Errorf("printSubcommands output =\n%s\nwant:\n%s", got, want)
	}
}

// TestPrintHelp_ShowsResearchSubcommand verifies the research dispatch kind
// (ADR 0022) is discoverable beside dispatch, not buried in a flag doc.
func TestPrintHelp_ShowsResearchSubcommand(t *testing.T) {
	var buf bytes.Buffer
	printHelp(&buf)
	out := buf.String()
	if !strings.Contains(out, "research") {
		t.Errorf("help output must show 'research' subcommand, got:\n%s", out)
	}
}

// TestPrintHelpFull_ContainsLabelEntry: the full reference includes --label with its doc.
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

// TestPrintHelpFull_RepoSlugAndGhTokenNoteLocalExemption verifies the full
// reference's REPO_SLUG/GH_TOKEN doc strings, sourced from lib/env-schema.nix,
// spell out the fully-local exemption — mirroring the existing JIRA_TOKEN
// doc's "required when ISSUE_TRACKER=jira" precedent (issue #1895). Matches
// on each line's leading env/flag token rather than a bare substring search,
// so it targets only GH_TOKEN's own two rendered lines (env-only and its
// --gh-token-file secret-file flag) and doesn't false-positive on a doc
// string that merely mentions "GH_TOKEN" in prose (e.g. BOX_GH_TOKEN's,
// issue #380).
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

// TestPrintHelpFull_GroupsFlags: the full reference groups flags under their
// schema-declared category headings rather than a flat dump.
func TestPrintHelpFull_GroupsFlags(t *testing.T) {
	var buf bytes.Buffer
	printHelpFull(&buf)
	out := buf.String()
	for _, g := range []string{"Issue discovery", "Models", "Sandbox & resources"} {
		if !strings.Contains(out, g) {
			t.Errorf("full help output missing group heading %q, got:\n%s", g, out)
		}
	}
}

// TestPrintHelpFull_CoversEverySchemaFlag: no flag may silently drop out of the
// full reference (e.g. a knob whose group is absent from groupOrder).
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

// TestPrintHelpFull_BoolFlagNoValuePlaceholder: a presence-style bool flag
// (kind = "bool", issue #2145) renders in the full reference labelled "bool"
// and with no value placeholder — it takes no following value.
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

// TestSchemaFlags_AllHaveGroup: every generated flag row must carry a group, so
// grouping in the full help and man page is total.
func TestSchemaFlags_AllHaveGroup(t *testing.T) {
	for _, e := range schemaFlags {
		if e.group == "" {
			t.Errorf("flag --%s has no group; add `group = ...` to its lib/env-schema.nix entry", e.flag)
		}
	}
}

// TestGroupOrder_CoversEverySchemaGroup: every group used by a flag must appear
// in groupOrder, else printHelpFull would drop that group's flags.
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

// TestParseFlags_AliasSetEnv: an alias flag resolves to the same env var as the long form.
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

// TestPrintHelpFull_ShowsAlias: aliased knobs show the alias next to the long form.
func TestPrintHelpFull_ShowsAlias(t *testing.T) {
	var buf bytes.Buffer
	printHelpFull(&buf)
	out := buf.String()
	if !strings.Contains(out, "--issue-number, --issue") {
		t.Errorf("full help output missing alias display; want --issue-number, --issue in:\n%s", out)
	}
}

// TestParseFlags_FileFlag_ReadsToken: --<name>-file reads the file and sets the env var.
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

// TestParseFlags_FileFlag_WinsOverEnv: file flag takes precedence over env var.
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

// TestParseFlags_FileFlag_MissingFile: --<name>-file with non-existent path returns an error.
func TestParseFlags_FileFlag_MissingFile(t *testing.T) {
	_, err := parseFlags([]string{"--gh-token-file", "/nonexistent/path/token.txt"})
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
	if !strings.Contains(err.Error(), "/nonexistent/path/token.txt") {
		t.Errorf("error should mention the path, got: %v", err)
	}
}

// TestParseFlags_FileFlag_MissingValue: --<name>-file with no following arg returns an error.
func TestParseFlags_FileFlag_MissingValue(t *testing.T) {
	_, err := parseFlags([]string{"--gh-token-file"})
	if err == nil {
		t.Fatal("expected error when file flag has no path argument, got nil")
	}
}

// TestPrintHelpFull_SecretKnobEnvOnly: secret knobs appear as env-only (no --flag prefix).
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

// TestParseFlags_FileFlag_StripsNewline: trailing newline is stripped from file content.
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

// TestParseFlags_CmdFlag_RunsCommand: --<name>-cmd runs the injected command
// runner and sets the env var to its trimmed output.
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

// TestParseFlags_CmdEnv_RunsCommand: <NAME>_CMD env var (no flag) runs the
// injected command runner and sets the env var to its trimmed output.
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

// TestParseFlags_CmdFlag_WinsOverCmdEnv: --<name>-cmd flag takes precedence
// over a <NAME>_CMD env var for the same secret.
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

// TestParseFlags_CmdEnv_WinsOverFileFlag: <NAME>_CMD env takes precedence
// over a --<name>-file flag for the same secret.
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

// TestParseFlags_CmdFlagAndFileFlag_IsConfigError: supplying both
// --<name>-cmd and --<name>-file for the same secret is a configuration error.
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

// TestParseFlags_CmdFlag_EmptyOutputIsError: an empty command result aborts
// with a named, value-free error instead of setting an empty secret.
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

// TestParseFlags_CmdFlag_NonZeroExitIsError: a failing command aborts with a
// named, value-free error and never leaks the command's stderr/stdout.
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

// TestResolveSecretCmd_NonZeroExit_NamesExitCodeAndUnlockHint: a failing
// secret command's error names the knob, the real exit code, and a generic,
// tool-agnostic unlock hint (issue #1972) — never the command string, stdout,
// or stderr.
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

// TestResolveSecretCmd_EmptyOutput_NamesUnlockHint: an empty secret command
// result's error names the knob and a generic, tool-agnostic unlock hint
// (issue #1972).
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

// TestResolveSecretCmd_NonZeroExit_NeverLeaksCommandOrOutput: the unlock-hint
// error never carries the command string or its captured stdout, even though
// both are available to resolveSecretCmd when building the message (issue
// #1972's exposure-model constraint).
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

// TestResolveSecretCmd_SignalKilled_OmitsFabricatedExitCode: a command
// terminated by a signal reports ExitCode() == -1 (not a real exit code);
// the error must not print that sentinel as if it were one, since -1 would
// mislead the operator (issue #1972 review finding).
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

// TestParseFlags_CmdFlag_MissingValue: --<name>-cmd with no following arg
// returns an error.
func TestParseFlags_CmdFlag_MissingValue(t *testing.T) {
	_, err := parseFlags([]string{"--gh-token-cmd"})
	if err == nil {
		t.Fatal("expected error when cmd flag has no command argument, got nil")
	}
}

// TestToKebab_ReplacesUnderscoresAndLowercases: toKebab mirrors lib/renderers.nix's
// toKebab, mapping a SCREAMING_SNAKE_CASE env name to its kebab-case vault-item form.
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

// resolveGlobalSecretCmd runs parseFlags then applySecretCmdFallback, the
// same two-step sequence main() runs (applySecretCmdFallback must run after
// loadedDoc is in place, so it is not folded into parseFlags itself — see
// applySecretCmdFallback's doc comment). Callers that need a document in
// place must set loadedDoc (and t.Cleanup it back to nil) before calling
// this.
func resolveGlobalSecretCmd(t *testing.T, args []string) error {
	t.Helper()
	if _, err := parseFlags(args); err != nil {
		return err
	}
	return applySecretCmdFallback()
}

// TestParseFlags_GlobalSecretCmd_RunsTemplate: --secret-cmd is a templated
// fallback below every per-secret form — {name} substitutes toKebab(env), and
// it fires only for a secret the run actually requires (GH_TOKEN here; the
// Claude/Anthropic pair is pre-satisfied and Jira/Box tokens aren't needed by
// default, so neither should reach secretCmdRunner).
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

// TestParseFlags_GlobalSecretCmd_LosesToPerSecretCmdFlag: a per-secret
// --<name>-cmd flag pre-empts the global template — highest precedence wins.
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

// TestParseFlags_GlobalSecretCmd_LosesToCmdEnv: a per-secret <NAME>_CMD env
// var pre-empts the global template.
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

// TestParseFlags_GlobalSecretCmd_LosesToFileFlag: a per-secret --<name>-file
// flag pre-empts the global template.
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

// TestParseFlags_GlobalSecretCmd_LosesToDirectEnv: a direct env value
// pre-empts the global template — the lowest of the four existing forms
// still outranks the new, fifth one.
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

// TestParseFlags_GlobalSecretCmdEnv_RunsTemplate: SECRET_CMD (no flag) is the
// env-var form of the same global template.
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

// TestParseFlags_GlobalSecretCmdFlag_WinsOverEnv: --secret-cmd flag takes
// precedence over a SECRET_CMD env var, same as every other flag-over-env form.
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

// TestParseFlags_GlobalSecretCmd_SkipsJiraWhenTrackerNotJira: the template
// fallback is gated per knob — JIRA_TOKEN is only required when
// ISSUE_TRACKER=jira, so it must not be sourced by the template otherwise.
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

// TestParseFlags_GlobalSecretCmd_AppliesToJiraWhenTrackerIsJira: the same
// knob is fetched once the run actually requires it.
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

// TestApplySecretCmdFallback_UsesDocumentSettings: CODE_FORGE/ISSUE_TRACKER
// may be set only via the Consumer flake's settings (ADR 0020), which the
// Launcher input document carries as loadedDoc — not via env or flag. The
// fallback must see that value, not just the ambient default, or an
// otherwise valid run set up entirely through the document either misses a
// secret it needs (this test: JIRA_TOKEN) or forces an unwanted vault lookup
// for one it doesn't (a fully-local run still fetching GH_TOKEN).
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

// TestApplySecretCmdFallback_SkipsGhTokenWhenDocumentIsFullyLocal: the
// inverse of the above — CODE_FORGE=local/ISSUE_TRACKER=local set only via
// the document must not force a GH_TOKEN vault lookup for an offline run.
func TestApplySecretCmdFallback_SkipsGhTokenWhenDocumentIsFullyLocal(t *testing.T) {
	t.Cleanup(func() { loadedDoc = nil })
	loadedDoc = &inputDocument{Settings: map[string]string{"CODE_FORGE": "local", "ISSUE_TRACKER": "local"}}

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

// TestParseFlags_GlobalSecretCmd_SkipsBoxGhToken: BOX_GH_TOKEN (ADR 0016
// two-actor separation) is opt-in with no requiredness signal of its own, so
// the template must never auto-source it.
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

// TestParseFlags_GlobalSecretCmd_ExplicitClaudeCmdPreemptsAnthropicFallback:
// an explicit --claude-code-oauth-token-cmd must stop the template from
// also fetching ANTHROPIC_API_KEY, regardless of secretKnobs' table order
// (ANTHROPIC_API_KEY sorts before CLAUDE_CODE_OAUTH_TOKEN) — the fallback
// pass must see every per-secret resolution parseFlags already made.
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

// TestParseFlags_GlobalSecretCmd_FailureIsError: a failing templated command
// aborts with a named, value-free error, same as a failing per-secret command.
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

// TestParseFlags_GlobalSecretCmd_MissingValue: --secret-cmd with no following
// arg returns an error.
func TestParseFlags_GlobalSecretCmd_MissingValue(t *testing.T) {
	_, err := parseFlags([]string{"--secret-cmd"})
	if err == nil {
		t.Fatal("expected error when --secret-cmd has no command argument, got nil")
	}
}

// TestParseFlags_NoCmdOrFile_LeavesDirectEnv: with neither a --*-cmd flag, a
// <NAME>_CMD env var, nor a --*-file flag set, the direct env value is left
// untouched.
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

// TestSecretCmdRunner_Default_RunsRealCommand: the production secretCmdRunner
// (unfaked) actually shells out and returns stdout, so the injected seam has
// a real implementation wired up, not just fakes in tests.
func TestSecretCmdRunner_Default_RunsRealCommand(t *testing.T) {
	out, err := secretCmdRunner("printf real-value")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "real-value" {
		t.Errorf("secretCmdRunner(%q) = %q, want %q", "printf real-value", out, "real-value")
	}
}

// TestSecretCmdRunner_Interactive_PassesStdinAndStderr: when the launcher's
// own stdin and stderr are TTYs (issue #1971), the secret command inherits
// them as raw file descriptors, so a vault tool's stderr prompt and stdin
// read both work, while stdout is still captured as the secret.
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

// TestSecretCmdRunner_NonInteractive_NoStdinAttached_NoHang: when not
// interactive, a command that tries to read stdin gets an immediate EOF
// instead of blocking the run, and its stderr never reaches the launcher's
// own stderr (issue #1971 non-interactive regression: behaviour must stay
// exactly as it was before the TTY gate existed).
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

// TestPrintHelpFull_ShowsSecretFileFlags: full help lists --<name>-file flags for secret knobs.
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

// TestPrintHelpFull_ShowsSecretCmdFlags: full help lists --<name>-cmd flags
// for secret knobs, mirroring the --<name>-file section.
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

// TestPrintHelpFull_ShowsGlobalSecretCmd: the singleton --secret-cmd/SECRET_CMD
// template fallback is documented in the full reference, alongside the
// per-secret cmd/file forms it sits below.
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

// TestParseFlags_NoBuildPassthrough: --no-build is returned as a remaining arg,
// not treated as an unknown flag error.
func TestParseFlags_NoBuildPassthrough(t *testing.T) {
	remaining, err := parseFlags([]string{"dispatch", "--no-build"})
	if err != nil {
		t.Fatalf("parseFlags with --no-build: unexpected error: %v", err)
	}
	if len(remaining) != 2 || remaining[0] != "dispatch" || remaining[1] != "--no-build" {
		t.Errorf("remaining = %v, want [dispatch --no-build]", remaining)
	}
}

// TestParseFlags_NoBuildWithIssue: --no-build passes through with an issue number.
func TestParseFlags_NoBuildWithIssue(t *testing.T) {
	remaining, err := parseFlags([]string{"dispatch", "--no-build", "42"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(remaining) != 3 || remaining[1] != "--no-build" || remaining[2] != "42" {
		t.Errorf("remaining = %v, want [dispatch --no-build 42]", remaining)
	}
}

// TestDispatchNoBuildArgs: dispatch --no-build arg extraction.
func TestDispatchNoBuildArgs(t *testing.T) {
	noBuild, rest := dispatchNoBuildArgs([]string{"--no-build", "123"})
	if !noBuild {
		t.Error("want noBuild=true, got false")
	}
	if len(rest) != 1 || rest[0] != "123" {
		t.Errorf("rest = %v, want [123]", rest)
	}
}

// TestDispatchNoBuildArgs_AbsentFlag: no --no-build flag leaves noBuild false.
func TestDispatchNoBuildArgs_AbsentFlag(t *testing.T) {
	noBuild, rest := dispatchNoBuildArgs([]string{"42"})
	if noBuild {
		t.Error("want noBuild=false, got true")
	}
	if len(rest) != 1 || rest[0] != "42" {
		t.Errorf("rest = %v, want [42]", rest)
	}
}

// TestDispatchIssueArgs_Variadic: multiple numeric args all returned in order.
func TestDispatchIssueArgs_Variadic(t *testing.T) {
	got := dispatchIssueArgs([]string{"12", "15", "18"})
	want := []string{"12", "15", "18"}
	if len(got) != len(want) {
		t.Fatalf("dispatchIssueArgs: got %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("pos %d: got %q, want %q", i, got[i], w)
		}
	}
}

// TestDispatchIssueArgs_Empty: empty args return nil.
func TestDispatchIssueArgs_Empty(t *testing.T) {
	got := dispatchIssueArgs([]string{})
	if len(got) != 0 {
		t.Errorf("dispatchIssueArgs([]): got %v, want empty", got)
	}
}

// TestDispatchIssueArgs_SkipsNonNumeric: non-numeric args are ignored.
func TestDispatchIssueArgs_SkipsNonNumeric(t *testing.T) {
	got := dispatchIssueArgs([]string{"--no-build", "42", "foo"})
	if len(got) != 1 || got[0] != "42" {
		t.Errorf("dispatchIssueArgs: got %v, want [42]", got)
	}
}

// TestDispatchContinuousArgs: dispatch --continuous arg extraction.
func TestDispatchContinuousArgs(t *testing.T) {
	continuous, rest := dispatchContinuousArgs([]string{"--continuous", "123"})
	if !continuous {
		t.Error("want continuous=true, got false")
	}
	if len(rest) != 1 || rest[0] != "123" {
		t.Errorf("rest = %v, want [123]", rest)
	}
}

// TestDispatchContinuousArgs_AbsentFlag: no --continuous flag leaves
// continuous false.
func TestDispatchContinuousArgs_AbsentFlag(t *testing.T) {
	continuous, rest := dispatchContinuousArgs([]string{"42"})
	if continuous {
		t.Error("want continuous=false, got true")
	}
	if len(rest) != 1 || rest[0] != "42" {
		t.Errorf("rest = %v, want [42]", rest)
	}
}

// TestDispatchYesArgs_YesFlag: --yes sets yes=true and is removed from remaining.
func TestDispatchYesArgs_YesFlag(t *testing.T) {
	yes, rest := dispatchYesArgs([]string{"--yes", "42"})
	if !yes {
		t.Error("want yes=true, got false")
	}
	if len(rest) != 1 || rest[0] != "42" {
		t.Errorf("rest = %v, want [42]", rest)
	}
}

// TestDispatchYesArgs_ForceAlias: --force is an alias for --yes.
func TestDispatchYesArgs_ForceAlias(t *testing.T) {
	yes, _ := dispatchYesArgs([]string{"--force"})
	if !yes {
		t.Error("--force must set yes=true")
	}
}

// TestDispatchYesArgs_Absent: no --yes/--force flag leaves yes=false.
func TestDispatchYesArgs_Absent(t *testing.T) {
	yes, rest := dispatchYesArgs([]string{"42"})
	if yes {
		t.Error("want yes=false, got true")
	}
	if len(rest) != 1 || rest[0] != "42" {
		t.Errorf("rest = %v, want [42]", rest)
	}
}

// TestParseFlags_YesPassthrough: --yes passes through like --no-build.
func TestParseFlags_YesPassthrough(t *testing.T) {
	remaining, err := parseFlags([]string{"dispatch", "--yes", "42"})
	if err != nil {
		t.Fatalf("parseFlags with --yes: unexpected error: %v", err)
	}
	if len(remaining) != 3 || remaining[1] != "--yes" || remaining[2] != "42" {
		t.Errorf("remaining = %v, want [dispatch --yes 42]", remaining)
	}
}

// TestParseFlags_ForcePassthrough: --force passes through like --no-build.
func TestParseFlags_ForcePassthrough(t *testing.T) {
	remaining, err := parseFlags([]string{"dispatch", "--force"})
	if err != nil {
		t.Fatalf("parseFlags with --force: unexpected error: %v", err)
	}
	if len(remaining) != 2 || remaining[1] != "--force" {
		t.Errorf("remaining = %v, want [dispatch --force]", remaining)
	}
}

// TestPrintHelp_ShowsNoBuildFlag: help output documents --no-build on dispatch.
func TestPrintHelp_ShowsNoBuildFlag(t *testing.T) {
	var buf bytes.Buffer
	printHelp(&buf)
	if !strings.Contains(buf.String(), "--no-build") {
		t.Error("help output missing --no-build flag")
	}
}

// TestPrintHelpFull_ShowsContinuousFlag: the full reference documents
// --continuous as the bare-flag alias for --continuous-dispatch 1
// (issue #2033).
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

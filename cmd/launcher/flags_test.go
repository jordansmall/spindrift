package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestModelDefault_IsSonnet5 asserts that the primary implementor model default
// is claude-sonnet-5, not an older release.
func TestModelDefault_IsSonnet5(t *testing.T) {
	const want = "claude-sonnet-5"
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

// TestPrintSubcommands_ExactOutput pins the rendered subcommand listing
// byte-for-byte so a future subcommandRegistry-vs-format change (e.g. the
// column-width constant in printSubcommands) can't silently misalign the
// output the way a hand-picked width once did (issue #1575 review).
func TestPrintSubcommands_ExactOutput(t *testing.T) {
	want := "Subcommands:\n" +
		"  console                                   browse the open backlog interactively (read-only)\n" +
		"  dispatch [--no-build] [--yes] [issue...]  dispatch agents in waves; an issue list dispatches exactly those (bypasses label/barrier gates)\n" +
		"  research [--no-build] [--yes] [issue...]  advise-only research dispatch: drains agent-research (or an issue list) and posts a verdict comment; never merges, never promotes\n" +
		"  preview [issue...]                        dry-run: show what dispatch would pick up, in order\n" +
		"  build                                     realize the agent image without running any agent\n" +
		"  recover <issue>                           run the merge gate for a single issue\n" +
		"  doctor                                    check forge credentials, repository connectivity, and label presence (triage fatal, research advisory)\n"

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

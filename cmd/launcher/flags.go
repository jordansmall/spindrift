package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/charmbracelet/x/term"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/inputdoc"
	"spindrift.dev/launcher/internal/launcherchecks"
)

// version and revision are injected at build time via -ldflags.
var version = "dev"
var revision = "unknown"

func printVersion(w io.Writer) {
	fmt.Fprintf(w, "spindrift %s (rev %s)\n", version, revision)
}

// doctorFlagArgs parses doctor's own args: --verbose or -v requests the full
// report (issue #3777); --butler additionally validates the butler config,
// folding a failure into doctor's config-invalid exit (issue #3920). Unlike
// parseIssuePositionals, doctor takes no positionals of its own, so any
// other token — flag or bare word — is a usage error; ok is false
// exactly when badArg holds that token.
func doctorFlagArgs(args []string) (opts doctorOptions, badArg string, ok bool) {
	for _, a := range args {
		switch a {
		case "--verbose", "-v":
			opts.verbose = true
		case butlerDoctorFlag:
			opts.butler = true
		case researchDoctorFlag:
			opts.research = true
		default:
			return doctorOptions{}, a, false
		}
	}
	return opts, "", true
}

// issueArgs is parseIssuePositionals's result. A struct rather than four
// return values because three of them are same-typed bools, which Go cannot
// catch swapped at a call site (issue #3060).
type issueArgs struct {
	noBuild       bool
	yes           bool
	selfContained bool
	remaining     []string
}

// parseIssuePositionals strips the dispatch-only booleans shared by every
// issue-taking verb (issue #3054): --no-build, --yes (aliased by --force), and
// --self-contained (issue #2202). Stripping them is the only filtering it
// does, and callers use remaining directly as the issue-ID list (issue
// #3055): recover accepts opaque, non-numeric identifiers such as Jira keys
// and local-forge slugs, so any filter here would silently narrow it.
func parseIssuePositionals(args []string) issueArgs {
	var parsed issueArgs
	for _, a := range args {
		switch a {
		case "--no-build":
			parsed.noBuild = true
		case "--yes", "--force":
			parsed.yes = true
		case "--self-contained":
			parsed.selfContained = true
		default:
			parsed.remaining = append(parsed.remaining, a)
		}
	}
	return parsed
}

// extractInputFlag pulls "--input <path>", the Launcher input document's
// store path, out of args (ADR 0020). It is not a schema knob, so it never reaches
// inputdoc.Resolve's flag tier. An invocation with no document leaves args alone.
func extractInputFlag(args []string) (path string, remaining []string, err error) {
	for i := 0; i < len(args); i++ {
		if args[i] != "--input" {
			remaining = append(remaining, args[i])
			continue
		}
		if i+1 >= len(args) {
			return "", nil, fmt.Errorf("flag --input requires a value")
		}
		path = args[i+1]
		i++
	}
	return path, remaining, nil
}

// toKebab mirrors lib/renderers.nix's toKebab, so --secret-cmd's {name}
// substitution matches each secret's vault item in harness.env.example.
func toKebab(env string) string {
	return strings.ToLower(strings.ReplaceAll(env, "_", "-"))
}

// isStdinTTY is a package var so tests can override it without a real TTY
// (issue #1971).
var isStdinTTY = func() bool {
	return term.IsTerminal(os.Stdin.Fd())
}

// isStderrTTY is a package var so tests can override it without a real TTY
// (issue #1971).
var isStderrTTY = func() bool {
	return term.IsTerminal(os.Stderr.Fd())
}

// isInteractiveTTY is a package var so tests can override either branch
// without a real TTY (issue #1971).
var isInteractiveTTY = func() bool {
	return isStdinTTY() && isStderrTTY()
}

// secretCmdRunner runs a secret-fetch command through the shell and returns
// its raw stdout, captured as the secret and never printed. A package var so
// tests can substitute a fake. Interactively the command inherits the
// launcher's stdin and stderr, so a vault tool's unlock prompt reaches the
// operator; otherwise stdin is /dev/null, so a blocking read fails fast.
var secretCmdRunner = func(cmd string) (string, error) {
	c := exec.Command("sh", "-c", cmd)
	if isInteractiveTTY() {
		c.Stdin = os.Stdin
		c.Stderr = os.Stderr
	}
	out, err := c.Output()
	return string(out), err
}

// secretCmdUnlockHint stays tool-agnostic because the command is opaque to
// the launcher: rbw, op, pass, vault, or anything else that prints a secret
// to stdout.
const secretCmdUnlockHint = "your vault may be locked; unlock it (e.g. `rbw unlock`) and re-run"

// resolveSecretCmd runs a secret-fetch command and returns its trimmed
// stdout. Errors stay value-free: the command's stdout and stderr may carry a
// partial secret, so neither ever reaches an error message or a log.
func resolveSecretCmd(env, cmd string) (string, error) {
	out, err := secretCmdRunner(cmd)
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() >= 0 {
			return "", fmt.Errorf("%s: command failed (exit %d) — %s", env, exitErr.ExitCode(), secretCmdUnlockHint)
		}
		return "", fmt.Errorf("%s: command failed — %s", env, secretCmdUnlockHint)
	}
	trimmed := strings.TrimRight(out, "\r\n")
	if trimmed == "" {
		return "", fmt.Errorf("%s: command produced no output — %s", env, secretCmdUnlockHint)
	}
	return trimmed, nil
}

// validateChoice checks a resolved value against the inputdoc.SchemaFlags row's
// declared choices (issue #2520 slice 2). A missing row or a row with no
// choices is a no-op.
func validateChoice(env, value string) error {
	for _, e := range inputdoc.SchemaFlags {
		if e.Env != env {
			continue
		}
		if len(e.Choices) == 0 {
			return nil
		}
		if slices.Contains(e.Choices, value) {
			return nil
		}
		return fmt.Errorf("%s=%q is not valid; must be %s", env, value, launcherchecks.JoinOxford(e.Choices))
	}
	return nil
}

// subcommandEntry is one row of the subcommand listing. nix/regen.nix
// generates the subcommandRegistry table from lib/subcommands.nix.
type subcommandEntry struct {
	name  string // must match a verbHandlers key exactly
	usage string // bracketed argument synopsis; empty when the verb takes none
	doc   string
}

// verbIndex returns the index of the verb in args — the first token that
// isn't itself a "--"-prefixed flag — or -1 if args has no verb.
// Dispatch-only booleans like --no-build pass through ahead of the verb, so
// the verb is not always args[0].
func verbIndex(args []string) int {
	for i, tok := range args {
		if !strings.HasPrefix(tok, "--") {
			return i
		}
	}
	return -1
}

// splitVerb separates the verb from the rest of args. rest holds the post-verb
// args first, then the leading pass-through flags that preceded the verb:
// handlers such as registry read their own first positional, so the verb's
// own args must lead. The consequence is that a handler's "unrecognized
// argument" error names a bad post-verb token before a bad leading flag.
// verb is "" and rest nil when args has no verb.
func splitVerb(args []string) (verb string, rest []string) {
	i := verbIndex(args)
	if i < 0 {
		return "", nil
	}
	rest = make([]string, 0, len(args)-1)
	rest = append(rest, args[i+1:]...)
	rest = append(rest, args[:i]...)
	return args[i], rest
}

// verbSoFar returns the verb parseFlags has accumulated into remaining so
// far, or "" if none has appeared yet.
func verbSoFar(remaining []string) string {
	if i := verbIndex(remaining); i >= 0 {
		return remaining[i]
	}
	return ""
}

// cliFlag is one row of the non-schema flag table generated from
// lib/cli-flags.nix. verb, when set, restricts the flag to follow that verb;
// arg, when set, means the flag consumes the next token as its value. short is
// the single-letter -x form. intercepted rows are acted on by mainRun before
// parseFlags, so lookupCliFlag skips them and parseFlags rejects them. They
// still sit in cliFlags, so any new parser-side consumer of it must filter on
// !f.intercepted as lookupCliFlag does; help rendering lists them all, like
// the man page. doc is the one-line description shown there.
type cliFlag struct {
	flag, verb, arg, short, doc string
	intercepted                 bool
}

// lookupCliFlag returns the cliFlags row matching arg that is valid after the
// verb accumulated in remaining, or nil. A verb-scoped row also matches its
// --flag=value spelling; the verb's own parser validates the value.
func lookupCliFlag(arg string, remaining []string) *cliFlag {
	for i := range cliFlags {
		f := &cliFlags[i]
		if f.intercepted {
			continue
		}
		if arg == "--"+f.flag && (f.verb == "" || verbSoFar(remaining) == f.verb) {
			return f
		}
		if f.verb != "" && strings.HasPrefix(arg, "--"+f.flag+"=") && verbSoFar(remaining) == f.verb {
			return f
		}
	}
	return nil
}

// parseFlags splits args into the verb arguments and the schema-flag tokens.
// Schema flags are never written to the environment: they are returned in
// order (a value flag's token and its value, a bool's single token) for
// inputdoc.Resolve, which ranks them over the document and schema default
// (ADR 0020). Only tokens classified here reach Resolve, so a verb flag's
// value or anything after "--" is never mistaken for a knob (#6707). The
// secret --*-file/--*-cmd flags are the exception: secrets are read from the
// process environment by design, so those still land there. An unrecognised
// --flag is an error; non-flag args and everything after "--" pass through.
func parseFlags(args []string) (remaining, schemaArgs []string, err error) {
	byFileFlag := make(map[string]*inputdoc.SecretKnob, len(inputdoc.SecretKnobs))
	byCmdFlag := make(map[string]*inputdoc.SecretKnob, len(inputdoc.SecretKnobs))
	for i := range inputdoc.SecretKnobs {
		if inputdoc.SecretKnobs[i].FileFlag != "" {
			byFileFlag["--"+inputdoc.SecretKnobs[i].FileFlag] = &inputdoc.SecretKnobs[i]
		}
		if inputdoc.SecretKnobs[i].CmdFlag != "" {
			byCmdFlag["--"+inputdoc.SecretKnobs[i].CmdFlag] = &inputdoc.SecretKnobs[i]
		}
	}

	// The scan below fills these without acting on them, so parseFlags can
	// reject a conflicting file/cmd pair before either side effect runs,
	// whichever flag came first on the command line.
	filePaths := make(map[string]string, len(inputdoc.SecretKnobs))
	cmdStrings := make(map[string]string, len(inputdoc.SecretKnobs))

	// Reset so repeated calls in one process (tests) see no stale value.
	globalSecretCmdTemplate = ""

	remaining = make([]string, 0, len(args))
	i := 0
	for i < len(args) {
		arg := args[i]
		if arg == "--" {
			remaining = append(remaining, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "--") {
			remaining = append(remaining, arg)
			i++
			continue
		}
		// Non-schema flags (lib/cli-flags.nix). A flag with a verb is that
		// verb's own subcommand flag and must follow it on the command line;
		// anywhere else it falls through to the unknown-flag error below
		// (issues #3777, #3920, #3795).
		if f := lookupCliFlag(arg, remaining); f != nil {
			i++
			if f.flag == "secret-cmd" {
				if i >= len(args) {
					return nil, nil, fmt.Errorf("flag --secret-cmd requires a command")
				}
				globalSecretCmdTemplate = args[i]
				i++
				continue
			}
			remaining = append(remaining, arg)
			// A missing value is left for the verb's own parser to report.
			if f.arg != "" && !strings.Contains(arg, "=") && i < len(args) {
				remaining = append(remaining, args[i])
				i++
			}
			continue
		}
		name, _, _ := strings.Cut(arg, "=")
		if entry, ok := inputdoc.LookupSchemaFlag(name); ok && entry.Kind == "bool" {
			schemaArgs = append(schemaArgs, arg)
			i++
			continue
		}
		if _, ok := inputdoc.LookupSchemaFlag(arg); ok {
			i++
			schemaArgs = append(schemaArgs, arg)
			// A missing value is reported by CheckSchemaFlags below.
			if i < len(args) {
				schemaArgs = append(schemaArgs, args[i])
				i++
			}
			continue
		}
		if knob, ok := byFileFlag[arg]; ok {
			i++
			if i >= len(args) {
				return nil, nil, fmt.Errorf("flag %s requires a path", arg)
			}
			filePaths[knob.Env] = args[i]
			i++
			continue
		}
		if knob, ok := byCmdFlag[arg]; ok {
			i++
			if i >= len(args) {
				return nil, nil, fmt.Errorf("flag %s requires a command", arg)
			}
			cmdStrings[knob.Env] = args[i]
			i++
			continue
		}
		return nil, nil, fmt.Errorf("unknown flag: %s", arg)
	}
	// Before any --*-cmd runs, so a malformed flag never executes a secret command.
	if err := inputdoc.CheckSchemaFlags(schemaArgs); err != nil {
		return nil, nil, err
	}

	for i := range inputdoc.SecretKnobs {
		knob := &inputdoc.SecretKnobs[i]
		_, hasFileFlag := filePaths[knob.Env]
		_, hasCmdFlag := cmdStrings[knob.Env]
		if hasFileFlag && hasCmdFlag {
			return nil, nil, fmt.Errorf("--%s and --%s are mutually exclusive", knob.CmdFlag, knob.FileFlag)
		}

		var value string
		var err error
		switch {
		case hasCmdFlag:
			value, err = resolveSecretCmd(knob.Env, cmdStrings[knob.Env])
		case os.Getenv(knob.Env+"_CMD") != "":
			value, err = resolveSecretCmd(knob.Env, os.Getenv(knob.Env+"_CMD"))
		case hasFileFlag:
			var data []byte
			data, err = os.ReadFile(filePaths[knob.Env])
			if err != nil {
				return nil, nil, fmt.Errorf("--%s: cannot read file %s: %w", knob.FileFlag, filePaths[knob.Env], err)
			}
			value = strings.TrimRight(string(data), "\r\n")
		default:
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		if err := os.Setenv(knob.Env, value); err != nil {
			return nil, nil, err
		}
	}

	// The flag wins over SECRET_CMD, matching every per-secret form.
	if globalSecretCmdTemplate == "" {
		globalSecretCmdTemplate = os.Getenv("SECRET_CMD")
	}

	return remaining, schemaArgs, nil
}

// globalSecretCmdTemplate is the --secret-cmd/SECRET_CMD fallback captured by
// parseFlags and consumed by applySecretCmdFallback. A package var, not a
// parseFlags return value, so the fallback can run later in main() without
// changing parseFlags' signature.
var globalSecretCmdTemplate string

// applySecretCmdFallback resolves globalSecretCmdTemplate, substituting
// {name} with the kebab-case env name, for every secret knob this run needs
// that still has no value. mainRun must call it after inputdoc.Resolve, not
// from parseFlags: secretRequiredThisRun reads the knobs through resolvedKnob
// (launcherInput), which is unresolved while parseFlags runs (ADR 0020).
func applySecretCmdFallback() error {
	if globalSecretCmdTemplate == "" {
		return nil
	}
	rules, models := activeCredentialRules()
	for i := range inputdoc.SecretKnobs {
		knob := &inputdoc.SecretKnobs[i]
		if os.Getenv(knob.Env) != "" || !secretRequiredThisRun(knob.Env, rules) {
			continue
		}
		// A Driver rule's variables are one unit: any one satisfies validate(),
		// so an OAuth-only vault must not abort on a missing ANTHROPIC_API_KEY
		// item (Claude's pair). The non-first variables defer to the rule's first.
		envs := []string{knob.Env}
		rule, isRuleVar := credentialRuleFor(rules, knob.Env)
		if isRuleVar {
			if knob.Env != rule.Env[0] {
				continue
			}
			envs = rule.Env
		}
		var errs []error
		fetched := false
		for _, env := range envs {
			cmd := strings.ReplaceAll(globalSecretCmdTemplate, "{name}", toKebab(env))
			value, err := resolveSecretCmd(env, cmd)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if err := os.Setenv(env, value); err != nil {
				return err
			}
			fetched = true
			break
		}
		// A Driver credential no visible tier needs is best-effort: another
		// Provider needs nothing from the environment.
		if !fetched && (!isRuleVar || rule.AppliesTo(models...)) {
			return errors.Join(errs...)
		}
	}
	return nil
}

// activeCredentialRules is the active Driver's credential rules and every
// visible model tier. DRIVER and the tiers go through resolveDriver/
// resolvedKnob so a document- or flake-only value is seen (ADR 0020). An
// unrecognised Driver has no rules; main emits the warning.
func activeCredentialRules() ([]driver.CredentialRule, []string) {
	name, _ := resolveDriver()
	rules, _ := driver.CredentialRules(name)
	return rules, modelTiers(resolvedKnob)
}

// modelTiers is every model tier a run can use, read through lookup by env
// name. The credential probe and the --secret-cmd fallback both build their
// tier list here.
func modelTiers(lookup func(env string) string) []string {
	return []string{
		lookup("MODEL"), lookup("REVIEW_MODEL"), lookup("WORKER_MODEL"),
		lookup("SCOUT_MODEL"), lookup("FILER_MODEL"),
	}
}

// credentialRuleFor returns the rule that lists env among its variables.
func credentialRuleFor(rules []driver.CredentialRule, env string) (driver.CredentialRule, bool) {
	for _, r := range rules {
		if slices.Contains(r.Env, env) {
			return r, true
		}
	}
	return driver.CredentialRule{}, false
}

// secretRequiredThisRun gates whether applySecretCmdFallback tries the global
// template for env, so it never forces a vault lookup for a secret this run
// does not use. rules are the active Driver's credential rules: a rule's
// variables are tried whenever none has a value, even when no visible tier
// applies the rule (a tier baked into the image can select github-copilot
// invisibly to the launcher); applySecretCmdFallback tolerates that miss.
// Reads CODE_FORGE and ISSUE_TRACKER through resolvedKnob, not os.Getenv, so a
// value set only in the Consumer flake is seen (ADR 0020).
func secretRequiredThisRun(env string, rules []driver.CredentialRule) bool {
	// Any one of a rule's variables satisfies validate(), so try the template
	// only when none already has a value. The driver warning is main's to emit.
	if rule, ok := credentialRuleFor(rules, env); ok {
		return !rule.Satisfied(os.Getenv)
	}
	codeForge, issueTracker := resolvedKnob("CODE_FORGE"), resolvedKnob("ISSUE_TRACKER")
	forgeRow, _ := backendkit.ByName(codeForge)
	trackerRow, _ := backendkit.ByName(issueTracker)
	switch env {
	case "GH_TOKEN":
		sig := resolveCapabilitySignals(codeForge, issueTracker)
		return !sig.fullyLocal && backend.NeedsGHToken(forgeRow.Descriptor, trackerRow.Descriptor)
	default:
		// Any other backend's own token is required exactly when a selected
		// backend names it as its TokenEnvVar. BOX_GH_TOKEN (ADR 0016) is opt-in and
		// no row names it as a TokenEnvVar (github has it only as its boxTokenEnvVar),
		// so the template never sources it.
		return env != "" && (forgeRow.TokenEnvVar == env || trackerRow.TokenEnvVar == env)
	}
}

// printSubcommands renders the listing both help modes share from
// subcommandRegistry (lib/subcommands.nix), so it cannot drift from the
// completions and man page rendered off the same registry.
func printSubcommands(w io.Writer) {
	fmt.Fprintln(w, "Subcommands:")
	lefts := make([]string, len(subcommandRegistry))
	width := 0
	for i, e := range subcommandRegistry {
		lefts[i] = e.name
		if e.usage != "" {
			lefts[i] += " " + e.usage
		}
		width = max(width, len(lefts[i]))
	}
	for i, e := range subcommandRegistry {
		// Two spaces separate the widest name+usage from its doc;
		// TestPrintSubcommands_ExactOutput pins the result.
		fmt.Fprintf(w, "  %-*s%s\n", width+2, lefts[i], e.doc)
	}
}

// subcommandSynopsis renders a verb's invocation synopsis from
// subcommandRegistry (lib/subcommands.nix), the single source of every
// per-verb synopsis. It panics on a name the registry lacks.
func subcommandSynopsis(name string) string {
	for _, e := range subcommandRegistry {
		if e.name != name {
			continue
		}
		line := "spindrift " + name
		if e.usage != "" {
			line += " " + e.usage
		}
		return line
	}
	panic("subcommandSynopsis: unregistered verb " + name)
}

// subcommandUsage renders a verb's error-path usage line. It panics on an
// unregistered verb, as subcommandSynopsis does.
func subcommandUsage(name string) string {
	return "usage: " + subcommandSynopsis(name)
}

// printHelp writes the concise usage summary. The exhaustive knob list lives
// in printHelpFull so the default --help stays scannable.
func printHelp(w io.Writer) {
	fmt.Fprintln(w, "spindrift — launch waves of headless coding agents, one container per issue")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage: spindrift [flags] <subcommand> [args]")
	fmt.Fprintln(w)
	printSubcommands(w)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Common flags:")
	fmt.Fprintln(w, "  --repo-slug owner/repo   target GitHub repository (required unless CODE_FORGE and ISSUE_TRACKER are both local)")
	fmt.Fprintln(w, "  --model NAME             primary implementor model")
	fmt.Fprintln(w, "  --max-parallel N         maximum concurrent agent containers")
	fmt.Fprintln(w, "  --merge-policy MODE      post-green merge policy: immediate | auto | manual")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Full reference (all flags, env vars, and secrets):")
	fmt.Fprintln(w, "  man spindrift            complete manual page")
	fmt.Fprintln(w, "  spindrift --help --all   the same reference in the terminal")
	fmt.Fprintln(w, "  spindrift --version      print version and revision")
}

// printCliFlags writes the Command flags block from cliFlags. Intercepted rows
// are listed too, matching the man page's COMMAND FLAGS section.
func printCliFlags(w io.Writer) {
	cols := make([]string, len(cliFlags))
	width := 0
	for i, f := range cliFlags {
		col := "--" + f.flag
		if f.short != "" {
			col += ", -" + f.short
		}
		if f.arg != "" {
			col += " <" + f.arg + ">"
		}
		cols[i] = col
		width = max(width, len(col))
	}
	fmt.Fprintln(w, "Command flags:")
	for i, f := range cliFlags {
		line := fmt.Sprintf("  %-*s  %s.", width, cols[i], f.doc)
		if f.verb != "" {
			line += " Valid only after the " + f.verb + " subcommand."
		}
		fmt.Fprintln(w, line)
	}
}

// printHelpFull writes the exhaustive reference reached by `spindrift --help --all`.
func printHelpFull(w io.Writer) {
	fmt.Fprintln(w, "spindrift — launch waves of headless coding agents, one container per issue")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage: spindrift [flags] <subcommand> [args]")
	fmt.Fprintln(w)
	printSubcommands(w)
	fmt.Fprintln(w)
	printCliFlags(w)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags (flag > flake setting > default precedence; a knob env var is")
	fmt.Fprintln(w, "rejected with exit 6 — ADR 0020):")

	byGroup := make(map[string][]inputdoc.FlagEntry, len(inputdoc.GroupOrder))
	for _, e := range inputdoc.SchemaFlags {
		byGroup[e.Group] = append(byGroup[e.Group], e)
	}
	for _, g := range inputdoc.GroupOrder {
		entries := byGroup[g]
		if len(entries) == 0 {
			continue
		}
		fmt.Fprintf(w, "\n  %s:\n", g)
		for _, e := range entries {
			dflt := e.Dflt
			if dflt == "" {
				dflt = "(none)"
			}
			flagCol := e.Flag
			if e.Alias != "" {
				flagCol = flagCol + ", --" + e.Alias
			}
			if e.DeprecatedAlias != "" {
				flagCol = flagCol + ", --" + e.DeprecatedAlias + " (deprecated)"
			}
			fmt.Fprintf(w, "    --%-30s  %-6s  default=%-20s  %s\n",
				flagCol, e.Kind, dflt, e.Doc)
		}
	}
	if len(inputdoc.SecretKnobs) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Env-only (secret; never a value flag):")
		for _, s := range inputdoc.SecretKnobs {
			fmt.Fprintf(w, "  %-32s  env-only  %s\n", s.Env, s.Doc)
		}
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Secret file flags (read value from file; takes precedence over env):")
		for _, s := range inputdoc.SecretKnobs {
			if s.FileFlag != "" {
				fmt.Fprintf(w, "  --%-30s  path  %s\n", s.FileFlag, s.Doc)
			}
		}
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Secret command flags (fetch value from a command's stdout; sibling <NAME>_CMD")
		fmt.Fprintln(w, "env var; highest precedence — preferred for external-vault sourcing):")
		for _, s := range inputdoc.SecretKnobs {
			if s.CmdFlag != "" {
				fmt.Fprintf(w, "  --%-30s  cmd   %s\n", s.CmdFlag, s.Doc)
			}
		}
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Global secret command template (lowest precedence; sibling SECRET_CMD env")
		fmt.Fprintln(w, "var; a fallback below every per-secret form above, for a secret with none of")
		fmt.Fprintln(w, "its own set): --secret-cmd CMD, where {name} substitutes the secret's")
		fmt.Fprintln(w, "kebab-case env name (e.g. GH_TOKEN -> gh-token), e.g. --secret-cmd 'rbw get")
		fmt.Fprintln(w, "spindrift-{name}'; only tried for a secret this run actually needs.")
	}
}

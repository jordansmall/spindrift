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
	"spindrift.dev/launcher/internal/launcherchecks"
)

// version and revision are injected at build time via -ldflags.
var version = "dev"
var revision = "unknown"

func printVersion(w io.Writer) {
	fmt.Fprintf(w, "spindrift %s (rev %s)\n", version, revision)
}

// dispatchNoBuildArgs reports whether --no-build is present and returns the
// args with it removed.
func dispatchNoBuildArgs(args []string) (noBuild bool, remaining []string) {
	for _, a := range args {
		if a == "--no-build" {
			noBuild = true
		} else {
			remaining = append(remaining, a)
		}
	}
	return
}

// dispatchYesArgs reports whether --yes or --force is present and returns the
// args with it removed.
func dispatchYesArgs(args []string) (yes bool, remaining []string) {
	for _, a := range args {
		if a == "--yes" || a == "--force" {
			yes = true
		} else {
			remaining = append(remaining, a)
		}
	}
	return
}

// dispatchSelfContainedArgs reports whether --self-contained is present and
// returns the args with it removed (issue #2202).
func dispatchSelfContainedArgs(args []string) (selfContained bool, remaining []string) {
	for _, a := range args {
		if a == "--self-contained" {
			selfContained = true
		} else {
			remaining = append(remaining, a)
		}
	}
	return
}

// doctorFlagArgs parses doctor's own args: --verbose or -v requests the full
// report (issue #3777); --butler additionally validates the butler config,
// folding a failure into doctor's config-invalid exit (issue #3920). Unlike
// the dispatch-only parsers above, doctor takes no positionals of its own, so
// any other token — flag or bare word — is a usage error; ok is false
// exactly when badArg holds that token.
func doctorFlagArgs(args []string) (opts doctorOptions, badArg string, ok bool) {
	for _, a := range args {
		switch a {
		case "--verbose", "-v":
			opts.verbose = true
		case "--butler":
			opts.butler = true
		case "--research":
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
// issue-taking verb (issue #3054). Stripping them is the only filtering it
// does, and callers use remaining directly as the issue-ID list (issue
// #3055): recover accepts opaque, non-numeric identifiers such as Jira keys
// and local-forge slugs, so any filter here would silently narrow it.
func parseIssuePositionals(args []string) issueArgs {
	noBuild, remaining := dispatchNoBuildArgs(args)
	yes, remaining := dispatchYesArgs(remaining)
	selfContained, remaining := dispatchSelfContainedArgs(remaining)
	return issueArgs{
		noBuild:       noBuild,
		yes:           yes,
		selfContained: selfContained,
		remaining:     remaining,
	}
}

// extractInputFlag pulls "--input <path>", the Launcher input document's
// store path, out of args (ADR 0020). It is not a schema knob, so it bypasses
// schemaFlags and os.Setenv. An invocation with no document leaves args alone.
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

// flagEntry maps a runtime knob to its CLI flag. nix/regen.nix generates the
// schemaFlags table from lib/env-schema.nix, via lib/renderers.nix's
// renderFlagTableGo.
type flagEntry struct {
	env             string // SCREAMING_SNAKE_CASE
	flag            string // kebab-case, no leading dashes
	alias           string // kebab-case, no leading dashes; empty when absent
	kind            string // "string", "int", or "bool"
	doc             string
	dflt            string // empty when there is none
	group           string
	settingsPath    string // derived flake path, e.g. git.merge.policy; empty for a non-flakeOption knob
	deprecatedAlias string // empty unless the canonical flag was renamed (ADR 0037 Pass 2)
	choices         []string
	// launcherIgnores marks a knob the launcher generates a flag for (1:1
	// flake-option/flag parity, #3567) but never itself reads; only
	// cmd/launcher/daemon consumes it, via the input document (#3698).
	launcherIgnores bool
	// emptyDisables marks a string knob whose doc gives "" its own meaning
	// (e.g. disables the limit); see emptyIsSetting.
	emptyDisables bool
}

// emptyIsSetting is the one rule for whether an empty env value is a real
// setting rather than "unset" (which falls back to the default): true iff the
// knob is bool kind (parseFlags encodes a bool's explicit off as set-but-empty)
// or emptyDisables. Read env through ambientSetting; never re-derive this.
// inputdoc.Document.Setting applies the same rule to a document value.
func (e flagEntry) emptyIsSetting() bool {
	return e.kind == "bool" || e.emptyDisables
}

// secretKnob is a knob the schema marks secret = true, so it gets no inline
// value flag: callers supply it through the environment, --<fileFlag>, or
// --<cmdFlag>. nix/regen.nix generates secretKnobs from lib/env-schema.nix,
// via lib/renderers.nix's renderFlagTableGo.
type secretKnob struct {
	env      string
	doc      string
	fileFlag string // kebab-case, no leading dashes
	cmdFlag  string // kebab-case, no leading dashes
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

// validateChoice checks a resolved value against the schemaFlags row's
// declared choices (issue #2520 slice 2). A missing row or a row with no
// choices is a no-op.
func validateChoice(env, value string) error {
	for _, e := range schemaFlags {
		if e.env != env {
			continue
		}
		if len(e.choices) == 0 {
			return nil
		}
		if slices.Contains(e.choices, value) {
			return nil
		}
		return fmt.Errorf("%s=%q is not valid; must be %s", env, value, launcherchecks.JoinOxford(e.choices))
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
// verb accumulated in remaining, or nil.
func lookupCliFlag(arg string, remaining []string) *cliFlag {
	for i := range cliFlags {
		f := &cliFlags[i]
		if !f.intercepted && arg == "--"+f.flag && (f.verb == "" || verbSoFar(remaining) == f.verb) {
			return f
		}
	}
	return nil
}

// parseFlags injects matching --flag value pairs into the process environment
// via os.Setenv so loadConfig picks them up. A flag lands in the same env
// channel as a deprecated ambient knob env var, which is how it wins the
// flag > document > schema default precedence (ADR 0020). An unrecognised
// --flag is an error; non-flag args and everything after "--" pass through.
func parseFlags(args []string) ([]string, error) {
	byFlag := make(map[string]*flagEntry, len(schemaFlags)*2)
	byBool := make(map[string]*flagEntry, 2)
	for i := range schemaFlags {
		if schemaFlags[i].kind == "bool" {
			byBool["--"+schemaFlags[i].flag] = &schemaFlags[i]
			if schemaFlags[i].alias != "" {
				byBool["--"+schemaFlags[i].alias] = &schemaFlags[i]
			}
			if schemaFlags[i].deprecatedAlias != "" {
				byBool["--"+schemaFlags[i].deprecatedAlias] = &schemaFlags[i]
			}
			continue
		}
		byFlag["--"+schemaFlags[i].flag] = &schemaFlags[i]
		if schemaFlags[i].alias != "" {
			byFlag["--"+schemaFlags[i].alias] = &schemaFlags[i]
		}
		if schemaFlags[i].deprecatedAlias != "" {
			byFlag["--"+schemaFlags[i].deprecatedAlias] = &schemaFlags[i]
		}
	}

	byFileFlag := make(map[string]*secretKnob, len(secretKnobs))
	byCmdFlag := make(map[string]*secretKnob, len(secretKnobs))
	for i := range secretKnobs {
		if secretKnobs[i].fileFlag != "" {
			byFileFlag["--"+secretKnobs[i].fileFlag] = &secretKnobs[i]
		}
		if secretKnobs[i].cmdFlag != "" {
			byCmdFlag["--"+secretKnobs[i].cmdFlag] = &secretKnobs[i]
		}
	}

	// The scan below fills these without acting on them, so parseFlags can
	// reject a conflicting file/cmd pair before either side effect runs,
	// whichever flag came first on the command line.
	filePaths := make(map[string]string, len(secretKnobs))
	cmdStrings := make(map[string]string, len(secretKnobs))

	// Reset so repeated calls in one process (tests) see no stale value.
	globalSecretCmdTemplate = ""

	remaining := make([]string, 0, len(args))
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
					return nil, fmt.Errorf("flag --secret-cmd requires a command")
				}
				globalSecretCmdTemplate = args[i]
				i++
				continue
			}
			remaining = append(remaining, arg)
			// A missing value is left for the verb's own parser to report.
			if f.arg != "" && i < len(args) {
				remaining = append(remaining, args[i])
				i++
			}
			continue
		}
		name, value, hasEquals := strings.Cut(arg, "=")
		if entry, ok := byBool[name]; ok {
			var on bool
			if hasEquals {
				on = value != "" && value != "0" && value != "false"
			} else {
				on = true
			}
			setTo := ""
			if on {
				setTo = "1"
			}
			if err := os.Setenv(entry.env, setTo); err != nil {
				return nil, err
			}
			i++
			continue
		}
		if entry, ok := byFlag[arg]; ok {
			i++
			if i >= len(args) {
				return nil, fmt.Errorf("flag %s requires a value", arg)
			}
			if err := os.Setenv(entry.env, args[i]); err != nil {
				return nil, err
			}
			i++
			continue
		}
		if knob, ok := byFileFlag[arg]; ok {
			i++
			if i >= len(args) {
				return nil, fmt.Errorf("flag %s requires a path", arg)
			}
			filePaths[knob.env] = args[i]
			i++
			continue
		}
		if knob, ok := byCmdFlag[arg]; ok {
			i++
			if i >= len(args) {
				return nil, fmt.Errorf("flag %s requires a command", arg)
			}
			cmdStrings[knob.env] = args[i]
			i++
			continue
		}
		return nil, fmt.Errorf("unknown flag: %s", arg)
	}

	for i := range secretKnobs {
		knob := &secretKnobs[i]
		_, hasFileFlag := filePaths[knob.env]
		_, hasCmdFlag := cmdStrings[knob.env]
		if hasFileFlag && hasCmdFlag {
			return nil, fmt.Errorf("--%s and --%s are mutually exclusive", knob.cmdFlag, knob.fileFlag)
		}

		var value string
		var err error
		switch {
		case hasCmdFlag:
			value, err = resolveSecretCmd(knob.env, cmdStrings[knob.env])
		case os.Getenv(knob.env+"_CMD") != "":
			value, err = resolveSecretCmd(knob.env, os.Getenv(knob.env+"_CMD"))
		case hasFileFlag:
			var data []byte
			data, err = os.ReadFile(filePaths[knob.env])
			if err != nil {
				return nil, fmt.Errorf("--%s: cannot read file %s: %w", knob.fileFlag, filePaths[knob.env], err)
			}
			value = strings.TrimRight(string(data), "\r\n")
		default:
			continue
		}
		if err != nil {
			return nil, err
		}
		if err := os.Setenv(knob.env, value); err != nil {
			return nil, err
		}
	}

	// The flag wins over SECRET_CMD, matching every per-secret form.
	if globalSecretCmdTemplate == "" {
		globalSecretCmdTemplate = os.Getenv("SECRET_CMD")
	}

	return remaining, nil
}

// globalSecretCmdTemplate is the --secret-cmd/SECRET_CMD fallback captured by
// parseFlags and consumed by applySecretCmdFallback. A package var, not a
// parseFlags return value, so the fallback can run later in main() without
// changing parseFlags' signature.
var globalSecretCmdTemplate string

// applySecretCmdFallback resolves globalSecretCmdTemplate, substituting
// {name} with the kebab-case env name, for every secret knob this run needs
// that still has no value. main() must call it after inputdoc.Load, not
// from parseFlags: secretRequiredThisRun reads loadedDoc through
// getenvSchema, and loadedDoc is still nil while parseFlags runs (ADR 0020).
func applySecretCmdFallback() error {
	if globalSecretCmdTemplate == "" {
		return nil
	}
	for i := range secretKnobs {
		knob := &secretKnobs[i]
		if os.Getenv(knob.env) != "" || !secretRequiredThisRun(knob.env) {
			continue
		}
		cmd := strings.ReplaceAll(globalSecretCmdTemplate, "{name}", toKebab(knob.env))
		value, err := resolveSecretCmd(knob.env, cmd)
		if err != nil {
			return err
		}
		if err := os.Setenv(knob.env, value); err != nil {
			return err
		}
	}
	return nil
}

// secretRequiredThisRun gates applySecretCmdFallback so the global template
// never forces a vault lookup for a secret this run does not use. Cases are
// hardcoded per env name because no uniform "required" rule covers these five
// secrets. Reads CODE_FORGE and ISSUE_TRACKER through getenvSchema, not
// os.Getenv, so a value set only in the Consumer flake is seen (ADR 0020).
func secretRequiredThisRun(env string) bool {
	codeForge, issueTracker := getenvSchema("CODE_FORGE"), getenvSchema("ISSUE_TRACKER")
	forgeRow, _ := backendByName(codeForge)
	trackerRow, _ := backendByName(issueTracker)
	switch env {
	case "GH_TOKEN":
		sig := resolveCapabilitySignals(codeForge, issueTracker)
		return !sig.fullyLocal && backend.NeedsGHToken(forgeRow.Descriptor, trackerRow.Descriptor)
	case "JIRA_TOKEN", "FORGEJO_TOKEN":
		return forgeRow.TokenEnvVar == env || trackerRow.TokenEnvVar == env
	case "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY":
		// Either one satisfies validate(), so try the template only when
		// neither already has a value.
		return os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") == "" && os.Getenv("ANTHROPIC_API_KEY") == ""
	default:
		// BOX_GH_TOKEN (ADR 0016) is opt-in with no requiredness signal of
		// its own, so the template never sources it.
		return false
	}
}

// printSubcommands renders the listing both help modes share from
// subcommandRegistry (lib/subcommands.nix), so it cannot drift from the
// completions and man page rendered off the same registry.
func printSubcommands(w io.Writer) {
	fmt.Fprintln(w, "Subcommands:")
	for _, e := range subcommandRegistry {
		left := e.name
		if e.usage != "" {
			left += " " + e.usage
		}
		// 76 = the 74-char widest name+usage in subcommandRegistry plus a
		// 2-space gap. TestPrintSubcommands_ExactOutput pins the result;
		// widen this if a later entry runs longer.
		fmt.Fprintf(w, "  %-76s%s\n", left, e.doc)
	}
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
	fmt.Fprintln(w, "Flags (flag > flake setting > default precedence; a knob env var still")
	fmt.Fprintln(w, "wins this release but is deprecated and warns — ADR 0020):")

	byGroup := make(map[string][]flagEntry, len(groupOrder))
	for _, e := range schemaFlags {
		byGroup[e.group] = append(byGroup[e.group], e)
	}
	for _, g := range groupOrder {
		entries := byGroup[g]
		if len(entries) == 0 {
			continue
		}
		fmt.Fprintf(w, "\n  %s:\n", g)
		for _, e := range entries {
			dflt := e.dflt
			if dflt == "" {
				dflt = "(none)"
			}
			flagCol := e.flag
			if e.alias != "" {
				flagCol = flagCol + ", --" + e.alias
			}
			if e.deprecatedAlias != "" {
				flagCol = flagCol + ", --" + e.deprecatedAlias + " (deprecated)"
			}
			fmt.Fprintf(w, "    --%-30s  %-6s  default=%-20s  %s\n",
				flagCol, e.kind, dflt, e.doc)
		}
	}
	if len(secretKnobs) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Env-only (secret; never a value flag):")
		for _, s := range secretKnobs {
			fmt.Fprintf(w, "  %-32s  env-only  %s\n", s.env, s.doc)
		}
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Secret file flags (read value from file; takes precedence over env):")
		for _, s := range secretKnobs {
			if s.fileFlag != "" {
				fmt.Fprintf(w, "  --%-30s  path  %s\n", s.fileFlag, s.doc)
			}
		}
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Secret command flags (fetch value from a command's stdout; sibling <NAME>_CMD")
		fmt.Fprintln(w, "env var; highest precedence — preferred for external-vault sourcing):")
		for _, s := range secretKnobs {
			if s.cmdFlag != "" {
				fmt.Fprintf(w, "  --%-30s  cmd   %s\n", s.cmdFlag, s.doc)
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

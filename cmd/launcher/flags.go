package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/charmbracelet/x/term"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/inputdoc"
	"spindrift.dev/launcher/internal/launcherchecks"
	"spindrift.dev/launcher/internal/rowcredential"
	"spindrift.dev/launcher/internal/secretcmd"
)

// version and revision are injected at build time via -ldflags.
var version = "dev"
var revision = "unknown"

func printVersion(w io.Writer) {
	fmt.Fprintf(w, "spindrift %s (rev %s)\n", version, revision)
}

// doctorFlagArgs parses doctor's own args: --verbose or -v requests the full
// report (issue #3777); --butler also validates the butler config, folding a
// failure into doctor's config-invalid exit (issue #3920). doctor takes no
// positionals, so any other token is a usage error; ok is false exactly when
// badArg holds it.
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
// --self-contained (issue #2202). It filters nothing else: callers use
// remaining as the issue-ID list (issue #3055), and recover accepts opaque IDs
// such as Jira keys and local-forge slugs that any filter would drop.
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
// tests can substitute a fake; it prompts only on an interactive TTY.
var secretCmdRunner = func(cmd string) (string, error) {
	return secretcmd.Run(cmd, isInteractiveTTY())
}

// resolveSecretCmd resolves a `_CMD` credential through secretCmdRunner; see
// secretcmd.Resolve for the value-free error contract.
func resolveSecretCmd(env, cmd string) (string, error) {
	return secretcmd.Resolve(env, cmd, secretCmdRunner)
}

// validateChoice checks a resolved value against the inputdoc.SchemaFlags row's
// declared choices (issue #2520 slice 2); a value in RemovedChoices fails with
// its migration message. A missing row or a row with no
// choices is a no-op.
func validateChoice(env, value string) error {
	for _, e := range inputdoc.SchemaFlags {
		if e.Env != env {
			continue
		}
		if msg, removed := inputdoc.RemovedChoice(env, value); removed {
			return errors.New(msg)
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
// args first, then the leading pass-through flags: handlers such as registry
// read their own first positional, so the verb's args must lead (a handler's
// "unrecognized argument" error then names a bad post-verb token first). verb
// is "" and rest nil when args has no verb.
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

// verbSoFar returns the verb scanFlags has accumulated into remaining so
// far, or "" if none has appeared yet.
func verbSoFar(remaining []string) string {
	if i := verbIndex(remaining); i >= 0 {
		return remaining[i]
	}
	return ""
}

// cliFlag is one row of the non-schema flag table generated from
// lib/cli-flags.nix. verb restricts the flag to follow that verb; arg means it
// consumes the next token as its value; short is the -x form; doc is its help
// line. intercepted rows are acted on by mainRun before parseFlags (which
// rejects them), so any parser-side consumer of cliFlags must skip them as
// lookupCliFlag does; help lists them all, like the man page.
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

// flagScan is what scanFlags classified without acting on any of it.
// filePaths and cmdStrings map a secret knob's env name to its --*-file path
// or --*-cmd command.
type flagScan struct {
	remaining, schemaArgs []string
	filePaths, cmdStrings map[string]string
	secretCmdTemplate     string
}

// scanFlags splits args into verb arguments, schema-flag tokens and the
// secret --*-file/--*-cmd values, without acting on any of them. It touches
// nothing outside its return value (no environment, files, commands or package
// state), so preDispatchVerb can ask which verb an invocation names.
func scanFlags(args []string) (flagScan, error) {
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
	sc := flagScan{
		remaining:  make([]string, 0, len(args)),
		filePaths:  make(map[string]string, len(inputdoc.SecretKnobs)),
		cmdStrings: make(map[string]string, len(inputdoc.SecretKnobs)),
	}

	i := 0
	for i < len(args) {
		arg := args[i]
		if arg == "--" {
			sc.remaining = append(sc.remaining, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "--") {
			sc.remaining = append(sc.remaining, arg)
			i++
			continue
		}
		// Non-schema flags (lib/cli-flags.nix). A flag with a verb is that
		// verb's own subcommand flag and must follow it on the command line;
		// anywhere else it falls through to the unknown-flag error below
		// (issues #3777, #3920, #3795).
		if f := lookupCliFlag(arg, sc.remaining); f != nil {
			i++
			if f.flag == "secret-cmd" {
				if i >= len(args) {
					return flagScan{}, fmt.Errorf("flag --secret-cmd requires a command")
				}
				sc.secretCmdTemplate = args[i]
				i++
				continue
			}
			sc.remaining = append(sc.remaining, arg)
			// A missing value is left for the verb's own parser to report.
			if f.arg != "" && !strings.Contains(arg, "=") && i < len(args) {
				sc.remaining = append(sc.remaining, args[i])
				i++
			}
			continue
		}
		name, _, _ := strings.Cut(arg, "=")
		if entry, ok := inputdoc.LookupSchemaFlag(name); ok && entry.Kind == "bool" {
			sc.schemaArgs = append(sc.schemaArgs, arg)
			i++
			continue
		}
		if _, ok := inputdoc.LookupSchemaFlag(arg); ok {
			i++
			sc.schemaArgs = append(sc.schemaArgs, arg)
			// A missing value is reported by CheckSchemaFlags in parseFlags.
			if i < len(args) {
				sc.schemaArgs = append(sc.schemaArgs, args[i])
				i++
			}
			continue
		}
		if knob, ok := byFileFlag[arg]; ok {
			i++
			if i >= len(args) {
				return flagScan{}, fmt.Errorf("flag %s requires a path", arg)
			}
			sc.filePaths[knob.Env] = args[i]
			i++
			continue
		}
		if knob, ok := byCmdFlag[arg]; ok {
			i++
			if i >= len(args) {
				return flagScan{}, fmt.Errorf("flag %s requires a command", arg)
			}
			sc.cmdStrings[knob.Env] = args[i]
			i++
			continue
		}
		return flagScan{}, fmt.Errorf("unknown flag: %s", arg)
	}
	return sc, nil
}

// parseFlags splits args into the verb arguments and the schema-flag tokens.
// Schema flags never reach the environment: they are returned in order for
// inputdoc.Resolve, which ranks them over the document and schema default
// (ADR 0020). Only tokens classified here reach Resolve, so a verb flag's
// value or anything after "--" is never mistaken for a knob (#6707). Secret
// --*-file/--*-cmd flags are the exception and land in the environment, where
// secrets are read by design. An unrecognised --flag is an error; non-flag
// args and everything after "--" pass through in remaining.
func parseFlags(args []string) (remaining, schemaArgs []string, err error) {
	sc, err := scanFlags(args)
	// Always overwrites the global (empty with no --secret-cmd or on a failed
	// scan), so repeated calls in one process see no stale value.
	globalSecretCmdTemplate = sc.secretCmdTemplate
	if err != nil {
		return nil, nil, err
	}
	remaining, schemaArgs = sc.remaining, sc.schemaArgs
	filePaths, cmdStrings := sc.filePaths, sc.cmdStrings

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

func isCoreSecretKnob(env string) bool {
	return slices.ContainsFunc(inputdoc.SecretKnobs, func(k inputdoc.SecretKnob) bool { return k.Env == env })
}

// applyRowCredentials chooses the credential source of each backend row, via
// the resolver the Daemon shares, for the Connector runtime to read at each
// use. A schema knob such as GH_TOKEN_REFRESH_FILE is read from the flag or
// document only; the environment applies to the non-schema keys alone, as
// backendRowSetting reads them.
// Applied is isCoreSecretKnob because parseFlags already ran each core secret's
// `_CMD`, so running it here would resolve the credential twice. An unset
// credential is tolerated: doctor must run without one, and validate and the
// launch gates refuse it per verb.
func applyRowCredentials() (rowcredential.Sourced, error) {
	res := rowcredential.Resolution{
		Forge: resolvedKnob("CODE_FORGE"), Tracker: resolvedKnob("ISSUE_TRACKER"),
		Template: globalSecretCmdTemplate, Run: secretCmdRunner, Applied: isCoreSecretKnob,
		Lookup: backendRowSetting(launcherInput, processEnv()),
	}
	sourced, err := res.Sources(backendrows.All())
	if errors.Is(err, rowcredential.ErrUnset) {
		return sourced, nil
	}
	return sourced, err
}

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
			cmd := secretcmd.Template(globalSecretCmdTemplate, env)
			value, err := secretcmd.Resolve(env, cmd, secretCmdRunner)
			if err == nil {
				err = os.Setenv(env, value)
			}
			if err != nil {
				errs = append(errs, err)
				continue
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
// template for env, so it never forces a vault lookup for an unused secret.
// rules are the active Driver's credential rules; a rule's variables are tried
// whenever none has a value, even if no visible tier applies the rule (an
// image-baked tier can select github-copilot invisibly);
// applySecretCmdFallback tolerates that miss.
// CODE_FORGE and ISSUE_TRACKER go through resolvedKnob so a flake-only value
// is seen (ADR 0020).
func secretRequiredThisRun(env string, rules []driver.CredentialRule) bool {
	// Any one of a rule's variables satisfies validate(), so try the template
	// only when none already has a value. The driver warning is main's to emit.
	if rule, ok := credentialRuleFor(rules, env); ok {
		return !rule.Satisfied(os.Getenv)
	}
	codeForge, issueTracker := resolvedKnob("CODE_FORGE"), resolvedKnob("ISSUE_TRACKER")
	forgeRow, _ := backendkit.ByName(codeForge)
	trackerRow, _ := backendkit.ByName(issueTracker)
	// A backend's token is required exactly when a selected backend names it as
	// its TokenEnvVar; local and git name none, so a fully-local run needs none.
	return env != "" && (forgeRow.TokenEnvVar == env || trackerRow.TokenEnvVar == env)
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
	fmt.Fprintln(w, "rejected with exit 6, doctor exits 2 — ADR 0020):")

	byGroup := make(map[string][]inputdoc.FlagEntry, len(inputdoc.GroupOrder))
	for _, e := range inputdoc.SchemaFlags {
		if e.Flag == "" {
			continue
		}
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

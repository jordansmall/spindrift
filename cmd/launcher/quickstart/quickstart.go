// Command quickstart is the pre-CLI Quickstart wizard (ADR 0027). It runs
// before the `spindrift` binary exists and authors flake.nix options rather
// than launcher env knobs, so it is its own binary, not a subcommand.
package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/connectorruntime"
	"spindrift.dev/launcher/internal/connectorsandbox"
	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/gitremote"
	"spindrift.dev/launcher/internal/launcherchecks"
	"spindrift.dev/launcher/internal/runner"
)

// Environment abstracts host detection so runQuickstart is testable without
// touching the real host.
type Environment interface {
	LookPath(file string) (string, error)
	LookupEnv(key string) (string, bool)

	Getenv(key string) string

	// GHAuthToken returns the host gh CLI's own token, the fallback for an
	// operator who declines to paste a fine-grained PAT.
	GHAuthToken() (string, error)

	GitConfig(key string) string

	// GitRemoteURL returns the raw "origin" remote URL, or "" when there is
	// no origin. Callers parse it with gitremote.ParseHostSlug to detect the
	// backend and seed the slug default.
	GitRemoteURL() string

	// InsideGitWorkTree reports whether dir sits inside a git work tree. An
	// untracked flake.nix is invisible to `nix develop`/direnv, so the
	// wizard warns the operator to `git add` the scaffold (issue #2567).
	InsideGitWorkTree(dir string) bool
}

// validateRepoSlug rejects anything but the single-slash "owner/name" shape
// the generated flake.nix's forge.repoSlug expects.
func validateRepoSlug(slug string) error {
	parts := strings.Split(slug, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || strings.ContainsAny(slug, " \t\n\r") {
		return fmt.Errorf("expected owner/repo, got %q", slug)
	}
	return nil
}

func validateRuntimeChoice(runtime string) error {
	for _, v := range runner.ValidValues {
		if runtime == v {
			return nil
		}
	}
	return fmt.Errorf("expected one of %s, got %q", strings.Join(runner.ValidValues, ", "), runtime)
}

// quickstartStrategies is the source the wizard detects and offers backends
// from: every registered Strategy, in registration order. A var so tests can
// add a row without leaking it into the global registry, which has no
// unregister.
var quickstartStrategies = backendkit.Strategies

// The axes a quickstart block may name (lib/renderers.nix quickstartAxes).
const (
	axisTracker = "tracker"
	axisForge   = "forge"
)

// offeredOn returns the rows the wizard offers on an axis, in registration
// order: those valid on it whose credential quickstart can collect. A row
// declaring a token env var needs an AcquireToken hook to be offered; one
// declaring none (local, git) needs nothing, and a forge-only row is never
// valid as a tracker.
func offeredOn(axis string, rows []backendkit.Strategy) []backendkit.Strategy {
	var offered []backendkit.Strategy
	for _, s := range rows {
		valid := s.ValidAsTracker
		if axis == axisForge {
			valid = s.ValidAsCodeForge
		}
		if valid && (harnessEnvTokenEnvVar(s.Name) == "" || s.AcquireToken != nil) {
			offered = append(offered, s)
		}
	}
	return offered
}

// strategyNames lists the rows' names in order, the choices a backend prompt offers.
func strategyNames(rows []backendkit.Strategy) []string {
	names := make([]string, len(rows))
	for i, s := range rows {
		names[i] = s.Name
	}
	return names
}

// detectBackend returns the quickstart row that claims the remote's host and
// the knobs its remote implies. A row claims a remote when it carries a
// quickstart block listing the host and its RemoteMatch accepts the remote
// under those knobs.
func detectBackend(rows []backendkit.Strategy, host, slug, remote string) (backendkit.Strategy, map[string]string, bool) {
	for _, s := range rows {
		if s.Quickstart == nil || !slices.Contains(s.Quickstart.Hosts, host) || s.RemoteMatch == nil {
			continue
		}
		knobs := remoteKnobs(s, remote)
		if s.RemoteMatch(backendkit.Shared{RepoSlug: slug}, knobLookup(knobs, nil), remote) {
			return s, knobs, true
		}
	}
	return backendkit.Strategy{}, nil, false
}

func remoteKnobs(s backendkit.Strategy, remote string) map[string]string {
	if s.KnobsFromRemote == nil {
		return nil
	}
	return s.KnobsFromRemote(remote)
}

// validateBackendChoice accepts only the names offered on one axis.
func validateBackendChoice(names []string) func(string) error {
	return func(b string) error {
		if slices.Contains(names, b) {
			return nil
		}
		return fmt.Errorf("expected one of %s, got %q", strings.Join(names, ", "), b)
	}
}

// CommandRunner abstracts the two subprocesses Quickstart shells out to,
// `claude setup-token` and `nix develop --command spindrift build`.
type CommandRunner interface {
	Run(name string, args ...string) error
}

// defaultBaseBranch and defaultMergePolicy match the launcher's own
// BASE_BRANCH/MERGE_MODE defaults. The generated flake.nix sets neither, so
// doctor.Run below must probe these rather than the Config zero value.
const (
	defaultBaseBranch  = "main"
	defaultMergePolicy = "manual"
)

// quickstartRerunCmd is quoted once so rewording the command the error hints
// name doesn't mean hunting down every message that mentions it.
const quickstartRerunCmd = "nix run github:jordansmall/spindrift#quickstart"

// gitAddReminder is printed twice (issue #2567): inside a git work tree `nix
// develop` resolves `.` via `git+file://` and silently excludes untracked
// files, and the early print scrolls off during the doctor and build steps.
const gitAddReminder = "\nRun `git add flake.nix .gitignore .envrc` — an untracked flake.nix is invisible to `nix develop`/direnv."

// defaultDispatchLabels are the launcher's own label defaults, which the
// generated flake relies on implicitly because the wizard never prompts for
// custom names, plus the fixed Ambiguous label (#2275) the trackers need even
// though doctor.Config never checks it.
var defaultDispatchLabels = forge.DispatchLabels{
	Dispatchable: "ready-for-agent",
	InProgress:   "agent-in-progress",
	Complete:     "agent-complete",
	Failed:       "agent-failed",
	Ambiguous:    "agent-ambiguous-spec",
}

// spindriftBuildArgs builds the Consumer's first image (ADR 0027). Shared
// with tests so the command can't drift from its assertions.
var spindriftBuildArgs = []string{"nix", "develop", "--command", "spindrift", "build"}

// spindriftDoctorArgs is the command text the doctor post-write rerun points
// at. Doctor runs in-process, so this is only ever displayed, never executed.
var spindriftDoctorArgs = "nix develop --command spindrift doctor"

// postWriteStep pairs one post-write step's name with its rerun command
// (issue #2563), so a call site can't mix one step's message with another's.
type postWriteStep struct {
	name  string
	rerun string
	next  *postWriteStep // optional: the step that remains after this one succeeds
}

// buildPostWriteStep is the rerun path a post-write build failure points at
// (issue #2563). Nothing remains after a successful build, so next is nil.
var buildPostWriteStep = postWriteStep{
	name:  "spindrift build",
	rerun: strings.Join(spindriftBuildArgs, " "),
}

// doctorPostWriteStep is the rerun path a post-write doctor failure points at
// (issue #2563). The `spindrift` binary only exists inside the generated
// flake's devShell, so the rerun goes through `nix develop`. next copies
// buildPostWriteStep's fields rather than aliasing that package-level var.
var doctorPostWriteStep = postWriteStep{
	name:  "spindrift doctor",
	rerun: spindriftDoctorArgs,
	next: &postWriteStep{
		name:  buildPostWriteStep.name,
		rerun: buildPostWriteStep.rerun,
	},
}

// postWriteFailure wraps a doctor or build failure that happens once the
// scaffold files are on disk (issue #2563). It names those files so the
// operator knows nothing needs re-writing, and points at the failed step's
// own rerun command, never --force (which guards only the pre-write clobber
// check) and never the wizard itself.
func postWriteFailure(step postWriteStep, written []string, insideGitWorkTree bool, err error) error {
	msg := fmt.Sprintf("%s failed after writing %s — fix the underlying issue (hand-edit the written files directly if that's the problem)",
		step.name, strings.Join(written, ", "))
	if insideGitWorkTree {
		msg += ", run `git add flake.nix .gitignore .envrc` (an untracked flake.nix is invisible to `nix develop`/direnv)"
	}
	msg += fmt.Sprintf(", then rerun directly (no need to redo quickstart): %s", step.rerun)
	if step.next != nil {
		msg += fmt.Sprintf("; once that passes, also run: %s", step.next.rerun)
	}
	return fmt.Errorf("%s (cause: %w)", msg, err)
}

// ForgeBuilder constructs the real IssueTracker and CodeForge from the chosen
// tracker and forge Strategies so the finish line's doctor validation runs
// in-process: the `spindrift` binary doesn't exist yet at this pre-CLI stage
// (ADR 0027). Injected so tests substitute a forge.Fake instead of reaching a
// live API.
type ForgeBuilder func(tracker, codeForge backendkit.Strategy, sh backendkit.Shared, l backendkit.Lookup) (forge.IssueTracker, forge.CodeForge, error)

// buildTracker builds the backend's issue tracker alone.
func buildTracker(s backendkit.Strategy, sh backendkit.Shared, l backendkit.Lookup) (forge.IssueTracker, error) {
	if s.NewIssueTracker == nil {
		return nil, fmt.Errorf("backend %q cannot build an issue tracker", s.Name)
	}
	it, err := s.NewIssueTracker(sh, l)
	if err != nil {
		return nil, fmt.Errorf("build %s issue tracker: %w", s.Name, err)
	}
	return it, nil
}

// buildForge is the production ForgeBuilder: the tracker comes from the
// tracker row and the Code Forge from the forge row, which the operator chose
// independently.
func buildForge(tracker, codeForge backendkit.Strategy, sh backendkit.Shared, l backendkit.Lookup) (forge.IssueTracker, forge.CodeForge, error) {
	it, err := buildTracker(tracker, sh, l)
	if err != nil {
		return nil, nil, err
	}
	if codeForge.NewCodeForge == nil {
		return nil, nil, fmt.Errorf("backend %q cannot build a code forge", codeForge.Name)
	}
	cf, err := codeForge.NewCodeForge(sh, l, forge.SanitizedParent{}, it)
	if err != nil {
		return nil, nil, fmt.Errorf("build %s code forge: %w", codeForge.Name, err)
	}
	return it, cf, nil
}

// quickstartShared is the Shared the wizard hands every Strategy hook;
// connectors is nil in a test that builds no Connector-routed tracker.
func quickstartShared(connectors *connectorruntime.Runtime, repoSlug, gitUserName, gitUserEmail string) backendkit.Shared {
	return backendkit.Shared{
		RepoSlug:     repoSlug,
		Labels:       defaultDispatchLabels,
		GitUserName:  gitUserName,
		GitUserEmail: gitUserEmail,
		Connectors:   connectors,
	}
}

// knobLookup resolves the remote-derived knobs plus, once acquired, each
// token under its backend's TokenEnvVar (secrets maps env name to value).
func knobLookup(knobs, secrets map[string]string) backendkit.Lookup {
	return func(name string) (string, bool) {
		if v, ok := secrets[name]; ok && v != "" {
			return v, true
		}
		v, ok := knobs[name]
		return v, ok
	}
}

// exportToken puts a backend's token in the process environment, where the gh
// CLI and a Connector row read it.
func exportToken(tokenEnv, token string) error {
	if err := os.Setenv(tokenEnv, token); err != nil {
		return fmt.Errorf("set %s: %w", tokenEnv, err)
	}
	return nil
}

// backupSuffixDigits is the zero-padded width of the .bak.NNNNNN suffix used
// when a backup name is taken (issue #2563), wide enough that lexical sort
// order still matches numeric order past any realistic number of reruns.
const backupSuffixDigits = 6

// backupRecord is one entry in the backup loop's undo log, so a later failure
// in the same call can reverse the rename.
type backupRecord struct {
	name, path, bakPath string
}

// rollbackAndFail reverses every successful rename in backedUp. A rollback
// that itself fails is never discarded: the message names which files are
// still under their .bak path and the `mv` to restore each one, rather than
// claiming the directory is clean when it isn't.
func rollbackAndFail(backedUp []backupRecord, restore func(bakPath, path string) error, name string, err error) error {
	backupErr := fmt.Errorf("back up %s: %w", name, err)
	var failedRestores []string
	for i := len(backedUp) - 1; i >= 0; i-- {
		b := backedUp[i]
		if restoreErr := restore(b.bakPath, b.path); restoreErr != nil {
			failedRestores = append(failedRestores, fmt.Sprintf("%s (still at %s, run: mv %s %s): %v", b.path, b.bakPath, b.bakPath, b.path, restoreErr))
		}
	}
	if len(failedRestores) > 0 {
		return fmt.Errorf("%w; rollback incomplete, still backed up as: %s; restore each manually, then rerun quickstart", backupErr, strings.Join(failedRestores, "; "))
	}
	return backupErr
}

// runQuickstart drives the wizard end-to-end over injected I/O and seams.
// Interactive-only for v1: a non-TTY stdin is a fatal error that directs
// scripted setups to write flake.nix and harness.env directly.
func runQuickstart(connectors *connectorruntime.Runtime, dir string, env Environment, cmdRunner CommandRunner, forgeBuilder ForgeBuilder, w io.Writer, stdin io.Reader, interactive, force bool) error {
	if !interactive {
		return fmt.Errorf("quickstart requires an interactive terminal — for scripted setups, write flake.nix and harness.env directly (see docs/flake-options.md)")
	}

	targets := []string{"flake.nix", "harness.env"}
	var clobbered []string
	for _, name := range targets {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			clobbered = append(clobbered, name)
		}
	}
	if len(clobbered) > 0 && !force {
		return fmt.Errorf("refusing to overwrite existing %s — rerun with --force to back each up and regenerate", strings.Join(clobbered, ", "))
	}

	driverName, _ := env.LookupEnv("DRIVER")
	// Quickstart has no input document, so a Connector-only Driver has no
	// registered Strategy; its Descriptor is what names it.
	if _, ok := connectors.Drivers().DescriptorFor(driver.ResolveName(driverName)); !ok {
		return driver.UnknownError(driverName, driver.DescriptorNames())
	}

	detectedRuntime, err := runner.Probe(env.LookPath)
	if err != nil {
		return err
	}

	scanner := bufio.NewScanner(stdin)
	readLine := func(label, def string) (string, bool) {
		if def != "" {
			fmt.Fprintf(w, "%s [%s]: ", label, def)
		} else {
			fmt.Fprintf(w, "%s: ", label)
		}
		ok := scanner.Scan()
		if v := scanner.Text(); ok && v != "" {
			return v, true
		}
		return def, ok
	}
	promptDefault := func(label, def string) string {
		v, _ := readLine(label, def)
		return v
	}
	prompt := func(label string) string { return promptDefault(label, "") }
	// promptValidated re-prompts on an invalid answer, but errors once stdin
	// is exhausted rather than spinning forever.
	promptValidated := func(label, def string, validate func(string) error) (string, error) {
		for {
			v, ok := readLine(label, def)
			if err := validate(v); err != nil {
				if !ok {
					return "", fmt.Errorf("%s: %w", label, err)
				}
				fmt.Fprintf(w, "invalid input: %v\n", err)
				continue
			}
			return v, nil
		}
	}
	promptMasked := func(label string) string {
		fmt.Fprintf(w, "%s: ", label)
		value, masked := readMasked(stdin, scanner)
		if masked {
			fmt.Fprintln(w)
		}
		return value
	}

	remoteURL := env.GitRemoteURL()
	host, repoSlugDefault := gitremote.ParseHostSlug(remoteURL)
	rows := quickstartStrategies()
	trackers, forges := offeredOn(axisTracker, rows), offeredOn(axisForge, rows)
	if len(trackers) == 0 || len(forges) == 0 {
		return errors.New("quickstart offers no issue tracker or no code forge")
	}
	// The remote's host pre-selects a backend per axis, but never skips the
	// prompts: an operator may put the tracker and the forge on different
	// backends. With no claimed host each axis defaults to its first row.
	trackerDefault, forgeDefault := trackers[0].Name, forges[0].Name
	var detectedName string
	if host != "" {
		if s, _, ok := detectBackend(rows, host, repoSlugDefault, remoteURL); ok {
			detectedName = s.Name
			fmt.Fprintf(w, "detected a %s remote — pre-selecting the %s backend\n", host, s.Name)
			if slices.Contains(s.Quickstart.Axes, axisTracker) && slices.ContainsFunc(trackers, func(t backendkit.Strategy) bool { return t.Name == s.Name }) {
				trackerDefault = s.Name
			}
			if slices.Contains(s.Quickstart.Axes, axisForge) && slices.ContainsFunc(forges, func(f backendkit.Strategy) bool { return f.Name == s.Name }) {
				forgeDefault = s.Name
			}
		}
	}
	pickBackend := func(label, def string, offered []backendkit.Strategy) (backendChoice, error) {
		names := strategyNames(offered)
		name, err := promptValidated(fmt.Sprintf("%s (%s)", label, strings.Join(names, "/")), def, validateBackendChoice(names))
		if err != nil {
			return backendChoice{}, err
		}
		return backendChoice{name: name, row: offered[slices.Index(names, name)]}, nil
	}
	trackerChoice, err := pickBackend("Issue tracker", trackerDefault, trackers)
	if err != nil {
		return err
	}
	forgeChoice, err := pickBackend("Code forge", forgeDefault, forges)
	if err != nil {
		return err
	}
	// The checkout remote is the forge's, so only the forge axis, or a tracker
	// on the forge's own backend or the row that claimed the remote, derives
	// knobs from it; any other tracker would be pointed at the forge's host.
	forgeChoice.knobs = remoteKnobs(forgeChoice.row, remoteURL)
	if trackerChoice.name == forgeChoice.name || trackerChoice.name == detectedName {
		trackerChoice.knobs = remoteKnobs(trackerChoice.row, remoteURL)
	} else if trackerChoice.row.KnobsFromRemote != nil {
		// Blank keeps the default instance, left as a commented default in flake.nix.
		label := fmt.Sprintf("%s issue tracker repo URL (blank for the default instance)", consumerStanzas[trackerChoice.name].DisplayName)
		trackerURL, err := promptValidated(label, "", func(u string) error {
			if u != "" && trackerChoice.row.KnobsFromRemote(u) == nil {
				return fmt.Errorf("cannot read an instance from %q — give the tracker's repo URL, or leave blank", u)
			}
			return nil
		})
		if err != nil {
			return err
		}
		if trackerURL != "" {
			trackerChoice.knobs = trackerChoice.row.KnobsFromRemote(trackerURL)
		}
	}
	a := answers{tracker: trackerChoice, forge: forgeChoice}

	repoSlug, err := promptValidated("Repo slug (owner/repo)", repoSlugDefault, validateRepoSlug)
	if err != nil {
		return err
	}
	runtime, err := promptValidated(fmt.Sprintf("Runtime (%s)", strings.Join(runner.ValidValues, "/")), detectedRuntime, validateRuntimeChoice)
	if err != nil {
		return err
	}
	if rerr := runner.ValidateRuntimeWithLookup(runtime, env.LookPath); rerr != nil {
		fmt.Fprintf(w, "WARNING: %v — without it installed, `spindrift build` will fail later.\n", rerr)
		if strings.ToLower(strings.TrimSpace(prompt("Proceed anyway and install it before the first build? [y/N]"))) != "y" {
			return fmt.Errorf("%w — install it, or rerun quickstart and choose a different runtime", rerr)
		}
	}
	gitUserName := promptDefault("Git user name", env.GitConfig(hostGitConfigKeyGitUserName))
	gitUserEmail := promptDefault("Git user email", env.GitConfig(hostGitConfigKeyGitUserEmail))

	shared := quickstartShared(connectors, repoSlug, gitUserName, gitUserEmail)
	// Quickstart installs no Connector sandbox, so a Connector-read tracker or
	// Code Forge defers its finish-line checks to `spindrift doctor`, as does
	// the token probe of any chosen row whose tracker side is the Connector.
	deferChecks := !connectors.HasSandbox() &&
		(backendkit.TrackerReadsThroughConnector(trackerChoice.name) ||
			backendkit.ForgeReadsThroughConnector(forgeChoice.name))
	tokens := map[string]string{}
	for _, c := range a.chosen() {
		row, knobs := c.row, c.knobs
		tokenEnv := harnessEnvTokenEnvVar(c.name)
		if tokenEnv == "" {
			continue
		}
		token, err := row.AcquireToken(
			shared,
			knobLookup(knobs, nil),
			backendkit.TokenPrompt{
				Out:          w,
				Getenv:       env.Getenv,
				Prompt:       prompt,
				PromptMasked: promptMasked,
				AmbientToken: env.GHAuthToken,
				Probe: func(token string) error {
					if deferChecks && backendkit.TrackerReadsThroughConnector(row.Name) {
						return connectorsandbox.ErrNoConnectorSandbox
					}
					// A Connector row authorizes from the process environment,
					// which the wizard's token has yet to reach. Every row is
					// exported; a knob-read row (jira) also gets it from the
					// lookup below, so the export is harmless there.
					if err := exportToken(tokenEnv, token); err != nil {
						return err
					}
					// The row is built as both axes so the probe needs nothing
					// the other chosen row has yet to collect.
					it, _, err := forgeBuilder(row, row, shared, knobLookup(knobs, map[string]string{tokenEnv: token}))
					if err != nil {
						return err
					}
					_, err = it.Probe()
					return err
				},
				RerunCmd: quickstartRerunCmd,
			})
		if err != nil {
			return err
		}
		tokens[row.Name] = token
	}

	rules, _ := connectors.Drivers().CredentialRules(driverName)
	credentials, err := gatherCredentials(env, rules, w, prompt, promptMasked, cmdRunner)
	if err != nil {
		return err
	}

	a.repoSlug, a.runtime = repoSlug, runtime
	a.gitUserName, a.gitUserEmail = gitUserName, gitUserEmail
	a.tokens, a.drivers, a.driver, a.credentials = tokens, connectors.Drivers(), driverName, credentials

	files, err := render(a)
	if err != nil {
		return err
	}

	// Backup runs only after every abort point, so an earlier abort never
	// loses a file to a rename. Lines print in a second pass: a failed rename
	// rolls back every backup, which would falsify a line printed inline.
	var backedUp []backupRecord
	for _, name := range clobbered {
		path := filepath.Join(dir, name)
		bakPath := path + ".bak"
		for n := 1; ; n++ {
			f, err := os.OpenFile(bakPath, os.O_CREATE|os.O_EXCL, 0o644)
			if err == nil {
				f.Close()
				break
			}
			if !os.IsExist(err) {
				return rollbackAndFail(backedUp, os.Rename, name, err)
			}
			bakPath = fmt.Sprintf("%s.bak.%0*d", path, backupSuffixDigits, n)
		}
		if err := os.Rename(path, bakPath); err != nil {
			_ = os.Remove(bakPath)
			return rollbackAndFail(backedUp, os.Rename, name, err)
		}
		backedUp = append(backedUp, backupRecord{name, path, bakPath})
	}
	for _, b := range backedUp {
		fmt.Fprintf(w, "backed up: %s -> %s\n", b.name, filepath.Base(b.bakPath))
	}

	var written []string
	for _, f := range files {
		outcome, err := preserveExisting(dir, f, w)
		if err != nil {
			return err
		}
		switch outcome {
		case mergedExisting:
			written = append(written, f.path+" (merged)")
			continue
		case keptExisting:
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, f.path), []byte(f.content), f.mode); err != nil {
			return fmt.Errorf("write %s: %w", f.path, err)
		}
		fmt.Fprintf(w, "wrote: %s\n", f.path)
		written = append(written, f.path)
	}

	// The gh CLI and a Connector row's requests read their credential from the
	// process environment. Keyed off the descriptor so each chosen backend's
	// token is exported under its own env var name.
	for _, c := range a.chosen() {
		tokenEnv := harnessEnvTokenEnvVar(c.name)
		if tokenEnv == "" {
			continue
		}
		if err := exportToken(tokenEnv, tokens[c.name]); err != nil {
			return err
		}
	}
	insideGitWorkTree := env.InsideGitWorkTree(dir)
	if insideGitWorkTree {
		fmt.Fprintln(w, gitAddReminder)
	}

	if deferChecks {
		fmt.Fprintln(w, "the finish-line checks need the Connector sandbox, which quickstart does not run — deferred to `spindrift doctor`. Before running it, fill in any settings left commented out in flake.nix and any blank values in harness.env.")
	} else {
		it, cf, err := forgeBuilder(a.tracker.row, a.forge.row, shared, a.lookup())
		if err != nil {
			return postWriteFailure(doctorPostWriteStep, written, insideGitWorkTree, err)
		}
		tokenHint, slugHint, noLabelRegistry := doctorHints(a.tracker.name)
		// launcherchecks carries no runtime row: doctor.Config.Runtime below
		// reports runtime validity.
		launcherRows := launcherchecks.All(quickstartCheckConfig(a), quickstartCheckDeps(connectors, a))
		// Verbose: quickstart's finish line always reports in full, whatever
		// `spindrift doctor`'s own quiet default does (issue #3777).
		rep := doctor.NewReporter(w, true)
		if err := doctor.Run(it, cf, doctor.Config{
			IssueTracker:    a.tracker.name,
			TokenHint:       tokenHint,
			SlugHint:        slugHint,
			NoLabelRegistry: noLabelRegistry,
			Label:           defaultDispatchLabels.Dispatchable,
			InProgressLabel: defaultDispatchLabels.InProgress,
			FailedLabel:     defaultDispatchLabels.Failed,
			CompleteLabel:   defaultDispatchLabels.Complete,
			Runtime:         runtime,
			MergePolicy:     defaultMergePolicy,
			BaseBranch:      defaultBaseBranch,
		}, rep, scanner, interactive, launcherRows); err != nil {
			return postWriteFailure(doctorPostWriteStep, written, insideGitWorkTree, err)
		}
	}

	fmt.Fprintln(w, "==> the first image build can take a while — building now")
	if err := cmdRunner.Run(spindriftBuildArgs[0], spindriftBuildArgs[1:]...); err != nil {
		return postWriteFailure(buildPostWriteStep, written, insideGitWorkTree, err)
	}

	fmt.Fprintln(w, "\nQuickstart complete. Wrote:")
	for _, f := range written {
		fmt.Fprintf(w, "  %s\n", f)
	}
	if insideGitWorkTree {
		fmt.Fprintln(w, gitAddReminder)
	}
	fmt.Fprintln(w, "\nNext: run `spindrift dispatch`.")

	return nil
}

// existingOutcome is what preserveExisting did with a scaffold file.
type existingOutcome int

const (
	writeFresh     existingOutcome = iota // no existing file to protect; write normally
	mergedExisting                        // .gitignore was appended to
	keptExisting                          // existing file left alone
)

// preserveExisting applies the existing-file policy for the two shared dotfiles
// the operator may already own.
func preserveExisting(dir string, f scaffoldFile, w io.Writer) (existingOutcome, error) {
	if f.path != ".gitignore" && f.path != ".envrc" {
		return writeFresh, nil
	}
	path := filepath.Join(dir, f.path)
	existing, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return writeFresh, nil
	}
	if err != nil {
		return writeFresh, fmt.Errorf("read %s: %w", f.path, err)
	}
	if f.path == ".envrc" {
		hint := ""
		if !hasUseFlake(string(existing)) {
			hint = " — add `use flake` so direnv loads the dev shell"
		}
		fmt.Fprintf(w, "kept: .envrc (existing)%s\n", hint)
		return keptExisting, nil
	}
	// Merged rather than refused: this file is what keeps the 0600
	// harness.env secrets out of git, and refusing would push operators
	// toward --force.
	suffix := mergeGitignore(string(existing))
	if suffix == "" {
		fmt.Fprintln(w, "kept: .gitignore (already has the spindrift entries)")
		return keptExisting, nil
	}
	// O_APPEND, not a rewrite: a failed write can never truncate the
	// operator's own entries.
	af, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, f.mode)
	if err != nil {
		return writeFresh, fmt.Errorf("write %s: %w", f.path, err)
	}
	if _, err := af.WriteString(suffix); err != nil {
		_ = af.Close()
		return writeFresh, fmt.Errorf("write %s: %w", f.path, err)
	}
	if err := af.Close(); err != nil {
		return writeFresh, fmt.Errorf("write %s: %w", f.path, err)
	}
	fmt.Fprintln(w, "merged: .gitignore")
	return mergedExisting, nil
}

func hasUseFlake(envrc string) bool {
	for _, line := range strings.Split(envrc, "\n") {
		if f := strings.Fields(line); len(f) >= 2 && f[0] == "use" && f[1] == "flake" {
			return true
		}
	}
	return false
}

// mergeGitignore returns the text to append to existing so every
// quickstartGitignore entry is ignored, or "" when none is missing. Presence
// follows git's matching: leading whitespace is literal, trailing whitespace
// is not, and for a given pattern the last of "X" / "!X" wins. The block uses
// CRLF when existing does.
func mergeGitignore(existing string) string {
	have := map[string]bool{}
	for _, line := range strings.Split(existing, "\n") {
		line = strings.TrimRight(line, " \t\r")
		if neg, ok := strings.CutPrefix(line, "!"); ok {
			have[neg] = false
		} else {
			have[line] = true
		}
	}
	var missing []string
	for _, line := range strings.Split(quickstartGitignore, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") || have[t] {
			continue
		}
		have[t] = true
		missing = append(missing, t)
	}
	if len(missing) == 0 {
		return ""
	}
	nl := "\n"
	if strings.Contains(existing, "\r\n") {
		nl = "\r\n"
	}
	var b strings.Builder
	if existing != "" {
		if !strings.HasSuffix(existing, "\n") {
			b.WriteString(nl)
		}
		b.WriteString(nl)
	}
	b.WriteString("# spindrift" + nl)
	for _, m := range missing {
		b.WriteString(m + nl)
	}
	return b.String()
}

// quickstartGitignore must stay a strict superset of
// templates/default/.gitignore, plus the flake.nix.bak*/harness.env.bak*
// entries for backup churn a `--force` rerun leaves behind.
const quickstartGitignore = `# nix build output
result
result-*

# spindrift artifacts (per-run logs, outbox, accumulation repo, issues)
.spindrift/

# local config + secrets — never commit this
harness.env
harness.env.bak*

# throwaway backups from --force reruns — flake.nix itself is meant to be
# committed, but its numbered .bak copies are not
flake.nix.bak*

# direnv
.direnv/

# container-fallback build artifacts (staged under tmpdir; listed here as safety net)
.spindrift-image.tar
.spindrift-image-path

# OS
.DS_Store
`

const quickstartEnvrc = "use flake\n"

// doctorHints resolves doctor.Config's TokenHint, SlugHint, and NoLabelRegistry.
// The token hint is the row's TokenEnvVar and an empty slug hint means
// doctor.Run's github-shaped default; an unknown tracker keeps the label check.
func doctorHints(issueTracker string) (tokenHint, slugHint string, noLabelRegistry bool) {
	row, ok := backend.ByName(issueTracker)
	if !ok {
		return "", "", false
	}
	return row.TokenEnvVar, row.DoctorSlugHint, !row.LabelRegistry
}

// backendChoice is the backend the wizard settled on, its Strategy row (set
// only by runQuickstart; render needs just the name) and the knob values (env
// name -> value) the row derived from the git remote.
type backendChoice struct {
	name  string
	row   backendkit.Strategy
	knobs map[string]string
}

// answers holds every operator decision, detected defaults folded in, so
// render can produce the scaffold without touching any I/O seam.
type answers struct {
	repoSlug     string
	runtime      string
	gitUserName  string
	gitUserEmail string
	tracker      backendChoice
	forge        backendChoice
	tokens       map[string]string // acquired token per backend name
	drivers      *driver.Set
	driver       string
	credentials  []credential
}

// chosen returns the distinct backends the scaffold names, tracker first. A
// backend on both axes derives its knobs from the same remote, so one entry
// stands for both.
func (a answers) chosen() []backendChoice {
	if a.forge.name == a.tracker.name {
		return []backendChoice{a.tracker}
	}
	return []backendChoice{a.tracker, a.forge}
}

// lookup serves every chosen backend's remote-derived knobs and acquired token
// (under its TokenEnvVar), as one Lookup for both axes.
func (a answers) lookup() backendkit.Lookup {
	knobs, secrets := map[string]string{}, map[string]string{}
	for _, c := range a.chosen() {
		maps.Copy(knobs, c.knobs)
		if env := harnessEnvTokenEnvVar(c.name); env != "" {
			secrets[env] = a.tokens[c.name]
		}
	}
	return knobLookup(knobs, secrets)
}

// credential is one secret the wizard gathered for the Driver, in the order
// harness.env writes it.
type credential struct {
	env, value string
}

type scaffoldFile struct {
	path    string
	content string
	mode    os.FileMode
}

// render turns answers into the whole scaffold with no I/O of its own. Every
// operator string crosses the nixEscape seam inside this call, before
// runQuickstart writes to disk.
func render(a answers) ([]scaffoldFile, error) {
	flakeNix, err := renderFlakeNix(a)
	if err != nil {
		return nil, err
	}
	return []scaffoldFile{
		{path: "flake.nix", content: flakeNix, mode: 0o644},
		{path: "harness.env", content: renderHarnessEnv(a), mode: 0o600},
		{path: ".gitignore", content: quickstartGitignore, mode: 0o644},
		{path: ".envrc", content: quickstartEnvrc, mode: 0o644},
	}, nil
}

// renderFlakeNix generates a minimal Consumer flake.nix carrying only the
// options the wizard collected (ADR 0027). No prompts/ directory is
// scaffolded, because the harness defaults every prompt.
func renderFlakeNix(a answers) (string, error) {
	settingsLines := fmt.Sprintf("            %s = \"%s\";\n", pathIssueTracker, nixEscape(a.tracker.name)) +
		fmt.Sprintf("            %s = \"%s\";\n", pathCodeForge, nixEscape(a.forge.name))
	for _, choice := range a.chosen() {
		stanza := consumerStanzas[choice.name]
		for env := range choice.knobs {
			if !slices.ContainsFunc(stanza.Settings, func(s stanzaSetting) bool { return s.Env == env }) {
				return "", fmt.Errorf("backend %q derives knob %s from the remote, but its Consumer stanza has no setting for it", choice.name, env)
			}
		}
		// A row chosen only as the tracker leaves its forge-group knobs out: the
		// forge axis is served by another row, so they would configure nothing.
		// Filtered by option group rather than whole-row, since the forgejo row's
		// issues.forgejo.baseURL is also its forge's knob.
		var settings []stanzaSetting
		for _, s := range stanza.Settings {
			if choice.name == a.forge.name || !strings.HasPrefix(s.Path, forgeOptionGroup) {
				settings = append(settings, s)
			}
		}
		if len(settings) == 0 {
			continue
		}
		settingsLines += "\n" + backendHeader("            ", choice.name)
		for _, knob := range settings {
			if v := choice.knobs[knob.Env]; v != "" {
				if lit := `"` + nixEscape(v) + `"`; lit != knob.Default {
					settingsLines += fmt.Sprintf("            %s = %s;\n", knob.Path, lit)
					continue
				}
			}
			settingsLines += fmt.Sprintf("            # %s = %s;\n", knob.Path, knob.Default)
		}
	}

	var extraInputs, moduleImports string
	for _, choice := range a.chosen() {
		stanza := consumerStanzas[choice.name]
		if stanza.FlakeInput != "" {
			for _, line := range strings.Split(strings.TrimSuffix(stanza.FlakeInput, "\n"), "\n") {
				extraInputs += "    " + line + "\n"
			}
		}
		if stanza.ModuleImport != "" {
			moduleImports += "        " + stanza.ModuleImport + "\n"
		}
	}
	// nixfmt keeps a one-element list on one line.
	imports := "      imports = [ spindrift.flakeModules.default ];"
	if moduleImports != "" {
		imports = "      imports = [\n        spindrift.flakeModules.default\n" + moduleImports + "      ];"
	}

	out := fmt.Sprintf(`{
  description = "A spindrift consumer — headless coding agents, one disposable container per issue";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-parts.url = "github:hercules-ci/flake-parts";
    spindrift.url = "github:jordansmall/spindrift";
%s  };

  outputs =
    inputs@{
      flake-parts,
      spindrift,
      ...
    }:
    flake-parts.lib.mkFlake { inherit inputs; } {
      systems = [
        "aarch64-darwin"
        "aarch64-linux"
        "x86_64-linux"
      ];

%s

      perSystem =
        { config, pkgs, ... }:
        {
          # Generated by quickstart with only the chosen options. Full
          # reference: docs/flake-options.md
          spindrift = {
            %s = "%s";
            %s = "%s";
            %s = "%s";
            %s = "%s";
%s          };

          devShells.default = pkgs.mkShell {
            packages = [ config.packages.spindrift ];
          };
        };
    };
}
`, extraInputs, imports,
		pathRuntime, nixEscape(a.runtime),
		pathRepoSlug, nixEscape(a.repoSlug),
		pathGitUserName, nixEscape(a.gitUserName),
		pathGitUserEmail, nixEscape(a.gitUserEmail),
		settingsLines)
	return out, nil
}

// nixEscape escapes a string for a Nix double-quoted literal. Go's %q is not
// a substitute: it escapes the quote but not "${", so an operator-supplied
// "${evil}" would splice live Nix interpolation into the generated flake.
func nixEscape(s string) string {
	r := strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
		"${", `\${`,
	)
	return r.Replace(s)
}

// backendHeader is the "# <DisplayName> backend (<name>)" line that opens a
// backend's group.
func backendHeader(indent, name string) string {
	return indent + fmt.Sprintf(backendHeaderFormat, consumerStanzas[name].DisplayName, name)
}

// renderHarnessEnv writes, for each chosen backend whose Consumer stanza has
// secrets, that stanza's harness.env lines in its order, with the acquired
// token filled in under the backend's TokenEnvVar and its Connector
// credential, then the Driver credentials gatherCredentials collected. The
// generated comments mirror lib/renderers.nix's renderHarnessEnvExample, the
// source of that wording.
func renderHarnessEnv(a answers) string {
	out := harnessEnvPreamble
	for _, c := range a.chosen() {
		stanza := consumerStanzas[c.name]
		if len(stanza.Secrets) == 0 {
			continue
		}
		out += backendHeader("", c.name) + "\n"
		tokenEnv := harnessEnvTokenEnvVar(c.name)
		for _, s := range stanza.Secrets {
			// A credential that is the backend's own token has no separate
			// token to swap in, so the note would mislead.
			if own := ownCredential(c.name, stanza); own != "" && s.Env == own {
				out += connectorCredentialNote
			}
			value := ""
			if s.Env == tokenEnv || s.Env == stanza.Credential {
				value = a.tokens[c.name]
			}
			if s.Secret {
				out += harnessEnvSecretLine(s.Env, value)
			} else {
				out += s.Env + "=" + value + "\n\n"
			}
		}
	}
	for _, c := range a.credentials {
		out += harnessEnvSecretLine(c.env, c.value)
	}
	return out
}

// harnessEnvTokenEnvVar returns the env var name the wizard writes the
// backend credential under, or "" for a backend that needs none. Every
// reader of that name goes through it, so the scaffold, the token loop and the
// doctor row cannot drift.
func harnessEnvTokenEnvVar(backendName string) string {
	if desc, ok := backend.ByName(backendName); ok {
		return desc.TokenEnvVar
	}
	return ""
}

// ownCredential is the Connector credential env var that backend name's stanza
// names as a secret of its own, or "" when the backend has none or its
// credential is the backend's core token.
func ownCredential(name string, stanza consumerStanza) string {
	if stanza.Credential == harnessEnvTokenEnvVar(name) {
		return ""
	}
	return stanza.Credential
}

// harnessEnvSecretLine renders one secret's stanza: the <NAME>_CMD
// indirection comment matching templates/default/harness.env.example, then
// the bare NAME=value line.
func harnessEnvSecretLine(name, value string) string {
	return fmt.Sprintf(secretCmdHintFormat, name, toKebab(name)) + name + "=" + value + "\n\n"
}

// toKebab derives the "spindrift-<kebab-name>" vault key suffix from an env
// var name, so GH_TOKEN becomes gh-token. Two other copies of this transform
// share the name: secretcmd.Template and lib/renderers.nix's toKebab.
func toKebab(name string) string {
	return strings.ReplaceAll(strings.ToLower(name), "_", "-")
}

// gatherCredentials walks the Driver's credential rules in order, reusing an
// ambient variable, else offering the setup command, else prompting for each
// remaining variable until one is non-empty. With none, the last is written
// blank so harness.env shows where it goes. A rule with no setup command only
// prints its Remedy; quickstart configures no models, so a model-gated rule
// is mentioned rather than demanded.
func gatherCredentials(env Environment, rules []driver.CredentialRule, w io.Writer, prompt, promptMasked func(string) string, cmdRunner CommandRunner) ([]credential, error) {
	var creds []credential
	for _, rule := range rules {
		if v, name, ok := ambientCredential(env, rule.Env); ok {
			creds = append(creds, credential{env: name, value: v})
			fmt.Fprintf(w, "reusing ambient %s\n", name)
			continue
		}
		if len(rule.SetupCommand) == 0 {
			remedy := rule.Remedy
			if rule.ModelPrefix != "" {
				remedy += fmt.Sprintf(" (needed only when a model starts with %s)", rule.ModelPrefix)
			}
			fmt.Fprintf(w, "No ambient credential found for %s: %s\n", strings.Join(rule.Env, "/"), remedy)
			continue
		}
		cmdLine := strings.Join(rule.SetupCommand, " ")
		hint := ""
		if rule.SetupHint != "" {
			hint = " (" + rule.SetupHint + ")"
		}
		if strings.ToLower(strings.TrimSpace(prompt(fmt.Sprintf("No ambient credential found (%s). Run `%s` now%s? [y/N]", strings.Join(rule.Env, "/"), cmdLine, hint)))) == "y" {
			if err := cmdRunner.Run(rule.SetupCommand[0], rule.SetupCommand[1:]...); err != nil {
				return nil, fmt.Errorf("run %s: %w", cmdLine, err)
			}
			v := promptMasked(fmt.Sprintf("Paste the %s printed by %s", rule.Env[0], cmdLine))
			if v == "" {
				return nil, fmt.Errorf("%s: no token pasted", cmdLine)
			}
			creds = append(creds, credential{env: rule.Env[0], value: v})
			continue
		}
		supplied := false
		for _, name := range rule.Env[1:] {
			question := "Value for " + name
			if label := rule.Labels[name]; label != "" {
				question = fmt.Sprintf("%s (%s)", label, name)
			}
			v := promptMasked(question)
			if v == "" {
				continue
			}
			creds = append(creds, credential{env: name, value: v})
			supplied = true
			break
		}
		if !supplied {
			creds = append(creds, credential{env: rule.Env[len(rule.Env)-1]})
			fmt.Fprintf(w, "No credential supplied for %s: %s\n", strings.Join(rule.Env, "/"), rule.Remedy)
		}
	}
	return creds, nil
}

func ambientCredential(env Environment, names []string) (value, name string, ok bool) {
	for _, n := range names {
		if v, set := env.LookupEnv(n); set && v != "" {
			return v, n, true
		}
	}
	return "", "", false
}

// forgeOptionGroup is the nix option group the code-forge knobs live under,
// derived from pathCodeForge's own prefix ("forge.").
var forgeOptionGroup = pathCodeForge[:strings.LastIndex(pathCodeForge, ".")+1]

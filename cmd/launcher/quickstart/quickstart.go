// Command quickstart is the pre-CLI Quickstart wizard (ADR 0027). It runs
// before the `spindrift` binary exists and authors flake.nix options rather
// than launcher env knobs, so it is its own binary, not a subcommand.
package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgejo"
	"spindrift.dev/launcher/internal/forge/github"
	"spindrift.dev/launcher/internal/forge/jira"
	"spindrift.dev/launcher/internal/forge/local"
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

	// TokenScopes reads the X-OAuth-Scopes header GitHub returns for a
	// ghp_/gho_ token. Fine-grained PATs have no such endpoint, so only the
	// classic audit branch calls this.
	TokenScopes(token string) ([]string, error)

	// GHAuthToken returns the host gh CLI's own token, the fallback for an
	// operator who declines to paste a fine-grained PAT.
	GHAuthToken() (string, error)

	GitConfig(key string) string
	GitRemoteRepoSlug() string

	// GitRemoteURL returns the raw "origin" remote URL, or "" when there is
	// no origin. Callers parse it with gitremote.ParseHostSlug to detect a
	// Forgejo host; the github-only GitRemoteRepoSlug seeds the slug default.
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

// quickstartBackendNames returns the backend names the wizard accepts when
// the git remote host is neither github.com nor codeberg.org. It reads the
// shared registry so a newly registered backend appears here unedited.
func quickstartBackendNames() []string {
	descriptors := backend.QuickstartEligible()
	names := make([]string, len(descriptors))
	for i, d := range descriptors {
		names[i] = d.Name
	}
	return names
}

func validateBackendChoice(b string) error {
	names := quickstartBackendNames()
	for _, v := range names {
		if b == v {
			return nil
		}
	}
	return fmt.Errorf("expected one of %s, got %q", strings.Join(names, ", "), b)
}

// CommandRunner abstracts the two subprocesses Quickstart shells out to,
// `claude setup-token` and `nix develop --command spindrift build`.
type CommandRunner interface {
	Run(name string, args ...string) error
}

// defaultBranchPrefix matches the launcher's own BRANCH_PREFIX default
// (flagtable_gen.go); Quickstart doesn't prompt for it.
const defaultBranchPrefix = "agent/issue-"

// defaultBaseBranch and defaultMergePolicy match the launcher's own
// BASE_BRANCH/MERGE_MODE defaults. The generated flake.nix sets neither, so
// doctor.Run below must probe these rather than the Config zero value.
const (
	defaultBaseBranch  = "main"
	defaultMergePolicy = "manual"
)

// codebergBaseURL is the Forgejo adapter's default, so a forgejoBaseURL of
// exactly this value must not be emitted as an explicit
// issues.forgejo.baseURL line in the generated flake.
const codebergBaseURL = "https://codeberg.org"

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

// ForgeBuilder constructs the real IssueTracker and CodeForge so the finish
// line's doctor validation runs in-process: the `spindrift` binary doesn't
// exist yet at this pre-CLI stage (ADR 0027). Injected so tests substitute a
// forge.Fake instead of shelling out to gh or Jira.
type ForgeBuilder func(repoSlug string, tracker trackerSettings, token string) (forge.IssueTracker, forge.CodeForge)

// forgejoProbeTimeout keeps a hung Forgejo host from blocking the wizard
// forever. Mirrors defaultForgejoHTTPTimeout in the sibling Forgejo adapters.
const forgejoProbeTimeout = 30 * time.Second

// buildForge is the production ForgeBuilder. The Code Forge is github (ADR
// 0027: Quickstart never prompts for it) except in the forgejo case, which
// builds both seams from the Forgejo adapters so doctor validates against
// that instance. token is empty for github and local, where the credential is
// ambient in the process environment or not needed at all.
func buildForge(repoSlug string, tracker trackerSettings, token string) (forge.IssueTracker, forge.CodeForge) {
	cf := github.NewExecClient(repoSlug, defaultDispatchLabels, defaultBranchPrefix)
	switch tracker.issueTracker {
	case "jira":
		return jira.NewJiraClient(jira.JiraConfig{
			BaseURL:    tracker.jiraBaseURL,
			ProjectKey: tracker.jiraProjectKey,
			Email:      tracker.jiraEmail,
			Token:      token,
			Labels:     defaultDispatchLabels,
		}), cf
	case "local":
		return local.NewLocalTracker(tracker.localIssuesDir, defaultDispatchLabels), cf
	case "forgejo":
		it := forgejo.NewForgejoClient(forgejo.ForgejoConfig{
			BaseURL:    tracker.forgejoBaseURL,
			Repo:       repoSlug,
			Token:      token,
			Labels:     defaultDispatchLabels,
			HTTPClient: &http.Client{Timeout: forgejoProbeTimeout},
		})
		cf := forgejo.NewForgejoCodeForge(forgejo.ForgejoCodeForgeConfig{
			BaseURL:      tracker.forgejoBaseURL,
			Repo:         repoSlug,
			Token:        token,
			BranchPrefix: defaultBranchPrefix,
		}, it)
		return it, cf
	default:
		return cf, cf
	}
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
func runQuickstart(dir string, env Environment, cmdRunner CommandRunner, forgeBuilder ForgeBuilder, w io.Writer, stdin io.Reader, interactive, force bool) error {
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
	if _, err := driver.New(driverName); err != nil {
		return err
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
	// promptValidated re-prompts on an invalid answer. Once stdin runs out
	// (ok == false) there is no more input to retry with, so it errors out
	// instead of spinning forever on the exhausted scanner.
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
	host, remoteSlug := gitremote.ParseHostSlug(remoteURL)
	backendName := "github"
	forgejoBaseURL := ""
	repoSlugDefault := env.GitRemoteRepoSlug()
	// The backend is github for a github.com remote or none, forgejo for
	// codeberg.org, and asked directly for any other host. There are no Jira
	// or local sub-prompts: those adapters stay in place for an operator who
	// hand-edits the generated flake.
	switch {
	case host == "codeberg.org":
		backendName, forgejoBaseURL, repoSlugDefault = "forgejo", codebergBaseURL, remoteSlug
		fmt.Fprintln(w, "detected a codeberg.org remote — using the forgejo backend")
	case host != "" && host != "github.com":
		b, err := promptValidated(fmt.Sprintf("Backend (%s)", strings.Join(quickstartBackendNames(), "/")), "github", validateBackendChoice)
		if err != nil {
			return err
		}
		backendName = b
		if b == "forgejo" {
			forgejoBaseURL, repoSlugDefault = "https://"+host, remoteSlug
		}
	}

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
	gitUserName := promptDefault("Git user name", env.GitConfig("user.name"))
	gitUserEmail := promptDefault("Git user email", env.GitConfig("user.email"))

	tracker := trackerSettings{issueTracker: backendName, forgejoBaseURL: forgejoBaseURL}
	desc, ok := backend.ByName(backendName)
	if !ok {
		return fmt.Errorf("unregistered backend %q", backendName)
	}

	strategy, ok := backendkit.ByName(backendName)
	if !ok || strategy.AcquireToken == nil {
		return fmt.Errorf("no token acquirer registered for backend %q", backendName)
	}
	knobs := map[string]string{}
	if forgejoBaseURL != "" {
		knobs["FORGEJO_BASE_URL"] = forgejoBaseURL
	}
	token, err := strategy.AcquireToken(
		backendkit.Shared{RepoSlug: repoSlug},
		func(name string) (string, bool) { v, ok := knobs[name]; return v, ok },
		backendkit.TokenPrompt{
			Out:          w,
			Getenv:       env.Getenv,
			Prompt:       prompt,
			PromptMasked: promptMasked,
			AmbientToken: env.GHAuthToken,
			TokenScopes:  env.TokenScopes,
			Probe: func(token string) error {
				it, _ := forgeBuilder(repoSlug, tracker, token)
				_, err := it.Probe()
				return err
			},
			RerunCmd:    quickstartRerunCmd,
			TokenEnvVar: desc.TokenEnvVar,
		})
	if err != nil {
		return err
	}

	rules, _ := driver.CredentialRules(driverName)
	credentials, err := gatherCredentials(env, rules, w, prompt, promptMasked, cmdRunner)
	if err != nil {
		return err
	}

	a := answers{
		repoSlug:     repoSlug,
		runtime:      runtime,
		gitUserName:  gitUserName,
		gitUserEmail: gitUserEmail,
		tracker:      tracker,
		token:        token,
		driver:       driverName,
		credentials:  credentials,
	}

	// Backup runs only here, after every prompt and abort point above has
	// succeeded, so an operator who aborts earlier never loses a file to a
	// rename with nothing written to replace it. A failed rename reverses
	// everything in backedUp, so the transcript lines come from a second pass
	// after the loop: printing inline would falsify a line a rollback undoes.
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
	for _, f := range render(a) {
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

	// The gh CLI reads auth from GH_TOKEN in the process environment. Keyed
	// off the descriptor rather than the backend name so a third backend's
	// credential is never exported under GitHub's env var name.
	if desc.TokenEnvVar == "GH_TOKEN" {
		if err := os.Setenv("GH_TOKEN", token); err != nil {
			return fmt.Errorf("set GH_TOKEN: %w", err)
		}
	}
	insideGitWorkTree := env.InsideGitWorkTree(dir)
	if insideGitWorkTree {
		fmt.Fprintln(w, gitAddReminder)
	}

	it, cf := forgeBuilder(repoSlug, tracker, a.token)
	tokenHint, slugHint, noLabelRegistry := doctorHints(tracker.issueTracker)
	// doctor.Config.Runtime below already reports runtime validity, so the
	// extraChecks rows must be runtime-stripped or the row reports twice.
	launcherRows := launcherchecks.WithoutRuntime(launcherchecks.All(quickstartCheckConfig(a, backendName), quickstartCheckDeps(a)))
	// Verbose: quickstart's finish line always reports in full, whatever
	// `spindrift doctor`'s own quiet default does (issue #3777).
	rep := doctor.NewReporter(w, true)
	if err := doctor.Run(it, cf, doctor.Config{
		IssueTracker:    tracker.issueTracker,
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
// Empty hints mean "use doctor.Run's github-shaped default"; an unknown
// tracker keeps the label check.
func doctorHints(issueTracker string) (tokenHint, slugHint string, noLabelRegistry bool) {
	row, ok := backend.ByName(issueTracker)
	if !ok {
		return "", "", false
	}
	return row.DoctorTokenHint, row.DoctorSlugHint, !row.LabelRegistry
}

// trackerSettings holds the fields buildForge needs to construct an Issue
// Tracker adapter (ADR 0013). The wizard sets issueTracker to only "github"
// or "forgejo" (issue #1559); the jira and local fields exist for tests.
type trackerSettings struct {
	issueTracker   string
	jiraBaseURL    string
	jiraProjectKey string
	jiraEmail      string
	localIssuesDir string
	// forgejoBaseURL is read only when issueTracker is "forgejo"; empty falls
	// back to the adapter's own codeberg.org default.
	forgejoBaseURL string
}

// answers holds every operator decision, detected defaults folded in, so
// render can produce the scaffold without touching any I/O seam.
type answers struct {
	repoSlug     string
	runtime      string
	gitUserName  string
	gitUserEmail string
	tracker      trackerSettings
	token        string
	driver       string
	credentials  []credential
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
func render(a answers) []scaffoldFile {
	return []scaffoldFile{
		{path: "flake.nix", content: renderFlakeNix(a.repoSlug, a.runtime, a.gitUserName, a.gitUserEmail, a.tracker), mode: 0o644},
		{path: "harness.env", content: renderHarnessEnv(a.tracker.issueTracker, a.token, a.credentials), mode: 0o600},
		{path: ".gitignore", content: quickstartGitignore, mode: 0o644},
		{path: ".envrc", content: quickstartEnvrc, mode: 0o644},
	}
}

// renderFlakeNix generates a minimal Consumer flake.nix carrying only the
// options the wizard collected (ADR 0027). No prompts/ directory is
// scaffolded, because the harness defaults every prompt.
func renderFlakeNix(repoSlug, runtime, gitUserName, gitUserEmail string, tracker trackerSettings) string {
	trackerLine := fmt.Sprintf("            %s = \"%s\";\n", pathIssueTracker, nixEscape(tracker.issueTracker))

	settingsLines := trackerLine
	if tracker.issueTracker == "forgejo" {
		// forgejo drives both axes, ISSUE_TRACKER and CODE_FORGE, so the
		// generated flake lands code on the same instance doctor validated.
		settingsLines += fmt.Sprintf("            %s = \"forgejo\";\n", pathCodeForge)
		if tracker.forgejoBaseURL != "" && tracker.forgejoBaseURL != codebergBaseURL {
			settingsLines += fmt.Sprintf("            %s = \"%s\";\n", quickstartKnobs["FORGEJO_BASE_URL"].path, nixEscape(tracker.forgejoBaseURL))
		}
	}

	return fmt.Sprintf(`{
  description = "A spindrift consumer — headless coding agents, one disposable container per issue";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-parts.url = "github:hercules-ci/flake-parts";
    spindrift.url = "github:jordansmall/spindrift";
  };

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

      imports = [ spindrift.flakeModules.default ];

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
`, pathRuntime, nixEscape(runtime),
		pathRepoSlug, nixEscape(repoSlug),
		pathGitUserName, nixEscape(gitUserName),
		pathGitUserEmail, nixEscape(gitUserEmail),
		settingsLines)
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

// renderHarnessEnv writes only the secrets the wizard collected: the
// code-forge credential under the backend's TokenEnvVar, and the Driver
// credentials gatherCredentials collected. The generated comments mirror
// lib/renderers.nix's renderHarnessEnvExample, the source of that wording.
func renderHarnessEnv(issueTracker, token string, credentials []credential) string {
	out := harnessEnvPreamble
	out += harnessEnvSecretLine(harnessEnvTokenEnvVar(issueTracker), token)
	for _, c := range credentials {
		out += harnessEnvSecretLine(c.env, c.value)
	}
	return out
}

// harnessEnvTokenEnvVar returns the env var name the wizard writes the
// backend credential under, falling back to GH_TOKEN. Keep it the only source
// of that name: renderHarnessEnv and quickstartCheckConfig both read it, and
// a second copy would let the scaffold and the doctor row drift apart.
func harnessEnvTokenEnvVar(issueTracker string) string {
	if desc, ok := backend.ByName(issueTracker); ok && desc.TokenEnvVar != "" {
		return desc.TokenEnvVar
	}
	return "GH_TOKEN"
}

// harnessEnvPreamble condenses templates/default/harness.env.example's
// preamble. Per the resolution precedence (docs/reference.md), both a
// secret's own <NAME>_CMD and the plaintext value below outrank SECRET_CMD,
// so that fallback applies only once the operator removes the plaintext.
const harnessEnvPreamble = "" +
	"# Preferred: source each secret below from a vault via its <NAME>_CMD\n" +
	"# form rather than the plaintext value — harness.env then holds\n" +
	"# fetch recipes, not live credentials.\n" +
	"# One vault, uniform naming? SECRET_CMD sets a single templated fetch\n" +
	"# command (e.g. \"rbw get spindrift-{name}\") for any secret without a\n" +
	"# <NAME>_CMD, but note the plaintext value below still wins over it\n" +
	"# too, so remove that value (or add <NAME>_CMD) for SECRET_CMD to apply.\n\n"

// harnessEnvSecretLine renders one secret's stanza: the <NAME>_CMD
// indirection comment matching templates/default/harness.env.example, then
// the bare NAME=value line.
func harnessEnvSecretLine(name, value string) string {
	return fmt.Sprintf(
		"# Preferred: fetch this from a vault instead of the plaintext value below —\n"+
			"# %s_CMD=\"rbw get spindrift-%s\" (or an op/pass/vault\n"+
			"# read); the command's stdout wins over %s and is never baked,\n"+
			"# logged, or written to disk.\n"+
			"%s=%s\n\n",
		name, toKebab(name), name, name, value,
	)
}

// toKebab derives the "spindrift-<kebab-name>" vault key suffix from an env
// var name, so GH_TOKEN becomes gh-token. Two other copies of this transform
// share the name: flags.go's toKebab and lib/renderers.nix's toKebab.
func toKebab(name string) string {
	return strings.ReplaceAll(strings.ToLower(name), "_", "-")
}

// gatherCredentials walks the Driver's credential rules in order. A rule an
// ambient variable already satisfies is reused; otherwise its setup command
// is offered, and if declined each remaining variable is prompted for in turn
// until one is non-empty. When none is, the last variable is written blank so
// harness.env still shows where the credential goes. A rule with no setup
// command only prints its Remedy. Quickstart configures no models, so a
// model-gated rule is mentioned rather than demanded.
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
		if strings.ToLower(strings.TrimSpace(prompt(fmt.Sprintf("No ambient credential found (%s). Run `%s` now? [y/N]", strings.Join(rule.Env, "/"), cmdLine)))) == "y" {
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
			v := promptMasked(fmt.Sprintf("Value for %s", name))
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

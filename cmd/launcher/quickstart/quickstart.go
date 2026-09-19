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
	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgejo"
	"spindrift.dev/launcher/internal/forge/github"
	"spindrift.dev/launcher/internal/forge/jira"
	"spindrift.dev/launcher/internal/forge/local"
	"spindrift.dev/launcher/internal/forge/rest"
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

// tokenAcquireContext bundles everything a TokenAcquirer needs. GitHub audits
// a token's prefix and scopes locally while Forgejo needs a live Probe
// against a constructed IssueTracker, so this can't collapse to one field.
type tokenAcquireContext struct {
	env          Environment
	w            io.Writer
	promptMasked func(string) string
	prompt       func(string) string
	forgeBuilder ForgeBuilder
	repoSlug     string
	baseURL      string
	desc         backend.Descriptor
}

// TokenAcquirer prompts for (or reuses an ambient) bearer token for one
// backend and validates it however that backend requires.
type TokenAcquirer func(ctx tokenAcquireContext) (string, error)

// tokenAcquirers dispatches token acquisition by backend name, so a new
// backend registers an entry here rather than adding a branch to
// runQuickstart. Unsynchronized, which holds only because quickstart is a
// single-threaded CLI that never mutates it after init.
var tokenAcquirers = map[string]TokenAcquirer{
	"github": func(ctx tokenAcquireContext) (string, error) {
		token, err := acquireGHToken(ctx.env, ctx.w, ctx.promptMasked, ctx.desc.TokenEnvVar)
		if err != nil {
			return "", err
		}
		if err := auditGHToken(token, ctx.env, ctx.w, ctx.prompt); err != nil {
			return "", err
		}
		return token, nil
	},
	"forgejo": func(ctx tokenAcquireContext) (string, error) {
		return acquireForgejoToken(ctx.w, ctx.promptMasked, ctx.forgeBuilder, ctx.repoSlug, ctx.baseURL)
	},
}

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

	// backendName comes from the git remote host, never a direct prompt, and
	// there are no Jira or local sub-prompts. Those adapters stay in place
	// for an operator who hand-edits the generated flake.
	tracker := trackerSettings{issueTracker: backendName, forgejoBaseURL: forgejoBaseURL}

	desc, ok := backend.ByName(backendName)
	if !ok {
		return fmt.Errorf("unregistered backend %q", backendName)
	}

	acquirer, ok := tokenAcquirers[backendName]
	if !ok {
		return fmt.Errorf("no token acquirer registered for backend %q", backendName)
	}
	token, err := acquirer(tokenAcquireContext{
		env:          env,
		w:            w,
		promptMasked: promptMasked,
		prompt:       prompt,
		forgeBuilder: forgeBuilder,
		repoSlug:     repoSlug,
		baseURL:      forgejoBaseURL,
		desc:         desc,
	})
	if err != nil {
		return err
	}

	claudeOAuthToken := ""
	anthropicAPIKey := ""
	if v, ok := env.LookupEnv("CLAUDE_CODE_OAUTH_TOKEN"); ok && v != "" {
		claudeOAuthToken = v
		fmt.Fprintln(w, "reusing ambient CLAUDE_CODE_OAUTH_TOKEN")
	} else if v, ok := env.LookupEnv("ANTHROPIC_API_KEY"); ok && v != "" {
		anthropicAPIKey = v
		fmt.Fprintln(w, "reusing ambient ANTHROPIC_API_KEY")
	} else if strings.ToLower(strings.TrimSpace(prompt("No ambient Claude credential found. Run `claude setup-token` now (browser OAuth)? [y/N]"))) == "y" {
		if err := cmdRunner.Run("claude", "setup-token"); err != nil {
			return fmt.Errorf("run claude setup-token: %w", err)
		}
		claudeOAuthToken = promptMasked("Paste the CLAUDE_CODE_OAUTH_TOKEN printed by claude setup-token")
		if claudeOAuthToken == "" {
			return fmt.Errorf("claude setup-token: no token pasted")
		}
	} else {
		anthropicAPIKey = promptMasked("Anthropic API key (ANTHROPIC_API_KEY)")
	}

	a := answers{
		repoSlug:         repoSlug,
		runtime:          runtime,
		gitUserName:      gitUserName,
		gitUserEmail:     gitUserEmail,
		tracker:          tracker,
		token:            token,
		claudeOAuthToken: claudeOAuthToken,
		anthropicAPIKey:  anthropicAPIKey,
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
	tokenHint, slugHint := doctorHints(tracker.issueTracker)
	// doctor.Config.Runtime below already reports runtime validity, so the
	// extraChecks rows must be runtime-stripped or the row reports twice.
	launcherRows := launcherchecks.WithoutRuntime(launcherchecks.All(quickstartCheckConfig(a, backendName), quickstartCheckDeps(a)))
	if err := doctor.Run(it, cf, doctor.Config{
		IssueTracker:    tracker.issueTracker,
		TokenHint:       tokenHint,
		SlugHint:        slugHint,
		Label:           defaultDispatchLabels.Dispatchable,
		InProgressLabel: defaultDispatchLabels.InProgress,
		FailedLabel:     defaultDispatchLabels.Failed,
		CompleteLabel:   defaultDispatchLabels.Complete,
		Runtime:         runtime,
		MergePolicy:     defaultMergePolicy,
		BaseBranch:      defaultBaseBranch,
	}, w, scanner, interactive, launcherRows); err != nil {
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

// acquireGHToken reuses an ambient GH_TOKEN without prompting, otherwise it
// guides the operator toward a fine-grained single-repo PAT. The `gh auth
// token` fallback warns, because that token is typically repo-wide.
func acquireGHToken(env Environment, w io.Writer, promptMasked func(string) string, tokenEnvVar string) (string, error) {
	if token := env.Getenv(tokenEnvVar); token != "" {
		return token, nil
	}
	fmt.Fprintf(w, "No ambient %s found.\n", tokenEnvVar)
	fmt.Fprint(w, "Create a fine-grained personal access token scoped to only this repo, with:\n"+requiredGHPermissions)
	token := promptMasked("GitHub token (paste a fine-grained PAT, or leave blank to fall back to `gh auth token` — broader scope warning)")
	if token != "" {
		return token, nil
	}
	fmt.Fprintln(w, "WARNING: gh auth token typically returns a repo-wide OAuth token, broader than the single-repo scope quickstart recommends.")
	token, err := env.GHAuthToken()
	if err != nil {
		return "", err
	}
	if token == "" {
		return "", fmt.Errorf("gh auth token returned no token — run `gh auth login` and retry")
	}
	return token, nil
}

// acquireForgejoToken validates the pasted token with a live Probe rather
// than a prefix audit: Forgejo PATs have no sniffable prefix the way GitHub's
// do. There is no retry loop, so every failure path here aborts the run and
// tells the operator to rerun the wizard.
func acquireForgejoToken(w io.Writer, promptMasked func(string) string, forgeBuilder ForgeBuilder, repoSlug, baseURL string) (string, error) {
	token := promptMasked("Forgejo token (paste a Forgejo personal access token)")
	if token == "" {
		return "", fmt.Errorf("no Forgejo token provided — rerun `%s` and paste a token when prompted", quickstartRerunCmd)
	}
	it, _ := forgeBuilder(repoSlug, trackerSettings{issueTracker: "forgejo", forgejoBaseURL: baseURL}, token)
	if _, err := it.Probe(); err != nil {
		if errors.Is(err, forge.ErrAuthFailure) {
			return "", fmt.Errorf("Forgejo token rejected by the API at %s — the instance is derived from the git remote, so if this is the wrong instance, `git remote set-url` to point at the right one before you rerun `%s` and paste a valid token: %w", baseURL, quickstartRerunCmd, err)
		}
		if !errors.Is(err, forge.ErrNotFound) {
			var statusErr rest.StatusError
			if errors.As(err, &statusErr) {
				cause := fmt.Sprintf("the instance responded with HTTP status %d", statusErr.Status)
				return "", forgejoConnectivityError(baseURL, repoSlug, cause, "check the Forgejo instance's health/logs", err)
			}
			var decodeErr rest.DecodeError
			if errors.As(err, &decodeErr) {
				return "", forgejoConnectivityError(baseURL, repoSlug, "the instance's response could not be parsed", "check the Forgejo instance's version/API compatibility", err)
			}
		}
		return "", forgejoConnectivityError(baseURL, repoSlug, "either the instance is unreachable or the repo slug is wrong", "check both", err)
	}
	fmt.Fprintln(w, "ok: Forgejo token validated via live API ping")
	return token, nil
}

// forgejoConnectivityError builds the shared error skeleton for
// acquireForgejoToken's three post-Probe failure branches, which differ only
// in the cause and remedy clauses.
func forgejoConnectivityError(baseURL, repoSlug, cause, remedy string, err error) error {
	return fmt.Errorf("Forgejo connectivity check to %s (repo slug %q) failed — %s; %s, then rerun `%s`: %w", baseURL, repoSlug, cause, remedy, quickstartRerunCmd, err)
}

// requiredGHPermissions are the permissions a token must carry on the single
// target repo. GitHub exposes no endpoint to introspect a fine-grained PAT,
// so quickstart prints these for the operator to check (ADR 0027).
const requiredGHPermissions = `  Issues: Read and write
  Contents: Read and write
  Pull requests: Read and write
  Metadata: Read
`

// auditGHToken checks a GitHub token for least privilege, branching on its
// prefix. A fine-grained PAT cannot be introspected, so it passes ungated.
func auditGHToken(token string, env Environment, w io.Writer, prompt func(string) string) error {
	if strings.HasPrefix(token, "github_pat_") {
		fmt.Fprintln(w, "fine-grained PAT detected — GitHub exposes no endpoint to introspect it.")
		fmt.Fprint(w, "It should carry only these permissions, on the single target repo:\n"+requiredGHPermissions)
		return nil
	}
	if strings.HasPrefix(token, "ghp_") || strings.HasPrefix(token, "gho_") {
		scopes, err := env.TokenScopes(token)
		if err != nil {
			return fmt.Errorf("read token scopes: %w", err)
		}
		fmt.Fprintf(w, "token scopes: %s\n", strings.Join(scopes, ", "))
		excess := excessGHScopes(scopes)
		if len(excess) == 0 {
			fmt.Fprintln(w, "ok: scopes are least-privilege")
			return nil
		}
		fmt.Fprintf(w, "WARNING: token grants broader-than-needed scope(s): %s\n", strings.Join(excess, ", "))
		fmt.Fprintln(w, "quickstart only needs single-repo Issues/Contents/Pull requests RW + Metadata R — mint a fine-grained PAT instead for least privilege.")
		answer := prompt("Type ACCEPT to continue with this over-broad token, anything else aborts")
		if answer != "ACCEPT" {
			return fmt.Errorf("aborted: GitHub token grants broader access than needed (%s) — mint a fine-grained single-repo PAT instead", strings.Join(excess, ", "))
		}
		return nil
	}
	// Any other prefix (ghs_ app-installation tokens, for one) is neither a
	// fine-grained PAT nor a classic token, so there is nothing to audit.
	return nil
}

// broadGHScopes are classic scopes wider than the single-repo least privilege
// quickstart wants. excessGHScopes catches admin:* separately, by prefix.
var broadGHScopes = map[string]bool{
	"repo":      true,
	"write:org": true,
	"read:org":  true,
}

func excessGHScopes(scopes []string) []string {
	var excess []string
	for _, s := range scopes {
		if broadGHScopes[s] || strings.HasPrefix(s, "admin:") {
			excess = append(excess, s)
		}
	}
	return excess
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

// doctorHints resolves doctor.Config's TokenHint and SlugHint. Empty values
// mean "use doctor.Run's github-shaped default".
func doctorHints(issueTracker string) (tokenHint, slugHint string) {
	row, ok := backend.ByName(issueTracker)
	if !ok {
		return "", ""
	}
	return row.DoctorTokenHint, row.DoctorSlugHint
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
	repoSlug         string
	runtime          string
	gitUserName      string
	gitUserEmail     string
	tracker          trackerSettings
	token            string
	claudeOAuthToken string
	anthropicAPIKey  string
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
		{path: "harness.env", content: renderHarnessEnv(a.tracker.issueTracker, a.token, a.claudeOAuthToken, a.anthropicAPIKey), mode: 0o600},
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
			settingsLines += fmt.Sprintf("            %s = \"%s\";\n", pathForgejoBaseURL, nixEscape(tracker.forgejoBaseURL))
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
// code-forge credential under the backend's TokenEnvVar, and whichever Claude
// credential the operator chose, never both. The generated comments mirror
// lib/renderers.nix's renderHarnessEnvExample, the source of that wording.
func renderHarnessEnv(issueTracker, token, claudeOAuthToken, anthropicAPIKey string) string {
	out := harnessEnvPreamble
	out += harnessEnvSecretLine(harnessEnvTokenEnvVar(issueTracker), token)
	if claudeOAuthToken != "" {
		out += harnessEnvSecretLine("CLAUDE_CODE_OAUTH_TOKEN", claudeOAuthToken)
	} else {
		out += harnessEnvSecretLine("ANTHROPIC_API_KEY", anthropicAPIKey)
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

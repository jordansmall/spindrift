// Command quickstart is the pre-CLI Quickstart wizard (ADR 0027): `nix run
// github:jordansmall/spindrift#quickstart`. It runs before the `spindrift`
// binary exists — `runtime`/`driver` are flake.nix options it authors, not
// launcher env knobs — so it lives as its own binary under the launcher
// module rather than a `spindrift` subcommand.
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
	"spindrift.dev/launcher/internal/runner"
)

// Environment abstracts host detection (available container runtimes, git
// identity, ambient tokens, repoSlug guess) so runQuickstart is testable
// without touching the real host. Detection itself lands in a later ticket
// (ADR 0027); this seam exists now so runQuickstart's signature does not
// change when it does.
type Environment interface {
	LookPath(file string) (string, error)
	LookupEnv(key string) (string, bool)

	// Getenv returns the value of the named environment variable, or "" if
	// unset — used to detect an ambient GH_TOKEN so quickstart can reuse it
	// without prompting.
	Getenv(key string) string

	// TokenScopes reads the X-OAuth-Scopes header GitHub returns for a
	// classic or OAuth token (ghp_/gho_ prefix). Fine-grained PATs
	// (github_pat_) have no equivalent introspection endpoint, so this is
	// only ever called for the classic/OAuth audit branch.
	TokenScopes(token string) ([]string, error)

	// GHAuthToken returns the host gh CLI's own authenticated token (`gh
	// auth token`) — the fallback offered to an operator who declines to
	// paste a fine-grained PAT.
	GHAuthToken() (string, error)

	GitConfig(key string) string
	GitRemoteRepoSlug() string

	// GitRemoteURL returns the raw "origin" remote URL (git remote get-url
	// origin), or "" when there is no origin remote. Callers parse it with
	// parseRemoteHostSlug to detect a Forgejo/Codeberg host; the github-only
	// GitRemoteRepoSlug still seeds the repo-slug default.
	GitRemoteURL() string
}

// validateRepoSlug rejects anything but a single-slash "owner/name" shape —
// the form the generated flake.nix's forge.repoSlug expects.
func validateRepoSlug(slug string) error {
	parts := strings.Split(slug, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || strings.ContainsAny(slug, " \t\n\r") {
		return fmt.Errorf("expected owner/repo, got %q", slug)
	}
	return nil
}

// validateRuntimeChoice rejects any value outside runner.ValidValues.
func validateRuntimeChoice(runtime string) error {
	for _, v := range runner.ValidValues {
		if runtime == v {
			return nil
		}
	}
	return fmt.Errorf("expected one of %s, got %q", strings.Join(runner.ValidValues, ", "), runtime)
}

// quickstartBackendNames returns the operator-facing code-forge backend
// names the wizard accepts when the git remote host is neither github.com
// nor codeberg.org, derived from backend.QuickstartEligible() so a new
// backend registered in the shared registry automatically appears here with
// no quickstart-side edit.
func quickstartBackendNames() []string {
	descriptors := backend.QuickstartEligible()
	names := make([]string, len(descriptors))
	for i, d := range descriptors {
		names[i] = d.Name
	}
	return names
}

// validateBackendChoice rejects any value outside quickstartBackendNames().
func validateBackendChoice(b string) error {
	names := quickstartBackendNames()
	for _, v := range names {
		if b == v {
			return nil
		}
	}
	return fmt.Errorf("expected one of %s, got %q", strings.Join(names, ", "), b)
}

// CommandRunner abstracts the two subprocesses Quickstart shells out to
// (`claude setup-token`, `nix develop --command spindrift build`), so
// runQuickstart is testable without a real shell-out.
type CommandRunner interface {
	Run(name string, args ...string) error
}

// defaultBranchPrefix matches the launcher's own BRANCH_PREFIX default
// (flagtable_gen.go) — Quickstart doesn't prompt for it.
const defaultBranchPrefix = "agent/issue-"

// codebergBaseURL is the Forgejo adapter's default base URL — a
// forgejoBaseURL of exactly this value is the adapter default and must not
// be emitted as an explicit issues.forgejo.baseURL line in the generated
// flake.
const codebergBaseURL = "https://codeberg.org"

// quickstartRerunCmd is the command the wizard's own error hints point the
// operator at rerunning to re-answer a prompt — quoted once here so a reword
// of the command doesn't require hunting down every error message that
// mentions it.
const quickstartRerunCmd = "nix run github:jordansmall/spindrift#quickstart"

// defaultDispatchLabels are the four operator-visible triage labels
// Quickstart's generated flake relies on implicitly (the launcher's own
// LABEL/IN_PROGRESS_LABEL/FAILED_LABEL/COMPLETE_LABEL defaults) — the wizard
// never prompts for custom label names — plus the fixed, non-configurable
// Ambiguous label (#2275), which matters for the trackers this struct feeds
// (github.NewExecClient / local.NewLocalTracker) even though it's not one of
// the four the wizard prompts for or doctor.Config validates via this
// struct.
var defaultDispatchLabels = forge.DispatchLabels{
	Dispatchable: "ready-for-agent",
	InProgress:   "agent-in-progress",
	Complete:     "agent-complete",
	Failed:       "agent-failed",
	Ambiguous:    "agent-ambiguous-spec",
}

// spindriftBuildArgs is the subprocess the finish line shells out to build
// the Consumer's first image (ADR 0027) — shared with tests so the command
// can't drift out of sync between the call site and its assertions.
var spindriftBuildArgs = []string{"nix", "develop", "--command", "spindrift", "build"}

// ForgeBuilder constructs the real IssueTracker/CodeForge from the wizard's
// collected repoSlug, Issue Tracker settings, and the single backend token
// the wizard collected (whichever one is relevant to the chosen backend —
// only one is ever active per call), so the finish line's doctor validation
// (ADR 0027) runs in-process against the real forge — no `spindrift doctor`
// subprocess, since the `spindrift` binary doesn't exist yet at Quickstart's
// pre-CLI stage. Injected so tests substitute a forge.Fake instead of
// shelling out to gh/Jira for real.
type ForgeBuilder func(repoSlug string, tracker trackerSettings, token string) (forge.IssueTracker, forge.CodeForge)

// tokenAcquireContext bundles everything a TokenAcquirer needs — different
// backends validate their token completely differently (GitHub: local
// prefix/scope audit; Forgejo: live Probe call against a constructed
// IssueTracker), so this can't collapse to a single field lookup.
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
// registered backend, validating it however that backend requires, before
// quickstart embeds it in flake.nix/harness.env. Registered per backend name
// in tokenAcquirers below — mirrors the ForgeBuilder seam — so a new
// QuickstartEligible backend needs its own acquirer wired in there, but
// requires zero changes to runQuickstart itself.
type TokenAcquirer func(ctx tokenAcquireContext) (string, error)

// tokenAcquirers dispatches token acquisition by backend name. A new
// QuickstartEligible backend registers its own entry here (and in
// backend.Registry) — runQuickstart itself never branches on backend name to
// acquire a token. Package-global and unsynchronized — fine here since
// quickstart is a single-threaded CLI that never mutates it after init.
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

// forgejoProbeTimeout bounds the HTTP client the forgejo IssueTracker uses
// for the interactive token-validation ping (acquireForgejoToken's Probe
// call), so an unreachable or hung Forgejo host can't block the wizard
// forever. Mirrors defaultForgejoHTTPTimeout in the sibling Forgejo
// IssueTracker/CodeForge adapters.
const forgejoProbeTimeout = 30 * time.Second

// buildForge is the production ForgeBuilder. The Code Forge is github by
// default (ADR 0027: Quickstart never prompts for it) except for the
// forgejo case, which builds both the IssueTracker and CodeForge seams
// from the Forgejo REST adapters so doctor validates against a Forgejo
// instance instead. The Issue Tracker switches on tracker.issueTracker,
// which the wizard always sets to "github" (issue #1559) — the
// jira/local/forgejo cases exist for buildForge's own tests. token is the
// single backend credential relevant to tracker.issueTracker (empty for
// "github"/"local", where the credential either lives ambient in the process
// environment or isn't needed at all): github.NewExecClient shells out to
// the gh CLI, which reads GH_TOKEN from the process environment —
// runQuickstart exports the collected token before calling this.
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

// backupSuffixDigits is the zero-padded width of the numbered .bak.NNNNNN
// suffix quickstart uses when a backup name is already taken (issue #2563) —
// wide enough that lexical sort order (`ls`) still matches numeric order
// past any realistic number of forced reruns.
const backupSuffixDigits = 6

// runQuickstart drives the wizard end-to-end: it takes injected I/O, a
// target directory to write the scaffold into, and the Environment/
// CommandRunner/ForgeBuilder seams (mirrors runDoctor's testability).
// Interactive-only for v1: a non-TTY stdin (interactive == false) is a fatal
// error directing scripted setups to write flake.nix/harness.env directly
// instead.
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
	// promptValidated re-prompts on an invalid answer. If stdin runs out
	// (ok == false) while the value is still invalid, there is no more
	// input to retry with, so it errors out instead of spinning forever
	// re-reading the same exhausted scanner.
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
	host, remoteSlug := parseRemoteHostSlug(remoteURL)
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

	// Quickstart derives backendName from the git remote host (a codeberg.org
	// remote or an explicit backend answer above) rather than prompting for
	// it directly; no Jira/local sub-prompts either way. The Jira/local
	// adapters and runtime ISSUE_TRACKER validation stay in place for an
	// operator who hand-edits the generated flake.
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

	// Backup happens only now, right before the scaffold is actually
	// (re)written — every prompt/validation/abort point above (repo slug,
	// runtime choice + PATH confirmation, token acquisition, ...) has already
	// succeeded, so an operator who aborts an earlier prompt never sees an
	// existing file renamed away with nothing written to replace it.
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
				return fmt.Errorf("back up %s: %w", name, err)
			}
			bakPath = fmt.Sprintf("%s.bak.%0*d", path, backupSuffixDigits, n)
		}
		if err := os.Rename(path, bakPath); err != nil {
			_ = os.Remove(bakPath)
			return fmt.Errorf("back up %s: %w", name, err)
		}
		fmt.Fprintf(w, "backed up: %s -> %s\n", name, filepath.Base(bakPath))
	}

	for _, f := range render(a) {
		if err := os.WriteFile(filepath.Join(dir, f.path), []byte(f.content), f.mode); err != nil {
			return fmt.Errorf("write %s: %w", f.path, err)
		}
		fmt.Fprintf(w, "wrote: %s\n", f.path)
	}

	// The gh CLI (used by the github Code Forge, and by the github Issue
	// Tracker branch) reads auth from GH_TOKEN in the process environment.
	// Keyed off the acquired token's own descriptor rather than a
	// backend-name branch: only export GH_TOKEN when the acquired credential
	// IS a GH_TOKEN, so a third backend's credential is never exported under
	// GitHub's well-known env var name.
	if desc.TokenEnvVar == "GH_TOKEN" {
		if err := os.Setenv("GH_TOKEN", token); err != nil {
			return fmt.Errorf("set GH_TOKEN: %w", err)
		}
	}
	it, cf := forgeBuilder(repoSlug, tracker, a.token)
	tokenHint, slugHint := doctorHints(tracker.issueTracker)
	if err := doctor.Run(it, cf, doctor.Config{
		IssueTracker:    tracker.issueTracker,
		TokenHint:       tokenHint,
		SlugHint:        slugHint,
		Label:           defaultDispatchLabels.Dispatchable,
		InProgressLabel: defaultDispatchLabels.InProgress,
		FailedLabel:     defaultDispatchLabels.Failed,
		CompleteLabel:   defaultDispatchLabels.Complete,
		Runtime:         runtime,
	}, w, scanner, interactive); err != nil {
		return err
	}

	fmt.Fprintln(w, "==> the first image build can take a while — building now")
	if err := cmdRunner.Run(spindriftBuildArgs[0], spindriftBuildArgs[1:]...); err != nil {
		return fmt.Errorf("spindrift build: %w", err)
	}

	fmt.Fprintln(w, "\nQuickstart complete. Wrote:")
	fmt.Fprintln(w, "  flake.nix")
	fmt.Fprintln(w, "  harness.env")
	fmt.Fprintln(w, "  .gitignore")
	fmt.Fprintln(w, "  .envrc")
	fmt.Fprintln(w, "\nNext: run `spindrift dispatch`.")

	return nil
}

// acquireGHToken reuses an ambient GH_TOKEN without prompting; otherwise it
// guides the operator toward minting a fine-grained single-repo PAT, with a
// `gh auth token` fallback for an operator in a hurry (labeled with a
// broad-scope warning, since the gh CLI's own OAuth token is typically
// repo-wide).
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

// acquireForgejoToken prompts for a Forgejo personal access token and
// validates it with a live API ping (Probe) rather than a prefix audit —
// Forgejo PATs have no sniffable prefix the way GitHub's do (ghp_/gho_/
// github_pat_), so there is nothing to inspect locally. The wizard prompts
// exactly once and has no retry loop, so any failure here aborts the whole
// run; remediation on every failure path is to rerun the wizard and
// re-answer the prompt, not to set an env var (this path never reads one).
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
		return "", fmt.Errorf("Forgejo connectivity check failed for %s (repo slug %q) — either the instance is unreachable or the repo slug is wrong; check both, then rerun `%s`: %w", baseURL, repoSlug, quickstartRerunCmd, err)
	}
	fmt.Fprintln(w, "ok: Forgejo token validated via live API ping")
	return token, nil
}

// requiredGHPermissions are the four permissions a token must carry on the
// single target repo — printed for a fine-grained PAT (github_pat_ prefix),
// which GitHub exposes no endpoint to introspect (ADR 0027).
const requiredGHPermissions = `  Issues: Read and write
  Contents: Read and write
  Pull requests: Read and write
  Metadata: Read
`

// auditGHToken checks a GitHub token for least privilege, asymmetrically by
// token prefix: a fine-grained PAT (github_pat_) cannot be introspected, so
// its required permissions are printed for the operator to double-check and
// it is accepted without a gate.
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
	// Any other prefix (e.g. ghs_ app-installation tokens) is neither a
	// fine-grained PAT nor a classic/OAuth token, so there is nothing to
	// audit — accept as-is.
	return nil
}

// broadGHScopes are classic/OAuth scopes that grant access wider than the
// single-repo least privilege quickstart wants: repo-wide (not just the one
// target repo) or org level. Any admin:* scope is caught separately by the
// prefix check in excessGHScopes.
var broadGHScopes = map[string]bool{
	"repo":      true,
	"write:org": true,
	"read:org":  true,
}

// excessGHScopes returns the scopes from a classic/OAuth token's grant that
// exceed what quickstart needs, in the caller's order.
func excessGHScopes(scopes []string) []string {
	var excess []string
	for _, s := range scopes {
		if broadGHScopes[s] || strings.HasPrefix(s, "admin:") {
			excess = append(excess, s)
		}
	}
	return excess
}

// quickstartGitignore protects the secrets-only harness.env file quickstart
// writes, plus the usual nix build/log noise (templates/default/.gitignore
// is the fuller reference; this is the minimal subset the github/happy path
// needs).
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

# OS
.DS_Store
`

const quickstartEnvrc = "use flake\n"

// doctorHints resolves doctor.Config's TokenHint/SlugHint for the wizard's
// own doctor.Run call, via backend.ByName. An unregistered issueTracker name
// (or a registered one with no hints, e.g. "github") returns empty values,
// meaning "use doctor.Run's github-shaped default".
func doctorHints(issueTracker string) (tokenHint, slugHint string) {
	row, ok := backend.ByName(issueTracker)
	if !ok {
		return "", ""
	}
	return row.DoctorTokenHint, row.DoctorSlugHint
}

// trackerSettings holds the fields buildForge needs to construct an Issue
// Tracker adapter (ADR 0013): github needs none beyond repoSlug, jira adds
// its base URL/project key/optional email, local adds an issues directory.
// The wizard only ever populates issueTracker: "github" (issue #1559) — the
// jira/local fields exist for buildForge's own adapter-construction tests
// (forge_test.go), not any wizard-driven path.
type trackerSettings struct {
	issueTracker   string
	jiraBaseURL    string
	jiraProjectKey string
	jiraEmail      string
	localIssuesDir string
	// forgejoBaseURL is the Forgejo/Gitea instance base URL; empty falls
	// back to the adapter's default (codeberg.org). Only consulted when
	// issueTracker == "forgejo".
	forgejoBaseURL string
}

// answers holds every operator decision the prompt/detect phase gathers —
// one field per decision, detected defaults already folded in — so render
// can turn it into the generated scaffold without touching Environment,
// stdin, or any other I/O seam.
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

// scaffoldFile is one generated scaffold file: its path relative to the
// target directory, its content, and the mode runQuickstart should write it
// with.
type scaffoldFile struct {
	path    string
	content string
	mode    os.FileMode
}

// render turns answers into the full generated scaffold — flake.nix,
// harness.env, .gitignore, .envrc — with no I/O of its own: every operator
// string crosses the Nix-escaping seam (nixEscape, via renderFlakeNix)
// inside this call, before runQuickstart writes the result to disk.
func render(a answers) []scaffoldFile {
	return []scaffoldFile{
		{path: "flake.nix", content: renderFlakeNix(a.repoSlug, a.runtime, a.gitUserName, a.gitUserEmail, a.tracker), mode: 0o644},
		{path: "harness.env", content: renderHarnessEnv(a.tracker.issueTracker, a.token, a.claudeOAuthToken, a.anthropicAPIKey), mode: 0o600},
		{path: ".gitignore", content: quickstartGitignore, mode: 0o644},
		{path: ".envrc", content: quickstartEnvrc, mode: 0o644},
	}
}

// renderFlakeNix generates a minimal Consumer flake.nix carrying only the
// options the wizard collected, with a comment pointing at the full
// reference (docs/flake-options.md) for everything else (ADR 0027). No
// prompts/ directory is scaffolded — the harness defaults every prompt.
func renderFlakeNix(repoSlug, runtime, gitUserName, gitUserEmail string, tracker trackerSettings) string {
	trackerLine := fmt.Sprintf("            %s = \"%s\";\n", pathIssueTracker, nixEscape(tracker.issueTracker))

	settingsLines := trackerLine
	if tracker.issueTracker == "forgejo" {
		// forgejo drives both axes: ISSUE_TRACKER=forgejo (the tracker line
		// above) and CODE_FORGE=forgejo, so the generated flake lands code on
		// the same instance doctor validated. Emitted in the current
		// domain-tree spelling (forge.backend / issues.forgejo.baseURL), the
		// same one templates/default/flake.nix documents.
		settingsLines += fmt.Sprintf("            %s = \"forgejo\";\n", pathCodeForge)
		if tracker.forgejoBaseURL != "" && tracker.forgejoBaseURL != codebergBaseURL {
			settingsLines += fmt.Sprintf("            %s = \"%s\";\n", pathForgejoBaseURL, nixEscape(tracker.forgejoBaseURL))
		}
	}

	return fmt.Sprintf(`{
  description = "A spindrift consumer — headless Claude Code agents in nix-built, disposable containers, one per GitHub issue";

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

// nixEscape escapes a string for embedding in a Nix double-quoted string
// literal: backslash and the quote terminate the literal, and "${" opens
// interpolation — each needs a backslash. Go's %q is not a substitute: it
// escapes the quote but not "${", so an operator-supplied value like
// "${evil}" would splice live Nix interpolation into the generated flake.
func nixEscape(s string) string {
	r := strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
		"${", `\${`,
	)
	return r.Replace(s)
}

// renderHarnessEnv writes only the secrets the wizard actually collected:
// the code-forge credential — under the registered backend's TokenEnvVar
// (e.g. FORGEJO_TOKEN for forgejo, GH_TOKEN for github), falling back to
// GH_TOKEN for an unregistered/tokenless issueTracker — and whichever Claude
// credential the operator chose (OAuth token or API key, never both).
func renderHarnessEnv(issueTracker, token, claudeOAuthToken, anthropicAPIKey string) string {
	tokenEnvVar := "GH_TOKEN"
	if desc, ok := backend.ByName(issueTracker); ok && desc.TokenEnvVar != "" {
		tokenEnvVar = desc.TokenEnvVar
	}
	out := fmt.Sprintf("%s=%s\n", tokenEnvVar, token)
	if claudeOAuthToken != "" {
		out += fmt.Sprintf("CLAUDE_CODE_OAUTH_TOKEN=%s\n", claudeOAuthToken)
	} else {
		out += fmt.Sprintf("ANTHROPIC_API_KEY=%s\n", anthropicAPIKey)
	}
	return out
}

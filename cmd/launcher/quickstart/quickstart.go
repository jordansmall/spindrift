// Command quickstart is the pre-CLI Quickstart wizard (ADR 0027): `nix run
// github:jordansmall/spindrift#quickstart`. It runs before the `spindrift`
// binary exists — `runtime`/`driver` are flake.nix options it authors, not
// launcher env knobs — so it lives as its own binary under the launcher
// module rather than a `spindrift` subcommand.
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/github"
	"spindrift.dev/launcher/internal/forge/jira"
	"spindrift.dev/launcher/internal/forge/local"
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
}

// runtimePrecedence is the order Quickstart probes for an available
// container runtime (ADR 0027): podman first, then docker, then nerdctl
// (Rancher Desktop's containerd mode) after docker — since Rancher Desktop
// in dockerd mode already surfaces as "docker" and only containerd mode
// exposes nerdctl — then the daemonless bwrap fallback.
var runtimePrecedence = []string{"podman", "docker", "nerdctl", "bwrap"}

// runtimeAlias maps a probed binary name to the operator-facing runtime
// value Quickstart offers/writes. Every binary is its own value except
// nerdctl, which reports as "rancher" — the same alias runtimeCLI (runner
// package) maps back to nerdctl when actually invoking the runtime.
func runtimeAlias(binary string) string {
	if binary == "nerdctl" {
		return "rancher"
	}
	return binary
}

// detectRuntime returns the first runtime in runtimePrecedence found on
// PATH (aliased via runtimeAlias), or an actionable error naming all four
// when none is available — Quickstart cannot proceed without one (ADR 0027).
func detectRuntime(env Environment) (string, error) {
	for _, rt := range runtimePrecedence {
		if _, err := env.LookPath(rt); err == nil {
			return runtimeAlias(rt), nil
		}
	}
	return "", fmt.Errorf("no supported container runtime found on PATH — install one of: podman, docker, bwrap, or nerdctl (Rancher Desktop containerd mode, offered as runtime = \"rancher\")")
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

// defaultDispatchLabels are the four triage labels Quickstart's generated
// flake relies on implicitly (the launcher's own LABEL/IN_PROGRESS_LABEL/
// FAILED_LABEL/COMPLETE_LABEL defaults) — the wizard never prompts for
// custom label names.
var defaultDispatchLabels = forge.DispatchLabels{
	Dispatchable: "ready-for-agent",
	InProgress:   "agent-in-progress",
	Complete:     "agent-complete",
	Failed:       "agent-failed",
}

// spindriftBuildArgs is the subprocess the finish line shells out to build
// the Consumer's first image (ADR 0027) — shared with tests so the command
// can't drift out of sync between the call site and its assertions.
var spindriftBuildArgs = []string{"nix", "develop", "--command", "spindrift", "build"}

// ForgeBuilder constructs the real IssueTracker/CodeForge from the wizard's
// collected repoSlug, GitHub token, and Issue Tracker settings, so the
// finish line's doctor validation (ADR 0027) runs in-process against the
// real forge — no `spindrift doctor` subprocess, since the `spindrift`
// binary doesn't exist yet at Quickstart's pre-CLI stage. Injected so tests
// substitute a forge.Fake instead of shelling out to gh/Jira for real.
type ForgeBuilder func(repoSlug string, tracker trackerSettings, ghToken, jiraToken string) (forge.IssueTracker, forge.CodeForge)

// buildForge is the production ForgeBuilder. The Code Forge is always
// github (ADR 0027: Quickstart never prompts for it); the Issue Tracker
// varies by the operator's choice. github.NewExecClient shells out to the
// gh CLI, which reads GH_TOKEN from the process environment — runQuickstart
// exports the collected token before calling this.
func buildForge(repoSlug string, tracker trackerSettings, ghToken, jiraToken string) (forge.IssueTracker, forge.CodeForge) {
	cf := github.NewExecClient(repoSlug, defaultDispatchLabels, defaultBranchPrefix)
	switch tracker.issueTracker {
	case "jira":
		return jira.NewJiraClient(jira.JiraConfig{
			BaseURL:    tracker.jiraBaseURL,
			ProjectKey: tracker.jiraProjectKey,
			Email:      tracker.jiraEmail,
			Token:      jiraToken,
			Labels:     defaultDispatchLabels,
		}), cf
	case "local":
		return local.NewLocalTracker(tracker.localIssuesDir, defaultDispatchLabels), cf
	default:
		return cf, cf
	}
}

// runQuickstart drives the wizard end-to-end: it takes injected I/O, a
// target directory to write the scaffold into, and the Environment/
// CommandRunner/ForgeBuilder seams (mirrors runDoctor's testability).
// Interactive-only for v1: a non-TTY stdin (interactive == false) is a fatal
// error directing scripted setups to write flake.nix/harness.env directly
// instead.
func runQuickstart(dir string, env Environment, runner CommandRunner, forgeBuilder ForgeBuilder, w io.Writer, stdin io.Reader, interactive, force bool) error {
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
		return fmt.Errorf("refusing to overwrite existing %s — rerun with --force to back each up to *.bak and regenerate", clobbered)
	}

	detectedRuntime, err := detectRuntime(env)
	if err != nil {
		return err
	}

	for _, name := range clobbered {
		path := filepath.Join(dir, name)
		if err := os.Rename(path, path+".bak"); err != nil {
			return fmt.Errorf("back up %s: %w", name, err)
		}
		fmt.Fprintf(w, "backed up: %s -> %s.bak\n", name, name)
	}

	scanner := bufio.NewScanner(stdin)
	promptDefault := func(label, def string) string {
		if def != "" {
			fmt.Fprintf(w, "%s [%s]: ", label, def)
		} else {
			fmt.Fprintf(w, "%s: ", label)
		}
		scanner.Scan()
		if v := scanner.Text(); v != "" {
			return v
		}
		return def
	}
	prompt := func(label string) string { return promptDefault(label, "") }

	repoSlug := promptDefault("Repo slug (owner/repo)", env.GitRemoteRepoSlug())
	runtime := promptDefault("Runtime (podman/docker/rancher/bwrap)", detectedRuntime)
	gitUserName := promptDefault("Git user name", env.GitConfig("user.name"))
	gitUserEmail := promptDefault("Git user email", env.GitConfig("user.email"))

	tracker := trackerSettings{issueTracker: prompt("Issue Tracker [github/jira/local]")}
	switch tracker.issueTracker {
	case "jira":
		tracker.jiraBaseURL = prompt("Jira base URL")
		tracker.jiraProjectKey = prompt("Jira project key")
		tracker.jiraEmail = prompt("Jira account email (optional)")
	case "local":
		tracker.localIssuesDir = prompt("Local issues directory [.spindrift/issues]")
		if tracker.localIssuesDir == "" {
			tracker.localIssuesDir = ".spindrift/issues"
		}
	}

	ghToken, err := acquireGHToken(env, w, prompt)
	if err != nil {
		return err
	}
	if err := auditGHToken(ghToken, env, w, prompt); err != nil {
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
		if err := runner.Run("claude", "setup-token"); err != nil {
			return fmt.Errorf("run claude setup-token: %w", err)
		}
		claudeOAuthToken = prompt("Paste the CLAUDE_CODE_OAUTH_TOKEN printed by claude setup-token")
		if claudeOAuthToken == "" {
			return fmt.Errorf("claude setup-token: no token pasted")
		}
	} else {
		anthropicAPIKey = prompt("Anthropic API key (ANTHROPIC_API_KEY)")
	}
	jiraToken := ""
	if tracker.issueTracker == "jira" {
		jiraToken = prompt("Jira API token (JIRA_TOKEN)")
	}

	if err := os.WriteFile(filepath.Join(dir, "flake.nix"), []byte(renderFlakeNix(repoSlug, runtime, gitUserName, gitUserEmail, tracker)), 0o644); err != nil {
		return fmt.Errorf("write flake.nix: %w", err)
	}
	fmt.Fprintln(w, "wrote: flake.nix")

	if err := os.WriteFile(filepath.Join(dir, "harness.env"), []byte(renderHarnessEnv(ghToken, claudeOAuthToken, anthropicAPIKey, jiraToken)), 0o600); err != nil {
		return fmt.Errorf("write harness.env: %w", err)
	}
	fmt.Fprintln(w, "wrote: harness.env")

	gitignore := quickstartGitignore
	if tracker.issueTracker == "local" {
		gitignore += fmt.Sprintf("\n# private local issue files — never commit these\n%s/\n", strings.TrimSuffix(tracker.localIssuesDir, "/"))
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(gitignore), 0o644); err != nil {
		return fmt.Errorf("write .gitignore: %w", err)
	}
	fmt.Fprintln(w, "wrote: .gitignore")

	if err := os.WriteFile(filepath.Join(dir, ".envrc"), []byte(quickstartEnvrc), 0o644); err != nil {
		return fmt.Errorf("write .envrc: %w", err)
	}
	fmt.Fprintln(w, "wrote: .envrc")

	// The gh CLI (used by the github Code Forge, and by the github Issue
	// Tracker branch) reads auth from GH_TOKEN in the process environment.
	if err := os.Setenv("GH_TOKEN", ghToken); err != nil {
		return fmt.Errorf("set GH_TOKEN: %w", err)
	}
	it, cf := forgeBuilder(repoSlug, tracker, ghToken, jiraToken)
	if err := doctor.Run(it, cf, doctor.Config{
		IssueTracker:    tracker.issueTracker,
		Label:           defaultDispatchLabels.Dispatchable,
		InProgressLabel: defaultDispatchLabels.InProgress,
		FailedLabel:     defaultDispatchLabels.Failed,
		CompleteLabel:   defaultDispatchLabels.Complete,
	}, w, scanner, interactive); err != nil {
		return err
	}

	fmt.Fprintln(w, "==> the first image build can take a while — building now")
	if err := runner.Run(spindriftBuildArgs[0], spindriftBuildArgs[1:]...); err != nil {
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
func acquireGHToken(env Environment, w io.Writer, prompt func(string) string) (string, error) {
	if token := env.Getenv("GH_TOKEN"); token != "" {
		return token, nil
	}
	fmt.Fprintln(w, "No ambient GH_TOKEN found.")
	fmt.Fprint(w, "Create a fine-grained personal access token scoped to only this repo, with:\n"+requiredGHPermissions)
	token := prompt("GitHub token (paste a fine-grained PAT, or leave blank to fall back to `gh auth token` — broader scope warning)")
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

# per-run agent logs
logs/

# local config + secrets — never commit this
harness.env

# direnv
.direnv/

# OS
.DS_Store
`

const quickstartEnvrc = "use flake\n"

// trackerSettings holds the fields collected for whichever Issue Tracker the
// operator chose (ADR 0013): github needs none beyond repoSlug, jira adds
// its base URL/project key/optional email, local adds an issues directory.
type trackerSettings struct {
	issueTracker   string
	jiraBaseURL    string
	jiraProjectKey string
	jiraEmail      string
	localIssuesDir string
}

// renderFlakeNix generates a minimal Consumer flake.nix carrying only the
// options the wizard collected, with a comment pointing at the full
// reference (docs/flake-options.md) for everything else (ADR 0027). No
// prompts/ directory is scaffolded — the harness defaults every prompt.
func renderFlakeNix(repoSlug, runtime, gitUserName, gitUserEmail string, tracker trackerSettings) string {
	var trackerLines strings.Builder
	fmt.Fprintf(&trackerLines, "            settings.issueDiscovery.issueTracker = \"%s\";\n", nixEscape(tracker.issueTracker))
	switch tracker.issueTracker {
	case "jira":
		fmt.Fprintf(&trackerLines, "            settings.repository.jiraBaseURL = \"%s\";\n", nixEscape(tracker.jiraBaseURL))
		fmt.Fprintf(&trackerLines, "            settings.repository.jiraProjectKey = \"%s\";\n", nixEscape(tracker.jiraProjectKey))
		if tracker.jiraEmail != "" {
			fmt.Fprintf(&trackerLines, "            settings.repository.jiraEmail = \"%s\";\n", nixEscape(tracker.jiraEmail))
		}
	case "local":
		fmt.Fprintf(&trackerLines, "            settings.issueDiscovery.localIssuesDir = \"%s\";\n", nixEscape(tracker.localIssuesDir))
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
            runtime = "%s";
            settings.repository.repoSlug = "%s";
            settings.repository.gitUserName = "%s";
            settings.repository.gitUserEmail = "%s";
%s          };

          devShells.default = pkgs.mkShell {
            packages = [ config.packages.spindrift ];
          };
        };
    };
}
`, nixEscape(runtime), nixEscape(repoSlug), nixEscape(gitUserName), nixEscape(gitUserEmail), trackerLines.String())
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
// GH_TOKEN, whichever Claude credential the operator chose (OAuth token or
// API key, never both), and JIRA_TOKEN when the jira tracker was chosen.
func renderHarnessEnv(ghToken, claudeOAuthToken, anthropicAPIKey, jiraToken string) string {
	out := fmt.Sprintf("GH_TOKEN=%s\n", ghToken)
	if claudeOAuthToken != "" {
		out += fmt.Sprintf("CLAUDE_CODE_OAUTH_TOKEN=%s\n", claudeOAuthToken)
	} else {
		out += fmt.Sprintf("ANTHROPIC_API_KEY=%s\n", anthropicAPIKey)
	}
	if jiraToken != "" {
		out += fmt.Sprintf("JIRA_TOKEN=%s\n", jiraToken)
	}
	return out
}

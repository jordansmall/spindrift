// Package main: spindrift launcher — orchestrates open issues into disposable
// containers. Config is baked into env vars by the nix wrapper (goRunPreamble,
// goRunDefaultsPreamble, etc.); harness.env overrides those at runtime. The
// binary contains no baked store paths of its own beyond what nix injects.
package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/heartbeat"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/usage"
)

type config struct {
	// OCI image config (baked by nix wrapper; empty for bwrap)
	imageArchive    string
	imageTag        string
	imageDrv        string
	nixBuilderImage string
	nixVolume       string
	flakeImageAttr  string

	// bwrap agent closure paths (bwrap only)
	agentFiles    string
	agentEnv      string
	agentFilesDrv string // .drv path; used by `launcher build` to realize the closure
	agentEnvDrv   string // .drv path; used by `launcher build` to realize the closure
	bakedPrefetch string

	// Runtime: podman | docker | bwrap
	runtime string

	// image is the runtime image reference; defaults to imageTag
	image string

	// Run defaults (overrideable via env / harness.env)
	repoSlug        string
	label           string
	issueNumber     string
	baseBranch      string
	maxParallel     int
	branchPrefix    string
	inProgressLabel string
	failedLabel     string
	completeLabel   string
	maxJobs         int

	// issueTracker selects the IssueTracker adapter: "github" (default) or
	// "local". localIssuesDir is the local adapter's issue directory.
	issueTracker   string
	localIssuesDir string

	// Transient-exit retry knobs
	transientRetryMax    int
	transientBackoffSecs int
	holdJitterSecs       int

	// Dependency-wave knobs
	depsPollSecs int
	depsWaitSecs int

	// Merge gate polling knobs
	mergePollInterval int
	mergePollTimeout  int
	maxFixAttempts    int
	maxRebaseAttempts int

	// Secrets / identity
	ghToken          string
	claudeOAuthToken string
	anthropicAPIKey  string
	gitUserName      string
	gitUserEmail     string

	// Optional prompt override
	spindriftPromptDir string
	// Optional skills override
	spindriftSkillsDir string

	// Network egress restriction knobs
	podmanNetwork   string // optional --network value for podman run
	bwrapUnshareNet bool   // when true, adds --unshare-net to bwrap

	// OCI container resource / security caps
	pidsLimit   string // --pids-limit value; empty omits the flag
	memoryLimit string // --memory value; empty omits the flag

	// Space-separated list of env var names to forward into each Box container.
	// Set by the nix-rendered preamble from the schema's boxEnv=true entries so
	// the Go source never needs to enumerate them by hand.
	boxEnvVars string

	// mergeMode controls post-green behavior: "immediate" merges the PR,
	// "manual" leaves it open, "auto" enqueues GitHub's native auto-merge.
	mergeMode string

	// mergeGuardPaths is a comma-separated list of globs matched against every
	// changed path in the PR; a hit downgrades the merge to manual regardless
	// of mergeMode. Empty disables the guard.
	mergeGuardPaths string
}

type issue struct {
	number  string
	title   string
	fixPass int // 0 = initial run; >0 = fix-pass number (sets FIX_PASS env)
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// atoi parses a positive integer; zero and negatives fall back to def.
// Use this for values where zero would cause a bug (e.g. semaphore capacity).
func atoi(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return n
	}
	return def
}

// atoiNonneg parses a non-negative integer; negatives fall back to def.
// Use this for values where zero is valid (e.g. timeouts, poll intervals).
func atoiNonneg(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil && n >= 0 {
		return n
	}
	return def
}

func loadConfig() config {
	imageTag := getenv("IMAGE_TAG", "spindrift:latest")
	image := os.Getenv("IMAGE")
	if image == "" {
		image = imageTag
	}
	return config{
		imageArchive:    os.Getenv("IMAGE_ARCHIVE"),
		imageTag:        imageTag,
		imageDrv:        os.Getenv("IMAGE_DRV"),
		nixBuilderImage: os.Getenv("NIX_BUILDER_IMAGE"),
		nixVolume:       getenv("NIX_VOLUME", "spindrift-nix"),
		flakeImageAttr:  os.Getenv("FLAKE_IMAGE_ATTR"),
		agentFiles:      os.Getenv("AGENT_FILES"),
		agentEnv:        os.Getenv("AGENT_ENV"),
		agentFilesDrv:   os.Getenv("AGENT_FILES_DRV"),
		agentEnvDrv:     os.Getenv("AGENT_ENV_DRV"),
		bakedPrefetch:   os.Getenv("BAKED_PREFETCH"),
		runtime:         os.Getenv("RUNTIME"),
		image:           image,

		repoSlug:        os.Getenv("REPO_SLUG"),
		label:           getenv("LABEL", "ready-for-agent"),
		issueNumber:     os.Getenv("ISSUE_NUMBER"),
		baseBranch:      getenv("BASE_BRANCH", "main"),
		maxParallel:     atoi(getenv("MAX_PARALLEL", "3"), 3),
		branchPrefix:    getenv("BRANCH_PREFIX", "agent/issue-"),
		inProgressLabel: getenv("IN_PROGRESS_LABEL", "agent-in-progress"),
		failedLabel:     getenv("FAILED_LABEL", "agent-failed"),
		completeLabel:   getenv("COMPLETE_LABEL", "agent-complete"),
		maxJobs:         atoiNonneg(os.Getenv("MAX_JOBS"), 0),

		issueTracker:   getenv("ISSUE_TRACKER", "github"),
		localIssuesDir: getenv("LOCAL_ISSUES_DIR", ".spindrift/issues"),

		transientRetryMax:    atoi(getenv("TRANSIENT_RETRY_MAX", "3"), 3),
		transientBackoffSecs: atoi(getenv("TRANSIENT_BACKOFF_SECS", "30"), 30),
		holdJitterSecs:       atoiNonneg(getenv("HOLD_JITTER_SECS", "5"), 5),

		depsPollSecs: atoiNonneg(getenv("DEPS_POLL_SECS", "30"), 30),
		depsWaitSecs: atoiNonneg(getenv("DEPS_WAIT_SECS", "7200"), 7200),

		mergePollInterval: atoiNonneg(getenv("MERGE_POLL_INTERVAL", "30"), 30),
		mergePollTimeout:  atoiNonneg(getenv("MERGE_POLL_TIMEOUT", "1800"), 1800),
		maxFixAttempts:    atoiNonneg(getenv("MAX_FIX_ATTEMPTS", "3"), 3),
		maxRebaseAttempts: atoiNonneg(getenv("MAX_REBASE_ATTEMPTS", "3"), 3),

		ghToken:          os.Getenv("GH_TOKEN"),
		claudeOAuthToken: os.Getenv("CLAUDE_CODE_OAUTH_TOKEN"),
		anthropicAPIKey:  os.Getenv("ANTHROPIC_API_KEY"),
		gitUserName:      os.Getenv("GIT_USER_NAME"),
		gitUserEmail:     os.Getenv("GIT_USER_EMAIL"),

		spindriftPromptDir: os.Getenv("SPINDRIFT_PROMPT_DIR"),
		spindriftSkillsDir: os.Getenv("SPINDRIFT_SKILLS_DIR"),

		podmanNetwork:   os.Getenv("PODMAN_NETWORK"),
		bwrapUnshareNet: os.Getenv("BWRAP_UNSHARE_NET") != "",
		pidsLimit:       getenv("PIDS_LIMIT", "512"),
		memoryLimit:     getenv("MEMORY_LIMIT", "4g"),

		boxEnvVars: os.Getenv("BOX_ENV_VARS"),

		mergeMode:       getenv("MERGE_MODE", "manual"),
		mergeGuardPaths: getenv("MERGE_GUARD_PATHS", ".github/**,**/CLAUDE.md,**/AGENTS.md,.claude/**,.opencode/**"),
	}
}

func validate(c config) error {
	if c.repoSlug == "" {
		return fmt.Errorf("set REPO_SLUG=owner/repo (the target GitHub repository)")
	}
	if c.gitUserName == "" {
		return fmt.Errorf("set GIT_USER_NAME, or configure git user.name on the host")
	}
	if c.gitUserEmail == "" {
		return fmt.Errorf("set GIT_USER_EMAIL, or configure git user.email on the host")
	}
	if c.ghToken == "" {
		return fmt.Errorf("set GH_TOKEN (fine-grained PAT scoped to the single target repo: Issues RW, Contents RW, Pull requests RW, Metadata R)")
	}
	if c.claudeOAuthToken == "" && c.anthropicAPIKey == "" {
		return fmt.Errorf("set CLAUDE_CODE_OAUTH_TOKEN (run 'claude setup-token') or ANTHROPIC_API_KEY")
	}
	if c.runtime == "" {
		return fmt.Errorf("RUNTIME is not set")
	}
	if _, err := exec.LookPath(c.runtime); err != nil {
		return fmt.Errorf("%s not found on PATH.", c.runtime)
	}
	switch c.mergeMode {
	case "immediate", "auto", "manual":
		// valid
	default:
		return fmt.Errorf("MERGE_MODE=%q is not valid; must be immediate, auto, or manual", c.mergeMode)
	}
	switch c.issueTracker {
	case "github", "local":
		// valid
	default:
		return fmt.Errorf("ISSUE_TRACKER=%q is not valid; must be github or local", c.issueTracker)
	}
	return nil
}

// dispatchLabels builds the DispatchLabels mapping from loaded config.
func dispatchLabels(c config) forge.DispatchLabels {
	return forge.DispatchLabels{
		Dispatchable: c.label,
		InProgress:   c.inProgressLabel,
		Complete:     c.completeLabel,
		Failed:       c.failedLabel,
	}
}

// newIssueTracker returns the IssueTracker adapter selected by ISSUE_TRACKER
// (default "github").
func newIssueTracker(c config) forge.IssueTracker {
	if c.issueTracker == "local" {
		return forge.NewLocalTracker(c.localIssuesDir, dispatchLabels(c))
	}
	return forge.NewExecClient(c.repoSlug, dispatchLabels(c))
}

// newCodeForge returns the CodeForge adapter. CODE_FORGE backend selection
// isn't wired yet, so this always returns the gh-exec adapter.
func newCodeForge(c config) forge.CodeForge {
	return forge.NewExecClient(c.repoSlug, dispatchLabels(c))
}

// newForgeClient composes the configured IssueTracker and CodeForge (which
// vary independently per ADR 0013) into a single Client for call sites that
// need both axes together.
func newForgeClient(c config) forge.Client {
	return forge.NewClient(newIssueTracker(c), newCodeForge(c))
}

// newRunner constructs the runner adapter for the `run` subcommand.
func newRunner(c config, pwd string) runner.Runner {
	if c.runtime == "bwrap" {
		return runner.NewBwrap(c.agentFiles, c.agentEnv, c.bakedPrefetch, c.spindriftPromptDir, c.spindriftSkillsDir, c.bwrapUnshareNet)
	}
	return runner.NewOCI(c.runtime, c.image, c.imageArchive, c.imageDrv, c.imageTag,
		c.nixBuilderImage, c.nixVolume, c.flakeImageAttr, pwd, c.spindriftPromptDir, c.spindriftSkillsDir,
		c.podmanNetwork, c.pidsLimit, c.memoryLimit)
}

// newBuildRunner constructs the runner adapter for the `build` subcommand.
func newBuildRunner(c config, pwd string) runner.Runner {
	if c.runtime == "bwrap" {
		return runner.NewBwrapBuild(c.agentFilesDrv, c.agentEnvDrv)
	}
	return runner.NewOCI(c.runtime, c.image, c.imageArchive, c.imageDrv, c.imageTag,
		c.nixBuilderImage, c.nixVolume, c.flakeImageAttr, pwd, "", "",
		"", c.pidsLimit, c.memoryLimit)
}

// buildBoxEnv assembles the env map forwarded into a Box. It combines the
// schema boxEnv=true vars (read from the ambient env by name) with per-issue
// vars. The adapter is responsible for any runtime-specific additions (e.g.
// PREFETCH for bwrap, HOME/PATH for the sandbox).
func buildBoxEnv(c config, iss issue) map[string]string {
	env := make(map[string]string)
	for _, name := range strings.Fields(c.boxEnvVars) {
		env[name] = os.Getenv(name)
	}
	env["ISSUE_NUMBER"] = iss.number
	env["ISSUE_TITLE"] = iss.title
	if iss.fixPass > 0 {
		env["FIX_PASS"] = strconv.Itoa(iss.fixPass)
	}
	return env
}

// build realizes the sandbox image or store closures without running any agent.
func build() error {
	c := loadConfig()
	if c.runtime == "" {
		return fmt.Errorf("RUNTIME is not set")
	}
	pwd, err := os.Getwd()
	if err != nil {
		return err
	}
	return newBuildRunner(c, pwd).EnsureReady()
}

// transitionState is a best-effort dispatch-state transition that logs but
// does not propagate errors, matching the original behaviour.
func transitionState(fc forge.Client, num string, from, to forge.DispatchState) {
	if err := fc.TransitionState(num, from, to); err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: could not transition to state %d\n", num, to)
	}
}

// claimIssue marks an issue in-progress before dispatch. When discovery already
// runs off the in-progress label — the workflow claimed the issue in YAML
// before the launcher started — the transition would be a no-op, so it is
// skipped.
func claimIssue(c config, fc forge.Client, num string) {
	if c.label == c.inProgressLabel {
		return
	}
	transitionState(fc, num, forge.Dispatchable, forge.InProgress)
}

// runOne dispatches one issue into a container and logs its output.
func runOne(c config, pwd string, r runner.Runner, iss issue) error {
	logPath := filepath.Join(pwd, "logs", "issue-"+iss.number+".log")
	fmt.Printf("    -> #%s: %s\n", iss.number, iss.title)

	logFile, err := os.Create(logPath)
	if err != nil {
		return fmt.Errorf("create log: %w", err)
	}
	defer logFile.Close()

	box := runner.Box{
		Issue:  iss.number,
		Name:   "agent-issue-" + iss.number,
		Env:    buildBoxEnv(c, iss),
		Output: heartbeat.New(logFile, iss.number, os.Stdout),
	}
	return r.Run(box)
}

// runFix dispatches a fix box for issue iss, writing output to a per-attempt
// log file. The fix box receives FIX_PASS=fixPass so the entrypoint can
// distinguish fix runs and check out the existing branch rather than creating a
// new one.
func runFix(c config, pwd string, r runner.Runner, iss issue, fixPass int) error {
	logPath := filepath.Join(pwd, "logs", fmt.Sprintf("issue-%s-fix-%d.log", iss.number, fixPass))
	fmt.Printf("    -> #%s (fix-pass-%d): %s\n", iss.number, fixPass, iss.title)

	logFile, err := os.Create(logPath)
	if err != nil {
		return fmt.Errorf("create fix log: %w", err)
	}
	defer logFile.Close()

	fixIss := issue{number: iss.number, title: iss.title, fixPass: fixPass}
	box := runner.Box{
		Issue:  fixIss.number,
		Name:   "agent-issue-" + fixIss.number,
		Env:    buildBoxEnv(c, fixIss),
		Output: heartbeat.New(logFile, fixIss.number, os.Stdout),
	}
	return r.Run(box)
}

// runConflictResolve dispatches a conflict-resolution box for issue iss.
// The box receives CONFLICT_RESOLVE_PR_URL so the entrypoint enters
// conflict-resolve mode: it resolves the rebase conflict, pushes the branch,
// and exits without running the main agent prompt.
func runConflictResolve(c config, pwd string, r runner.Runner, iss issue, pr string) error {
	logPath := filepath.Join(pwd, "logs", fmt.Sprintf("issue-%s-conflict-resolve.log", iss.number))
	fmt.Printf("    -> #%s (conflict-resolve): %s\n", iss.number, iss.title)

	logFile, err := os.Create(logPath)
	if err != nil {
		return fmt.Errorf("create conflict-resolve log: %w", err)
	}
	defer logFile.Close()

	env := buildBoxEnv(c, iss)
	env["CONFLICT_RESOLVE_PR_URL"] = pr
	box := runner.Box{
		Issue:  iss.number,
		Name:   "agent-issue-" + iss.number,
		Env:    env,
		Output: heartbeat.New(logFile, iss.number, os.Stdout),
	}
	return r.Run(box)
}

// gateToGreen polls CheckState on the PR's head commit until the state
// reaches confirmed SUCCESS, a terminal failure, or mergePollTimeout seconds
// elapse. On confirmed green, agent-complete is swapped unconditionally.
//
// Returns (green, genuineRed):
//   - (true, false)  — CI confirmed green; issue swapped to completeLabel.
//   - (false, true)  — CI red (FAILURE or ERROR); caller decides whether to
//     dispatch a fix box. No label swap performed.
//   - (false, false) — non-retriable outcome (timeout, API error); no label
//     swap. Caller must swap to failedLabel.
func gateToGreen(c config, fc forge.Client, num, pr string) (bool, bool) {
	pollIv := c.mergePollInterval
	deadline := c.mergePollTimeout
	// actualIv is used for elapsed tracking; floor to 1 so we don't
	// hot-spin. When pollIv is 0 (test mode) the sleep duration is also 0,
	// so elapsed still advances and the loop terminates.
	actualIv := pollIv
	if actualIv <= 0 {
		actualIv = 1
	}
	elapsed := 0

	for {
		state, stateErr := fc.CheckState(pr)
		if stateErr != nil {
			fmt.Printf("    #%s  pr=%s  status=check-state-error  !! %v\n", num, pr, stateErr)
			return false, false
		}

		switch state {
		case forge.StateSuccess:
			// Pause before confirming — back-to-back GraphQL calls return the
			// same snapshot, so a late-registered job would not yet appear.
			time.Sleep(time.Duration(pollIv) * time.Second)
			// Re-poll to confirm the snapshot is stable. A partial check
			// registration can briefly show SUCCESS before all jobs appear.
			confirm, confirmErr := fc.CheckState(pr)
			if confirmErr != nil {
				fmt.Printf("    #%s  pr=%s  status=check-state-error  !! %v\n", num, pr, confirmErr)
				return false, false
			}
			if confirm != forge.StateSuccess {
				if confirm == forge.StateFailure || confirm == forge.StateError {
					return false, true
				}
				// PENDING/EXPECTED/NONE — keep waiting for checks to settle.
				break
			}
			// Confirmed green: mark complete regardless of merge outcome.
			transitionState(fc, num, forge.InProgress, forge.Complete)
			return true, false
		case forge.StateFailure, forge.StateError:
			// Genuine red — signal caller so it can dispatch a fix pass.
			return false, true
		}

		// PENDING, EXPECTED, NONE (no checks yet), or unrecognised — keep
		// waiting until timeout.
		if elapsed >= deadline {
			break
		}
		// Sleep 0 when pollIv is 0 (test mode) so tests run without real
		// delays; actualIv still advances elapsed to prevent a tight loop.
		time.Sleep(time.Duration(pollIv) * time.Second)
		elapsed += actualIv
	}
	return false, false
}

// checkAutoMergePreflight verifies that the repo allows GitHub's native
// auto-merge when MERGE_MODE=auto. Returns a non-nil error if the repo
// disallows it or the capability check fails; no-ops for other modes.
func checkAutoMergePreflight(c config, fc forge.Client) error {
	if c.mergeMode != "auto" {
		return nil
	}
	ok, err := fc.CanAutoMerge()
	if err != nil {
		return fmt.Errorf("MERGE_MODE=auto: auto-merge capability check failed: %w", err)
	}
	if !ok {
		return fmt.Errorf("MERGE_MODE=auto: the repo does not allow auto-merge — enable \"Allow auto-merge\" in repo Settings → General, or switch to MERGE_MODE=manual")
	}
	return nil
}

// applyMergeMode performs the mode-specific action after CI reaches green.
// agent-complete is already set; a merge failure is returned as an error but
// does not revert the label.
//
// conflictResolveFn, when non-nil, is called when fc.Rebase returns
// ErrMergeConflict to attempt an agent-assisted resolution. When nil, a
// rebase conflict is immediately non-retriable.
func applyMergeMode(c config, fc forge.Client, num, pr string, conflictResolveFn func(string) error) error {
	switch c.mergeMode {
	case "immediate":
		return mergeImmediate(c, fc, num, pr, conflictResolveFn)
	case "auto":
		if err := fc.EnqueueAutoMerge(pr); err != nil {
			fmt.Printf("    #%s  pr=%s  status=auto-merge-enqueue-failed  !! %v\n", num, pr, err)
			fc.Comment(num, fmt.Sprintf("auto-merge enqueue failed: %v — PR is green; approve and merge manually", err))
			return nil
		}
		fmt.Printf("    #%s  pr=%s  status=auto-merge-enqueued\n", num, pr)
		return nil
	case "manual":
		fmt.Printf("    #%s  pr=%s  status=agent-complete  merge-mode=%s\n", num, pr, c.mergeMode)
		return nil
	default:
		return fmt.Errorf("unrecognised MERGE_MODE: %q", c.mergeMode)
	}
}

// mergeImmediate attempts to merge the green PR with rebase retry on conflict.
// It embodies the existing rebase-retry and agent conflict-resolve behaviors.
//
// A successful conflict-resolve already rebased and force-pushed the branch,
// so the next Merge conflict is retried directly (after a brief settle wait
// for the forge's mergeability snapshot to catch up) instead of invoking
// Rebase a second time.
//
// A Rebase force-push failure that forge.ErrTransientPushFailure wraps (an
// infra or network fault, not a genuine stale-lease rejection) is retried up
// to maxRebaseAttempts times before it's treated as terminal.
func mergeImmediate(c config, fc forge.Client, num, pr string, conflictResolveFn func(string) error) error {
	rebaseAttempts := 0
	pushRetries := 0
	skipRebase := false
	for {
		err := fc.Merge(pr)
		if err == nil {
			return nil
		}
		if !errors.Is(err, forge.ErrMergeConflict) {
			return err
		}
		if skipRebase {
			skipRebase = false
			fmt.Printf("    #%s  pr=%s  status=merge-retry-settle\n", num, pr)
			time.Sleep(time.Duration(c.mergePollInterval) * time.Second)
			continue
		}
		if rebaseAttempts >= c.maxRebaseAttempts {
			return err
		}
		rebaseAttempts++
		fmt.Printf("    #%s  pr=%s  status=rebase-retry  attempt=%d/%d\n",
			num, pr, rebaseAttempts, c.maxRebaseAttempts)
		rbErr := fc.Rebase(pr)
		for rbErr != nil && errors.Is(rbErr, forge.ErrTransientPushFailure) && pushRetries < c.maxRebaseAttempts {
			pushRetries++
			fmt.Printf("    #%s  pr=%s  status=rebase-push-retry  attempt=%d/%d  !! %v\n",
				num, pr, pushRetries, c.maxRebaseAttempts, rbErr)
			rbErr = fc.Rebase(pr)
		}
		if rbErr != nil {
			if errors.Is(rbErr, forge.ErrTransientPushFailure) {
				fmt.Printf("    #%s  pr=%s  status=rebase-push-retries-exhausted  attempts=%d  !! %v\n",
					num, pr, pushRetries, rbErr)
				return rbErr
			}
			if errors.Is(rbErr, forge.ErrMergeConflict) && conflictResolveFn != nil {
				fmt.Printf("    #%s  pr=%s  status=conflict-resolve\n", num, pr)
				if crErr := conflictResolveFn(pr); crErr != nil {
					fmt.Printf("    #%s  pr=%s  status=conflict-resolve-failed  !! %v\n", num, pr, crErr)
					return crErr
				}
				skipRebase = true
			} else {
				fmt.Printf("    #%s  pr=%s  status=rebase-failed  !! %v\n", num, pr, rbErr)
				return rbErr
			}
		}
	}
}

// selfHeal polls the merge gate, dispatching fix boxes on genuine red up to
// maxFixAttempts times. On green it swaps agent-complete (via gateToGreen)
// then applies the merge mode; a merge failure after green leaves the issue
// agent-complete and is never demoted to agent-failed.
//
// Returns (ok, merged): ok is true when CI reached green; merged is true only
// when immediate mode completed an actual merge. A merge failure keeps the
// issue at agent-complete (merge-blocked note) and returns (true, false).
//
// runFixFn is called with the 1-based fix-pass number and must dispatch the
// fix box synchronously. runConflictResolveFn, when non-nil, is forwarded to
// applyMergeMode for agent-assisted rebase-conflict resolution.
func selfHeal(c config, fc forge.Client, runFixFn func(int) error, runConflictResolveFn func(string) error, num, pr string) (ok bool, merged bool) {
	for attempt := 0; ; attempt++ {
		green, genuineRed := gateToGreen(c, fc, num, pr)
		if green {
			matched, guardErr := mergeGuardHit(c, fc, pr)
			if guardErr != nil {
				fmt.Printf("    #%s  pr=%s  status=merge-guard-check-error  !! %v\n", num, pr, guardErr)
				fc.Comment(num, fmt.Sprintf("merge guard: could not list changed files (%v) — downgrading to manual as a precaution; review and merge by hand", guardErr))
				return true, false
			}
			if len(matched) > 0 {
				fmt.Printf("    #%s  pr=%s  status=merge-guard-hit  paths=%v\n", num, pr, matched)
				fc.Comment(num, mergeGuardComment(matched))
				return true, false
			}
			if err := applyMergeMode(c, fc, num, pr, runConflictResolveFn); err != nil {
				fmt.Printf("    #%s  pr=%s  status=merge-blocked  !! %v\n", num, pr, err)
				fc.Comment(num, fmt.Sprintf("merge blocked after green CI: %v", err))
				return true, false
			}
			return true, c.mergeMode == "immediate"
		}
		if !genuineRed || attempt >= c.maxFixAttempts {
			if genuineRed && c.maxFixAttempts > 0 {
				fmt.Printf("    #%s  pr=%s  status=fix-exhausted  !! exhausted %d fix pass(es)\n",
					num, pr, c.maxFixAttempts)
			}
			transitionState(fc, num, forge.InProgress, forge.Failed)
			return false, false
		}
		fmt.Printf("    #%s  pr=%s  fix-pass=%d/%d\n", num, pr, attempt+1, c.maxFixAttempts)
		if err := runFixFn(attempt + 1); err != nil {
			fmt.Printf("    !! #%s fix-pass-%d exited non-zero\n", num, attempt+1)
		}
	}
}

func verifyMerged(c config, fc forge.Client, num, pr string) {
	prState, _ := fc.PRState(pr)
	iss, _ := fc.Issue(num)
	if prState == "MERGED" && containsLabel(iss.Labels, c.completeLabel) {
		fmt.Printf("    #%s  pr=%s  status=verified-merged\n", num, pr)
		return
	}
	var reason string
	if prState != "MERGED" {
		if prState == "" {
			reason = "PR state is 'unknown', expected MERGED"
		} else {
			reason = fmt.Sprintf("PR state is '%s', expected MERGED", prState)
		}
	} else {
		reason = fmt.Sprintf("issue does not carry '%s'", c.completeLabel)
	}
	fmt.Printf("    #%s  pr=%s  status=failed  !! %s\n", num, pr, reason)
	transitionState(fc, num, forge.InProgress, forge.Failed)
}

// adoptAndGate runs the merge gate (selfHeal → verifyMerged) on an
// already-discovered open non-draft PR for iss. Prints "status=adopted"
// before running the gate. Called by both printOutcomeReport and
// reconcileStranded.
func adoptAndGate(c config, fc forge.Client, iss issue, prURL string, runFixFn func(int) error, runConflictResolveFn func(string) error) {
	branch := c.branchPrefix + iss.number
	fmt.Printf("    #%s  pr=%s  status=adopted  note=no outcome line; PR discovered on %s\n", iss.number, prURL, branch)
	ok, merged := selfHeal(c, fc, runFixFn, runConflictResolveFn, iss.number, prURL)
	if ok {
		if merged {
			verifyMerged(c, fc, iss.number, prURL)
		}
	} else {
		fmt.Printf("    #%s  pr=%s  status=failed  !! CI or merge failed\n", iss.number, prURL)
	}
}

// postUsageComment posts an aggregate usage-statistics comment to the issue.
// If no result event is found in the log the comment notes that usage is
// unavailable. Errors posting the comment are logged but do not abort the
// caller.
func postUsageComment(fc forge.Client, issNum, logPath string) {
	model := os.Getenv("MODEL")
	if model == "" {
		model = "unknown"
	}
	u, found, err := usage.LastInLog(logPath)
	var body string
	if err != nil || !found {
		body = fmt.Sprintf("## Run usage\n\nModel: `%s`\n\nUsage data unavailable (no result event in log).", model)
	} else {
		body = fmt.Sprintf(
			"## Run usage\n\n"+
				"| Field | Value |\n"+
				"| --- | --- |\n"+
				"| Model | `%s` |\n"+
				"| Cost | $%.4f |\n"+
				"| Input tokens | %d |\n"+
				"| Output tokens | %d |\n"+
				"| Cache read tokens | %d |\n"+
				"| Cache creation tokens | %d |\n"+
				"| Wall time | %s |\n"+
				"| API time | %s |\n"+
				"| Turns | %d |",
			model,
			u.TotalCostUSD,
			u.InputTokens,
			u.OutputTokens,
			u.CacheReadInputTokens,
			u.CacheCreationInputTokens,
			usage.FormatDuration(u.DurationMs),
			usage.FormatDuration(u.DurationApiMs),
			u.NumTurns,
		)
		body += breakdownSection(logPath)
	}
	if commentErr := fc.Comment(issNum, body); commentErr != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: post usage comment: %v\n", issNum, commentErr)
	}
}

// breakdownSection returns a Markdown per-role breakdown section, or empty
// string if no assistant events are found or the log cannot be read.
func breakdownSection(logPath string) string {
	roles, err := usage.BreakdownByRole(logPath)
	if err != nil || len(roles) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n\n### Per-role breakdown\n\n")
	sb.WriteString("| Role | Input tokens | Output tokens | Cache read | Cache creation |\n")
	sb.WriteString("| --- | --- | --- | --- | --- |\n")
	for _, r := range roles {
		fmt.Fprintf(&sb, "| %s | %d | %d | %d | %d |\n",
			r.Role, r.InputTokens, r.OutputTokens,
			r.CacheReadInputTokens, r.CacheCreationInputTokens)
	}
	return sb.String()
}

// gateIssue runs the merge gate for a single issue immediately after its box
// exits. It reads the outcome log, drives selfHeal or adoptAndGate as needed,
// and posts the usage comment. Called from fanOut goroutines so each issue
// reaches completeLabel or failedLabel independently of its wave siblings.
func gateIssue(c config, fc forge.Client, pwd string, r runner.Runner, iss issue) {
	logPath := filepath.Join(pwd, "logs", "issue-"+iss.number+".log")
	o, found, err := outcome.LastInLog(logPath)
	if err != nil {
		fmt.Printf("    #%s  status=malformed  note=unparseable outcome line\n", iss.number)
		return
	}
	if !found {
		branch := c.branchPrefix + iss.number
		pr, isDraft, prFound, prErr := openPRForBranch(fc, branch)
		if prErr != nil || !prFound {
			cls, clsErr := outcome.Classify(logPath)
			clsNote := ""
			if clsErr != nil {
				fmt.Fprintf(os.Stderr, "    ?? #%s: classify: %v\n", iss.number, clsErr)
			} else {
				clsNote = fmt.Sprintf("  class=%s  reason=%s", cls.Class, cls.Reason)
				if cls.ResetAt != nil {
					clsNote += "  resetsAt=" + cls.ResetAt.UTC().Format(time.RFC3339)
				}
			}
			fmt.Printf("    #%s  status=missing%s  note=no outcome in log\n", iss.number, clsNote)
			return
		}
		if isDraft {
			fmt.Printf("    #%s  pr=%s  status=blocked  note=draft PR on %s; no outcome line\n", iss.number, pr, branch)
			return
		}
		fixFn := func(fixPass int) error { return runFix(c, pwd, r, iss, fixPass) }
		conflictFn := func(pr string) error { return runConflictResolve(c, pwd, r, iss, pr) }
		adoptAndGate(c, fc, iss, pr, fixFn, conflictFn)
		return
	}

	switch o.Status {
	case "blocked":
		fmt.Printf("    #%s  pr=%s  status=%s  !! %s\n", iss.number, o.PR, o.Status, o.Note)
		postUsageComment(fc, iss.number, logPath)
	case "ready":
		fixFn := func(fixPass int) error { return runFix(c, pwd, r, iss, fixPass) }
		conflictFn := func(pr string) error { return runConflictResolve(c, pwd, r, iss, pr) }
		ok, merged := selfHeal(c, fc, fixFn, conflictFn, iss.number, o.PR)
		if ok {
			if merged {
				verifyMerged(c, fc, iss.number, o.PR)
			}
		} else {
			fmt.Printf("    #%s  pr=%s  status=failed  !! CI or merge failed\n", iss.number, o.PR)
		}
		postUsageComment(fc, iss.number, logPath)
	case "merged":
		verifyMerged(c, fc, iss.number, o.PR)
		postUsageComment(fc, iss.number, logPath)
	default:
		fmt.Printf("    #%s  pr=%s  status=%s\n", iss.number, o.PR, o.Status)
		postUsageComment(fc, iss.number, logPath)
	}
}

// printOutcomeReport prints the outcome-report header. Per-issue gating now
// runs inside fanOut goroutines via gateIssue, so each issue reaches its
// terminal label independently before this point.
func printOutcomeReport(_ config, _ forge.Client, _ string, _ runner.Runner, _ []issue) {
	fmt.Println("==> outcome report")
}

// openPRForBranch wraps fc.OpenPRForBranch to unpack the PR struct for callers
// that need the URL and draft flag separately.
func openPRForBranch(fc forge.Client, branch string) (url string, isDraft bool, found bool, err error) {
	pr, ok, err := fc.OpenPRForBranch(branch)
	if err != nil || !ok {
		return "", false, false, err
	}
	return pr.URL, pr.IsDraft, true, nil
}

// Sentinel errors translated to specific exit codes so callers like dogfood.sh
// can distinguish termination reasons without a separate gh probe.
//
//	exit 2 (errQueueEmpty): discoverIssues found no open dispatchable issues.
var errQueueEmpty = errors.New("queue empty")

// buildEdges returns the dependency graph for the given batch of issues by
// calling the IssueTracker's DepsOf for each. Non-fatal per-issue errors are
// skipped, matching the original best-effort behaviour.
func buildEdges(fc forge.Client, issues []issue) (map[string][]string, error) {
	edges := map[string][]string{}
	for _, iss := range issues {
		deps, err := fc.DepsOf(iss.number)
		if err != nil {
			// Non-fatal: skip issues whose data cannot be fetched.
			continue
		}
		if len(deps) > 0 {
			edges[iss.number] = deps
		}
	}
	return edges, nil
}

// detectCycle runs Kahn's algorithm on the in-batch portion of the dependency
// graph. Only edges where both endpoints appear in nums are considered; external
// blockers (not in the batch) are ignored. Returns a cycle-member issue number
// and true when a cycle exists; returns "" and false for an acyclic graph.
func detectCycle(edges map[string][]string, nums []string) (string, bool) {
	inBatch := make(map[string]bool, len(nums))
	for _, n := range nums {
		inBatch[n] = true
	}

	indegree := make(map[string]int, len(nums))
	adj := map[string][]string{}
	for _, n := range nums {
		indegree[n] = 0
	}
	for child, blockers := range edges {
		if !inBatch[child] {
			continue
		}
		for _, blocker := range blockers {
			if !inBatch[blocker] {
				continue
			}
			indegree[child]++
			adj[blocker] = append(adj[blocker], child)
		}
	}

	queue := make([]string, 0, len(nums))
	for _, n := range nums {
		if indegree[n] == 0 {
			queue = append(queue, n)
		}
	}
	done := 0
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		done++
		for _, dep := range adj[node] {
			indegree[dep]--
			if indegree[dep] == 0 {
				queue = append(queue, dep)
			}
		}
	}
	if done < len(nums) {
		for _, n := range nums {
			if indegree[n] > 0 {
				return n, true
			}
		}
	}
	return "", false
}

func containsLabel(labels []string, target string) bool {
	for _, l := range labels {
		if l == target {
			return true
		}
	}
	return false
}

// blockerReady returns true when the blocker's PR is merged, or when the
// blocker issue is closed with no discoverable PR (human-handled work).
func blockerReady(c config, fc forge.Client, dep string) bool {
	branch := c.branchPrefix + dep
	prURL, ok, err := fc.PRForBranch(branch)
	if err == nil && ok {
		state, stateErr := fc.PRState(prURL)
		if stateErr == nil {
			return state == "MERGED"
		}
		return false
	}
	fi, err := fc.Issue(dep)
	if err != nil {
		return false
	}
	if fi.State == "CLOSED" {
		fmt.Printf("    .. blocker #%s is closed (no discoverable PR); treating as satisfied\n", dep)
		return true
	}
	return false
}

// issueIsReady returns true when all of num's declared blockers are ready.
func issueIsReady(c config, fc forge.Client, num string, edges map[string][]string) bool {
	return len(unreadyBlockers(c, fc, num, edges)) == 0
}

// hasFailedInBatchBlocker returns true when any of num's in-batch declared
// blockers carry failedLabel, meaning the dependent can never proceed.
func hasFailedInBatchBlocker(c config, fc forge.Client, num string, edges map[string][]string) bool {
	for _, dep := range edges[num] {
		fi, err := fc.Issue(dep)
		if err != nil {
			continue
		}
		if containsLabel(fi.Labels, c.failedLabel) {
			return true
		}
	}
	return false
}

// unreadyBlockers returns num's declared blockers that are not yet satisfied,
// in edge order. Empty means the issue is ready to dispatch.
func unreadyBlockers(c config, fc forge.Client, num string, edges map[string][]string) []string {
	var out []string
	for _, dep := range edges[num] {
		if !blockerReady(c, fc, dep) {
			out = append(out, dep)
		}
	}
	return out
}

// blockedMarker is the file the launcher drops under logs/ when a claimed
// single issue cannot start because a blocker is unmet. The dispatching
// pipeline reads it to release the claim and comment; detection stays here so
// the two blocker formats are parsed once, in one place.
const blockedMarker = "blocked.txt"

// writeBlockedMarker records the unmet blockers as a "#a, #b" list for the
// workflow to interpolate into its release comment.
func writeBlockedMarker(pwd string, blockers []string) error {
	refs := make([]string, len(blockers))
	for i, b := range blockers {
		refs[i] = "#" + b
	}
	path := filepath.Join(pwd, "logs", blockedMarker)
	return os.WriteFile(path, []byte(strings.Join(refs, ", ")), 0o644)
}

// discoverIssues resolves the batch of issues to dispatch. When ISSUE_NUMBER is
// set the workflow has already claimed exactly this issue (label swapped to
// in-progress before the build), so we target it directly rather than querying
// by label — a label query could otherwise pick up a different issue stranded
// on the same in-progress label by an earlier crash.
func discoverIssues(c config, fc forge.Client) ([]issue, error) {
	if c.issueNumber != "" {
		fmt.Printf("==> targeting claimed issue #%s in %s\n", c.issueNumber, c.repoSlug)
		fi, err := fc.Issue(c.issueNumber)
		if err != nil {
			return nil, err
		}
		return []issue{{number: fi.Number, title: fi.Title}}, nil
	}
	fmt.Printf("==> querying open '%s' issues in %s\n", c.label, c.repoSlug)
	rawIssues, err := fc.ListIssues(forge.Dispatchable)
	if err != nil {
		return nil, err
	}
	var issues []issue
	for _, fi := range rawIssues {
		issues = append(issues, issue{number: fi.Number, title: fi.Title})
	}
	return issues, nil
}

// reconcileStranded discovers open issues carrying inProgressLabel that also
// have an open non-draft PR on their agent branch, and runs the merge gate on
// each. Draft PRs and in-progress issues with no open PR are skipped silently.
// Called at launcher start, before any new dispatch.
func reconcileStranded(c config, fc forge.Client, pwd string, r runner.Runner) {
	fiList, err := fc.ListIssues(forge.InProgress)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reconcile: list in-progress issues: %v\n", err)
		return
	}
	if len(fiList) == 0 {
		return
	}
	fmt.Println("==> reconciling stranded in-progress issues")
	for _, fi := range fiList {
		iss := issue{number: fi.Number, title: fi.Title}
		branch := c.branchPrefix + iss.number
		prURL, isDraft, found, prErr := openPRForBranch(fc, branch)
		if prErr != nil || !found || isDraft {
			continue
		}
		fixFn := func(fixPass int) error { return runFix(c, pwd, r, iss, fixPass) }
		conflictFn := func(pr string) error { return runConflictResolve(c, pwd, r, iss, pr) }
		adoptAndGate(c, fc, iss, prURL, fixFn, conflictFn)
	}
}

// recoverByNumber resolves the open non-draft PR for the issue numbered issueNum
// and drives the same adopt-and-gate path used by reconcileStranded. Returns an
// error when the issue cannot be fetched, the PR is a draft, or no open PR
// exists; the caller should treat those as non-success exits.
func recoverByNumber(c config, fc forge.Client, pwd string, r runner.Runner, issueNum string) error {
	fi, err := fc.Issue(issueNum)
	if err != nil {
		return fmt.Errorf("issue %s: %w", issueNum, err)
	}
	iss := issue{number: fi.Number, title: fi.Title}
	branch := c.branchPrefix + iss.number
	prURL, isDraft, found, prErr := openPRForBranch(fc, branch)
	if prErr != nil {
		return fmt.Errorf("issue %s: resolve PR: %w", issueNum, prErr)
	}
	if !found {
		fmt.Printf("    #%s  status=skipped  note=no open PR on %s\n", issueNum, branch)
		return fmt.Errorf("issue %s: no open PR", issueNum)
	}
	if isDraft {
		fmt.Printf("    #%s  pr=%s  status=skipped  note=draft PR; recover operates on non-draft PRs only\n", issueNum, prURL)
		return fmt.Errorf("issue %s: draft PR", issueNum)
	}
	if err := os.MkdirAll(filepath.Join(pwd, "logs"), 0o755); err != nil {
		return fmt.Errorf("mkdir logs: %w", err)
	}
	fixFn := func(fixPass int) error { return runFix(c, pwd, r, iss, fixPass) }
	conflictFn := func(pr string) error { return runConflictResolve(c, pwd, r, iss, pr) }
	adoptAndGate(c, fc, iss, prURL, fixFn, conflictFn)
	return nil
}

// recoverIssue is the entry point for the `recover` subcommand. It loads config,
// wires the forge client and runner, then calls recoverByNumber.
func recoverIssue(issueNum string) error {
	pwd, err := os.Getwd()
	if err != nil {
		return err
	}
	c := loadConfig()
	if err := validate(c); err != nil {
		return err
	}
	r := newRunner(c, pwd)
	if err := r.EnsureReady(); err != nil {
		return err
	}
	fc := newForgeClient(c)
	return recoverByNumber(c, fc, pwd, r, issueNum)
}

// labelMeta holds the default color and description for a triage label.
type labelMeta struct {
	description string
	color       string // hex without leading #
}

// triageLabelMeta is the single source of truth for default triage label
// colors and descriptions, keyed by the canonical label name.
var triageLabelMeta = map[string]labelMeta{
	"ready-for-agent":   {description: "Fully specified; ready for an AFK agent", color: "0075ca"},
	"agent-in-progress": {description: "An AFK agent is actively working this issue", color: "e4e669"},
	"agent-failed":      {description: "Box exited non-zero; needs human triage", color: "d93f0b"},
	"agent-complete":    {description: "Agent work merged and green", color: "0e8a16"},
}

// runDoctor probes both seams (IssueTracker + CodeForge), then checks that
// all configured triage labels exist in the repository. When interactive is
// true and labels are missing, it prompts to create them; otherwise it reports
// and exits non-zero.
func runDoctor(it forge.IssueTracker, cf forge.CodeForge, c config, w io.Writer, stdin io.Reader, interactive bool) error {
	repo, err := it.Probe()
	if err != nil {
		if errors.Is(err, forge.ErrAuthFailure) {
			return fmt.Errorf("forge auth check failed (check GH_TOKEN is set and valid): %w", err)
		}
		if errors.Is(err, forge.ErrRepoNotFound) {
			return fmt.Errorf("forge repo not found (check --repo-slug / REPO_SLUG is correct): %w", err)
		}
		return fmt.Errorf("forge connectivity check failed: %w", err)
	}
	fmt.Fprintf(w, "ok: issue tracker confirmed — %s is reachable\n", repo)
	if _, err := cf.Probe(); err != nil {
		return fmt.Errorf("code forge connectivity check failed: %w", err)
	}
	fmt.Fprintf(w, "ok: code forge confirmed — %s is reachable\n", repo)

	checkLabels := func() ([]string, error) {
		existing, lerr := it.ListLabels()
		if lerr != nil {
			return nil, fmt.Errorf("label check failed: %w", lerr)
		}
		present := make(map[string]bool, len(existing))
		for _, l := range existing {
			present[l] = true
		}
		expected := []string{c.label, c.inProgressLabel, c.failedLabel, c.completeLabel}
		var missing []string
		for _, label := range expected {
			if present[label] {
				fmt.Fprintf(w, "ok: label %q present\n", label)
			} else {
				fmt.Fprintf(w, "MISSING: label %q missing\n", label)
				missing = append(missing, label)
			}
		}
		return missing, nil
	}

	missing, err := checkLabels()
	if err != nil {
		return err
	}
	if len(missing) == 0 {
		return nil
	}

	if !interactive {
		return fmt.Errorf("one or more triage labels are missing — create them in the repository")
	}

	fmt.Fprintf(w, "Create %d missing label(s)? [y/N] ", len(missing))
	scanner := bufio.NewScanner(stdin)
	if !scanner.Scan() || strings.ToLower(strings.TrimSpace(scanner.Text())) != "y" {
		fmt.Fprintln(w)
		return fmt.Errorf("one or more triage labels are missing — create them in the repository")
	}

	for _, name := range missing {
		meta, ok := triageLabelMeta[name]
		if !ok {
			meta = labelMeta{color: "ededed"}
		}
		if cerr := it.CreateLabel(name, meta.description, meta.color); cerr != nil {
			return fmt.Errorf("create label %q: %w", name, cerr)
		}
		fmt.Fprintf(w, "created: label %q\n", name)
	}

	// Re-verify after creation.
	missing, err = checkLabels()
	if err != nil {
		return err
	}
	if len(missing) == 0 {
		fmt.Fprintln(w, "ok: all triage labels present")
		return nil
	}
	return fmt.Errorf("one or more triage labels are still missing after creation")
}

// previewIssues is the testable core of the preview verb. When issueNums is
// non-empty it performs a selective dry-run: fetches exactly those issues,
// prints label-bypass warnings, blocker annotations, and cascade-eviction
// notices without launching any Box or prompting. When issueNums is empty it
// falls back to queue-drain discovery.
func previewIssues(c config, fc forge.Client, w io.Writer, issueNums []string) error {
	if len(issueNums) > 0 {
		return previewSelectiveList(c, fc, w, issueNums)
	}

	issues, err := discoverIssues(c, fc)
	if err != nil {
		return err
	}
	if c.issueNumber == "" && len(issues) == 0 {
		fmt.Fprintf(w, "repo: %s  merge-mode: %s\nno open '%s' issues — nothing to dispatch.\n", c.repoSlug, c.mergeMode, c.label)
		return nil
	}
	edges, err := buildEdges(fc, issues)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "repo: %s  merge-mode: %s\n", c.repoSlug, c.mergeMode)
	fmt.Fprintf(w, "%d issue(s) would be dispatched:\n", len(issues))
	for _, iss := range issues {
		blockers := edges[iss.number]
		if len(blockers) > 0 {
			fmt.Fprintf(w, "  #%s  %s  (blocked by #%s)\n", iss.number, iss.title, strings.Join(blockers, ", #"))
		} else {
			fmt.Fprintf(w, "  #%s  %s\n", iss.number, iss.title)
		}
	}
	return nil
}

// previewSelectiveList performs a dry-run of the selective-list dispatch path.
// It prints label-bypass warnings, per-issue blocker annotations, and cascade-
// eviction notices. No Boxes are started and no Forge mutations occur.
func previewSelectiveList(c config, fc forge.Client, w io.Writer, nums []string) error {
	issues, unlabeled, err := fetchSelectiveIssues(c, fc, nums)
	if err != nil {
		return err
	}

	// Label-bypass warnings (no prompt in preview).
	for _, num := range unlabeled {
		fmt.Fprintf(w, "⚠ #%s not ready-for-agent; dispatching anyway (explicit)\n", num)
	}

	// Parse blocker graph.
	edges, err := buildEdges(fc, issues)
	if err != nil {
		return err
	}

	// Eviction pass (dry-run; no side effects).
	kept, notices := evictUnmetBlockers(c, fc, issues, edges)
	for _, n := range notices {
		fmt.Fprintln(w, n)
	}

	fmt.Fprintf(w, "repo: %s  merge-mode: %s\n", c.repoSlug, c.mergeMode)
	if len(kept) == 0 {
		fmt.Fprintf(w, "no issues would be dispatched after eviction\n")
		return nil
	}
	fmt.Fprintf(w, "%d issue(s) would be dispatched:\n", len(kept))
	for _, iss := range kept {
		blockers := edges[iss.number]
		if len(blockers) > 0 {
			fmt.Fprintf(w, "  #%s  %s  (blocked by #%s)\n", iss.number, iss.title, strings.Join(blockers, ", #"))
		} else {
			fmt.Fprintf(w, "  #%s  %s\n", iss.number, iss.title)
		}
	}
	return nil
}

// preview is the entry point for the `preview` subcommand.
func preview(issueNums []string) error {
	c := loadConfig()
	if err := validate(c); err != nil {
		return err
	}
	fc := newForgeClient(c)
	return previewIssues(c, fc, os.Stdout, issueNums)
}

// issueNums returns the number strings from a slice of issues.
func issueNums(issues []issue) []string {
	nums := make([]string, len(issues))
	for i, iss := range issues {
		nums[i] = iss.number
	}
	return nums
}

// sleepFn and nowFn are injectable for tests; they default to the real
// time.Sleep and time.Now so production behaviour is unchanged.
var sleepFn func(time.Duration) = time.Sleep
var nowFn func() time.Time = time.Now

// classifyFn is injectable for tests so callers can supply predetermined
// classifications without writing log files on disk.
var classifyFn func(string) (outcome.Classification, error) = outcome.Classify

// runWithRetry dispatches iss, retrying transient failures according to
// config limits. It returns true when the box exits zero, false after a
// terminal failure or once the retry cap is exhausted.
//
//   - 429 with a known resetsAt: hold until the reset time (+ holdJitterSecs),
//     then re-dispatch. A hold that ends in success or terminal does NOT
//     consume the retry cap. Consecutive holds that each end in another 429
//     count toward the cap (the "no-progress" case — the token never recovered).
//     NOTE: full progress detection (distinguishing an immediate re-429 from a
//     legitimate long-running re-dispatch that also 429s) requires branch-push
//     tracking (#140). Until then we count all consecutive 429-then-hold cycles,
//     which is conservative but prevents infinite looping on permanently-bad tokens.
//   - Other transients (529/overloaded, network, 429 without resetsAt): linear
//     backoff retry up to transientRetryMax, then agent-failed.
//   - Terminal: agent-failed immediately, no retry.
//   - Re-dispatch clones fresh until #140 lands (incremental branch push); once
//     #140 is implemented, a pre-existing remote branch is handled
//     deterministically and hold-then-resume picks up from the last push.
func runWithRetry(c config, pwd string, r runner.Runner, iss issue) bool {
	holdCount := 0
	transientCount := 0
	prevWasHold := false

	for {
		if err := runOne(c, pwd, r, iss); err == nil {
			return true
		}

		logPath := filepath.Join(pwd, "logs", "issue-"+iss.number+".log")
		cl, err := classifyFn(logPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "    ?? #%s: classify error: %v\n", iss.number, err)
			return false
		}

		if cl.Class == outcome.Terminal {
			return false
		}

		// Transient exit: branch on reason.
		if cl.Reason == outcome.RateLimit && cl.ResetAt != nil {
			// 429 with known reset: hold until reset + jitter.
			// A hold following another hold (prevWasHold=true) means the token
			// has not recovered — consume the cap. A hold after a non-hold
			// iteration (success, terminal, or different transient) is "free".
			if prevWasHold {
				holdCount++
			}
			if holdCount >= c.transientRetryMax {
				fmt.Printf("    !! #%s: hold cap exhausted (%d consecutive no-progress hold(s))\n",
					iss.number, c.transientRetryMax)
				return false
			}
			wait := cl.ResetAt.Sub(nowFn()) + time.Duration(c.holdJitterSecs)*time.Second
			if wait < 0 {
				wait = time.Duration(c.holdJitterSecs) * time.Second
			}
			fmt.Printf("    .. #%s: rate limit; holding until %s\n",
				iss.number, cl.ResetAt.UTC().Format("15:04 UTC"))
			sleepFn(wait)
			prevWasHold = true
			continue
		}

		// 529/overloaded, network, or 429 without a known reset time → backoff retry.
		prevWasHold = false
		transientCount++
		if transientCount > c.transientRetryMax {
			fmt.Printf("    !! #%s: transient retry cap exhausted (%d)\n",
				iss.number, c.transientRetryMax)
			return false
		}
		backoff := time.Duration(c.transientBackoffSecs) * time.Second * time.Duration(transientCount)
		fmt.Printf("    .. #%s: transient (%s); retry %d/%d in %s\n",
			iss.number, cl.Reason, transientCount, c.transientRetryMax, backoff)
		sleepFn(backoff)
	}
}

// fanOut dispatches a batch of issues in parallel (up to maxParallel at once).
// Each goroutine claims its issue only after acquiring a semaphore slot so that
// at most maxParallel issues are ever in the in-progress state simultaneously.
func fanOut(c config, fc forge.Client, pwd string, r runner.Runner, batch []issue) {
	sem := make(chan struct{}, c.maxParallel)
	var wg sync.WaitGroup
	for _, iss := range batch {
		wg.Add(1)
		iss := iss
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			claimIssue(c, fc, iss.number)
			if ok := runWithRetry(c, pwd, r, iss); !ok {
				fmt.Printf("    !! #%s FAILED (logs/issue-%s.log)\n", iss.number, iss.number)
				transitionState(fc, iss.number, forge.InProgress, forge.Failed)
			} else {
				fmt.Printf("    <- #%s done  (logs/issue-%s.log)\n", iss.number, iss.number)
				gateIssue(c, fc, pwd, r, iss)
			}
		}()
	}
	wg.Wait()
}

// dispatchWaves fans issues out in dependency order. Each wave dispatches the
// currently unblocked set; blocked issues are held and rechecked after
// depsPollSecs. The deadlock timer resets on any progress; if no issue becomes
// ready within depsWaitSecs the function returns an error rather than blocking
// forever. Dispatched issues leave the remaining set even when they fail.
func dispatchWaves(c config, fc forge.Client, pwd string, r runner.Runner, issues []issue, edges map[string][]string) error {
	remaining := make([]issue, len(issues))
	copy(remaining, issues)
	elapsed := 0

	for len(remaining) > 0 {
		var ready, blockerFailed, held []issue
		for _, iss := range remaining {
			switch {
			case issueIsReady(c, fc, iss.number, edges):
				ready = append(ready, iss)
			case hasFailedInBatchBlocker(c, fc, iss.number, edges):
				blockerFailed = append(blockerFailed, iss)
			default:
				held = append(held, iss)
			}
		}

		for _, iss := range blockerFailed {
			fmt.Printf("    !! #%s  status=blocker-failed  note=a dependency failed; skipping\n", iss.number)
			transitionState(fc, iss.number, forge.Dispatchable, forge.Failed)
		}

		if len(ready) == 0 {
			if len(blockerFailed) > 0 {
				elapsed = 0
				remaining = held
				continue
			}
			if elapsed >= c.depsWaitSecs {
				fmt.Fprintf(os.Stderr,
					"ERROR: dependency deadlock — blockers did not reach '%s' after %ds\n",
					c.completeLabel, c.depsWaitSecs)
				for _, iss := range remaining {
					fmt.Fprintf(os.Stderr, "    #%s %s\n", iss.number, iss.title)
				}
				return fmt.Errorf("dependency deadlock")
			}
			fmt.Printf("    .. all remaining issues blocked; retrying in %ds (%ds elapsed)\n",
				c.depsPollSecs, elapsed)
			time.Sleep(time.Duration(c.depsPollSecs) * time.Second)
			elapsed += c.depsPollSecs
			continue
		}

		// Progress: reset the deadlock timer.
		elapsed = 0
		fanOut(c, fc, pwd, r, ready)
		remaining = held
	}
	return nil
}

// drainMaxJobs drains up to c.maxJobs currently-unblocked issues from the
// batch and exits. Blocked issues are skipped so no slot is wasted on a
// dependency that hasn't merged yet; they wait for the next invocation.
// A cycle in the in-batch dependency graph is an error returned immediately.
func drainMaxJobs(c config, fc forge.Client, pwd string, r runner.Runner, issues []issue, edges map[string][]string) error {
	if len(edges) > 0 {
		if node, cycle := detectCycle(edges, issueNums(issues)); cycle {
			return fmt.Errorf("ERROR: dependency cycle detected (issue #%s is in the cycle)", node)
		}
	}
	var selected []issue
	for _, iss := range issues {
		if issueIsReady(c, fc, iss.number, edges) {
			selected = append(selected, iss)
			if len(selected) >= c.maxJobs {
				break
			}
		} else {
			fmt.Printf("    ~~ #%s blocked (a blocker is not '%s'); skipping\n", iss.number, c.completeLabel)
		}
	}
	if len(selected) == 0 {
		// Claimed single-issue path: the caller already swapped this issue
		// onto the in-progress label, so a bare skip would strand it there.
		// Drop a marker naming the unmet blockers; the dispatching pipeline
		// releases the claim and comments. Give up — no wait, no recovery.
		if c.issueNumber != "" {
			if blockers := unreadyBlockers(c, fc, c.issueNumber, edges); len(blockers) > 0 {
				if err := writeBlockedMarker(pwd, blockers); err != nil {
					return err
				}
				fmt.Printf("==> #%s blocked; wrote logs/%s for the pipeline to release the claim\n", c.issueNumber, blockedMarker)
			}
		}
		fmt.Printf("no unblocked '%s' issues to drain — nothing to do.\n", c.label)
		return nil
	}
	fmt.Printf("==> draining %d unblocked issue(s) (MAX_JOBS=%d)\n", len(selected), c.maxJobs)
	fanOut(c, fc, pwd, r, selected)
	printOutcomeReport(c, fc, pwd, r, selected)
	return nil
}

func run(noBuild bool) error {
	pwd, err := os.Getwd()
	if err != nil {
		return err
	}

	c := loadConfig()
	if err := validate(c); err != nil {
		return err
	}

	r := newRunner(c, pwd)
	if noBuild {
		if err := r.IsReady(); err != nil {
			return err
		}
	} else {
		if err := r.EnsureReady(); err != nil {
			return err
		}
	}

	fc := newForgeClient(c)
	fmt.Printf("repo: %s  merge-mode: %s\n", c.repoSlug, c.mergeMode)

	if err := checkAutoMergePreflight(c, fc); err != nil {
		return err
	}

	// Reconcile stranded in-progress issues before dispatching new work.
	// Skip when ISSUE_NUMBER is set — the caller already claimed a specific issue.
	if c.issueNumber == "" {
		reconcileStranded(c, fc, pwd, r)
	}

	issues, err := discoverIssues(c, fc)
	if err != nil {
		return err
	}

	if c.issueNumber == "" && len(issues) == 0 {
		fmt.Printf("no open '%s' issues — nothing to do.\n", c.label)
		return errQueueEmpty
	}

	// Build the dependency graph for the batch.
	edges, err := buildEdges(fc, issues)
	if err != nil {
		return err
	}
	hasEdges := len(edges) > 0

	if err := os.MkdirAll(filepath.Join(pwd, "logs"), 0o755); err != nil {
		return err
	}

	if c.maxJobs > 0 {
		if err := drainMaxJobs(c, fc, pwd, r, issues, edges); err != nil {
			return err
		}
	} else if hasEdges {
		// MAX_JOBS = 0 with dependency edges: multi-wave dispatch.
		if node, cycle := detectCycle(edges, issueNums(issues)); cycle {
			return fmt.Errorf("ERROR: dependency cycle detected (issue #%s is in the cycle)", node)
		}
		fmt.Println("==> dependency edges found; dispatching in waves")
		if err := dispatchWaves(c, fc, pwd, r, issues, edges); err != nil {
			return err
		}
		printOutcomeReport(c, fc, pwd, r, issues)
	} else {
		// MAX_JOBS = 0, no declared edges: original single-wave fan-out.
		fmt.Printf("==> %d issue(s); launching up to %d container(s) at a time\n", len(issues), c.maxParallel)
		fanOut(c, fc, pwd, r, issues)
		printOutcomeReport(c, fc, pwd, r, issues)
	}

	fmt.Printf("==> all agents finished — branches pushed and PRs opened on %s.\n", c.repoSlug)
	return nil
}

func main() {
	help, helpAll := false, false
	for _, a := range os.Args[1:] {
		switch a {
		case "--help", "-h":
			help = true
		case "--all":
			helpAll = true
		case "--version":
			printVersion(os.Stdout)
			os.Exit(0)
		}
	}
	if help {
		if helpAll {
			printHelpFull(os.Stdout)
		} else {
			printHelp(os.Stdout)
		}
		os.Exit(0)
	}
	args, err := parseFlags(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
		os.Exit(1)
	}
	if len(args) > 0 && args[0] == "build" {
		if err := build(); err != nil {
			fmt.Fprintf(os.Stderr, "%s\n", err)
			os.Exit(1)
		}
		return
	}
	if len(args) > 0 && args[0] == "doctor" {
		c := loadConfig()
		it := newIssueTracker(c)
		cf := newCodeForge(c)
		stat, serr := os.Stdin.Stat()
		interactive := serr == nil && (stat.Mode()&os.ModeCharDevice) != 0
		if err := runDoctor(it, cf, c, os.Stdout, os.Stdin, interactive); err != nil {
			fmt.Fprintf(os.Stderr, "%s\n", err)
			os.Exit(1)
		}
		return
	}
	if len(args) > 0 && args[0] == "recover" {
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: spindrift recover <issue-number>")
			os.Exit(1)
		}
		if err := recoverIssue(args[1]); err != nil {
			fmt.Fprintf(os.Stderr, "%s\n", err)
			os.Exit(1)
		}
		return
	}
	if len(args) > 0 && args[0] == "preview" {
		previewNums := dispatchIssueArgs(args[1:])
		if err := preview(previewNums); err != nil {
			fmt.Fprintf(os.Stderr, "%s\n", err)
			os.Exit(1)
		}
		return
	}
	if len(args) > 0 && args[0] == "dispatch" {
		noBuild, dispatchArgs := dispatchNoBuildArgs(args[1:])
		forceYes, dispatchArgs := dispatchYesArgs(dispatchArgs)
		nums := dispatchIssueArgs(dispatchArgs)
		if len(nums) > 0 {
			// Operator explicit list: selective path (bypasses label/barrier gates).
			pwd, err := os.Getwd()
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s\n", err)
				os.Exit(1)
			}
			c := loadConfig()
			if err := validate(c); err != nil {
				fmt.Fprintf(os.Stderr, "%s\n", err)
				os.Exit(1)
			}
			r := newRunner(c, pwd)
			if noBuild {
				if err := r.IsReady(); err != nil {
					fmt.Fprintf(os.Stderr, "%s\n", err)
					os.Exit(1)
				}
			} else {
				if err := r.EnsureReady(); err != nil {
					fmt.Fprintf(os.Stderr, "%s\n", err)
					os.Exit(1)
				}
			}
			fc := newForgeClient(c)
			if err := selectiveListDispatch(c, fc, pwd, r, nums, forceYes, os.Stdin, os.Stdout); err != nil {
				fmt.Fprintf(os.Stderr, "%s\n", err)
				os.Exit(1)
			}
			return
		}
		if err := run(noBuild); err != nil {
			if errors.Is(err, errQueueEmpty) {
				os.Exit(2)
			}
			fmt.Fprintf(os.Stderr, "%s\n", err)
			os.Exit(1)
		}
		return
	}
	if err := run(false); err != nil {
		if errors.Is(err, errQueueEmpty) {
			os.Exit(2)
		}
		fmt.Fprintf(os.Stderr, "%s\n", err)
		os.Exit(1)
	}
}

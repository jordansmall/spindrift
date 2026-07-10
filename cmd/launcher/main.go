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
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/settle"
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

	// driver selects the Go Driver strategy (ADR 0009): transient
	// classification and heartbeat parsing. Empty defaults to "claude",
	// matching the nix side's default.
	driver string

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

	// issueTracker selects the IssueTracker adapter: "github" (default),
	// "local", or "jira". localIssuesDir is the local adapter's issue
	// directory; the jira* fields are only consulted when issueTracker ==
	// "jira". The Code Forge (PR/CI/merge) stays github regardless.
	issueTracker   string
	localIssuesDir string

	jiraBaseURL         string
	jiraProjectKey      string
	jiraEmail           string
	jiraToken           string
	jiraStatusMapping   string
	jiraIncludeComments bool

	// Transient-exit retry knobs
	transientRetryMax    int
	transientBackoffSecs int
	holdJitterSecs       int

	// Dependency-wave knobs
	depsPollSecs int
	depsWaitSecs int

	// overlapGate controls the declared-## Touches overlap check: "defer"
	// holds a Dispatchable issue whose touch-set intersects an InProgress
	// issue's, retrying once the collider completes; "off" disables the
	// check entirely.
	overlapGate string

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

	// In-box mount targets declared by the selected Driver (ADR 0009),
	// nix-baked at wrap time. driverSessionCacheDir is empty when the
	// Driver declares no session-state dir.
	driverSkillsDir       string
	driverSessionCacheDir string

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

	// codeForge selects the Code Forge adapter: "github" (open PR, watch CI,
	// merge) or "git" (push-only to codeForgeRemoteURL; no PR, CI-watch, or
	// merge gate).
	codeForge string

	// codeForgeRemoteURL is the plain git remote URL the Box clones from and
	// pushes to when codeForge is "git". Unused (and unrequired) otherwise.
	codeForgeRemoteURL string
}

type issue struct {
	number string
	title  string
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
		driver:          os.Getenv("DRIVER"),
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

		issueTracker:        getenv("ISSUE_TRACKER", "github"),
		localIssuesDir:      getenv("LOCAL_ISSUES_DIR", ".spindrift/issues"),
		jiraBaseURL:         os.Getenv("JIRA_BASE_URL"),
		jiraProjectKey:      os.Getenv("JIRA_PROJECT_KEY"),
		jiraEmail:           os.Getenv("JIRA_EMAIL"),
		jiraToken:           os.Getenv("JIRA_TOKEN"),
		jiraStatusMapping:   os.Getenv("JIRA_STATUS_MAPPING"),
		jiraIncludeComments: os.Getenv("JIRA_INCLUDE_COMMENTS") != "",

		transientRetryMax:    atoi(getenv("TRANSIENT_RETRY_MAX", "3"), 3),
		transientBackoffSecs: atoi(getenv("TRANSIENT_BACKOFF_SECS", "30"), 30),
		holdJitterSecs:       atoiNonneg(getenv("HOLD_JITTER_SECS", "5"), 5),

		depsPollSecs: atoiNonneg(getenv("DEPS_POLL_SECS", "30"), 30),
		depsWaitSecs: atoiNonneg(getenv("DEPS_WAIT_SECS", "7200"), 7200),
		overlapGate:  getenv("OVERLAP_GATE", "defer"),

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

		driverSkillsDir:       os.Getenv("DRIVER_SKILLS_DIR"),
		driverSessionCacheDir: os.Getenv("DRIVER_SESSION_CACHE_DIR"),

		podmanNetwork:   os.Getenv("PODMAN_NETWORK"),
		bwrapUnshareNet: os.Getenv("BWRAP_UNSHARE_NET") != "",
		pidsLimit:       getenv("PIDS_LIMIT", "512"),
		memoryLimit:     getenv("MEMORY_LIMIT", "4g"),

		boxEnvVars: os.Getenv("BOX_ENV_VARS"),

		mergeMode:       getenv("MERGE_MODE", "manual"),
		mergeGuardPaths: getenv("MERGE_GUARD_PATHS", ".github/**,**/CLAUDE.md,**/AGENTS.md,.claude/**,.opencode/**"),

		codeForge:          getenv("CODE_FORGE", "github"),
		codeForgeRemoteURL: os.Getenv("CODE_FORGE_REMOTE_URL"),
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
	if err := runner.ValidateRuntime(c.runtime); err != nil {
		return err
	}
	if _, err := driver.New(c.driver); err != nil {
		return err
	}
	if err := settle.ValidateMergeMode(c.mergeMode); err != nil {
		return err
	}
	switch c.overlapGate {
	case "defer", "off":
		// valid
	default:
		return fmt.Errorf("OVERLAP_GATE=%q is not valid; must be defer or off", c.overlapGate)
	}
	switch c.issueTracker {
	case "github", "local", "jira":
		// valid
	default:
		return fmt.Errorf("ISSUE_TRACKER=%q is not valid; must be github, local, or jira", c.issueTracker)
	}
	if c.issueTracker == "jira" {
		if err := forge.ValidateJiraEnv(c.jiraBaseURL, c.jiraProjectKey, c.jiraToken, c.jiraStatusMapping); err != nil {
			return err
		}
	}
	switch c.codeForge {
	case "github":
		// valid
	case "git":
		if c.codeForgeRemoteURL == "" {
			return fmt.Errorf("set CODE_FORGE_REMOTE_URL (the plain git remote to clone from and push to) when CODE_FORGE=git")
		}
	default:
		return fmt.Errorf("CODE_FORGE=%q is not valid; must be github or git", c.codeForge)
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
	switch c.issueTracker {
	case "local":
		return forge.NewLocalTracker(c.localIssuesDir, dispatchLabels(c))
	case "jira":
		statusMapping, err := forge.ParseStatusMapping(c.jiraStatusMapping)
		if err != nil {
			// validate() already rejects a malformed mapping before this is
			// reached; treat it as unmapped (label-only lifecycle) as a
			// fallback.
			statusMapping = map[forge.DispatchState]string{}
		}
		return forge.NewJiraClient(forge.JiraConfig{
			BaseURL:         c.jiraBaseURL,
			ProjectKey:      c.jiraProjectKey,
			Email:           c.jiraEmail,
			Token:           c.jiraToken,
			StatusMapping:   statusMapping,
			Labels:          dispatchLabels(c),
			IncludeComments: c.jiraIncludeComments,
		})
	default:
		return forge.NewExecClient(c.repoSlug, dispatchLabels(c), c.branchPrefix)
	}
}

// newCodeForge returns the CodeForge adapter selected by CODE_FORGE: "github"
// (open PR, watch CI, merge) or "git" (push-only to codeForgeRemoteURL; no
// PR, CI-watch, or merge gate).
func newCodeForge(c config) forge.CodeForge {
	if c.codeForge == "git" {
		return forge.NewGitClient(c.codeForgeRemoteURL, c.baseBranch, c.gitUserName, c.gitUserEmail, c.branchPrefix)
	}
	return forge.NewExecClient(c.repoSlug, dispatchLabels(c), c.branchPrefix)
}

// newForgeClient composes the configured IssueTracker and CodeForge (which
// vary independently per ADR 0013) into a single Client for call sites that
// need both axes together.
func newForgeClient(c config) forge.Client {
	return forge.NewClient(newIssueTracker(c), newCodeForge(c))
}

// runnerConfig builds the runner.Config a runner adapter needs from loaded
// config. Shared by both the `run` and `build` subcommand entry points; the
// build entry point never calls Run(), so leaving PromptDir/SkillsDir/
// PodmanNetwork populated is harmless there.
func runnerConfig(c config) runner.Config {
	return runner.Config{
		Runtime:               c.runtime,
		Image:                 c.image,
		ImageArchive:          c.imageArchive,
		ImageDrv:              c.imageDrv,
		ImageTag:              c.imageTag,
		NixBuilderImage:       c.nixBuilderImage,
		NixVolume:             c.nixVolume,
		FlakeImageAttr:        c.flakeImageAttr,
		PodmanNetwork:         c.podmanNetwork,
		PidsLimit:             c.pidsLimit,
		MemoryLimit:           c.memoryLimit,
		AgentFiles:            c.agentFiles,
		AgentEnv:              c.agentEnv,
		AgentFilesDrv:         c.agentFilesDrv,
		AgentEnvDrv:           c.agentEnvDrv,
		BakedPrefetch:         c.bakedPrefetch,
		BwrapUnshareNet:       c.bwrapUnshareNet,
		PromptDir:             c.spindriftPromptDir,
		SkillsDir:             c.spindriftSkillsDir,
		DriverSkillsDir:       c.driverSkillsDir,
		DriverSessionCacheDir: c.driverSessionCacheDir,
	}
}

// newDriver returns the Go Driver strategy selected by c.driver (ADR 0009).
// validate() already rejects an unrecognised DRIVER before this is reached,
// so the error here is treated as impossible in production and falls back to
// the registry default.
func newDriver(c config) driver.Driver {
	d, err := driver.New(c.driver)
	if err != nil {
		d, _ = driver.New("")
	}
	return d
}

// dispatchConfig builds the subset of config a dispatch.Factory needs.
func dispatchConfig(c config) dispatch.Config {
	return dispatch.Config{
		BoxEnvVars:            c.boxEnvVars,
		TransientRetryMax:     c.transientRetryMax,
		TransientBackoffSecs:  c.transientBackoffSecs,
		HoldJitterSecs:        c.holdJitterSecs,
		DriverSessionCacheDir: c.driverSessionCacheDir,
	}
}

// newDispatchFactory constructs the dispatch.Factory for one top-level
// dispatch entry point (run, the selective `dispatch <nums>` path, or
// recover). A driver-cache creation failure is logged and degrades to no
// cache (fix boxes cold-start) rather than failing the dispatch -- the cache
// is a resume optimization, not a correctness requirement (issue #427).
func newDispatchFactory(c config, pwd string, r runner.Runner) *dispatch.Factory {
	f, err := dispatch.NewFactory(dispatchConfig(c), pwd, r, newDriver(c), dispatch.RealClock())
	if err != nil {
		fmt.Fprintf(os.Stderr, "==> driver cache unavailable (%v) -- fix boxes will cold-start\n", err)
	}
	return f
}

// settleConfig builds the subset of config a settle.Settle needs.
func settleConfig(c config) settle.Config {
	return settle.Config{
		MergeMode:         c.mergeMode,
		MergeGuardPaths:   c.mergeGuardPaths,
		CompleteLabel:     c.completeLabel,
		MergePollInterval: c.mergePollInterval,
		MergePollTimeout:  c.mergePollTimeout,
		MaxFixAttempts:    c.maxFixAttempts,
		MaxRebaseAttempts: c.maxRebaseAttempts,
	}
}

// newSettle constructs the settle.Settle for one top-level dispatch entry
// point, reused across every issue in that invocation.
func newSettle(c config, fc forge.Client) *settle.Settle {
	return settle.New(settleConfig(c), fc)
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
	rc := runnerConfig(c)
	var r runner.Runner
	if c.runtime == "bwrap" {
		r = runner.NewBwrapBuild(rc)
	} else {
		r = runner.NewOCI(rc, pwd)
	}
	return r.EnsureReady()
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

// checkAutoMergePreflight verifies that the repo allows GitHub's native
// auto-merge when MERGE_MODE=auto. Returns a non-nil error if the repo
// disallows it or the capability check fails; no-ops for other modes.
func checkAutoMergePreflight(c config, fc forge.Client) error {
	if c.mergeMode != "auto" {
		return nil
	}
	if fc.PushOnly() {
		return fmt.Errorf("MERGE_MODE=auto requires CODE_FORGE=github (got %q) — auto-merge is a GitHub-native feature with no meaning off github; switch to MERGE_MODE=manual or immediate", c.codeForge)
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
//	exit 3 (errOpenNoneDispatchable): open dispatchable issues exist but drain
//	  selected zero (all blocked/deferred); the driving loop should stop with a
//	  triage message rather than hot-looping.
var (
	errQueueEmpty           = errors.New("queue empty")
	errOpenNoneDispatchable = errors.New("open issues exist but none are dispatchable")
)

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
	branch := fc.AgentBranch(dep)
	prURL, ok, err := fc.PRForBranch(branch)
	if err == nil && ok {
		state, stateErr := fc.PRState(prURL)
		if stateErr == nil {
			return state == forge.PRMerged
		}
		return false
	}
	fi, err := fc.Issue(dep)
	if err != nil {
		return false
	}
	if fi.State == forge.IssueClosed {
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
func reconcileStranded(c config, fc forge.Client, f *dispatch.Factory, s settle.Settler) {
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
		branch := fc.AgentBranch(iss.number)
		prURL, isDraft, found, prErr := openPRForBranch(fc, branch)
		if prErr != nil || !found || isDraft {
			continue
		}
		d := f.New(iss.number, iss.title)
		s.SettleAdopted(d, iss.number, prURL)
		d.Close()
	}
}

// recoverByNumber resolves the open non-draft PR for the issue numbered issueNum
// and drives the same adopt-and-gate path used by reconcileStranded. Returns an
// error when the issue cannot be fetched, the PR is a draft, or no open PR
// exists; the caller should treat those as non-success exits.
func recoverByNumber(c config, fc forge.Client, pwd string, f *dispatch.Factory, s settle.Settler, issueNum string) error {
	fi, err := fc.Issue(issueNum)
	if err != nil {
		return fmt.Errorf("issue %s: %w", issueNum, err)
	}
	iss := issue{number: fi.Number, title: fi.Title}
	branch := fc.AgentBranch(iss.number)
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
	d := f.New(iss.number, iss.title)
	defer d.Close()
	s.SettleAdopted(d, iss.number, prURL)
	return nil
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
	tokenHint, slugHint := "GH_TOKEN", "--repo-slug / REPO_SLUG"
	if c.issueTracker == "jira" {
		tokenHint, slugHint = "JIRA_TOKEN", "JIRA_BASE_URL / JIRA_PROJECT_KEY"
	}
	repo, err := it.Probe()
	if err != nil {
		if errors.Is(err, forge.ErrAuthFailure) {
			return fmt.Errorf("forge auth check failed (check %s is set and valid): %w", tokenHint, err)
		}
		if errors.Is(err, forge.ErrRepoNotFound) {
			return fmt.Errorf("forge repo not found (check %s is correct): %w", slugHint, err)
		}
		return fmt.Errorf("forge connectivity check failed: %w", err)
	}
	fmt.Fprintf(w, "ok: issue tracker confirmed — %s is reachable\n", repo)
	cfRepo, err := cf.Probe()
	if err != nil {
		return fmt.Errorf("code forge connectivity check failed: %w", err)
	}
	fmt.Fprintf(w, "ok: code forge confirmed — %s is reachable\n", cfRepo)

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

// dispatchWave dispatches a batch of issues in parallel (up to maxParallel at once).
// Each goroutine claims its issue only after acquiring a semaphore slot so that
// at most maxParallel issues are ever in the in-progress state simultaneously.
func dispatchWave(c config, fc forge.Client, f *dispatch.Factory, s settle.Settler, batch []issue) {
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
			d := f.New(iss.number, iss.title)
			defer d.Close()
			result := d.Run()
			if !result.Success {
				fmt.Printf("    !! #%s FAILED (logs/issue-%s.log)\n", iss.number, iss.number)
				transitionState(fc, iss.number, forge.InProgress, forge.Failed)
			} else {
				fmt.Printf("    <- #%s done  (logs/issue-%s.log)\n", iss.number, iss.number)
				s.Settle(d, iss.number, result)
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
func dispatchWaves(c config, fc forge.Client, f *dispatch.Factory, s settle.Settler, issues []issue, edges map[string][]string) error {
	remaining := make([]issue, len(issues))
	copy(remaining, issues)
	elapsed := 0

	for len(remaining) > 0 {
		checkOverlap := waveOverlapCheck(c, fc)
		var ready, blockerFailed, held []issue
		for _, iss := range remaining {
			collider, overlapped := checkOverlap(iss.number)
			switch {
			case issueIsReady(c, fc, iss.number, edges) && !overlapped:
				ready = append(ready, iss)
			case hasFailedInBatchBlocker(c, fc, iss.number, edges):
				blockerFailed = append(blockerFailed, iss)
			default:
				if overlapped {
					fmt.Printf("    ~~ #%s touches overlap in-progress #%s; deferring\n", iss.number, collider)
				}
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
		dispatchWave(c, fc, f, s, ready)
		remaining = held
	}
	return nil
}

// drainMaxJobs drains up to c.maxJobs currently-unblocked issues from the
// batch and exits. Blocked issues are skipped so no slot is wasted on a
// dependency that hasn't merged yet; they wait for the next invocation.
// A cycle in the in-batch dependency graph is an error returned immediately.
func drainMaxJobs(c config, fc forge.Client, pwd string, f *dispatch.Factory, s settle.Settler, issues []issue, edges map[string][]string) error {
	if len(edges) > 0 {
		if node, cycle := detectCycle(edges, issueNums(issues)); cycle {
			return fmt.Errorf("ERROR: dependency cycle detected (issue #%s is in the cycle)", node)
		}
	}
	checkOverlap := waveOverlapCheck(c, fc)
	var selected, blockerFailed []issue
outer:
	for _, iss := range issues {
		switch {
		// Cascade-fail only in the multi-issue drain path (c.issueNumber == "").
		// The claimed single-issue path swaps the issue onto in-progress before
		// calling here; cascading it would add Failed on top of in-progress,
		// leaving the issue double-labeled. That path has its own blocked-marker
		// signaling via the writeBlockedMarker call below.
		case c.issueNumber == "" && hasFailedInBatchBlocker(c, fc, iss.number, edges):
			blockerFailed = append(blockerFailed, iss)
		case !issueIsReady(c, fc, iss.number, edges):
			fmt.Printf("    ~~ #%s blocked (a blocker is not '%s'); skipping\n", iss.number, c.completeLabel)
		default:
			if collider, overlapped := checkOverlap(iss.number); overlapped {
				fmt.Printf("    ~~ #%s touches overlap in-progress #%s; deferring\n", iss.number, collider)
				continue
			}
			selected = append(selected, iss)
			if len(selected) >= c.maxJobs {
				break outer
			}
		}
	}
	for _, iss := range blockerFailed {
		fmt.Printf("    !! #%s  status=blocker-failed  note=a dependency failed; skipping\n", iss.number)
		transitionState(fc, iss.number, forge.Dispatchable, forge.Failed)
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
			fmt.Printf("no unblocked '%s' issues to drain — nothing to do.\n", c.label)
			return nil
		}
		// Unattended drain path: if issues remain after cascade-failing blockers,
		// signal callers with exit 3 so they stop instead of hot-looping.
		remaining := len(issues) - len(blockerFailed)
		if remaining > 0 {
			fmt.Printf("no unblocked '%s' issues to drain — %d remain blocked or deferred.\n", c.label, remaining)
			return errOpenNoneDispatchable
		}
		fmt.Printf("no unblocked '%s' issues to drain — nothing to do.\n", c.label)
		return nil
	}
	fmt.Printf("==> draining %d unblocked issue(s) (MAX_JOBS=%d)\n", len(selected), c.maxJobs)
	dispatchWave(c, fc, f, s, selected)
	return nil
}

// run is the orchestration logic for the `dispatch` subcommand: preflight,
// stranded-issue reconciliation, discovery, dependency-graph construction,
// and drain/wave/fan-out dispatch. lc is wired by bootstrap in production;
// tests construct it directly with fakes.
func run(lc *launchContext) error {
	c, fc, f, s, pwd := lc.config, lc.forge, lc.factory, lc.settle, lc.pwd

	fmt.Printf("repo: %s  merge-mode: %s\n", c.repoSlug, c.mergeMode)

	if err := checkAutoMergePreflight(c, fc); err != nil {
		return err
	}

	// Reconcile stranded in-progress issues before dispatching new work.
	// Skip when ISSUE_NUMBER is set — the caller already claimed a specific issue.
	if c.issueNumber == "" {
		reconcileStranded(c, fc, f, s)
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
		if err := drainMaxJobs(c, fc, pwd, f, s, issues, edges); err != nil {
			return err
		}
	} else if hasEdges || batchHasTouchOverlap(c, fc, issues) {
		// MAX_JOBS = 0 with dependency edges, or a declared touch-set
		// overlapping an already in-progress issue: multi-wave dispatch.
		if node, cycle := detectCycle(edges, issueNums(issues)); cycle {
			return fmt.Errorf("ERROR: dependency cycle detected (issue #%s is in the cycle)", node)
		}
		if hasEdges {
			fmt.Println("==> dependency edges found; dispatching in waves")
		} else {
			fmt.Println("==> declared touches overlap an in-progress issue; dispatching in waves")
		}
		if err := dispatchWaves(c, fc, f, s, issues, edges); err != nil {
			return err
		}
	} else {
		// MAX_JOBS = 0, no declared edges: original single-wave dispatch.
		fmt.Printf("==> %d issue(s); launching up to %d container(s) at a time\n", len(issues), c.maxParallel)
		dispatchWave(c, fc, f, s, issues)
	}

	fmt.Printf("==> all agents finished — branches pushed and PRs opened on %s.\n", c.repoSlug)
	return nil
}

// cmdBuild is the `build` subcommand: realize the sandbox image or store
// closures without running any agent.
func cmdBuild() int {
	if err := build(); err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
		return 1
	}
	return 0
}

// cmdDoctor is the `doctor` subcommand: probe each forge seam through its
// own adapter (not the combined Client) so a CODE_FORGE=git deployment
// checks the actual remote it will push to, not the IssueTracker's repo a
// second time. No runner/dispatch/settle wiring needed, so it does not go
// through bootstrap.
func cmdDoctor() int {
	c := loadConfig()
	it := newIssueTracker(c)
	cf := newCodeForge(c)
	stat, serr := os.Stdin.Stat()
	interactive := serr == nil && (stat.Mode()&os.ModeCharDevice) != 0
	if err := runDoctor(it, cf, c, os.Stdout, os.Stdin, interactive); err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
		return 1
	}
	return 0
}

// cmdRecover is the `recover` subcommand: adopt an already-discovered open
// non-draft PR with no outcome line and drive it through the merge gate. lc
// is wired by bootstrap in production; tests construct it directly with
// fakes (and a spy cleanup) to exercise the cleanup-on-every-exit contract.
func cmdRecover(lc *launchContext, issueNum string) int {
	defer lc.cleanup()
	if err := recoverByNumber(lc.config, lc.forge, lc.pwd, lc.factory, lc.settle, issueNum); err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
		return 1
	}
	return 0
}

// cmdPreview is the `preview` subcommand: report what dispatch would do
// without launching any Box.
func cmdPreview(issueNums []string) int {
	if err := preview(issueNums); err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
		return 1
	}
	return 0
}

// cmdDispatchSelective is the `dispatch <nums>` subcommand: an
// operator-supplied issue list that bypasses the label/barrier gates. lc is
// wired by bootstrap in production; tests construct it directly with fakes.
func cmdDispatchSelective(lc *launchContext, nums []string, forceYes bool) int {
	defer lc.cleanup()
	if err := selectiveListDispatch(lc.config, lc.forge, lc.pwd, lc.factory, lc.settle, nums, forceYes, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
		return 1
	}
	return 0
}

// runExitCode translates run's result into the launcher's process exit
// code: 2 for an empty dispatch queue, 3 for open issues that exist but
// none are dispatchable, 1 for any other error, 0 on success. Split out
// from cmdDispatch so it's unit-testable against a fake-populated
// launchContext without going through bootstrap.
func runExitCode(lc *launchContext) int {
	err := run(lc)
	if err == nil {
		return 0
	}
	if errors.Is(err, errQueueEmpty) {
		return 2
	}
	if errors.Is(err, errOpenNoneDispatchable) {
		return 3
	}
	fmt.Fprintf(os.Stderr, "%s\n", err)
	return 1
}

// cmdDispatch is the default `dispatch` subcommand (and the no-args
// default): drain the labeled queue. lc is wired by bootstrap in
// production; tests construct it directly with fakes.
func cmdDispatch(lc *launchContext) int {
	defer lc.cleanup()
	return runExitCode(lc)
}

// mainRun parses argv and dispatches to the selected subcommand, returning
// the process exit code. It contains no business logic of its own beyond
// arg parsing and subcommand selection.
func mainRun(argv []string) int {
	help, helpAll := false, false
	for _, a := range argv {
		switch a {
		case "--help", "-h":
			help = true
		case "--all":
			helpAll = true
		case "--version":
			printVersion(os.Stdout)
			return 0
		}
	}
	if help {
		if helpAll {
			printHelpFull(os.Stdout)
		} else {
			printHelp(os.Stdout)
		}
		return 0
	}
	args, err := parseFlags(argv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
		return 1
	}
	if len(args) > 0 && args[0] == "build" {
		return cmdBuild()
	}
	if len(args) > 0 && args[0] == "doctor" {
		return cmdDoctor()
	}
	if len(args) > 0 && args[0] == "recover" {
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: spindrift recover <issue-number>")
			return 1
		}
		lc, err := bootstrap(true)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s\n", err)
			return 1
		}
		return cmdRecover(lc, args[1])
	}
	if len(args) > 0 && args[0] == "preview" {
		return cmdPreview(dispatchIssueArgs(args[1:]))
	}
	if len(args) > 0 && args[0] == "dispatch" {
		noBuild, dispatchArgs := dispatchNoBuildArgs(args[1:])
		forceYes, dispatchArgs := dispatchYesArgs(dispatchArgs)
		nums := dispatchIssueArgs(dispatchArgs)
		lc, err := bootstrap(!noBuild)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s\n", err)
			return 1
		}
		if len(nums) > 0 {
			return cmdDispatchSelective(lc, nums, forceYes)
		}
		return cmdDispatch(lc)
	}
	lc, err := bootstrap(true)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
		return 1
	}
	return cmdDispatch(lc)
}

func main() {
	os.Exit(mainRun(os.Args[1:]))
}

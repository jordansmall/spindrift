// Package main: spindrift launcher — orchestrates open issues into disposable
// containers. Nix-computed config (resolved knob settings and build/run
// artifacts) reaches the binary as one Launcher input document, passed via
// --input (ADR 0020); an explicit CLI flag overrides the document, and an
// ambient knob env var still wins this release but draws a deprecation
// warning (see warnAmbientKnobEnv). Secrets and BOX_ENV_VARS plumbing stay
// env-only. The binary contains no baked store paths of its own beyond what
// nix injects via the document.
package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"spindrift.dev/launcher/internal/console"
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/git"
	"spindrift.dev/launcher/internal/forge/github"
	"spindrift.dev/launcher/internal/forge/jira"
	"spindrift.dev/launcher/internal/forge/local"
	"spindrift.dev/launcher/internal/freshness"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/settle"
	"spindrift.dev/launcher/internal/waves"
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

	// continuousDispatch opts into the slot-refill dispatch mode (#527):
	// instead of a single wave, the launcher runs long enough to refill each
	// freed slot from a live re-discovery, gated by the image-freshness
	// probe before every launch. Off by default; the queue-discovery path
	// only (ISSUE_NUMBER-claimed and selective dispatch ignore it).
	continuousDispatch bool

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

	// preflightStaleBase opts into ADR 0026's proactive stale-base rebase:
	// a green PR that is behind its base (no textual conflict) is rebased and
	// re-greened before merging. Off by default — a green-but-behind PR
	// merges as-is (ADR 0028).
	preflightStaleBase bool

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

	// dispatchKind is "work" (the default, zero value) or "research" (ADR
	// 0022). Set once by bootstrap via applyDispatchKind, never read from
	// the environment directly — it is operator intent carried by which
	// subcommand launched (dispatch vs research), not a config knob.
	dispatchKind string
}

// dispatchKindWork and dispatchKindResearch are the two Dispatch kinds (ADR
// 0022). Kinds share the four canonical DispatchState lifecycle states;
// research selects the fixed agent-research label family (see
// applyDispatchKind) and a one-shot Settle instead of work's full merge
// gate.
const (
	dispatchKindWork     = "work"
	dispatchKindResearch = "research"
)

// applyDispatchKind sets c's dispatchKind marker and, for research, swaps
// the four lifecycle label fields to the fixed research family
// (forge.ResearchDispatchLabels) — unlike the work labels these aren't
// operator-configurable, since the research CI workflow and prompt key off
// them directly. completeLabel is left blank: the verdict-carrying
// transition uses IssueTracker.CompleteVerdict instead of a single Complete
// label.
func applyDispatchKind(c config, kind string) config {
	c.dispatchKind = kind
	if kind == dispatchKindResearch {
		rl := forge.ResearchDispatchLabels()
		c.label = rl.Dispatchable
		c.inProgressLabel = rl.InProgress
		c.completeLabel = rl.Complete
		c.failedLabel = rl.Failed
	}
	return c
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

// schemaDefault returns key's resolved default: the loaded Launcher input
// document's settings value (ADR 0020 — schema default overridden by the
// Consumer flake's settings) when --input loaded one and it carries key,
// else the generated schemaFlags table (cmd/launcher/flagtable_gen.go), or
// "" when the knob has none. getenvSchema/atoiSchema/atoiNonnegSchema below
// consult this, so document precedence applies to every knob they resolve
// with no other call-site change (issue #625).
func schemaDefault(key string) string {
	if loadedDoc != nil {
		if v, ok := loadedDoc.Settings[key]; ok {
			return v
		}
	}
	for _, e := range schemaFlags {
		if e.env == key {
			return e.dflt
		}
	}
	return ""
}

// intSchemaDefault parses key's schema default as an int; a non-numeric or
// absent default parses to 0.
func intSchemaDefault(key string) int {
	n, _ := strconv.Atoi(schemaDefault(key))
	return n
}

// getenvSchema reads key from the environment, falling back to its schema
// default instead of a hand-written literal.
func getenvSchema(key string) string {
	return getenv(key, schemaDefault(key))
}

// atoiSchema parses key's env value as a positive integer (see atoi),
// falling back to its schema default instead of a literal.
func atoiSchema(key string) int {
	return atoi(os.Getenv(key), intSchemaDefault(key))
}

// atoiNonnegSchema parses key's env value as a non-negative integer (see
// atoiNonneg), falling back to its schema default instead of a literal.
func atoiNonnegSchema(key string) int {
	return atoiNonneg(os.Getenv(key), intSchemaDefault(key))
}

// gitIdentityField resolves a commit-identity knob (GIT_USER_NAME/
// GIT_USER_EMAIL) via the normal document/flag/env chain, falling back to
// the host git config when none of those supply a value — the in-process
// replacement for the wrapper's former `${VAR:-$(git config ...)}` bash
// fallback (ADR 0020: the wrapper exports no knob env at all).
func gitIdentityField(env, gitConfigKey string) string {
	if v := getenvSchema(env); v != "" {
		return v
	}
	return gitConfigLookup(gitConfigKey)
}

func loadConfig() config {
	imageTag := getenvArtifact("IMAGE_TAG", "spindrift:latest")
	image := getenvArtifact("IMAGE", imageTag)
	return config{
		imageArchive:    getenvArtifact("IMAGE_ARCHIVE", ""),
		imageTag:        imageTag,
		imageDrv:        getenvArtifact("IMAGE_DRV", ""),
		nixBuilderImage: getenvArtifact("NIX_BUILDER_IMAGE", ""),
		nixVolume:       getenvArtifact("NIX_VOLUME", "spindrift-nix"),
		flakeImageAttr:  getenvArtifact("FLAKE_IMAGE_ATTR", ""),
		agentFiles:      getenvArtifact("AGENT_FILES", ""),
		agentEnv:        getenvArtifact("AGENT_ENV", ""),
		agentFilesDrv:   getenvArtifact("AGENT_FILES_DRV", ""),
		agentEnvDrv:     getenvArtifact("AGENT_ENV_DRV", ""),
		bakedPrefetch:   getenvArtifact("BAKED_PREFETCH", ""),
		runtime:         getenvArtifact("RUNTIME", ""),
		driver:          getenvArtifact("DRIVER", ""),
		image:           image,

		repoSlug:           getenvSchema("REPO_SLUG"),
		label:              getenvSchema("LABEL"),
		issueNumber:        os.Getenv("ISSUE_NUMBER"),
		baseBranch:         getenvSchema("BASE_BRANCH"),
		maxParallel:        atoiSchema("MAX_PARALLEL"),
		branchPrefix:       getenvSchema("BRANCH_PREFIX"),
		inProgressLabel:    getenvSchema("IN_PROGRESS_LABEL"),
		failedLabel:        getenvSchema("FAILED_LABEL"),
		completeLabel:      getenvSchema("COMPLETE_LABEL"),
		maxJobs:            atoiNonnegSchema("MAX_JOBS"),
		continuousDispatch: getenvSchema("CONTINUOUS_DISPATCH") != "",

		issueTracker:        getenvSchema("ISSUE_TRACKER"),
		localIssuesDir:      getenvSchema("LOCAL_ISSUES_DIR"),
		jiraBaseURL:         getenvSchema("JIRA_BASE_URL"),
		jiraProjectKey:      getenvSchema("JIRA_PROJECT_KEY"),
		jiraEmail:           getenvSchema("JIRA_EMAIL"),
		jiraToken:           os.Getenv("JIRA_TOKEN"),
		jiraStatusMapping:   getenvSchema("JIRA_STATUS_MAPPING"),
		jiraIncludeComments: getenvSchema("JIRA_INCLUDE_COMMENTS") != "",

		transientRetryMax:    atoiSchema("TRANSIENT_RETRY_MAX"),
		transientBackoffSecs: atoiSchema("TRANSIENT_BACKOFF_SECS"),
		holdJitterSecs:       atoiNonnegSchema("HOLD_JITTER_SECS"),

		overlapGate: getenvSchema("OVERLAP_GATE"),

		mergePollInterval:  atoiNonnegSchema("MERGE_POLL_INTERVAL"),
		mergePollTimeout:   atoiNonnegSchema("MERGE_POLL_TIMEOUT"),
		maxFixAttempts:     atoiNonnegSchema("MAX_FIX_ATTEMPTS"),
		maxRebaseAttempts:  atoiNonnegSchema("MAX_REBASE_ATTEMPTS"),
		preflightStaleBase: getenvSchema("PREFLIGHT_STALE_BASE") != "",

		ghToken:          os.Getenv("GH_TOKEN"),
		claudeOAuthToken: os.Getenv("CLAUDE_CODE_OAUTH_TOKEN"),
		anthropicAPIKey:  os.Getenv("ANTHROPIC_API_KEY"),
		gitUserName:      gitIdentityField("GIT_USER_NAME", "user.name"),
		gitUserEmail:     gitIdentityField("GIT_USER_EMAIL", "user.email"),

		spindriftPromptDir: getenvSchema("SPINDRIFT_PROMPT_DIR"),
		spindriftSkillsDir: getenvSchema("SPINDRIFT_SKILLS_DIR"),

		driverSkillsDir:       getenvArtifact("DRIVER_SKILLS_DIR", ""),
		driverSessionCacheDir: getenvArtifact("DRIVER_SESSION_CACHE_DIR", ""),

		podmanNetwork:   getenvSchema("PODMAN_NETWORK"),
		bwrapUnshareNet: getenvSchema("BWRAP_UNSHARE_NET") != "",
		pidsLimit:       getenvSchema("PIDS_LIMIT"),
		memoryLimit:     getenvSchema("MEMORY_LIMIT"),

		boxEnvVars: getenvArtifact("BOX_ENV_VARS", ""),

		mergeMode:       getenvSchema("MERGE_MODE"),
		mergeGuardPaths: getenvSchema("MERGE_GUARD_PATHS"),

		codeForge:          getenvSchema("CODE_FORGE"),
		codeForgeRemoteURL: getenvSchema("CODE_FORGE_REMOTE_URL"),
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
		if err := jira.ValidateJiraEnv(c.jiraBaseURL, c.jiraProjectKey, c.jiraToken, c.jiraStatusMapping); err != nil {
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

// researchVerdictLabels returns the fixed verdict-label mapping for
// research-kind construction, or the zero value for work — only
// ResearchSettle ever calls CompleteVerdict, so a work-kind tracker carrying
// a zero VerdictLabels is inert.
func researchVerdictLabels(c config) forge.VerdictLabels {
	if c.dispatchKind == dispatchKindResearch {
		return forge.ResearchVerdictLabels()
	}
	return forge.VerdictLabels{}
}

// newIssueTracker returns the IssueTracker adapter selected by ISSUE_TRACKER
// (default "github"), carrying c.dispatchKind's label family (dispatchLabels)
// and verdict labels (researchVerdictLabels) — the kind-aware seam ADR 0022
// describes.
func newIssueTracker(c config) forge.IssueTracker {
	vl := researchVerdictLabels(c)
	switch c.issueTracker {
	case "local":
		return local.NewLocalTracker(c.localIssuesDir, dispatchLabels(c), vl)
	case "jira":
		statusMapping, err := jira.ParseStatusMapping(c.jiraStatusMapping)
		if err != nil {
			// validate() already rejects a malformed mapping before this is
			// reached; treat it as unmapped (label-only lifecycle) as a
			// fallback.
			statusMapping = map[forge.DispatchState]string{}
		}
		return jira.NewJiraClient(jira.JiraConfig{
			BaseURL:         c.jiraBaseURL,
			ProjectKey:      c.jiraProjectKey,
			Email:           c.jiraEmail,
			Token:           c.jiraToken,
			StatusMapping:   statusMapping,
			Labels:          dispatchLabels(c),
			VerdictLabels:   vl,
			IncludeComments: c.jiraIncludeComments,
		})
	default:
		return github.NewExecClient(c.repoSlug, dispatchLabels(c), c.branchPrefix, vl)
	}
}

// newCodeForge returns the CodeForge adapter selected by CODE_FORGE: "github"
// (open PR, watch CI, merge) or "git" (push-only to codeForgeRemoteURL; no
// PR, CI-watch, or merge gate).
func newCodeForge(c config) forge.CodeForge {
	if c.codeForge == "git" {
		return git.NewGitClient(c.codeForgeRemoteURL, c.baseBranch, c.gitUserName, c.gitUserEmail, c.branchPrefix)
	}
	return github.NewExecClient(c.repoSlug, dispatchLabels(c), c.branchPrefix)
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
// OpenPRForIssue wires forge.ResolveOpenPR (issue #565), so a zero-exit
// rate-limited retry never re-runs a box whose work already landed a PR;
// ResolveOpenPR itself resolves to Found: false, nil for a push-only Code
// Forge, so the retry proceeds unguarded there without any guard here.
func dispatchConfig(c config, cf forge.CodeForge) dispatch.Config {
	return dispatch.Config{
		BoxEnvVars:            c.boxEnvVars,
		ResolveEnv:            resolveBoxEnvVar,
		Kind:                  c.dispatchKind,
		TransientRetryMax:     c.transientRetryMax,
		TransientBackoffSecs:  c.transientBackoffSecs,
		HoldJitterSecs:        c.holdJitterSecs,
		DriverSessionCacheDir: c.driverSessionCacheDir,
		OpenPRForIssue: func(number string) (bool, error) {
			res, err := forge.ResolveOpenPR(cf, number)
			return res.Found, err
		},
	}
}

// newDispatchFactory constructs the dispatch.Factory for one top-level
// dispatch entry point (run, the selective `dispatch <nums>` path, or
// recover). A driver-cache creation failure is logged and degrades to no
// cache (fix boxes cold-start) rather than failing the dispatch -- the cache
// is a resume optimization, not a correctness requirement (issue #427).
func newDispatchFactory(c config, pwd string, r runner.Runner, cf forge.CodeForge) *dispatch.Factory {
	f, err := dispatch.NewFactory(dispatchConfig(c, cf), pwd, r, newDriver(c), dispatch.RealClock())
	if err != nil {
		fmt.Fprintf(os.Stderr, "==> driver cache unavailable (%v) -- fix boxes will cold-start\n", err)
	}
	return f
}

// settleConfig builds the subset of config a settle.Settle needs.
func settleConfig(c config) settle.Config {
	return settle.Config{
		MergeMode:          c.mergeMode,
		MergeGuardPaths:    c.mergeGuardPaths,
		CompleteLabel:      c.completeLabel,
		MergePollInterval:  c.mergePollInterval,
		MergePollTimeout:   c.mergePollTimeout,
		MaxFixAttempts:     c.maxFixAttempts,
		MaxRebaseAttempts:  c.maxRebaseAttempts,
		PreflightStaleBase: c.preflightStaleBase,
	}
}

// newSettle constructs the Settler for one top-level dispatch entry point,
// reused across every issue in that invocation: the research kind's one-shot
// ResearchSettle, or work's full merge-gate Settle.
func newSettle(c config, it forge.IssueTracker, cf forge.CodeForge) settle.Settler {
	if c.dispatchKind == dispatchKindResearch {
		return settle.NewResearchSettle(it)
	}
	return settle.New(settleConfig(c), it, cf)
}

// wavesConfig builds the subset of config the wave engine (internal/waves)
// needs.
func wavesConfig(c config) waves.Config {
	// "work" is not a CLI verb; the dispatch subcommand is.
	verb := "dispatch"
	if c.dispatchKind == dispatchKindResearch {
		verb = dispatchKindResearch
	}
	return waves.Config{
		MaxParallel:     c.maxParallel,
		MaxJobs:         c.maxJobs,
		OverlapGate:     c.overlapGate,
		Label:           c.label,
		InProgressLabel: c.inProgressLabel,
		CompleteLabel:   c.completeLabel,
		FailedLabel:     c.failedLabel,
		IgnoreBlockers:  c.dispatchKind == dispatchKindResearch,
		Verb:            verb,
	}
}

// selectiveWavesConfig builds the wave-engine config for the operator-
// specified `dispatch <nums>` path: MAX_JOBS never applies to an explicit
// selection (the operator already named the exact issues to run), so it's
// zeroed regardless of the global config value — matching the original
// behaviour of drain being run()-only.
func selectiveWavesConfig(c config) waves.Config {
	cfg := wavesConfig(c)
	cfg.MaxJobs = 0
	return cfg
}

// toWaveIssues converts main's local issue type to waves.Issue for a call
// into the wave engine.
func toWaveIssues(issues []issue) []waves.Issue {
	out := make([]waves.Issue, len(issues))
	for i, iss := range issues {
		out[i] = waves.Issue{Number: iss.number, Title: iss.title}
	}
	return out
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

// checkAutoMergePreflight verifies that the repo allows GitHub's native
// auto-merge when MERGE_MODE=auto. Returns a non-nil error if the repo
// disallows it or the capability check fails; no-ops for other modes.
func checkAutoMergePreflight(c config, cf forge.CodeForge) error {
	if c.mergeMode != "auto" {
		return nil
	}
	pr, ok := cf.(forge.PRForge)
	if !ok {
		return fmt.Errorf("MERGE_MODE=auto requires CODE_FORGE=github (got %q) — auto-merge is a GitHub-native feature with no meaning off github; switch to MERGE_MODE=manual or immediate", c.codeForge)
	}
	canAuto, err := pr.CanAutoMerge()
	if err != nil {
		return fmt.Errorf("MERGE_MODE=auto: auto-merge capability check failed: %w", err)
	}
	if !canAuto {
		return fmt.Errorf("MERGE_MODE=auto: the repo does not allow auto-merge — enable \"Allow auto-merge\" in repo Settings → General, or switch to MERGE_MODE=manual")
	}
	return nil
}

// Sentinel error translated to a specific exit code so callers like
// dogfood.sh can distinguish termination reasons without a separate gh
// probe.
//
//	exit 2 (errQueueEmpty): discoverIssues found no open dispatchable issues.
//	exit 3 (waves.ErrOpenNoneDispatchable): open dispatchable issues exist but
//	  drain selected zero (all blocked/deferred); the driving loop should
//	  stop with a triage message rather than hot-looping.
//	exit 4 (waves.ErrImageStale): CONTINUOUS_DISPATCH mode only — the
//	  image-freshness probe found the loaded image would be rebuilt against
//	  the current base-branch tip; in-flight Boxes finished, no new ones
//	  launched, and the driving loop should rebuild and re-invoke.
var errQueueEmpty = errors.New("queue empty")

func containsLabel(labels []string, target string) bool {
	for _, l := range labels {
		if l == target {
			return true
		}
	}
	return false
}

// resolveOrigin is the one place c.issueNumber is consulted as the
// claimed-single-issue-vs-discovered-batch sentinel; every other call site
// (discovery, run, drain, preview) reads the derived Origin value instead of
// re-checking the sentinel itself.
func resolveOrigin(c config) waves.Origin {
	if c.issueNumber != "" {
		return waves.OriginClaimed
	}
	return waves.OriginDiscovered
}

// discoverIssues resolves the batch of issues to dispatch and the Origin that
// batch came from. When ISSUE_NUMBER is set the workflow has already claimed
// exactly this issue (label swapped to in-progress before the build), so we
// target it directly rather than querying by label — a label query could
// otherwise pick up a different issue stranded on the same in-progress label
// by an earlier crash.
func discoverIssues(c config, it forge.IssueTracker) ([]issue, waves.Origin, error) {
	origin := resolveOrigin(c)
	if origin == waves.OriginClaimed {
		fmt.Printf("==> targeting claimed issue #%s in %s\n", c.issueNumber, c.repoSlug)
		fi, err := it.Issue(c.issueNumber)
		if err != nil {
			return nil, origin, err
		}
		return []issue{{number: fi.Number, title: fi.Title}}, origin, nil
	}
	fmt.Printf("==> querying open '%s' issues in %s\n", c.label, c.repoSlug)
	rawIssues, err := it.ListIssues(forge.Dispatchable)
	if err != nil {
		return nil, origin, err
	}
	var issues []issue
	for _, fi := range rawIssues {
		issues = append(issues, issue{number: fi.Number, title: fi.Title})
	}
	return issues, origin, nil
}

// recoverByNumber resolves the open non-draft PR for the issue numbered issueNum
// and drives the adopt-and-gate path: the sole way an agent-in-progress issue
// is ever adopted, gated on the operator's explicit agent-recover label (see
// .github/workflows/agent-recover.yml) rather than any automatic sweep (#600).
// Returns an error when the issue cannot be fetched, the PR is a draft, or no
// open PR exists; the caller should treat those as non-success exits.
func recoverByNumber(c config, it forge.IssueTracker, cf forge.CodeForge, pwd string, f *dispatch.Factory, s settle.Settler, issueNum string) error {
	fi, err := it.Issue(issueNum)
	if err != nil {
		return fmt.Errorf("issue %s: %w", issueNum, err)
	}
	iss := issue{number: fi.Number, title: fi.Title}
	branch := cf.AgentBranch(iss.number)
	res, prErr := forge.ResolveOpenPR(cf, iss.number)
	if prErr != nil {
		return fmt.Errorf("issue %s: resolve PR: %w", issueNum, prErr)
	}
	if !res.Found {
		fmt.Printf("    #%s  status=skipped  note=no open PR on %s\n", issueNum, branch)
		return fmt.Errorf("issue %s: no open PR", issueNum)
	}
	if res.IsDraft {
		fmt.Printf("    #%s  landing=%s  status=skipped  note=draft PR; recover operates on non-draft PRs only\n", issueNum, res.URL)
		return fmt.Errorf("issue %s: draft PR", issueNum)
	}
	if err := os.MkdirAll(filepath.Join(pwd, "logs"), 0o755); err != nil {
		return fmt.Errorf("mkdir logs: %w", err)
	}
	d := f.New(iss.number, iss.title)
	defer d.Close()
	s.SettleAdopted(d, iss.number, 0, res.URL)
	return nil
}

// run is the orchestration logic for the `dispatch` subcommand: preflight,
// stranded-issue reconciliation, discovery, dependency-graph construction,
// and drain/wave/fan-out dispatch. lc is wired by bootstrap in production;
// tests construct it directly with fakes.
func run(lc *launchContext) error {
	c, it, cf, f, s, pwd := lc.config, lc.issueTracker, lc.codeForge, lc.factory, lc.settle, lc.pwd

	fmt.Printf("repo: %s  merge-mode: %s\n", c.repoSlug, c.mergeMode)

	if err := checkAutoMergePreflight(c, cf); err != nil {
		return err
	}

	// A bare agent-in-progress issue is never adopted automatically here: it
	// carries no liveness signal, so it cannot be told apart from an issue a
	// live runner (another Box, or an overlapping local run) is actively
	// committing to right now (#600). The only adopt path is the explicit,
	// operator-driven `spindrift recover <n>`, fired by the agent-recover
	// label — see recoverByNumber and .github/workflows/agent-recover.yml.
	if resolveOrigin(c) == waves.OriginDiscovered && c.continuousDispatch {
		return runContinuousDispatch(c, it, cf, pwd, f, s, runner.NixEvaluator{})
	}

	issues, origin, err := discoverIssues(c, it)
	if err != nil {
		return err
	}

	if origin == waves.OriginDiscovered && len(issues) == 0 {
		fmt.Printf("no open '%s' issues — nothing to do.\n", c.label)
		return errQueueEmpty
	}

	// Build the dependency graph for the batch.
	result, err := waves.BuildEdges(it, toWaveIssues(issues))
	if err != nil {
		return err
	}

	plan, err := waves.NewPlan(wavesConfig(c), waves.Input{Origin: origin, Issues: toWaveIssues(issues), Edges: result.Edges, Sources: result.Sources, Failed: result.Failed})
	if err != nil {
		return err
	}
	if err := waves.Run(wavesConfig(c), it, cf, pwd, f, s, plan); err != nil {
		return err
	}

	fmt.Printf("==> all agents finished — branches pushed and PRs opened on %s.\n", c.repoSlug)
	return nil
}

// runContinuousDispatch is the entry point for CONTINUOUS_DISPATCH: the
// opt-in slot-refill dispatch mode (#527). It performs the same empty-queue
// check as the batch path (errQueueEmpty), then hands off to
// waves.RunContinuous with a Discoverer that re-runs the label query and
// edge build on every refill, and a FreshnessChecker wired to
// freshness.Probe against the fetched base-branch tip. eval is injected so
// tests can substitute a Fake instead of shelling out to nix — mirrors
// previewIssues's own eval parameter.
func runContinuousDispatch(c config, it forge.IssueTracker, cf forge.CodeForge, pwd string, f *dispatch.Factory, s settle.Settler, eval freshness.Evaluator) error {
	issues, origin, err := discoverIssues(c, it)
	if err != nil {
		return err
	}
	if origin == waves.OriginDiscovered && len(issues) == 0 {
		fmt.Printf("no open '%s' issues — nothing to do.\n", c.label)
		return errQueueEmpty
	}

	discover := func() ([]waves.Issue, map[string][]string, waves.Sources, map[string]bool, error) {
		issues, _, err := discoverIssues(c, it)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		waveIssues := toWaveIssues(issues)
		result, err := waves.BuildEdges(it, waveIssues)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		return waveIssues, result.Edges, result.Sources, result.Failed, nil
	}

	fresh := func() (bool, bool, string) {
		res := freshness.Probe(c.runtime, pwd, c.baseBranch, c.flakeImageAttr, c.imageTag, eval)
		return res.Applicable, res.Fresh, res.Message
	}

	if err := waves.RunContinuous(wavesConfig(c), it, cf, pwd, f, s, discover, fresh); err != nil {
		return err
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

// cmdConsole is the `console` subcommand: the interactive picks-only
// driving loop (#645, #646). Unlike cmdDoctor, it needs the full
// runner/dispatch/settle wiring bootstrap provides — a Pick launches a real
// Dispatch — so it goes through bootstrap like cmdDispatch. Fresh and
// RebuildFn wire the same freshness.Probe seam runContinuousDispatch uses
// for the headless exit-4 path into an in-session banner/hold plus a
// one-key rebuild instead of an exit (issue #652). lc is wired by bootstrap
// in production; tests construct it directly with fakes. stdin/stdout are
// threaded explicitly (mirroring cmdDoctor/runDoctor's io.Reader/io.Writer
// split) rather than reading os.Stdin/os.Stdout directly, so a test can drive
// the real Bubble Tea program with a scripted reader instead of a live TTY.
func cmdConsole(lc *launchContext, stdin io.Reader, stdout io.Writer) int {
	defer lc.cleanup()
	fresh, rebuild := newConsoleFreshness(lc.config, lc.pwd, runner.NixEvaluator{},
		func() (string, error) { return consoleGitSync(lc.pwd, lc.config.baseBranch) },
		func() (string, error) { return consoleNixBuild(lc.pwd) })
	launch := &console.Launcher{
		CodeForge:   lc.codeForge,
		Factory:     lc.factory,
		Settle:      lc.settle,
		Queue:       console.NewQueue(),
		MaxParallel: lc.config.maxParallel,
		FailedLabel: lc.config.failedLabel,
		Fresh:       fresh,
		RebuildFn:   rebuild,
		RecoverFn: func(issueNum string) error {
			return recoverByNumber(lc.config, lc.issueTracker, lc.codeForge, lc.pwd, lc.factory, lc.settle, issueNum)
		},
	}
	if err := console.Run(lc.issueTracker, lc.pwd, stdin, stdout, launch); err != nil {
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
	if err := recoverByNumber(lc.config, lc.issueTracker, lc.codeForge, lc.pwd, lc.factory, lc.settle, issueNum); err != nil {
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

// selectiveDispatchExitCode translates selectiveListDispatch's result into
// the launcher's process exit code: 3 for open issues that exist but none
// are dispatchable (the same ErrOpenNoneDispatchable sentinel the queue
// path uses — a selective wave can defer every listed issue just as a
// queue drain can), 1 for any other error, 0 on success. Split out from
// cmdDispatchSelective so it's unit-testable against a fake-populated
// launchContext without going through bootstrap.
func selectiveDispatchExitCode(lc *launchContext, nums []string, forceYes bool) int {
	err := selectiveListDispatch(lc.config, lc.issueTracker, lc.codeForge, lc.pwd, lc.factory, lc.settle, nums, forceYes, os.Stdin, os.Stdout)
	if err == nil {
		return 0
	}
	if errors.Is(err, waves.ErrOpenNoneDispatchable) {
		return 3
	}
	fmt.Fprintf(os.Stderr, "%s\n", err)
	return 1
}

// cmdDispatchSelective is the `dispatch <nums>` subcommand: an
// operator-supplied issue list that bypasses the label/barrier gates. lc is
// wired by bootstrap in production; tests construct it directly with fakes.
func cmdDispatchSelective(lc *launchContext, nums []string, forceYes bool) int {
	defer lc.cleanup()
	return selectiveDispatchExitCode(lc, nums, forceYes)
}

// runExitCode translates run's result into the launcher's process exit
// code: 2 for an empty dispatch queue, 3 for open issues that exist but
// none are dispatchable, 4 for CONTINUOUS_DISPATCH mode stopping on a stale
// image, 1 for any other error, 0 on success. Split out from cmdDispatch so
// it's unit-testable against a fake-populated launchContext without going
// through bootstrap.
func runExitCode(lc *launchContext) int {
	err := run(lc)
	if err == nil {
		return 0
	}
	if errors.Is(err, errQueueEmpty) {
		return 2
	}
	if errors.Is(err, waves.ErrOpenNoneDispatchable) {
		return 3
	}
	if errors.Is(err, waves.ErrImageStale) {
		return 4
	}
	fmt.Fprintf(os.Stderr, "%s\n", err)
	return 1
}

// cmdDispatch is the `dispatch` subcommand: drain the labeled queue. lc is
// wired by bootstrap in production; tests construct it directly with fakes.
func cmdDispatch(lc *launchContext) int {
	defer lc.cleanup()
	return runExitCode(lc)
}

// mainRun parses argv and dispatches to the selected subcommand, returning
// the process exit code. It contains no business logic of its own beyond
// arg parsing and subcommand selection. stdout/stderr are injected so tests
// can assert on help/error output without touching the real process streams.
func mainRun(argv []string, stdout, stderr io.Writer) int {
	help, helpAll := false, false
	for _, a := range argv {
		switch a {
		case "--help", "-h":
			help = true
		case "--all":
			helpAll = true
		case "--version":
			printVersion(stdout)
			return 0
		}
	}
	// Snapshot ambient-env deprecation warnings before parseFlags mutates the
	// environment via os.Setenv, so a flag that also sets the same var never
	// masks the ambient value the warning reports on (ADR 0020). Snapshotted
	// ahead of the help/bare-invocation early returns below so both still
	// surface the warning instead of silently dropping it (issue #814).
	var ambientWarnings bytes.Buffer
	warnAmbientKnobEnv(&ambientWarnings)
	if help {
		stderr.Write(ambientWarnings.Bytes())
		if helpAll {
			printHelpFull(stdout)
		} else {
			printHelp(stdout)
		}
		return 0
	}
	inputPath, argv, err := extractInputFlag(argv)
	if err != nil {
		fmt.Fprintf(stderr, "%s\n", err)
		return 1
	}
	args, err := parseFlags(argv)
	if err != nil {
		fmt.Fprintf(stderr, "%s\n", err)
		return 1
	}
	if len(args) == 0 {
		// Bare `spindrift`: print help rather than silently dispatching
		// (issue #555). `dispatch` remains the sole way to drain the queue.
		stderr.Write(ambientWarnings.Bytes())
		printHelp(stdout)
		return 0
	}
	if inputPath != "" {
		doc, err := loadInputDocument(inputPath)
		if err != nil {
			fmt.Fprintf(stderr, "%s\n", err)
			return 1
		}
		loadedDoc = doc
	}
	stderr.Write(ambientWarnings.Bytes())
	if args[0] == "build" {
		return cmdBuild()
	}
	if args[0] == "doctor" {
		return cmdDoctor()
	}
	if args[0] == "console" {
		lc, err := bootstrap(true, dispatchKindWork)
		if err != nil {
			fmt.Fprintf(stderr, "%s\n", err)
			return 1
		}
		return cmdConsole(lc, os.Stdin, os.Stdout)
	}
	if args[0] == "recover" {
		if len(args) < 2 {
			fmt.Fprintln(stderr, "usage: spindrift recover <issue-number>")
			return 1
		}
		lc, err := bootstrap(true, dispatchKindWork)
		if err != nil {
			fmt.Fprintf(stderr, "%s\n", err)
			return 1
		}
		return cmdRecover(lc, args[1])
	}
	if args[0] == "preview" {
		return cmdPreview(dispatchIssueArgs(args[1:]))
	}
	if args[0] == "dispatch" {
		noBuild, dispatchArgs := dispatchNoBuildArgs(args[1:])
		forceYes, dispatchArgs := dispatchYesArgs(dispatchArgs)
		nums := dispatchIssueArgs(dispatchArgs)
		lc, err := bootstrap(!noBuild, dispatchKindWork)
		if err != nil {
			fmt.Fprintf(stderr, "%s\n", err)
			return 1
		}
		if len(nums) > 0 {
			return cmdDispatchSelective(lc, nums, forceYes)
		}
		return cmdDispatch(lc)
	}
	if args[0] == "research" {
		noBuild, researchArgs := dispatchNoBuildArgs(args[1:])
		forceYes, researchArgs := dispatchYesArgs(researchArgs)
		nums := dispatchIssueArgs(researchArgs)
		lc, err := bootstrap(!noBuild, dispatchKindResearch)
		if err != nil {
			fmt.Fprintf(stderr, "%s\n", err)
			return 1
		}
		if len(nums) > 0 {
			return cmdDispatchSelective(lc, nums, forceYes)
		}
		return cmdDispatch(lc)
	}
	// Unrecognized subcommand: print help rather than silently dispatching
	// (issue #555).
	fmt.Fprintf(stderr, "unknown subcommand: %s\n\n", args[0])
	printHelp(stderr)
	return 1
}

func main() {
	os.Exit(mainRun(os.Args[1:], os.Stdout, os.Stderr))
}

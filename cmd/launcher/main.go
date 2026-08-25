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
	"strings"
	"time"

	"spindrift.dev/launcher/internal/console"
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/local"
	"spindrift.dev/launcher/internal/freshness"
	"spindrift.dev/launcher/internal/localloop"
	"spindrift.dev/launcher/internal/reconcile"
	"spindrift.dev/launcher/internal/retry"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/settle"
	"spindrift.dev/launcher/internal/waves"
)

type config struct {
	// schemaConfig carries every schema-derived member (issue #2364,
	// #2365): one field per lib/env-schema.nix host-config entry, loaded by
	// loadSchemaConfig(). Embedded by value (not pointer) so a
	// copy-and-mutate helper like applyDispatchKind can never alias the
	// caller's config through the embedded struct.
	schemaConfig

	// OCI image config (baked by nix wrapper; empty for bwrap)
	imageArchive      string
	imageTag          string
	imageDrv          string
	nixBuilderImage   string
	nixVolume         string
	flakeImageAttr    string
	flakeLauncherAttr string

	// loadedLauncherHash is the bare 32-char nix store hash of the
	// launcher-currency flake package this launcher binary was built from
	// (LAUNCHER_CURRENCY_HASH, rendered by lib/preambles.nix/lib/mkHarness.nix
	// alongside flakeLauncherAttr — issue #2677, issue #1364 slice 4).
	// freshness.Probe compares it against a freshly-evaluated hash at the
	// base-branch tip to answer the launcher-staleness dimension.
	loadedLauncherHash string

	// bwrap agent closure paths (bwrap only)
	agentFiles    string
	agentEnv      string
	agentFilesDrv string // .drv path; used by `launcher build` to realize the closure
	agentEnvDrv   string // .drv path; used by `launcher build` to realize the closure
	bakedPrefetch string

	// Runtime: podman | docker | rancher | bwrap (runner.ValidValues)
	runtime string

	// runnerKind selects the launcher's runner implementation: "bwrap" or
	// "oci" (issue #2538). Nix-rendered alongside runtime, but a distinct
	// artifact — runner selection reads this, never runtime's raw value,
	// since runtime also carries operator-facing runtime *names* (podman,
	// docker, rancher) that aren't "oci" literally.
	runnerKind string

	// driver selects the Go Driver strategy (ADR 0009): transient
	// classification and heartbeat parsing. Empty defaults to "claude",
	// matching the nix side's default.
	driver string

	// image is the runtime image reference; defaults to imageTag
	image string

	// driverSessionCacheDir is the in-box mount target for the selected
	// Driver's session-state dir (ADR 0009), nix-baked at wrap time. Empty
	// when the Driver declares no session-state dir.
	driverSessionCacheDir string

	// Space-separated list of env var names to forward into each Box container.
	// Set by the nix-rendered preamble from the schema's boxEnv=true entries so
	// the Go source never needs to enumerate them by hand.
	boxEnvVars string

	// dispatchKind is "work" (the default, zero value) or "research" (ADR
	// 0022). Set once by bootstrap via applyDispatchKind, never read from
	// the environment directly — it is operator intent carried by which
	// subcommand launched (dispatch vs research), not a config knob.
	dispatchKind string

	// selfContained is the research kind's no-repo sub-mode (issue #2202,
	// --self-contained): the Box clones no repo and explores none, and startup
	// validation permits the no-REPO_SLUG/no-GH_TOKEN configuration. Set only by
	// the research subcommand handler; rejected by validate for any other kind.
	selfContained bool
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
	number   string
	title    string
	priority forge.Priority
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

// floatSchemaDefault parses key's schema default as a float64; a
// non-numeric or absent default parses to 0.
func floatSchemaDefault(key string) float64 {
	n, _ := strconv.ParseFloat(schemaDefault(key), 64)
	return n
}

// floatNonnegSchema parses key's env value as a non-negative float64 (the
// USD-budget counterpart to atoiNonnegSchema), falling back to its schema
// default; a negative or unparseable value also falls back to the default.
func floatNonnegSchema(key string) float64 {
	if n, err := strconv.ParseFloat(os.Getenv(key), 64); err == nil && n >= 0 {
		return n
	}
	return floatSchemaDefault(key)
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

	sc := loadSchemaConfig()
	sc.gitUserName = gitIdentityField("GIT_USER_NAME", "user.name")
	sc.gitUserEmail = gitIdentityField("GIT_USER_EMAIL", "user.email")
	sc.codeForgeAccumulationRepoDir = absCodeForgeAccumulationRepoDir(sc.codeForge, getenvSchema("CODE_FORGE_ACCUMULATION_REPO_DIR"))

	runtime := getenvArtifact("RUNTIME", "")
	// runnerKind is read straight from the RUNNER_KIND artifact/env with no
	// runtime-name fallback: the nix pipeline always renders RUNNER_KIND
	// alongside RUNTIME (issue #2538 AC1 — "no runtime override path, so no
	// Go guard is needed"). A direct binary invocation with no --input
	// document (inputdoc.go documents this as a supported path: tests,
	// manual debugging) that also omits RUNNER_KIND gets runnerKind == "",
	// which runnerForKind/buildRunnerForKind's c.runnerKind == "bwrap" check
	// treats as "oci" — the same default an absent artifact gets everywhere
	// else in this file.
	runnerKind := getenvArtifact("RUNNER_KIND", "")

	return config{
		schemaConfig: sc,

		imageArchive:       getenvArtifact("IMAGE_ARCHIVE", ""),
		imageTag:           imageTag,
		imageDrv:           getenvArtifact("IMAGE_DRV", ""),
		nixBuilderImage:    getenvArtifact("NIX_BUILDER_IMAGE", ""),
		nixVolume:          getenvArtifact("NIX_VOLUME", "spindrift-nix"),
		flakeImageAttr:     getenvArtifact("FLAKE_IMAGE_ATTR", ""),
		flakeLauncherAttr:  getenvArtifact("FLAKE_LAUNCHER_ATTR", ""),
		loadedLauncherHash: getenvArtifact("LAUNCHER_CURRENCY_HASH", ""),
		agentFiles:         getenvArtifact("AGENT_FILES", ""),
		agentEnv:           getenvArtifact("AGENT_ENV", ""),
		agentFilesDrv:      getenvArtifact("AGENT_FILES_DRV", ""),
		agentEnvDrv:        getenvArtifact("AGENT_ENV_DRV", ""),
		bakedPrefetch:      getenvArtifact("BAKED_PREFETCH", ""),
		runtime:            runtime,
		runnerKind:         runnerKind,
		driver:             getenvArtifact("DRIVER", ""),
		image:              image,

		driverSessionCacheDir: getenvArtifact("DRIVER_SESSION_CACHE_DIR", ""),

		boxEnvVars: getenvArtifact("BOX_ENV_VARS", ""),
	}
}

// capabilitySignals bundles the four capability bits nix resolves per
// CODE_FORGE/ISSUE_TRACKER pairing (lib/backends/default.nix).
type capabilitySignals struct {
	hostMediatedRemote      bool
	outboxRelayCapable      bool
	inBoxUnreachableTracker bool
	fullyLocal              bool
}

// resolveCapabilitySignals returns the capability signals for the
// codeForge/issueTracker pairing actually in effect this run. The
// nix-forwarded artifacts (docArtifact) describe whatever pairing was
// baked into the --input document at build time; a later CLI flag or env
// var overriding CODE_FORGE/ISSUE_TRACKER away from that baked pairing (or
// no document at all — direct binary invocation, tests) moves the pairing
// out from under the baked artifacts, so this falls back to a registry
// lookup on the resolved names instead of trusting a forwarded bool that
// would silently describe the wrong backend (issue #2527 review). A document
// whose Artifacts section carries none of the four keys at all (predates
// this feature, or a rendering bug), or only some of them (a partial or
// malformed render), falls back the same way, rather than letting a missing
// key's docArtifact(key) == "true" comparison silently read as false (issue
// #2527 review, absent-key and partial-key findings) -- the trust branch
// below requires all four keys present, not merely any one of them.
func resolveCapabilitySignals(codeForge, issueTracker string) capabilitySignals {
	if loadedDoc != nil && codeForge == loadedDoc.Settings["CODE_FORGE"] && issueTracker == loadedDoc.Settings["ISSUE_TRACKER"] {
		_, hostMediatedRemotePresent := loadedDoc.Artifacts["HOST_MEDIATED_REMOTE"]
		_, outboxRelayCapablePresent := loadedDoc.Artifacts["OUTBOX_RELAY_CAPABLE"]
		_, inBoxUnreachableTrackerPresent := loadedDoc.Artifacts["IN_BOX_UNREACHABLE_TRACKER"]
		_, fullyLocalPresent := loadedDoc.Artifacts["FULLY_LOCAL"]
		if hostMediatedRemotePresent && outboxRelayCapablePresent && inBoxUnreachableTrackerPresent && fullyLocalPresent {
			return capabilitySignals{
				hostMediatedRemote:      docArtifact("HOST_MEDIATED_REMOTE") == "true",
				outboxRelayCapable:      docArtifact("OUTBOX_RELAY_CAPABLE") == "true",
				inBoxUnreachableTracker: docArtifact("IN_BOX_UNREACHABLE_TRACKER") == "true",
				fullyLocal:              docArtifact("FULLY_LOCAL") == "true",
			}
		}
	}
	codeForgeRow, _ := backendByName(codeForge)
	trackerRow, _ := backendByName(issueTracker)
	hostMediatedRemote := codeForgeRow.HostMediatedRemote
	inBoxUnreachableTracker := trackerRow.InBoxUnreachableTracker
	return capabilitySignals{
		hostMediatedRemote:      hostMediatedRemote,
		outboxRelayCapable:      codeForgeRow.OutboxRelayCapable,
		inBoxUnreachableTracker: inBoxUnreachableTracker,
		fullyLocal:              hostMediatedRemote && inBoxUnreachableTracker,
	}
}

// trackerAxisSignals mirrors lib/backends/default.nix's registry rows
// (issue #2533 review): reads the tracker-axis facts straight off the
// matching backendRows entry instead of re-deriving the mapping with its
// own name switch, so nix and Go read the one place the mapping is
// declared and can never drift on it. An empty TrackerAxisRead covers two
// cases at once, both resolving to the same GITHUB/GITHUB/GH defaults: an
// unregistered name (no row at all), and github/jira's own rows, whose
// real resolved value IS "GITHUB" but whose Go zero value leaves the field
// unset rather than spelling out the literal -- the same default arm the
// old switch's `default:` case used either way.
func trackerAxisSignals(issueTracker string) (read, write, filer string) {
	row, ok := backendByName(issueTracker)
	if !ok || row.TrackerAxisRead == "" {
		return "GITHUB", "GITHUB", "GH"
	}
	// TrackerAxisWrite is read as-is: unlike TrackerAxisRead/TrackerAxisFiler,
	// "" is a legitimate resolved value for a found row (local, which has
	// no write-step axis at all), not an unset-field placeholder. Filer
	// gets the same "GH" default a found row's own Go zero value produces
	// when it doesn't override trackerAxisFiler (e.g. local's registry row
	// omits it) -- matching lib/mkHarness.nix's
	// `issueTrackerRow.trackerAxisFiler or "GH"` in effect for every row
	// registered today, though the two mechanisms differ (nix's `or` fires
	// on attribute absence; this fires on the empty string the renderer
	// produces for an absent attribute) -- distinct from write's Go zero
	// value, which for local IS the real resolved value.
	filer = row.TrackerAxisFiler
	if filer == "" {
		filer = "GH"
	}
	return row.TrackerAxisRead, row.TrackerAxisWrite, filer
}

// forgeBackendSignal mirrors lib/backends/default.nix's registry rows
// (issue #2533 review), the same registry-driven shape as
// trackerAxisSignals above.
func forgeBackendSignal(codeForge string) string {
	row, ok := backendByName(codeForge)
	if !ok || row.ForgeBackend == "" {
		return "GH"
	}
	return row.ForgeBackend
}

// resolveTrackerAndForgeSignals returns the tracker-axis/forge-backend
// signals for the codeForge/issueTracker pairing actually in effect this
// run, mirroring resolveCapabilitySignals's trust-then-fallback shape
// (issue #2527) for exactly the same reason: TRACKER_AXIS_READ/WRITE/FILER
// and FORGE_BACKEND are derived purely from ISSUE_TRACKER/CODE_FORGE, both
// flakeOption (not boxEnvOnly) knobs an operator can override at dispatch
// time independent of whatever pairing was baked into the --input document
// at image-build time. Trust the document's forwarded artifacts only when
// the resolved pairing matches what was baked in (loadedDoc.Settings) and
// every needed artifact key is present; otherwise fall back to
// trackerAxisSignals/forgeBackendSignal, the pure Go mirror of
// lib/mkHarness.nix's own computation, rather than reading an absent
// docArtifact key as "" (issue #2533 review).
func resolveTrackerAndForgeSignals(codeForge, issueTracker string) (read, write, filer, forge string) {
	if loadedDoc != nil && codeForge == loadedDoc.Settings["CODE_FORGE"] && issueTracker == loadedDoc.Settings["ISSUE_TRACKER"] {
		_, readOK := loadedDoc.Artifacts["TRACKER_AXIS_READ"]
		_, writeOK := loadedDoc.Artifacts["TRACKER_AXIS_WRITE"]
		_, filerOK := loadedDoc.Artifacts["TRACKER_AXIS_FILER"]
		_, forgeOK := loadedDoc.Artifacts["FORGE_BACKEND"]
		if readOK && writeOK && filerOK && forgeOK {
			return docArtifact("TRACKER_AXIS_READ"), docArtifact("TRACKER_AXIS_WRITE"), docArtifact("TRACKER_AXIS_FILER"), docArtifact("FORGE_BACKEND")
		}
	}
	read, write, filer = trackerAxisSignals(issueTracker)
	return read, write, filer, forgeBackendSignal(codeForge)
}

// resolveAgentPresenceSignals returns the roster/orchestration-derived gate
// signals (FILER_ENABLED, WORKER_PROVISIONED, REVIEW_LOOP_INLINE,
// REVIEW_LOOP_ORCHESTRATOR) for this run, mirroring
// resolveTrackerAndForgeSignals's trust-then-fallback shape (issue #2527)
// but with two INDEPENDENT trust gates rather than one shared gate spanning
// all four (issue #2533 review): the pairs have different override
// semantics, so a single "everything must match live" gate over-fires on
// the first pair and would under-fire on the second if loosened uniformly.
//
// FILER_ENABLED/WORKER_PROVISIONED are baked once, at image-build/eval
// time, from agentsJsonTemplate's own parsed keys (lib/mkHarness.nix), and
// agentsJsonTemplate is itself a FIXED value: either derived from the
// *configured* filerModel/workerModel at eval time (lib/image.nix -- "not a
// :-default... derived from the configured models, not a standalone knob"),
// or unconditionally "" for the opencode driver regardless of roster
// contents (lib/drivers/opencode.nix, which provisions subagents via
// on-disk agents/*.md files instead). Either way, a dispatch-time
// FILER_MODEL/WORKER_MODEL env override has ZERO effect on the real
// --agents roster the box uses, so these two artifacts are trusted whenever
// both keys are present in the document, regardless of whether the live
// FILER_MODEL/WORKER_MODEL match what the document baked into its Settings
// section -- trusting them conditionally on a live-model match would
// produce a rendered-prompt divergence from what's actually baked into the
// image (e.g. FILER_MODEL=haiku overridden at dispatch time against an
// image built with filerModel="" must still report filerEnabled=false).
//
// REVIEW_LOOP_INLINE/REVIEW_LOOP_ORCHESTRATOR are different:
// ORCHESTRATOR_ENABLED is boxEnv=true (lib/env-schema.nix), so
// buildBoxEnv/resolveBoxEnvVar (dispatch.go) forward whatever it resolves to
// in the *ambient* environment at dispatch time, independent of what was
// baked into the document at image-build time, and entrypoint.sh reads it
// live at runtime ("[ -n "${ORCHESTRATOR_ENABLED:-}" ] && ORCHESTRATOR=1").
// A dispatch-time override therefore genuinely changes box behavior, so
// these two artifacts stay gated on the live value matching what the
// document baked in -- trusting a stale artifact here would hand the box
// off to the orchestrator ($ORCHESTRATOR, sourced from that same ambient
// ORCHESTRATOR_ENABLED) while still rendering the inline review-loop
// section, or vice versa (exactly the divergence 4d36a298 fixed for the
// tracker axis, issue #2533 review). Compare the live value (getenvSchema,
// ambient-env-first) against what the document baked in
// (loadedDoc.Settings) and trust the document's REVIEW_LOOP_* artifacts only
// when it matches; otherwise fall back to the live value directly instead
// of schemaDefault's document-first read, so an override is honored on both
// the trust check and the fallback consistently.
//
// On any individual missing-artifact-key fallback within either pair, the
// fallback computation for REVIEW_LOOP_INLINE/REVIEW_LOOP_ORCHESTRATOR is
// unchanged: !orchestratorOn/orchestratorOn. FILER_ENABLED/WORKER_PROVISIONED
// fall back to driver-aware mirror of agentsJsonTemplate above (opencode:
// always false, matching lib/drivers/opencode.nix, pinned by
// mkharness-filer-worker-false-for-opencode-driver; every other driver:
// filerModel != ""/workerModel != "", matching lib/drivers/claude.nix's
// "#392 semantics" empty-model drop) rather than a driver-blind
// filerModel != ""/workerModel != "" that would report filerEnabled=true
// for an opencode box with a configured FILER_MODEL even though nix always
// bakes FILER_ENABLED=false for that Driver (issue #2533 review).
// WorkerProvisioned in particular defaults false under the old
// all-four-or-nothing behavior even though workerModel's own schema default
// is non-empty (the worker is provisioned by default), and defaulting both
// review-loop bools false violates their exactly-one-true invariant (issue
// #2533 review).
func resolveAgentPresenceSignals(driver string) (filerEnabled, workerProvisioned, reviewLoopInline, reviewLoopOrchestrator bool) {
	filerModel := getenvSchema("FILER_MODEL")
	workerModel := getenvSchema("WORKER_MODEL")
	orchestratorEnabled := getenvSchema("ORCHESTRATOR_ENABLED")

	if driver == "opencode" {
		filerEnabled, workerProvisioned = false, false
	} else {
		filerEnabled, workerProvisioned = filerModel != "", workerModel != ""
	}
	if loadedDoc != nil {
		_, filerOK := loadedDoc.Artifacts["FILER_ENABLED"]
		_, workerOK := loadedDoc.Artifacts["WORKER_PROVISIONED"]
		if filerOK && workerOK {
			// AGENTS_JSON_TEMPLATE is a fixed, non-overridable bake (see
			// doc comment above) -- trust it regardless of whether the
			// live FILER_MODEL/WORKER_MODEL match the document's baked
			// Settings.
			filerEnabled = docArtifact("FILER_ENABLED") == "true"
			workerProvisioned = docArtifact("WORKER_PROVISIONED") == "true"
		}
	}

	// A bool-kind schema knob's live value is the ambient-env/flag convention
	// ("1" or "", set by parseFlags's byBool handling and Nix's own
	// toString-of-bool documentSettings rendering) -- never the literal
	// string "true" the Artifacts-section docArtifact reads above compare
	// against (preambles.nix's runArtifacts renders those explicitly as
	// "true"/"false", a distinct convention).
	orchestratorOn := orchestratorEnabled != ""
	reviewLoopInline, reviewLoopOrchestrator = !orchestratorOn, orchestratorOn
	if loadedDoc != nil && orchestratorEnabled == loadedDoc.Settings["ORCHESTRATOR_ENABLED"] {
		_, inlineOK := loadedDoc.Artifacts["REVIEW_LOOP_INLINE"]
		_, orchOK := loadedDoc.Artifacts["REVIEW_LOOP_ORCHESTRATOR"]
		if inlineOK && orchOK {
			reviewLoopInline = docArtifact("REVIEW_LOOP_INLINE") == "true"
			reviewLoopOrchestrator = docArtifact("REVIEW_LOOP_ORCHESTRATOR") == "true"
		}
	}
	return filerEnabled, workerProvisioned, reviewLoopInline, reviewLoopOrchestrator
}

func validate(c config) error {
	if c.selfContained && c.dispatchKind != dispatchKindResearch {
		return fmt.Errorf("--self-contained is only valid for the research dispatch kind")
	}
	// See checks.go's repoRequirementExemptionFor for the REPO_SLUG/GH_TOKEN
	// exemption logic (fully-local runs, self-contained research with an
	// in-Box-unreachable tracker).
	if err := doctor.RunRequiredFailFast(launcherRequiredKnobChecks(c)); err != nil {
		return err
	}
	if err := validateChoice("MERGE_MODE", c.mergeMode); err != nil {
		return err
	}
	if err := validateChoice("MERGE_METHOD", c.mergeMethod); err != nil {
		return err
	}
	if err := validateChoice("SYNC_METHOD", c.syncMethod); err != nil {
		return err
	}
	if err := validateChoice("OVERLAP_GATE", c.overlapGate); err != nil {
		return err
	}
	if err := validateChoice("NETWORK_MODE", c.networkMode); err != nil {
		return err
	}
	if err := doctor.RunRequiredFailFast(launcherCrossKnobChecks(c)); err != nil {
		return err
	}
	if err := validateChoice("BOX_FORGE_AND_ISSUE_ACCESS", c.boxForgeAndIssueAccess); err != nil {
		return err
	}
	if _, err := forge.ParseResearchVerdicts(c.researchVerdicts); err != nil {
		return err
	}
	return nil
}

// repoBanner formats the "repo: ... merge-mode: ..." line preview and run
// print at the top of a dispatch, omitting the repo segment when repoSlug is
// empty — the fully-local case (CODE_FORGE=local && ISSUE_TRACKER=local)
// where no target repo exists, so a bare "repo: " would print nothing useful.
func repoBanner(c config) string {
	if c.repoSlug == "" {
		return fmt.Sprintf("merge-mode: %s", c.mergeMode)
	}
	return fmt.Sprintf("repo: %s  merge-mode: %s", c.repoSlug, c.mergeMode)
}

// dispatchLabels builds the DispatchLabels mapping from loaded config.
// Recoverable is a fixed string literal, not sourced from config/env: it
// only ever applies to CODE_FORGE=local push-only runs and is stored as a
// local frontmatter marker, never a real GitHub label, so it doesn't need
// an env-configurable knob (which would also mean touching the
// Nix-generated lib/env-schema.nix / flagtable_gen.go files) (#2254).
// Ambiguous is likewise a fixed string literal: unlike Recoverable it is a
// real GitHub label (the advisory tier doctor.AmbiguousLabelNames() checks
// for), but issue #2275 doesn't ask for it to be operator-configurable
// either, so it gets the same fixed treatment.
func dispatchLabels(c config) forge.DispatchLabels {
	return forge.DispatchLabels{
		Dispatchable: c.label,
		InProgress:   c.inProgressLabel,
		Complete:     c.completeLabel,
		Failed:       c.failedLabel,
		Recoverable:  "agent-recoverable",
		Ambiguous:    "agent-ambiguous-spec",
	}
}

// researchVerdictLabels returns the configured verdict-label mapping for
// research-kind construction (RESEARCH_VERDICTS; defaults to the built-in
// three), or the zero value for work — only ResearchSettle ever calls
// CompleteVerdict, so a work-kind tracker carrying a zero VerdictLabels is
// inert.
func researchVerdictLabels(c config) forge.VerdictLabels {
	if c.dispatchKind == dispatchKindResearch {
		vl, err := forge.ParseResearchVerdicts(c.researchVerdicts)
		if err != nil {
			// validate() already rejects a malformed set before this is
			// reached; fall back to the compiled default set.
			return forge.ResearchVerdictLabels()
		}
		return vl
	}
	return forge.VerdictLabels{}
}

// newIssueTracker returns the IssueTracker adapter selected by ISSUE_TRACKER
// (default "github"), carrying c.dispatchKind's label family (dispatchLabels)
// and verdict labels (researchVerdictLabels) — the kind-aware seam ADR 0022
// describes. Looks up the backend registry (backend.go); an unregistered
// c.issueTracker or a row with no newIssueTracker constructor is unreachable
// post-validate() (which already rejects any ISSUE_TRACKER not registered/
// not validAsTracker) and falls back to github's own constructor, matching
// the pre-registry switch's default case.
func newIssueTracker(c config) forge.IssueTracker {
	row, ok := backendByName(c.issueTracker)
	if !ok || row.newIssueTracker == nil {
		gh, _ := backendByName("github")
		return gh.newIssueTracker(c)
	}
	return row.newIssueTracker(c)
}

// newCodeForge returns the CodeForge adapter selected by CODE_FORGE: "github"
// (open PR, watch CI, merge), "git" (push-only to codeForgeRemoteURL; no PR,
// CI-watch, or merge gate), or "local" (host-mediated landing onto the
// Accumulation repo's Integration branch; ADR 0033). parent is the seam's
// own resolved Integration-branch key (local.ResolveParent, issue #1734);
// every other codeForge ignores it — there is no per-run parent knob left
// to derive it from. Looks up the backend registry (backend.go): an
// unregistered c.codeForge or a row with no newCodeForge constructor is
// unreachable post-validate() and falls back to github, matching the
// pre-registry switch's default case. BOX_FORGE_AND_ISSUE_ACCESS=read-only
// swaps in the row's read-only wrapper when it has one (github, forgejo);
// git and local carry no such wrapper (newReadOnlyCodeForge is nil for
// both, per backend.go), so read-only falls through to the plain
// constructor unchanged, byte-for-byte.
func newCodeForge(c config, parent local.SanitizedParent, it forge.IssueTracker) forge.CodeForge {
	row, ok := backendByName(c.codeForge)
	if !ok || row.newCodeForge == nil {
		row, _ = backendByName("github")
	}
	if c.boxForgeAndIssueAccess == "read-only" && row.newReadOnlyCodeForge != nil {
		return row.newReadOnlyCodeForge(c, parent, it)
	}
	return row.newCodeForge(c, parent, it)
}

// dispatchCompletionBanner returns the forge-aware end-of-dispatch
// completion line for c.codeForge, so the single/wave and continuous
// dispatch paths can share one wording and never drift out of sync
// (issue #1733).
func dispatchCompletionBanner(c config) string {
	switch c.codeForge {
	case "git", "forgejo":
		return fmt.Sprintf("==> all agents finished — branches pushed on %s.\n", c.repoSlug)
	case "local":
		return "==> all agents finished — seams landed host-side into their own Integration branches in the Accumulation repo.\n"
	default:
		// validate() restricts c.codeForge to "git", "local", "forgejo", or
		// github — github is the fallback so a future forge fails loud in
		// validate() rather than silently inheriting this wording.
		return fmt.Sprintf("==> all agents finished — branches pushed and PRs opened on %s.\n", c.repoSlug)
	}
}

// absLocalIssuesDir resolves the local tracker's issues dir to an absolute
// path for the runner's /issues mount source (ADR 0032, issue #1691): the
// OCI/bwrap adapters render Source directly into their bind syntax, so a
// relative path would resolve against the wrong process. Empty stays empty
// (no ISSUE_TRACKER=local configured); a resolution error falls back to dir
// unchanged, matching LocalTracker.Probe()'s own fallback.
func absLocalIssuesDir(dir string) string {
	if dir == "" {
		return ""
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	return abs
}

// absCodeForgeAccumulationRepoDir resolves the Accumulation repo dir (ADR
// 0033) for CODE_FORGE=local to an absolute host path, defaulting an unset
// knob to .spindrift/accum.git under the process cwd rather than requiring
// an operator-supplied path (issue #1726): both the read-only /repo Box
// mount and the host-side landing forge's git subprocesses (which run from
// inside cwd) need the same absolute path, so resolving it once here — and
// storing the result back onto config — keeps every consumer downstream in
// agreement. Other forges leave dir untouched and unresolved, matching the
// field's existing empty-and-unused treatment.
func absCodeForgeAccumulationRepoDir(codeForge, dir string) string {
	if codeForge != "local" {
		return dir
	}
	if dir == "" {
		dir = filepath.Join(".spindrift", "accum.git")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	return abs
}

// runnerConfig builds the runner.Config a runner adapter needs from loaded
// config. Shared by both the `run` and `build` subcommand entry points; the
// build entry point never calls Run(), so leaving PromptDir/SkillsDir/
// PodmanNetwork populated is harmless there.
func runnerConfig(c config) runner.Config {
	sig := resolveCapabilitySignals(c.codeForge, c.issueTracker)
	return runner.Config{
		Runtime:                  c.runtime,
		Image:                    c.image,
		ImageArchive:             c.imageArchive,
		ImageDrv:                 c.imageDrv,
		ImageTag:                 c.imageTag,
		NixBuilderImage:          c.nixBuilderImage,
		NixVolume:                c.nixVolume,
		FlakeImageAttr:           c.flakeImageAttr,
		PodmanNetwork:            c.podmanNetwork,
		NetworkMode:              c.networkMode,
		PidsLimit:                c.pidsLimit,
		MemoryLimit:              c.memoryLimit,
		AgentFiles:               c.agentFiles,
		AgentEnv:                 c.agentEnv,
		AgentFilesDrv:            c.agentFilesDrv,
		AgentEnvDrv:              c.agentEnvDrv,
		BakedPrefetch:            c.bakedPrefetch,
		BwrapUnshareNet:          c.bwrapUnshareNet,
		PromptDir:                c.spindriftPromptDir,
		SkillsDir:                c.spindriftSkillsDir,
		DriverSessionCacheDir:    c.driverSessionCacheDir,
		HostMediatedIssueTracker: sig.inBoxUnreachableTracker,
		LocalIssuesDir:           absLocalIssuesDir(c.localIssuesDir),
		HostMediatedRemote:       sig.hostMediatedRemote,
		AccumulationRepoDir:      c.codeForgeAccumulationRepoDir,
		OutboxRelayCapable:       sig.outboxRelayCapable,
		BoxForgeAndIssueAccess:   c.boxForgeAndIssueAccess,
	}
}

// runnerForKind selects the run-time runner adapter (bwrap or OCI) for
// bootstrap and the reconcile local-tracker liveness probe. Keyed solely on
// c.runnerKind (the RUNNER_KIND document artifact, issue #2538) — never
// c.runtime, which also carries operator-facing runtime *names* (podman,
// docker, rancher) that aren't "oci" literally.
func runnerForKind(c config, rc runner.Config, pwd string) runner.Runner {
	if c.runnerKind == freshness.KindBwrap {
		return runner.NewBwrap(rc)
	}
	return runner.NewOCI(rc, pwd)
}

// buildRunnerForKind is runnerForKind's `launcher build` counterpart: it
// selects NewBwrapBuild instead of NewBwrap for the bwrap arm (build realizes
// store closures rather than running an agent), but keys off the same
// c.runnerKind == freshness.KindBwrap check.
func buildRunnerForKind(c config, rc runner.Config, pwd string) runner.Runner {
	if c.runnerKind == freshness.KindBwrap {
		return runner.NewBwrapBuild(rc)
	}
	return runner.NewOCI(rc, pwd)
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

// localBaseBranchResolver returns resolveBoxEnvVar, except that under
// CODE_FORGE=local it forwards BASE_BRANCH as the seam's Integration branch
// (integration/<parent>, ADR 0033) once cf.BranchExists confirms it's
// there, instead of the operator's real base branch. Any seam's Box needs
// the Integration branch as its clone target once one exists, so it builds
// on whatever has landed so far (issue #1700). But ensureIntegrationBranch
// only ever creates that ref host-side, from inside RelayBundle, once some
// seam actually lands -- a broad ticket's first (or wholly independent)
// seam dispatches before that, so forwarding a not-yet-existing ref would
// break its Box's clone; ResolveEnv falls back to c.baseBranch until
// BranchExists says otherwise (and, defensively, on a BranchExists error --
// logged rather than silent, since ResolveEnv's func(string) string shape
// leaves no error to propagate to its dispatch.Config caller). c.baseBranch
// itself still reaches newCodeForge unchanged either way, since
// ensureIntegrationBranch needs the real base branch to seed
// integration/<parent> the first time a parent's seam lands. A missing
// Integration branch is only silently expected for a blocker-free seam; a
// seam it (via DepsOf) reports has blockers should have been held by the
// #2130 readiness gate until its blocker landed onto this very branch, so
// that combination is logged loudly rather than silently seeded onto bare
// base (issue #2130 complementary hardening).
func localBaseBranchResolver(c config, it forge.IssueTracker, lw *localloop.Wired, cf forge.CodeForge) func(num, name string) string {
	if c.codeForge != "local" {
		return func(_, name string) string { return resolveBoxEnvVar(name) }
	}
	return func(num, name string) string {
		if name == "BASE_BRANCH" {
			// Resolved fresh on every call (once per Box constructed) rather
			// than cached at construction: a later seam's dispatch, within
			// the same continuous run, must see integration/<parent> as it
			// exists at that later moment, not as it stood at process
			// start -- and each num may resolve to a different parent's
			// Integration branch entirely (issue #1734). The parent itself
			// is resolved through lw (issue #1810), the same sealed
			// SanitizedParent value CodeForgeForIssue and Surface consume,
			// not an independent derivation of its own.
			integrationBranch := local.IntegrationBranch(lw.ResolveParent(num))
			exists, err := cf.BranchExists(integrationBranch)
			switch {
			case err == nil && exists:
				return integrationBranch
			case err != nil:
				fmt.Printf("!! BASE_BRANCH: checking %s: %v; falling back to %s\n", integrationBranch, err, c.baseBranch)
			default:
				// integration/<parent> does not exist yet. A blocker-free
				// first (or wholly independent) seam legitimately seeds
				// from base on first dispatch -- stay silent. But a seam
				// that HAS blockers should have been held by the #2130
				// readiness gate until its blocker's work landed onto this
				// very branch; reaching the resolver with the branch still
				// missing means a blocked seam slipped through onto bare
				// base. Make that loud rather than silently seeding the
				// operator base branch.
				deps, derr := it.DepsOf(num)
				switch {
				case derr != nil:
					fmt.Printf("!! BASE_BRANCH: seam #%s: checking blockers: %v; integration branch %s does not exist, falling back to %s -- cannot confirm the #2130 readiness gate held a blocked seam\n", num, derr, integrationBranch, c.baseBranch)
				case len(deps) > 0:
					fmt.Printf("!! BASE_BRANCH: seam #%s has %d blocker(s) but its integration branch %s does not exist; falling back to %s -- the #2130 readiness gate should have held this seam rather than seeding it onto bare base\n", num, len(deps), integrationBranch, c.baseBranch)
				}
			}
		}
		return resolveBoxEnvVar(name)
	}
}

// boxTokenResolver wraps next, overriding a Box's bearer-token BoxEnvVars
// name to the operator's own BOX_<X>_TOKEN override when set for whichever
// registered backend owns that env var name (ADR 0016's opt-in two-actor
// separation, issue #380, generalized across every backend row carrying a
// boxTokenEnvVar per ADR 0038's backend-prefixed token knobs, instead of one
// hand-copied wrapper per backend). The Box then receives the override value
// as its own token, while the launcher's own os.Getenv(token) stays
// untouched for merges, labels, and every other host-side forge call. A row
// with no boxTokenEnvVar (jira, local, git) never matches, so name falls
// straight through to next unchanged for those. Checked ahead of next's own
// dispatch (which fans out on CODE_FORGE, e.g. localBaseBranchResolver's
// BASE_BRANCH case) so the override applies under every Code Forge, not
// just one.
func boxTokenResolver(next func(num, name string) string) func(num, name string) string {
	return func(num, name string) string {
		for _, row := range backendRows {
			if row.TokenEnvVar == "" || row.boxTokenEnvVar == "" || name != row.TokenEnvVar {
				continue
			}
			if v := os.Getenv(row.boxTokenEnvVar); v != "" {
				return v
			}
			break
		}
		return next(num, name)
	}
}

// dispatchConfig builds the subset of config a dispatch.Factory needs.
// OpenPRForIssue wires forge.ResolveOpenPR (issue #565), so a zero-exit
// rate-limited retry never re-runs a box whose work already landed a PR;
// ResolveOpenPR itself resolves to Found: false, nil for a push-only Code
// Forge, so the retry proceeds unguarded there without any guard here.
func dispatchConfig(c config, it forge.IssueTracker, lw *localloop.Wired, cf forge.CodeForge) dispatch.Config {
	sig := resolveCapabilitySignals(c.codeForge, c.issueTracker)
	trackerAxisRead, trackerAxisWrite, trackerAxisFiler, forgeBackend := resolveTrackerAndForgeSignals(c.codeForge, c.issueTracker)
	filerEnabled, workerProvisioned, reviewLoopInline, reviewLoopOrchestrator := resolveAgentPresenceSignals(c.driver)
	return dispatch.Config{
		BoxEnvVars:              c.boxEnvVars,
		ResolveEnv:              boxTokenResolver(localBaseBranchResolver(c, it, lw, cf)),
		Kind:                    c.dispatchKind,
		SelfContained:           c.selfContained,
		HostMediatedRemote:      sig.hostMediatedRemote,
		OutboxRelayCapable:      sig.outboxRelayCapable,
		FullyLocal:              sig.fullyLocal,
		InBoxUnreachableTracker: sig.inBoxUnreachableTracker,
		BoxForgeAndIssueAccess:  c.boxForgeAndIssueAccess,
		TrackerAxisRead:         trackerAxisRead,
		TrackerAxisWrite:        trackerAxisWrite,
		TrackerAxisFiler:        trackerAxisFiler,
		ForgeBackend:            forgeBackend,
		FilerEnabled:            filerEnabled,
		WorkerProvisioned:       workerProvisioned,
		ReviewLoopInline:        reviewLoopInline,
		ReviewLoopOrchestrator:  reviewLoopOrchestrator,
		TransientRetryMax:       c.transientRetryMax,
		TransientBackoffSecs:    c.transientBackoffSecs,
		HoldJitterSecs:          c.holdJitterSecs,
		DriverSessionCacheDir:   c.driverSessionCacheDir,
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
func newDispatchFactory(c config, pwd string, r runner.Runner, it forge.IssueTracker, lw *localloop.Wired, cf forge.CodeForge) *dispatch.Factory {
	f, err := dispatch.NewFactory(dispatchConfig(c, it, lw, cf), pwd, r, newDriver(c), dispatch.RealClock())
	if err != nil {
		fmt.Fprintf(os.Stderr, "==> driver cache unavailable (%v) -- fix boxes will cold-start\n", err)
	}
	return f
}

// settleConfig builds the subset of config a settle.Settle needs. OutboxDir
// resolves an issue number to the same per-issue outbox path runOnce mounts
// (dispatch.OutboxDirFor) — read via os.Getwd() rather than a threaded pwd
// so every existing settleConfig/newSettle call site (test and production)
// is unaffected; only a Code Forge implementing forge.BundleRelay (CODE_FORGE
// =local, or CODE_FORGE=github under BOX_FORGE_AND_ISSUE_ACCESS=read-only,
// issue #1918) ever consults it. CodeForgeForIssue resolves each dispatched
// issue's own CodeForge instance (ADR 0033, issue #1734): for CODE_FORGE
// =local, a fresh instance keyed to that issue's own resolved parent — lw is
// the one localloop.Wired the caller resolved for this run (issue #1810), so
// the parent it hands to CodeForgeForIssue is the same sealed value the
// base-branch resolver and surface grouping consume, not an independent
// derivation; every other codeForge has no per-issue parent concept, so it
// always returns cf unchanged — the same instance New() itself received, not
// a freshly constructed one, so a caller substituting a fake or specially
// configured cf (every test, and any future non-local construction site) is
// honored rather than silently bypassed.
func settleConfig(c config, lw *localloop.Wired, cf forge.CodeForge) settle.Config {
	return settle.Config{
		MergeMode:         c.mergeMode,
		MergeGuardPaths:   c.mergeGuardPaths,
		CompleteLabel:     c.completeLabel,
		MergePollInterval: c.mergePollInterval,
		MergePollTimeout:  c.mergePollTimeout,
		MaxFixAttempts:    c.maxFixAttempts,
		MaxRebaseAttempts: c.maxRebaseAttempts,
		// The rebase-push retry loops reuse the same transient-backoff and
		// hold-jitter knobs the dispatch-exit retry path does (issue #2095) --
		// a settle push 403/5xx is the same class of transient, so it backs
		// off on the same schedule rather than inventing a second knob pair.
		// Clock is left zero here; settle.New defaults it to RealClock.
		TransientBackoffSecs: c.transientBackoffSecs,
		HoldJitterSecs:       c.holdJitterSecs,
		// TransientRetryMax caps merge-transient retries (issue #2325) on
		// the same TRANSIENT_RETRY_MAX knob dispatch's exit-retry path
		// reads, not MaxRebaseAttempts (a merge-conflict budget).
		TransientRetryMax:  c.transientRetryMax,
		MaxBudgetTokens:    c.maxBudgetTokens,
		MaxBudgetUSD:       c.maxBudgetUSD,
		PreflightStaleBase: c.preflightStaleBase,
		OutboxDir:          lw.OutboxDir,
		CodeForgeForIssue: func(num string) forge.CodeForge {
			if c.codeForge != "local" {
				return cf
			}
			return lw.CodeForgeForIssue(num)
		},
		ReadOnly:   c.boxForgeAndIssueAccess == "read-only",
		BaseBranch: c.baseBranch,
	}
}

// localloopConfig builds the subset of config a localloop.Wire needs for
// CODE_FORGE=local's per-issue Code Forge construction and surface sweep —
// shared by every settleConfig/surfaceAfterDispatch construction site so
// they can never drift out of agreement on which Accumulation repo, base
// branch, or git identity a seam lands through.
func localloopConfig(c config) localloop.Config {
	return localloop.Config{
		AccumulationRepoDir: c.codeForgeAccumulationRepoDir,
		BaseBranch:          c.baseBranch,
		GitUserName:         c.gitUserName,
		GitUserEmail:        c.gitUserEmail,
		BranchPrefix:        c.branchPrefix,
	}
}

// newSettle constructs the Settler for one top-level dispatch entry point,
// reused across every issue in that invocation: the research kind's one-shot
// ResearchSettle, or work's full merge-gate Settle.
func newSettle(c config, it forge.IssueTracker, lw *localloop.Wired, cf forge.CodeForge) settle.Settler {
	if c.dispatchKind == dispatchKindResearch {
		vl := researchVerdictLabels(c)
		filerEnabled, _, _, _ := resolveAgentPresenceSignals(c.driver)
		if c.boxForgeAndIssueAccess == "read-only" {
			return settle.NewResearchSettleReadOnly(it, vl, filerEnabled)
		}
		return settle.NewResearchSettle(it, vl, filerEnabled)
	}
	return settle.New(settleConfig(c, lw, cf), it, cf)
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
		out[i] = waves.Issue{Number: iss.number, Title: iss.title, Priority: iss.priority}
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
	r := buildRunnerForKind(c, rc, pwd)
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

// checkReadOnlyCapabilityGate enforces BOX_FORGE_AND_ISSUE_ACCESS=read-only's
// capability requirement (issue #1916, ADR extending 0032/0033's
// host-mediated model to the github backends): the Box may only be denied a
// write token when the Launcher can perform every write it would otherwise
// make, on both axes. read-write (the default) is a no-op.
//
// mkHarness's readOnlyCapabilityOk eval assert (issue #2526) already proves
// this coherent for every combination a Consumer can bake into an image: it
// rejects an incoherent BOX_FORGE_AND_ISSUE_ACCESS/CODE_FORGE/ISSUE_TRACKER
// pairing at `nix build` time, before an image can ever exist, reading the
// same backend-registry RelayCapable/HostPostingCapable bits this gate
// checks. This Go gate exists only as a backstop for a *runtime* override of
// those three knobs (env var or CLI flag) past what nix validated at build
// time — it looks up the final resolved config's CODE_FORGE/ISSUE_TRACKER by
// name in the backend registry rather than inspecting live cf/it interfaces,
// since the registry is the single source of truth the eval assert already
// checked against.
func checkReadOnlyCapabilityGate(c config) error {
	if c.boxForgeAndIssueAccess != "read-only" {
		return nil
	}
	forgeRow, ok := backendByName(c.codeForge)
	if !ok {
		return fmt.Errorf("BOX_FORGE_AND_ISSUE_ACCESS=read-only: CODE_FORGE=%q is not a registered backend", c.codeForge)
	}
	if !forgeRow.RelayCapable {
		return fmt.Errorf("BOX_FORGE_AND_ISSUE_ACCESS=read-only: the selected CODE_FORGE=%q does not implement bundle-relay for the Box's finished branch hand-off", c.codeForge)
	}
	trackerRow, ok := backendByName(c.issueTracker)
	if !ok {
		return fmt.Errorf("BOX_FORGE_AND_ISSUE_ACCESS=read-only: ISSUE_TRACKER=%q is not a registered backend", c.issueTracker)
	}
	if !trackerRow.HostPostingCapable {
		return fmt.Errorf("BOX_FORGE_AND_ISSUE_ACCESS=read-only: the selected ISSUE_TRACKER=%q does not implement host-posted comments and issue-filing", c.issueTracker)
	}
	return nil
}

// checkNetworkModeRuntimeGate backstops two NETWORK_MODE combinations that
// mkHarness's networkModeCoherenceOk eval assert (lib/mkHarness.nix) cannot
// see, because that assert only ever runs against what a Consumer flake
// bakes at `nix build` time — never a runtime override (env var or CLI
// flag) — while NETWORK_MODE, PODMAN_NETWORK, and BWRAP_UNSHARE_NET are all
// runtime-overridable:
//
//  1. NETWORK_MODE=no-host-loopback's runner requirement (issue #2562):
//     bwrap can only unshare its network namespace all-or-nothing
//     (--unshare-net), with no partial host-loopback-only isolation, so
//     no-host-loopback has no bwrap rendering. An override can set
//     NETWORK_MODE=no-host-loopback at runtime on an image nix validated
//     for a different runner, a combination the eval assert never saw. The
//     bwrap runner adapter itself only special-cases networkMode="none" and
//     otherwise fails open (shares the full host network namespace) rather
//     than reject no-host-loopback, so this gate is what actually prevents
//     the combination from reaching it.
//
//  2. NETWORK_MODE / raw-knob coherence (review finding on issue #2562):
//     mkHarness rejects baking a non-default networkMode alongside a raw
//     knob (PODMAN_NETWORK / BWRAP_UNSHARE_NET) on the same Consumer flake —
//     but a flake can bake only one of them (say, just PODMAN_NETWORK, mode
//     left at the default "open") and then take a runtime override that
//     sets NETWORK_MODE to a non-open value, a pairing the eval assert never
//     saw either. Without this gate,
//     cmd/launcher/internal/runner/oci.go's networkArg() picks the raw knob
//     over the runtime-overridden mode ("raw wins whenever set"), silently
//     rendering full egress instead of the isolation NETWORK_MODE asked
//     for.
//
// Case 1 keys on c.runnerKind, never c.runtime (issue #2538 invariant, see
// the runnerKind field doc and runnerForKind above): runnerKind is what
// actually selects the bwrap adapter, and RUNNER_KIND=bwrap/RUNTIME=podman
// is a supported combination (bootstrap_test.go) where the OCI adapter's
// no-host-loopback rendering applies fine — keying on c.runtime would reject
// that valid combination and, worse, let RUNNER_KIND=bwrap/RUNTIME=podman +
// NETWORK_MODE=no-host-loopback pass straight through to bwrap.go's fail-open
// isolateNet=false.
//
// Case 2 only covers the unambiguously-detectable subset: a resolved
// c.networkMode that is not NetworkModeOpen alongside a set raw knob. Go has
// no way to distinguish "networkMode defaulted to open" from "networkMode
// was explicitly set to open" at this layer — only the resolved value is
// visible — so, unlike the nix-side assert (which checks presence, not
// value), an explicit NETWORK_MODE=open paired with a raw knob is out of
// scope here and left to raw-wins in networkArg().
func checkNetworkModeRuntimeGate(c config) error {
	if c.networkMode == runner.NetworkModeNoHostLoopback && c.runnerKind == freshness.KindBwrap {
		return fmt.Errorf("NETWORK_MODE=no-host-loopback is unsupported on RUNNER_KIND=bwrap -- bwrap can only unshare its network namespace all-or-nothing (--unshare-net), with no partial host-loopback isolation; use RUNNER_KIND=oci instead, or NETWORK_MODE=none/open on bwrap")
	}
	if c.networkMode != runner.NetworkModeOpen && c.networkMode != "" && (c.podmanNetwork != "" || c.bwrapUnshareNet) {
		var rawKnobs []string
		if c.podmanNetwork != "" {
			rawKnobs = append(rawKnobs, "PODMAN_NETWORK")
		}
		if c.bwrapUnshareNet {
			rawKnobs = append(rawKnobs, "BWRAP_UNSHARE_NET")
		}
		return fmt.Errorf("NETWORK_MODE=%s is set alongside raw network knob(s) %s -- there is no precedence rule between a runtime-overridden NETWORK_MODE and a raw knob; set only one", c.networkMode, strings.Join(rawKnobs, ", "))
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
//	  freshness probe found the loaded image, the loaded host-launcher
//	  binary, or both would be rebuilt (or, for the launcher, restarted)
//	  against the current base-branch tip; in-flight Boxes finished, no new
//	  ones launched, and the driving loop should rebuild and re-invoke.
//	exit 5 (errImageHostTainted): CONTINUOUS_DISPATCH mode only — a stale
//	  divergence persisted after a rebuild to the base tip (a host-system
//	  derivation reached the image graph through a consumer flake); the
//	  driving loop must halt, not rebuild-and-retry (issue #2113).
//	exit 6 (errConfigInvalid, exitConfigInvalid): bootstrap()'s validate(c)
//	  step rejected the loaded config — lets a caller (the dispatch verb,
//	  wired in issue #2568 slice 2) distinguish a config-validation failure
//	  from any other bootstrap failure (a readiness check, the
//	  accumulation-repo seed, etc.), which still falls back to exit 1.
var errQueueEmpty = errors.New("queue empty")

// exitConfigInvalid is the process exit code for a bootstrap() failure whose
// error wraps errConfigInvalid (bootstrap.go) -- see the exit-code doc
// comment above for the full mapping.
const exitConfigInvalid = 6

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
		return []issue{{number: fi.Number, title: fi.Title, priority: fi.Priority}}, origin, nil
	}
	fmt.Printf("==> querying open '%s' issues in %s\n", c.label, c.repoSlug)
	issues, err := queryOpenIssues(c, it)
	return issues, origin, err
}

// queryOpenIssues fetches the dispatchable-labelled batch without printing
// anything, so a caller that polls repeatedly (runContinuousDispatch's
// discover closure) can decide for itself whether this poll is worth
// announcing — see logDiscoveryPoll.
func queryOpenIssues(c config, it forge.IssueTracker) ([]issue, error) {
	rawIssues, err := it.ListIssues(forge.Dispatchable)
	if err != nil {
		return nil, err
	}
	var issues []issue
	for _, fi := range rawIssues {
		issues = append(issues, issue{number: fi.Number, title: fi.Title, priority: fi.Priority})
	}
	return issues, nil
}

// logDiscoveryPoll decides whether a continuous-dispatch refill poll should
// print the "==> querying open" announcement, then records this poll's issue
// numbers into seen. The first poll of a run always announces — the #1645
// invariant that a continuous run's very first discover establishes the
// baseline exactly once — regardless of what seen already holds. Every later
// poll stays silent unless it surfaces an issue number not in seen, in which
// case it announces and names only the newly-seen numbers.
func logDiscoveryPoll(c config, issues []issue, first bool, seen map[string]bool) {
	if first {
		fmt.Printf("==> querying open '%s' issues in %s\n", c.label, c.repoSlug)
	} else {
		var newNums []string
		for _, iss := range issues {
			if !seen[iss.number] {
				newNums = append(newNums, iss.number)
			}
		}
		if len(newNums) > 0 {
			fmt.Printf("==> querying open '%s' issues in %s — new: #%s\n", c.label, c.repoSlug, strings.Join(newNums, ", #"))
		}
	}
	for _, iss := range issues {
		seen[iss.number] = true
	}
}

// recoverByNumber resolves the open PR for the issue numbered issueNum,
// draft or not, and drives it through the adopt-and-gate path: the sole way
// an agent-in-progress issue is ever adopted, gated on the operator's
// explicit agent-recover label (see .github/workflows/agent-recover.yml)
// rather than any automatic sweep (#600). When no open PR exists, recover
// falls back to a second adopt arm (issue #2225): it recovers the driver's
// last genuine self-report from the issue's on-disk pass logs, and — when
// that report is a genuine success and the prior run left a relayable
// finished branch in the outbox — opens the PR on that relayed branch
// itself and drives it through the same merge gate, rather than immediately
// giving up. Returns an error when the issue cannot be fetched, or neither
// an open PR nor an adoptable relayed-branch success exists (labels
// untouched in that last case); the caller should treat those as
// non-success exits.
func recoverByNumber(c config, it forge.IssueTracker, cf forge.CodeForge, pwd string, f *dispatch.Factory, s settle.WorkSettler, issueNum string) error {
	fi, err := it.Issue(issueNum)
	if err != nil {
		return recoverFailed(it, issueNum, fmt.Errorf("issue %s: %w", issueNum, err))
	}
	iss := issue{number: fi.Number, title: fi.Title, priority: fi.Priority}
	branch := cf.AgentBranch(iss.number)
	backoff := retry.LinearBackoff{Unit: time.Duration(c.transientBackoffSecs) * time.Second, Clock: retry.RealClock()}
	// transientRetryMax is a max-retries knob everywhere else (dispatch/retry.go),
	// so the first attempt is on top of it: maxAttempts = retries + 1.
	res, prErr := forge.ResolveOpenPRWithRetry(cf, iss.number, backoff, c.transientRetryMax+1)
	if prErr != nil {
		return recoverFailed(it, issueNum, fmt.Errorf("issue %s: resolve PR: %w", issueNum, prErr))
	}
	if !res.Found {
		// resolved.SelfReportFound is consulted even on a near-miss resolveErr
		// (an unparseable leading-token line propagates as Resolve's own
		// error): the self-report walk runs unconditionally alongside the
		// genuine/synthetic tier (issue #2268), so a driver's genuine but
		// unparseable success report must still be adoptable here exactly as
		// it was before this recovered through the shared Resolve seam.
		//
		// SettleRelayedBranch is always attempted here, even when the log
		// carried no self-report at all: for the local push-only shape it
		// also accepts a bundle actually sitting in the outbox as sufficient
		// evidence on its own (issue #2378 — a signal-killed Box never gets
		// the chance to print a self-report, and this later, separate
		// recover process has no access to the original run's in-memory
		// KilledBySignal bit to re-derive that from disk). Its own gate
		// decides whether there is actually anything recoverable; the
		// unchanged "no open PR" exit below still fires whenever it returns
		// false.
		resolved, resolveErr := dispatch.ResolveFromLogs(pwd, iss.number, "")
		if resolveErr != nil {
			fmt.Fprintf(os.Stderr, "    ?? #%s: resolve pass logs: %v\n", issueNum, resolveErr)
		}
		if err := os.MkdirAll(dispatch.HostLogDirFor(pwd), 0o755); err != nil {
			return fmt.Errorf("mkdir logs: %w", err)
		}
		d := f.New(iss.number, iss.title)
		defer d.Close()
		// SettleRelayedBranch's own gate can reach CumulativeUsage/
		// UsageReport/Fix (selfHeal -> selfHealGate, adopt_relayed.go) on
		// this same never-Run()'d Dispatch -- see the SettleAdopted arm's
		// identical EnsureRunLineage call below for why (issue #2575).
		if err := d.EnsureRunLineage(); err != nil {
			fmt.Fprintf(os.Stderr, "    ?? #%s: ensure run lineage: %v\n", issueNum, err)
		}
		result := dispatch.Result{Resolved: resolved}
		sit := s.SituationFor(iss.number, res.Found, result)
		if s.SettleRelayedBranch(d, iss.number, 0, sit, result) {
			return nil
		}
		fmt.Printf("    #%s  status=skipped  note=no open PR on %s\n", issueNum, branch)
		return recoverFailed(it, issueNum, fmt.Errorf("issue %s: no open PR", issueNum))
	}
	if err := os.MkdirAll(dispatch.HostLogDirFor(pwd), 0o755); err != nil {
		return fmt.Errorf("mkdir logs: %w", err)
	}
	d := f.New(iss.number, iss.title)
	defer d.Close()
	// This Dispatch adopts an already-open PR and never calls Run() itself
	// (SettleAdopted drives it straight into CumulativeUsage/UsageReport/Fix
	// instead), so it never gets Run's own quarantine-prior-run-logs
	// guarantee for free. EnsureRunLineage establishes the same guarantee
	// explicitly, once, before any of those three ever reads a pass log
	// (issue #2575).
	if err := d.EnsureRunLineage(); err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: ensure run lineage: %v\n", issueNum, err)
	}
	s.SettleAdopted(d, iss.number, 0, res.URL)
	return nil
}

// recoverFailed is recoverByNumber's single terminal-failure exit: every
// return-error path in recoverByNumber funnels through it so a recover
// attempt that claimed an issue already in a successful terminal state
// (agent-complete) can never downgrade it to agent-failed (issue #2477). The
// claim itself runs host-side in the dispatch workflow
// (.github/workflows/agent-recover.yml), ahead of this process, stripping
// the prior terminal label before recoverByNumber ever sees the issue's
// current labels — so the pre-claim state has to be read back out of the
// issue's timeline instead, through the optional PriorClaimStateReader
// surface (only the github adapter implements it today; other trackers fall
// straight through to origErr below, unchanged). Every issue with no prior
// terminal label, or whose prior terminal label was agent-failed (not
// agent-complete), also falls straight through to origErr, preserving
// today's park-to-agent-failed behavior — the workflow's own "Park if
// nothing to recover" step, gated on this process's exit code, still fires
// for those exactly as before.
func recoverFailed(it forge.IssueTracker, num string, origErr error) error {
	pr, ok := it.(forge.PriorClaimStateReader)
	if !ok {
		return origErr
	}
	prior, found, err := pr.PriorClaimState(num)
	if err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: could not determine pre-claim state: %v\n", num, err)
		return origErr
	}
	if !found || prior != forge.Complete {
		return origErr
	}
	if err := it.TransitionState(num, forge.InProgress, forge.Complete); err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: could not restore agent-complete: %v\n", num, err)
		return origErr
	}
	note := fmt.Sprintf("recover attempted and declined to change anything: %v. This issue was already `agent-complete` before recover claimed it — that state is restored rather than parking `agent-failed`.", origErr)
	if commentErr := it.Comment(num, note); commentErr != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: could not post recover-declined comment: %v\n", num, commentErr)
	}
	fmt.Printf("    #%s  status=recover-declined  note=%v\n", num, origErr)
	return nil
}

// run is the orchestration logic for the `dispatch` subcommand: preflight,
// stranded-issue reconciliation, discovery, dependency-graph construction,
// and drain/wave/fan-out dispatch. lc is wired by bootstrap in production;
// tests construct it directly with fakes.
func run(lc *launchContext) error {
	c, it, cf, f, s, pwd := lc.config, lc.issueTracker, lc.codeForge, lc.factory, lc.settle, lc.pwd
	lp := reconcile.NewFSProbe(pwd, lc.runner)

	fmt.Println(repoBanner(c))

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
		return runContinuousDispatch(c, it, cf, pwd, f, s, runner.NixEvaluator{}, runner.NixRealizer{}, lp)
	}

	issues, origin, err := discoverIssues(c, it)
	if err != nil {
		return err
	}

	if origin == waves.OriginDiscovered && len(issues) == 0 {
		fmt.Printf("no open '%s' issues — nothing to do.\n", c.label)
		if err := reconcileAfterDispatch(c, it, cf, lp, pwd, os.Stdout); err != nil {
			return err
		}
		return errQueueEmpty
	}

	readiness, err := waves.NewReadiness(it, toWaveIssues(issues))
	if err != nil {
		return err
	}
	in := waves.Input{Origin: origin, Issues: toWaveIssues(issues), Edges: readiness.Edges, Sources: readiness.Sources, Failed: readiness.Failed}
	cfg := wavesConfig(c)
	cfg.SeedScopeOf = localloop.SeedScopeResolver(it, cf)
	if err := waves.Dispatch(cfg, it, cf, pwd, f, s, in); err != nil {
		return err
	}

	fmt.Print(dispatchCompletionBanner(c))
	return reconcileAfterDispatch(c, it, cf, lp, pwd, os.Stdout)
}

// runContinuousDispatch is the entry point for CONTINUOUS_DISPATCH: the
// opt-in slot-refill dispatch mode (#527). It hands off straight to
// waves.RunContinuous with a Discoverer that re-runs the label query and
// edge build on every refill, and a FreshnessChecker wired to
// freshness.Probe against the fetched base-branch tip; there is no separate
// empty-queue precheck here (#1645) — the discover closure's first call,
// made from RunContinuous's own bootstrap refill, is the only query a
// continuous run makes before its first dispatch.
//
// firstQueryEmpty, set by that same first call, records whether it found no
// open issues at all, as opposed to open issues that turned out blocked or
// deferred: only the former still maps ErrOpenNoneDispatchable to
// errQueueEmpty/exit 2 below. It's tracked here rather than inside
// waves.RunContinuous because RunContinuous's sentinel is shared with the
// console package's own Discoverer, which pre-filters claimed/dissolved
// picks — a zero-issue result there doesn't mean the tracker itself is
// empty (#1645). eval is injected so tests can substitute a Fake instead of
// shelling out to nix — mirrors previewIssues's own eval parameter. realize
// is injected for the same reason: tests substitute a RealizerFake instead
// of shelling out to `nix build` for the background base-tip realize
// (#2679).
func runContinuousDispatch(c config, it forge.IssueTracker, cf forge.CodeForge, pwd string, f *dispatch.Factory, s settle.Settler, eval freshness.Evaluator, realize freshness.Realizer, lp reconcile.LivenessProbe) error {
	firstQuery := true
	firstQueryEmpty := false
	var firstQueryErr error
	// seenIssues carries logDiscoveryPoll's per-run dedupe state (#1666): a
	// refill poll only announces when it surfaces a number not already in
	// this set, so a long-running slot-refill loop doesn't repeat the
	// "querying open" line every cycle once the queue has settled.
	seenIssues := make(map[string]bool)
	// firstQuery/firstQueryEmpty/firstQueryErr need no locking of their own:
	// every discover() call, on every refill, runs under RunContinuous's own
	// mutex (see its doc comment), so this closure is never invoked
	// concurrently with itself.
	discover := func() ([]waves.Issue, map[string][]string, waves.Sources, map[string]bool, error) {
		wasFirst := firstQuery
		issues, err := queryOpenIssues(c, it)
		if firstQuery {
			firstQuery = false
			firstQueryErr = err
			firstQueryEmpty = err == nil && len(issues) == 0
		}
		// A non-first poll that errors passes a nil/empty issues slice here,
		// so logDiscoveryPoll finds nothing new and stays silent -- this
		// poll simply never gets announced, unlike the pre-#1666 code that
		// printed the query line on every poll regardless of outcome.
		logDiscoveryPoll(c, issues, wasFirst, seenIssues)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		waveIssues := toWaveIssues(issues)
		result, err := waves.NewReadiness(it, waveIssues)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		return waveIssues, result.Edges, result.Sources, result.Failed, nil
	}

	guard := freshness.NewGuard(pwd)
	var staleResult freshness.Result

	fresh := func() (bool, bool, string) {
		res := freshness.Probe(c.runnerKind, pwd, c.baseBranch, c.flakeImageAttr, c.imageTag, c.flakeLauncherAttr, c.loadedLauncherHash, eval)
		// fresh() is called under RunContinuous's mutex (see its doc
		// comment), so this plain write is serialized — no separate
		// locking needed, mirroring the firstQuery*/firstQueryEmpty comment
		// above.
		if res.Applicable && !res.Fresh {
			staleResult = res
		}
		freshness.RealizeTip(realize, pwd, res, c.flakeImageAttr)
		return res.Applicable, res.Fresh, res.Message
	}

	cfg := wavesConfig(c)
	cfg.SeedScopeOf = localloop.SeedScopeResolver(it, cf)
	if err := waves.RunContinuous(cfg, nil, it, cf, pwd, f, s, discover, fresh); err != nil {
		// ErrImageStale takes priority over firstQueryErr below: refill's
		// stale-drain report (see continuous.go) can itself trigger an
		// extra, reporting-only discover() call the very first time
		// staleness fires, and if that call is also the run's first-ever
		// discover() call, a transient error from it would otherwise get
		// caught by firstQueryErr and flatten this more specific,
		// higher-priority ErrImageStale/exit-4 outcome into a raw error.
		if errors.Is(err, waves.ErrImageStale) {
			if guard.Classify(staleResult) == freshness.HostTainted {
				fmt.Fprintln(os.Stdout, freshness.HostTaintDiagnostic(c.baseBranch, staleResult.Rev, c.flakeImageAttr, staleResult.TipTag, c.imageTag))
				return errImageHostTainted
			}
			return waves.ErrImageStale
		}
		// refill swallows every discover error to stderr and retries on the
		// next trigger (a transient-tracker-hiccup tolerance that's fine for
		// refill 2+, but the first call has no next trigger to retry on once
		// nothing ever dispatches — see RunContinuous). Surface that first
		// error here instead of letting it flatten into
		// ErrOpenNoneDispatchable/exit 3, matching the raw-error/exit-1
		// result the removed precheck gave a startup query failure.
		if firstQueryErr != nil {
			return firstQueryErr
		}
		if errors.Is(err, waves.ErrOpenNoneDispatchable) && firstQueryEmpty {
			fmt.Printf("no open '%s' issues — nothing to do.\n", c.label)
			if err := reconcileAfterDispatch(c, it, cf, lp, pwd, os.Stdout); err != nil {
				return err
			}
			_ = guard.Reset()
			return errQueueEmpty
		}
		return err
	}
	fmt.Print(dispatchCompletionBanner(c))
	return reconcileAfterDispatch(c, it, cf, lp, pwd, os.Stdout)
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
	cf := newCodeForge(c, local.SanitizedParent{}, it)
	if err := runDoctor(it, cf, c, os.Stdout, os.Stdin, isStdinTTY()); err != nil {
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
	// Bubble Tea owns the terminal in alt-screen/raw mode (tea.go's
	// WithAltScreen); a heartbeat line's bare \n moves the cursor down but
	// not back to column 0, stairstepping across the screen. The sidebar
	// activity feed already re-renders the same lines by independently
	// re-reading the pass log from disk, so the live-terminal echo is both
	// redundant and corrupting here (issue #1583).
	lc.factory.SetHeartbeatOut(io.Discard)
	fresh, rebuild := newConsoleFreshness(lc.config, lc.pwd, runner.NixEvaluator{},
		func() (string, string, error) { return consoleGitSync(lc.pwd, lc.config.baseBranch) },
		func() (string, error) { return consoleNixBuild(lc.pwd) })
	researchTracker, researchFactory, researchSettle := researchLaunchStack(lc)
	defer researchFactory.Cleanup()
	launch := &console.Launcher{
		CodeForge:       lc.codeForge,
		Factory:         lc.factory,
		Settle:          lc.settle,
		ResearchTracker: researchTracker,
		ResearchFactory: researchFactory,
		ResearchSettle:  researchSettle,
		MaxParallel:     lc.config.maxParallel,
		FailedLabel:     lc.config.failedLabel,
		Fresh:           fresh,
		RebuildFn:       rebuild,
		RecoverFn: func(issueNum string) error {
			return recoverByNumber(lc.config, lc.issueTracker, lc.codeForge, lc.pwd, lc.factory, lc.workSettle(), issueNum)
		},
	}
	if err := console.Run(lc.issueTracker, lc.pwd, stdin, stdout, launch); err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
		return 1
	}
	return 0
}

// writeGithubOutput appends a "key=value\n" line to the file named by the
// GITHUB_OUTPUT environment variable, the mechanism GitHub Actions uses for
// step outputs. It is a no-op returning nil when GITHUB_OUTPUT is unset or
// empty (local runs, tests). value is sanitized by replacing any newline
// with a space so it can't corrupt the single-line key=value format.
func writeGithubOutput(key, value string) error {
	path := getenvArtifact("GITHUB_OUTPUT", "")
	if path == "" {
		return nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	value = strings.ReplaceAll(value, "\n", " ")
	_, err = fmt.Fprintf(f, "%s=%s\n", key, value)
	return err
}

// cmdRecover is the `recover` subcommand: adopt an already-discovered open
// PR (draft or not) with no outcome line and drive it through the merge
// gate. lc is wired by bootstrap in production; tests construct it directly
// with fakes (and a spy cleanup) to exercise the cleanup-on-every-exit
// contract.
func cmdRecover(lc *launchContext, issueNum string) int {
	defer lc.cleanup()
	if err := recoverByNumber(lc.config, lc.issueTracker, lc.codeForge, lc.pwd, lc.factory, lc.workSettle(), issueNum); err != nil {
		if writeErr := writeGithubOutput("recover-reason", err.Error()); writeErr != nil {
			fmt.Fprintf(os.Stderr, "warning: writing recover-reason output: %v\n", writeErr)
		}
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

// exitCodeFor translates a run/runContinuousDispatch error into the
// launcher's process exit code: 2 for an empty dispatch queue, 3 for open
// issues that exist but none are dispatchable, 4 for CONTINUOUS_DISPATCH
// mode stopping on a stale image (a rebuild-and-retry signal), 5 for
// CONTINUOUS_DISPATCH mode halting on a non-converging, host-tainted stale
// image (issue #2113 — a rebuild cannot fix this, so the driving loop must
// stop instead of looping on exit 4 forever), 1 for any other error, 0 on
// success. Pure and side-effect-free so it's unit-testable in isolation from
// run's own I/O.
func exitCodeFor(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, errQueueEmpty):
		return 2
	case errors.Is(err, waves.ErrOpenNoneDispatchable):
		return 3
	case errors.Is(err, waves.ErrImageStale):
		return 4
	case errors.Is(err, errImageHostTainted):
		return 5
	default:
		return 1
	}
}

// bootstrapExitCode translates a bootstrap() error into the launcher's
// process exit code: exitConfigInvalid (6) when the error wraps
// errConfigInvalid (bootstrap()'s own validate(c) failure), 1 for any other
// error, 0 on success (nil). Pure and side-effect-free, mirroring
// exitCodeFor's shape for run/runContinuousDispatch errors.
func bootstrapExitCode(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, errConfigInvalid):
		return exitConfigInvalid
	default:
		return 1
	}
}

// runExitCode translates run's result into the launcher's process exit
// code via exitCodeFor. Split out from cmdDispatch so it's unit-testable
// against a fake-populated launchContext without going through bootstrap.
func runExitCode(lc *launchContext) int {
	err := run(lc)
	code := exitCodeFor(err)
	if code == 1 && err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
	}
	return code
}

// cmdDispatch is the `dispatch` subcommand: drain the labeled queue. lc is
// wired by bootstrap in production; tests construct it directly with fakes.
func cmdDispatch(lc *launchContext) int {
	defer lc.cleanup()
	return runExitCode(lc)
}

// flushAmbientWarnings writes any snapshotted ambient-env deprecation
// warnings to stderr. Callers pass the same buffer at each mainRun
// early-return site that must surface warnings (ADR 0020, issue #814).
func flushAmbientWarnings(stderr io.Writer, warnings *bytes.Buffer) {
	stderr.Write(warnings.Bytes())
}

// verbHandler is the uniform shape every entry in verbHandlers implements,
// even though the underlying cmd* functions take different arguments: args
// is args[1:] (the subcommand's own arguments, subcommand name stripped).
type verbHandler func(args []string, stderr io.Writer) int

// verbHandlers is the declared table of the eight real subcommands (issue
// #1574), keyed by verb name, replacing what used to be an inline
// if-args[0]-== chain in mainRun. It is the single source of truth for
// "what subcommands actually exist" — a test enumerates its keys to prove
// that set programmatically. The hidden __complete-issues shell-completion
// verb is deliberately not in this table (mainRun dispatches it separately,
// before the table lookup), since it isn't one of the documented verbs.
var verbHandlers = map[string]verbHandler{
	"build":     func(args []string, stderr io.Writer) int { return cmdBuild() },
	"doctor":    func(args []string, stderr io.Writer) int { return cmdDoctor() },
	"reconcile": func(args []string, stderr io.Writer) int { return cmdReconcile() },
	"console": func(args []string, stderr io.Writer) int {
		lc, err := bootstrap(true, dispatchKindWork, false)
		if err != nil {
			fmt.Fprintf(stderr, "%s\n", err)
			return 1
		}
		return cmdConsole(lc, os.Stdin, os.Stdout)
	},
	"recover": func(args []string, stderr io.Writer) int {
		if sc, _ := dispatchSelfContainedArgs(args); sc {
			fmt.Fprintln(stderr, "flag --self-contained is only valid for the research subcommand")
			return 1
		}
		if len(args) < 1 {
			fmt.Fprintln(stderr, "usage: spindrift recover <issue-number>")
			return 1
		}
		lc, err := bootstrap(true, dispatchKindWork, false)
		if err != nil {
			fmt.Fprintf(stderr, "%s\n", err)
			return 1
		}
		return cmdRecover(lc, args[0])
	},
	"preview": func(args []string, stderr io.Writer) int {
		return cmdPreview(dispatchIssueArgs(args))
	},
	"dispatch": func(args []string, stderr io.Writer) int {
		if sc, _ := dispatchSelfContainedArgs(args); sc {
			fmt.Fprintln(stderr, "flag --self-contained is only valid for the research subcommand")
			return 1
		}
		noBuild, dispatchArgs := dispatchNoBuildArgs(args)
		forceYes, dispatchArgs := dispatchYesArgs(dispatchArgs)
		nums := dispatchIssueArgs(dispatchArgs)
		lc, err := bootstrap(!noBuild, dispatchKindWork, false)
		if err != nil {
			fmt.Fprintf(stderr, "%s\n", err)
			return bootstrapExitCode(err)
		}
		if len(nums) > 0 {
			return cmdDispatchSelective(lc, nums, forceYes)
		}
		return cmdDispatch(lc)
	},
	"research": func(args []string, stderr io.Writer) int {
		noBuild, researchArgs := dispatchNoBuildArgs(args)
		forceYes, researchArgs := dispatchYesArgs(researchArgs)
		selfContained, researchArgs := dispatchSelfContainedArgs(researchArgs)
		nums := dispatchIssueArgs(researchArgs)
		lc, err := bootstrap(!noBuild, dispatchKindResearch, selfContained)
		if err != nil {
			fmt.Fprintf(stderr, "%s\n", err)
			return 1
		}
		if len(nums) > 0 {
			return cmdDispatchSelective(lc, nums, forceYes)
		}
		return cmdDispatch(lc)
	},
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
	// ahead of the help/bare-invocation early returns and the
	// extractInputFlag/parseFlags/loadInputDocument error returns below so
	// all of them still surface the warning instead of silently dropping it
	// (issues #814, #1191).
	var ambientWarnings bytes.Buffer
	warnAmbientKnobEnv(&ambientWarnings)
	if help {
		flushAmbientWarnings(stderr, &ambientWarnings)
		if helpAll {
			printHelpFull(stdout)
		} else {
			printHelp(stdout)
		}
		return 0
	}
	inputPath, argv, err := extractInputFlag(argv)
	if err != nil {
		stderr.Write(ambientWarnings.Bytes())
		fmt.Fprintf(stderr, "%s\n", err)
		return 1
	}
	args, err := parseFlags(argv)
	if err != nil {
		stderr.Write(ambientWarnings.Bytes())
		fmt.Fprintf(stderr, "%s\n", err)
		return 1
	}
	if len(args) == 0 {
		// Bare `spindrift`: print help rather than silently dispatching
		// (issue #555). `dispatch` remains the sole way to drain the queue.
		flushAmbientWarnings(stderr, &ambientWarnings)
		printHelp(stdout)
		return 0
	}
	if inputPath != "" {
		doc, err := loadInputDocument(inputPath)
		if err != nil {
			stderr.Write(ambientWarnings.Bytes())
			fmt.Fprintf(stderr, "%s\n", err)
			return 1
		}
		loadedDoc = doc
	}
	// Runs after loadedDoc is in place (see applySecretCmdFallback's doc
	// comment): CODE_FORGE/ISSUE_TRACKER may be set only via the document.
	if err := applySecretCmdFallback(); err != nil {
		stderr.Write(ambientWarnings.Bytes())
		fmt.Fprintf(stderr, "%s\n", err)
		return 1
	}
	flushAmbientWarnings(stderr, &ambientWarnings)
	if args[0] == "__complete-issues" {
		return cmdCompleteIssues()
	}
	if handler, ok := verbHandlers[args[0]]; ok {
		return handler(args[1:], stderr)
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

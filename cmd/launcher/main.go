// Package main: spindrift launcher — orchestrates open issues into disposable
// containers. Nix-computed config (resolved knob settings and build/run
// artifacts) reaches the binary as one Launcher input document, passed via
// --input (ADR 0020); an explicit CLI flag overrides the document, and an
// ambient knob env var still wins this release but draws a deprecation
// warning (see warnAmbientKnobEnv). Secrets and BOX_ENV_VARS plumbing stay
// env-only, and the binary bakes no store paths of its own.
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

	"spindrift.dev/launcher/internal/backend"
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
	// schemaConfig carries one field per lib/env-schema.nix host-config
	// entry, loaded by loadSchemaConfig(). Embedded by value (not pointer)
	// so a copy-and-mutate helper like applyDispatchKind can never alias the
	// caller's config through the embedded struct.
	schemaConfig

	// OCI image config (baked by nix wrapper). imageArchive/imageDrv/
	// nixBuilderImage/nixVolume are empty for bwrap (no image to load).
	// flakeImageAttr/imageTag are dual-purpose: the OCI image's flake
	// attr/content-hash tag for an OCI runtime, or the bundled bwrap
	// agent-closure's flake attr/loaded output path for bwrap — either way,
	// freshness.Probe compares a freshly-evaluated value at these same two
	// slots against a base-branch tip. flakeLauncherAttr is populated for
	// both runtimes (host-launcher freshness).
	imageArchive      string
	imageTag          string
	imageDrv          string
	nixBuilderImage   string
	nixVolume         string
	flakeImageAttr    string
	flakeLauncherAttr string

	// loadedLauncherHash is the bare 32-char nix store hash of the
	// launcher-currency flake package this binary was built from.
	// freshness.Probe compares it against a freshly-evaluated hash at the
	// base-branch tip to answer the launcher-staleness dimension.
	loadedLauncherHash string

	// bwrap agent closure paths (bwrap only). The *Drv fields hold .drv
	// paths, used by `launcher build` to realize the closure.
	agentFiles    string
	agentEnv      string
	agentFilesDrv string
	agentEnvDrv   string
	bakedPrefetch string
	passwdFile    string
	groupFile     string
	passwdFileDrv string
	groupFileDrv  string

	// nixConfigFile is the baked nix store path for /etc/nix/nix.conf (ADR
	// 0042, bwrap only); empty when the Consumer's nixInBox knob is off. Its
	// .drv also drives the host nix store DB snapshot.
	nixConfigFile    string
	nixConfigFileDrv string

	// syscallFilterPath is the baked nix store path to the compiled BPF
	// syscall-filter file (bwrap only). Unlike nixConfigFile, always
	// populated -- the filter is a bwrap-hardening concern independent of
	// the nixInBox knob.
	syscallFilterPath string
	syscallFilterDrv  string

	// nixStoreWritable gates whether the bwrap adapter overlays /nix/store
	// as an ephemeral tmpfs-backed writable layer instead of a plain
	// read-only bind (ADR 0042, bwrap only).
	nixStoreWritable bool

	// Runtime: podman | docker | rancher | bwrap (runner.ValidValues)
	runtime string

	// runnerKind selects the launcher's runner implementation: "bwrap" or
	// "oci". Nix-rendered alongside runtime, but a distinct artifact —
	// runner selection reads this, never runtime's raw value, since runtime
	// also carries operator-facing runtime *names* (podman, docker, rancher)
	// that aren't "oci" literally.
	runnerKind string

	// driver selects the Go Driver strategy (ADR 0009): transient
	// classification and heartbeat parsing. Empty defaults to "claude",
	// matching the nix side's default.
	driver string

	// image is the OCI runtime image reference; defaults to imageTag, which
	// for bwrap holds the bundled agent-closure's store path instead of an
	// image tag — harmless since only oci.go reads image, and oci.go is
	// never reached for a bwrap runnerKind.
	image string

	// driverSessionCacheDir is the in-box mount target for the selected
	// Driver's session-state dir (ADR 0009), nix-baked at wrap time. Empty
	// when the Driver declares no session-state dir.
	driverSessionCacheDir string

	// registryProxyCredential is the resolved value of the registry proxy
	// Credential reference (ADR 0044): schemaConfig's
	// registryProxyCredentialEnv/registryProxyCredentialFile carry a
	// *reference* (an env-var NAME or a file PATH), never the credential
	// value itself. bootstrap() resolves it exactly once and stores the
	// result here. Empty when neither reference is set.
	registryProxyCredential string

	// Space-separated list of env var names to forward into each Box container.
	// Set by the nix-rendered preamble from the schema's boxEnv=true entries so
	// the Go source never needs to enumerate them by hand.
	boxEnvVars string

	// dispatchKind is "" (doctor, reconcile, preview -- none of which ever
	// dispatch), or dispatchKindWork/dispatchKindResearch (ADR 0022). Set
	// via applyDispatchKind, never read from the environment directly: it is
	// operator intent carried by which subcommand launched, not a knob.
	dispatchKind string

	// selfContained is the research kind's no-repo sub-mode
	// (--self-contained): the Box clones no repo and explores none, and
	// startup validation permits the no-REPO_SLUG/no-GH_TOKEN configuration.
	// Rejected by validate for any other kind.
	selfContained bool
}

// The two Dispatch kinds (ADR 0022). Both share the four canonical
// DispatchState lifecycle states; research selects the fixed agent-research
// label family and a one-shot Settle instead of work's full merge gate.
const (
	dispatchKindWork     = "work"
	dispatchKindResearch = "research"
)

// applyDispatchKind sets c's dispatchKind marker and, for research, swaps
// the four lifecycle label fields to the fixed research family — unlike the
// work labels these aren't operator-configurable, since the research CI
// workflow and prompt key off them directly. completeLabel is left blank:
// the verdict-carrying transition uses IssueTracker.CompleteVerdict instead
// of a single Complete label.
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

// newIssue is the sole conversion point from forge.Issue to the launcher's
// local issue type; build every issue through it rather than hand-copying
// fields. The completion path is the one caller that then explicitly zeroes
// .priority (complete_issues.go).
func newIssue(fi forge.Issue) issue {
	return issue{number: fi.Number, title: fi.Title, priority: fi.Priority}
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
// document's settings value (ADR 0020) when --input loaded one carrying key,
// else the generated schemaFlags table, or "" when the knob has none. Every
// getenvSchema/atoiSchema/atoiNonnegSchema resolution routes through here,
// so document precedence applies uniformly.
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

// The getenv*/atoi*/float* Schema helpers below all resolve key from the
// environment and fall back to schemaDefault rather than a hand-written
// literal. A non-numeric or absent default parses to 0.

// intSchemaDefault parses key's schema default as an int.
func intSchemaDefault(key string) int {
	n, _ := strconv.Atoi(schemaDefault(key))
	return n
}

func getenvSchema(key string) string {
	return getenv(key, schemaDefault(key))
}

// getenvSchemaPreserveEmpty is like getenvSchema but distinguishes "set to
// empty" from "unset": use it for knobs whose schema doc gives the empty
// string its own meaning (e.g. "disables the limit"), where an operator's
// explicit KEY= override must not collapse into the schema default.
func getenvSchemaPreserveEmpty(key string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return schemaDefault(key)
}

// atoiSchema parses a positive integer (see atoi).
func atoiSchema(key string) int {
	return atoi(os.Getenv(key), intSchemaDefault(key))
}

// atoiNonnegSchema parses a non-negative integer (see atoiNonneg).
func atoiNonnegSchema(key string) int {
	return atoiNonneg(os.Getenv(key), intSchemaDefault(key))
}

// floatSchemaDefault parses key's schema default as a float64.
func floatSchemaDefault(key string) float64 {
	n, _ := strconv.ParseFloat(schemaDefault(key), 64)
	return n
}

// floatNonnegSchema parses a non-negative float64, the USD-budget
// counterpart to atoiNonnegSchema; a negative value falls back to the default.
func floatNonnegSchema(key string) float64 {
	if n, err := strconv.ParseFloat(os.Getenv(key), 64); err == nil && n >= 0 {
		return n
	}
	return floatSchemaDefault(key)
}

// gitIdentityField resolves a commit-identity knob (GIT_USER_NAME/
// GIT_USER_EMAIL) via the normal document/flag/env chain, falling back to
// the host git config when none of those supply a value (ADR 0020: the
// wrapper exports no knob env at all).
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
	// Read straight from the artifact/env with no runtime-name fallback: the
	// nix pipeline always renders RUNNER_KIND alongside RUNTIME. A direct
	// binary invocation with no --input document (a supported path: tests,
	// manual debugging) that omits it gets "", which runnerForKind treats as
	// "oci" — the same default every other absent artifact gets here.
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
		passwdFile:         getenvArtifact("PASSWD_FILE", ""),
		groupFile:          getenvArtifact("GROUP_FILE", ""),
		passwdFileDrv:      getenvArtifact("PASSWD_FILE_DRV", ""),
		groupFileDrv:       getenvArtifact("GROUP_FILE_DRV", ""),
		nixConfigFile:      getenvArtifact("NIX_CONFIG_FILE", ""),
		nixConfigFileDrv:   getenvArtifact("NIX_CONFIG_FILE_DRV", ""),
		syscallFilterPath:  getenvArtifact("SYSCALL_FILTER", ""),
		syscallFilterDrv:   getenvArtifact("SYSCALL_FILTER_DRV", ""),
		nixStoreWritable:   getenvArtifact("NIX_STORE_WRITABLE", "") == "true",
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
// nix-forwarded artifacts describe whatever pairing was baked into the
// --input document at build time; a CLI flag or env var overriding
// CODE_FORGE/ISSUE_TRACKER (or no document at all) moves the pairing out
// from under them, so trust the artifacts only on an exact pairing match and
// otherwise fall back to a registry lookup on the resolved names. The trust
// branch also requires all four keys present, since a missing key's
// docArtifact(key) == "true" would silently read as false.
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

// trackerAxisSignals reads the tracker-axis facts straight off the matching
// backendRows entry rather than re-deriving the mapping with its own name
// switch, so nix and Go can never drift on it. An empty TrackerAxisRead
// covers two cases at once, both resolving to the GITHUB/GITHUB/GH defaults:
// an unregistered name, and github/jira's own rows, whose real resolved
// value IS "GITHUB" but which leave the field at its Go zero value.
func trackerAxisSignals(issueTracker string) (read, write, filer string) {
	row, ok := backendByName(issueTracker)
	if !ok || row.TrackerAxisRead == "" {
		return "GITHUB", "GITHUB", "GH"
	}
	// TrackerAxisWrite is read as-is: unlike read/filer, "" is a legitimate
	// resolved value for a found row (local has no write-step axis at all),
	// not an unset-field placeholder. Filer's "GH" default mirrors
	// lib/mkHarness.nix's `issueTrackerRow.trackerAxisFiler or "GH"` — nix's
	// `or` fires on attribute absence, this on the empty string the renderer
	// produces for one.
	filer = row.TrackerAxisFiler
	if filer == "" {
		filer = "GH"
	}
	return row.TrackerAxisRead, row.TrackerAxisWrite, filer
}

// forgeBackendSignal is trackerAxisSignals' registry-driven counterpart for
// the CODE_FORGE axis.
func forgeBackendSignal(codeForge string) string {
	row, ok := backendByName(codeForge)
	if !ok || row.ForgeBackend == "" {
		return "GH"
	}
	return row.ForgeBackend
}

// resolveTrackerAndForgeSignals mirrors resolveCapabilitySignals's
// trust-then-fallback shape for exactly the same reason: these signals are
// derived purely from ISSUE_TRACKER/CODE_FORGE, both flakeOption (not
// boxEnvOnly) knobs an operator can override at dispatch time independent of
// whatever pairing was baked into the --input document. Trust the forwarded
// artifacts only on a pairing match with every key present; otherwise fall
// back to trackerAxisSignals/forgeBackendSignal, the pure Go mirror of
// lib/mkHarness.nix's own computation.
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
// signals for this run. It mirrors resolveTrackerAndForgeSignals's
// trust-then-fallback shape but with two INDEPENDENT trust gates, because
// the pairs have different override semantics and one shared gate would
// over-fire on the first and under-fire on the second.
//
// FILER_ENABLED/WORKER_PROVISIONED are baked at eval time from
// agentsJsonTemplate, itself a fixed value, so a dispatch-time
// FILER_MODEL/WORKER_MODEL override has ZERO effect on the real --agents
// roster. Both are trusted whenever present regardless of whether the live
// models match the document; gating them on a live-model match would diverge
// the rendered prompt from what is actually baked into the image. Their
// fallback is driver-aware (opencode: always false) rather than a
// driver-blind model != "" test.
//
// REVIEW_LOOP_INLINE/REVIEW_LOOP_ORCHESTRATOR are different:
// ORCHESTRATOR_ENABLED is boxEnv=true, forwarded from the *ambient*
// environment and read live by entrypoint.sh, so an override genuinely
// changes box behavior. Trusting a stale artifact would hand the box to the
// orchestrator while still rendering the inline review-loop section, or vice
// versa. These stay gated on the live value matching the document, and their
// fallback reads that same live value rather than schemaDefault's
// document-first read — defaulting both false would violate their
// exactly-one-true invariant.
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
			filerEnabled = docArtifact("FILER_ENABLED") == "true"
			workerProvisioned = docArtifact("WORKER_PROVISIONED") == "true"
		}
	}

	// A bool-kind schema knob's live value follows the ambient-env/flag
	// convention ("1" or "") -- never the literal "true" the Artifacts-section
	// reads above compare against, which is a distinct convention.
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
	before, after := splitChoiceKnobRegistry(choiceKnobRegistry)
	if err := validateChoiceKnobsFailFast(c, before); err != nil {
		return err
	}
	if err := doctor.RunRequiredFailFast(launcherCrossKnobChecks(c)); err != nil {
		return err
	}
	if err := validateChoiceKnobsFailFast(c, after); err != nil {
		return err
	}
	if _, err := forge.ParseResearchVerdicts(c.researchVerdicts); err != nil {
		return err
	}
	return nil
}

// validateConfig runs the same configuration-correctness checks validate(c)
// gates dispatch on — required-knob presence, driver credentials, cross-knob
// pairs, enum-choice knobs, RESEARCH_VERDICTS — with two deliberate
// omissions. doctor.RuntimeCheck is an environment/installation concern
// doctor treats as advisory, so it must not fold into cmdDoctor's exit-2
// "configuration invalid" classification even though validate(c) still
// requires it before dispatch can launch a Box. The --self-contained check
// can never fire here (cmdDoctor's config source always passes
// selfContained=false), so it is omitted rather than carried as dead code.
// It reuses doctorExtraChecks(c) so runDoctor and this can never disagree
// about which rows count as "configuration".
//
// Unlike validate(), this runs every row and joins the failures: none of
// these checks are network probes, so running them all is cheap and
// cmdDoctor's summary can name every simultaneously-broken check instead of
// only the first. validate() keeps its own fail-fast precedence.
func validateConfig(c config) error {
	var errs []error
	for _, r := range doctor.RunChecks(doctorExtraChecks(c)) {
		if r.Check.Tier == doctor.Required && r.Err != nil {
			errs = append(errs, r.Err)
		}
	}
	errs = append(errs, validateChoiceKnobsErrors(c, choiceKnobRegistry)...)
	if _, err := forge.ParseResearchVerdicts(c.researchVerdicts); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
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
// Recoverable and Ambiguous are fixed string literals rather than knobs:
// Recoverable only ever applies to CODE_FORGE=local push-only runs and is
// stored as a local frontmatter marker, never a real GitHub label;
// Ambiguous is a real label but nothing asks for it to be
// operator-configurable.
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
// (default "github"), carrying c.dispatchKind's label family and verdict
// labels — the kind-aware seam ADR 0022 describes. An unregistered
// c.issueTracker, or a row with no constructor, is unreachable
// post-validate() and falls back to github.
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
// own resolved Integration-branch key; every other codeForge ignores it. An
// unregistered c.codeForge, or a row with no constructor, is unreachable
// post-validate() and falls back to github.
// BOX_FORGE_AND_ISSUE_ACCESS=read-only swaps in the row's read-only wrapper
// when it has one (github, forgejo); git and local carry none, so read-only
// falls through to the plain constructor unchanged.
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
// completion line, so the single/wave and continuous dispatch paths share
// one wording and never drift out of sync.
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
// knob to .spindrift/accum.git under the process cwd: both the read-only
// /repo Box mount and the host-side landing forge's git subprocesses (which
// run from inside cwd) need the same absolute path, so resolving it once
// here keeps every downstream consumer in agreement. Other forges leave dir
// untouched and unresolved.
func absCodeForgeAccumulationRepoDir(codeForge, dir string) string {
	row, _ := backendByName(codeForge)
	if !row.HostMediatedRemote {
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
		Runtime:           c.runtime,
		Image:             c.image,
		ImageArchive:      c.imageArchive,
		ImageDrv:          c.imageDrv,
		ImageTag:          c.imageTag,
		NixBuilderImage:   c.nixBuilderImage,
		NixVolume:         c.nixVolume,
		FlakeImageAttr:    c.flakeImageAttr,
		PodmanNetwork:     c.podmanNetwork,
		NetworkMode:       c.networkMode,
		PidsLimit:         c.pidsLimit,
		MemoryLimit:       c.memoryLimit,
		AgentFiles:        c.agentFiles,
		AgentEnv:          c.agentEnv,
		AgentFilesDrv:     c.agentFilesDrv,
		AgentEnvDrv:       c.agentEnvDrv,
		BakedPrefetch:     c.bakedPrefetch,
		PasswdFile:        c.passwdFile,
		GroupFile:         c.groupFile,
		PasswdFileDrv:     c.passwdFileDrv,
		GroupFileDrv:      c.groupFileDrv,
		NixConfigFile:     c.nixConfigFile,
		NixConfigFileDrv:  c.nixConfigFileDrv,
		NixStoreWritable:  c.nixStoreWritable,
		SyscallFilterPath: c.syscallFilterPath,
		SyscallFilterDrv:  c.syscallFilterDrv,
		BwrapUnshareNet:   c.bwrapUnshareNet,
		MountParams: runner.MountParams{
			PromptDir:                c.spindriftPromptDir,
			SkillsDir:                c.spindriftSkillsDir,
			DriverSessionCacheDir:    c.driverSessionCacheDir,
			HostMediatedIssueTracker: sig.inBoxUnreachableTracker,
			LocalIssuesDir:           absLocalIssuesDir(c.localIssuesDir),
			HostMediatedRemote:       sig.hostMediatedRemote,
			AccumulationRepoDir:      c.codeForgeAccumulationRepoDir,
			OutboxRelayCapable:       sig.outboxRelayCapable,
			BoxForgeAndIssueAccess:   c.boxForgeAndIssueAccess,
		},
	}
}

// runnerForKind selects the run-time runner adapter (bwrap or OCI). Keyed
// solely on c.runnerKind — never c.runtime, which also carries
// operator-facing runtime *names* (podman, docker, rancher) that aren't
// "oci" literally.
func runnerForKind(c config, rc runner.Config, pwd string) runner.Runner {
	if c.runnerKind == freshness.KindBwrap {
		return runner.NewBwrap(rc, pwd)
	}
	return runner.NewOCI(rc, pwd)
}

// buildRunnerForKind is runnerForKind's `launcher build` counterpart: the
// bwrap arm realizes store closures rather than running an agent.
func buildRunnerForKind(c config, rc runner.Config, pwd string) runner.Runner {
	if c.runnerKind == freshness.KindBwrap {
		return runner.NewBwrapBuild(rc, pwd)
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
// (integration/<parent>, ADR 0033) once cf.BranchExists confirms it's there,
// so each Box builds on whatever has landed so far. That ref only exists once
// some seam has landed, so a first or wholly independent seam must fall back
// to c.baseBranch — as must a BranchExists error, logged rather than silent
// since ResolveEnv's signature leaves no error to propagate. c.baseBranch
// still reaches newCodeForge unchanged either way, since
// ensureIntegrationBranch needs it to seed integration/<parent> the first
// time. A missing Integration branch is silently expected only for a
// blocker-free seam; one with blockers should have been held by the #2130
// readiness gate, so that combination is logged loudly.
func localBaseBranchResolver(c config, it forge.IssueTracker, lw *localloop.Wired, cf forge.CodeForge, caps forge.Capabilities) func(num, name string) string {
	if !caps.ForgeDescriptor.HostMediatedRemote {
		return func(_, name string) string { return resolveBoxEnvVar(name) }
	}
	return func(num, name string) string {
		if name == "BASE_BRANCH" {
			// Resolved fresh on every call rather than cached at
			// construction: a later seam's dispatch within the same
			// continuous run must see integration/<parent> as it exists at
			// that moment, and each num may resolve to a different parent's
			// Integration branch entirely. The parent comes through lw, the
			// same sealed SanitizedParent value CodeForgeForIssue and
			// Surface consume.
			integrationBranch := local.IntegrationBranch(lw.ResolveParent(num))
			exists, err := cf.BranchExists(integrationBranch)
			switch {
			case err == nil && exists:
				return integrationBranch
			case err != nil:
				fmt.Printf("!! BASE_BRANCH: checking %s: %v; falling back to %s\n", integrationBranch, err, c.baseBranch)
			default:
				// integration/<parent> does not exist yet. A blocker-free
				// seam legitimately seeds from base -- stay silent. A seam
				// that HAS blockers reaching here means the #2130 readiness
				// gate let it slip onto bare base; make that loud.
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
// separation, ADR 0038's backend-prefixed token knobs). The Box receives the
// override as its own token while the launcher's own os.Getenv(token) stays
// untouched for merges, labels, and every other host-side forge call. A row
// with no boxTokenEnvVar (jira, local, git) never matches. Checked ahead of
// next's own CODE_FORGE fan-out so the override applies under every Code
// Forge, not just one.
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

// retryPolicy builds the transient-retry tuning once, so dispatchConfig,
// settleConfig, and wavesConfig all carry the same value instead of
// independently converting the same three ints.
func retryPolicy(c config) retry.Policy {
	return retry.Policy{
		Max:    c.transientRetryMax,
		Unit:   time.Duration(c.transientBackoffSecs) * time.Second,
		Jitter: time.Duration(c.holdJitterSecs) * time.Second,
	}
}

// dispatchConfig builds the subset of config a dispatch.Factory needs.
// OpenPRForIssue keeps a zero-exit rate-limited retry from re-running a box
// whose work already landed a PR; ResolveOpenPR reports Found: false for a
// push-only Code Forge, so the retry proceeds unguarded there with no extra
// guard needed here.
func dispatchConfig(c config, it forge.IssueTracker, lw *localloop.Wired, cf forge.CodeForge, caps forge.Capabilities) dispatch.Config {
	trackerAxisRead, trackerAxisWrite, trackerAxisFiler, forgeBackend := resolveTrackerAndForgeSignals(c.codeForge, c.issueTracker)
	filerEnabled, workerProvisioned, reviewLoopInline, reviewLoopOrchestrator := resolveAgentPresenceSignals(c.driver)
	return dispatch.Config{
		BoxEnvVars:               c.boxEnvVars,
		ResolveEnv:               boxTokenResolver(localBaseBranchResolver(c, it, lw, cf, caps)),
		Kind:                     c.dispatchKind,
		SelfContained:            c.selfContained,
		Capabilities:             caps,
		BoxForgeAndIssueAccess:   c.boxForgeAndIssueAccess,
		TrackerAxisRead:          trackerAxisRead,
		TrackerAxisWrite:         trackerAxisWrite,
		TrackerAxisFiler:         trackerAxisFiler,
		ForgeBackend:             forgeBackend,
		FilerEnabled:             filerEnabled,
		WorkerProvisioned:        workerProvisioned,
		ReviewLoopInline:         reviewLoopInline,
		ReviewLoopOrchestrator:   reviewLoopOrchestrator,
		Policy:                   retryPolicy(c),
		DriverSessionCacheDir:    c.driverSessionCacheDir,
		RegistryProxyUpstreamURL: c.registryProxyUpstreamURL,
		RegistryProxyCredential:  c.registryProxyCredential,
		OpenPRForIssue: func(number string) (bool, error) {
			res, err := forge.ResolveOpenPR(cf, number)
			return res.Found, err
		},
	}
}

// newDispatchFactory constructs the dispatch.Factory for one top-level
// dispatch entry point. A driver-cache creation failure is logged and
// degrades to no cache (fix boxes cold-start) rather than failing the
// dispatch -- the cache is a resume optimization, not a correctness
// requirement.
func newDispatchFactory(c config, pwd string, r runner.Runner, it forge.IssueTracker, lw *localloop.Wired, cf forge.CodeForge, caps forge.Capabilities) *dispatch.Factory {
	f, err := dispatch.NewFactory(dispatchConfig(c, it, lw, cf, caps), pwd, r, newDriver(c), dispatch.RealClock())
	if err != nil {
		fmt.Fprintf(os.Stderr, "==> driver cache unavailable (%v) -- fix boxes will cold-start\n", err)
	}
	return f
}

// settleConfig builds the subset of config a settle.Settle needs. OutboxDir
// resolves an issue number to the same per-issue outbox path runOnce mounts;
// only a Code Forge implementing forge.BundleRelay consults it.
// CodeForgeForIssue resolves each issue's own CodeForge instance (ADR 0033):
// under CODE_FORGE=local, one keyed to that issue's resolved parent, taken
// from the caller's single localloop.Wired so it is the same sealed value the
// base-branch resolver and surface grouping consume. Every other codeForge
// gets cf back unchanged — the very instance New() received, so a caller
// substituting a fake is honored rather than silently bypassed.
//
// Gotcha: that guarantee covers CodeForgeForIssue only, not caps. caps is
// resolved once against whatever cf the caller held at the time, so
// substituting cf here without re-resolving caps yields stale
// PRForge/LandingRecorder handles silently rather than a failure.
func settleConfig(c config, lw *localloop.Wired, cf forge.CodeForge, caps forge.Capabilities) settle.Config {
	return settle.Config{
		Capabilities:      caps,
		MergeMode:         c.mergeMode,
		MergeGuardPaths:   c.mergeGuardPaths,
		CompleteLabel:     c.completeLabel,
		MergePollInterval: c.mergePollInterval,
		MergePollTimeout:  c.mergePollTimeout,
		MaxFixAttempts:    c.maxFixAttempts,
		MaxRebaseAttempts: c.maxRebaseAttempts,
		// Policy reuses dispatch's transient-retry tuning rather than a
		// second knob pair: the rebase-push backoff/jitter and the
		// merge-transient retry cap (not MaxRebaseAttempts, a
		// merge-conflict budget) all read this one value. Clock is left
		// zero; settle.New defaults it to RealClock.
		Policy:             retryPolicy(c),
		MaxBudgetTokens:    c.maxBudgetTokens,
		MaxBudgetUSD:       c.maxBudgetUSD,
		PreflightStaleBase: c.preflightStaleBase,
		OutboxDir:          lw.OutboxDir,
		CodeForgeForIssue: func(num string) forge.CodeForge {
			if !caps.ForgeDescriptor.HostMediatedRemote {
				return cf
			}
			return lw.CodeForgeForIssue(num)
		},
		ReadOnly:   c.boxForgeAndIssueAccess == "read-only",
		BaseBranch: c.baseBranch,
	}
}

// localloopConfig builds the subset of config a localloop.Wire needs, shared
// by every construction site so they can never drift out of agreement on
// which Accumulation repo, base branch, or git identity a seam lands through.
func localloopConfig(c config) localloop.Config {
	return localloop.Config{
		AccumulationRepoDir: c.codeForgeAccumulationRepoDir,
		BaseBranch:          c.baseBranch,
		GitUserName:         c.gitUserName,
		GitUserEmail:        c.gitUserEmail,
		BranchPrefix:        c.branchPrefix,
	}
}

// newSettle constructs the Settler reused across every issue in one dispatch
// invocation: research's one-shot ResearchSettle, or work's full merge gate.
func newSettle(c config, it forge.IssueTracker, lw *localloop.Wired, cf forge.CodeForge, caps forge.Capabilities) settle.Settler {
	if c.dispatchKind == dispatchKindResearch {
		vl := researchVerdictLabels(c)
		filerEnabled, _, _, _ := resolveAgentPresenceSignals(c.driver)
		if c.boxForgeAndIssueAccess == "read-only" {
			return settle.NewResearchSettleReadOnly(it, vl, filerEnabled)
		}
		return settle.NewResearchSettle(it, vl, filerEnabled)
	}
	return settle.New(settleConfig(c, lw, cf, caps), it, cf)
}

func wavesConfig(c config) waves.Config {
	// "work" is not a CLI verb; the dispatch subcommand is.
	verb := "dispatch"
	if c.dispatchKind == dispatchKindResearch {
		verb = dispatchKindResearch
	}
	return waves.Config{
		MaxParallel:    c.maxParallel,
		MaxJobs:        c.maxJobs,
		OverlapGate:    c.overlapGate,
		CompleteLabel:  c.completeLabel,
		FailedLabel:    c.failedLabel,
		IgnoreBlockers: c.dispatchKind == dispatchKindResearch,
		Verb:           verb,
		// Same tuning dispatch's exit-retry path and settleConfig thread,
		// here reaching RunContinuous's rate-limited re-discover loop.
		// Clock is left zero; RunContinuous defaults it to RealClock.
		Policy: retryPolicy(c),
	}
}

// selectiveWavesConfig builds the wave-engine config for the operator-
// specified `dispatch <nums>` path: MAX_JOBS never applies to an explicit
// selection (the operator already named the exact issues to run), so it's
// zeroed regardless of the global config value.
func selectiveWavesConfig(c config) waves.Config {
	cfg := wavesConfig(c)
	cfg.MaxJobs = 0
	return cfg
}

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
func checkAutoMergePreflight(c config, caps forge.Capabilities) error {
	if c.mergeMode != "auto" {
		return nil
	}
	if caps.PRForge == nil {
		return fmt.Errorf("MERGE_MODE=auto requires CODE_FORGE=github (got %q) — auto-merge is a GitHub-native feature with no meaning off github; switch to MERGE_MODE=manual or immediate", c.codeForge)
	}
	canAuto, err := caps.PRForge.CanAutoMerge()
	if err != nil {
		return fmt.Errorf("MERGE_MODE=auto: auto-merge capability check failed: %w", err)
	}
	if !canAuto {
		return fmt.Errorf("MERGE_MODE=auto: the repo does not allow auto-merge — enable \"Allow auto-merge\" in repo Settings → General, or switch to MERGE_MODE=manual")
	}
	return nil
}

// errLaunchGateConfigInvalid is the sentinel checkReadOnlyCapabilityGate and
// checkNetworkModeRuntimeGate wrap their misconfiguration errors with.
// Deliberately distinct from bootstrap.go's errConfigInvalid: these gates are
// also called directly by bootstrap.go and by preview.go, so reusing
// errConfigInvalid would extend bootstrapExitCode's exit 6 — a versioned code
// reserved for validate(c) failures — to dispatch/recover/preview.
// doctorExitCodeFor checks for this sentinel instead, keeping `spindrift
// doctor` at exit 2 without touching those other exit codes.
var errLaunchGateConfigInvalid = errors.New("launch gate config invalid")

// launchGateConfigError's Error() deliberately returns only the
// operator-facing message text, never the sentinel's own, so
// dispatch/recover/preview print it verbatim and unprefixed. Unwrap() still
// surfaces the sentinel for doctor.go's exit-code classification.
type launchGateConfigError struct {
	msg string
}

func (e *launchGateConfigError) Error() string { return e.msg }
func (e *launchGateConfigError) Unwrap() error { return errLaunchGateConfigInvalid }

func newLaunchGateConfigError(format string, args ...any) error {
	return &launchGateConfigError{msg: fmt.Sprintf(format, args...)}
}

// checkReadOnlyCapabilityGate enforces BOX_FORGE_AND_ISSUE_ACCESS=read-only's
// capability requirement (extending ADR 0032/0033's host-mediated model to
// the github backends): the Box may only be denied a write token when the
// Launcher can perform every write it would otherwise make, on both axes.
// read-write is a no-op.
//
// mkHarness's readOnlyCapabilityOk eval assert already rejects an incoherent
// pairing at `nix build` time, reading the same registry bits. This Go gate
// is only a backstop for a *runtime* override of those three knobs past what
// nix validated — hence the by-name registry lookup rather than inspecting
// live cf/it interfaces, since the registry is what the eval assert checked.
func checkReadOnlyCapabilityGate(c config) error {
	if c.boxForgeAndIssueAccess != "read-only" {
		return nil
	}
	forgeRow, ok := backendByName(c.codeForge)
	if !ok {
		return newLaunchGateConfigError("BOX_FORGE_AND_ISSUE_ACCESS=read-only: CODE_FORGE=%q is not a registered backend", c.codeForge)
	}
	if !forgeRow.RelayCapable {
		return newLaunchGateConfigError("BOX_FORGE_AND_ISSUE_ACCESS=read-only: the selected CODE_FORGE=%q does not implement bundle-relay for the Box's finished branch hand-off", c.codeForge)
	}
	trackerRow, ok := backendByName(c.issueTracker)
	if !ok {
		return newLaunchGateConfigError("BOX_FORGE_AND_ISSUE_ACCESS=read-only: ISSUE_TRACKER=%q is not a registered backend", c.issueTracker)
	}
	if !trackerRow.HostPostingCapable {
		return newLaunchGateConfigError("BOX_FORGE_AND_ISSUE_ACCESS=read-only: the selected ISSUE_TRACKER=%q does not implement host-posted comments and issue-filing", c.issueTracker)
	}
	return nil
}

// checkNetworkModeRuntimeGate backstops two NETWORK_MODE combinations that
// mkHarness's networkModeCoherenceOk eval assert cannot see, because that
// assert only runs against what a Consumer flake bakes at `nix build` time
// while NETWORK_MODE, PODMAN_NETWORK, and BWRAP_UNSHARE_NET are all
// runtime-overridable:
//
//  1. no-host-loopback has no bwrap rendering distinct from the
//     isolated-by-default "open" — pasta-backed netns isolation applies to
//     every mode except the "host" opt-out and "none", so the two render
//     byte-identical. Keyed on c.runnerKind, never c.runtime:
//     RUNNER_KIND=bwrap/RUNTIME=podman is supported and renders fine on the
//     OCI adapter, so keying on c.runtime would reject it and, worse, let it
//     fall through to bwrap.go's fail-open isolateNet=false.
//
//  2. A flake can bake just one of NETWORK_MODE / a raw knob and then take a
//     runtime override setting the other. Without this gate oci.go's
//     networkArg() picks the raw knob over the overridden mode ("raw wins
//     whenever set"), silently rendering full egress instead of the
//     isolation NETWORK_MODE asked for. Only the unambiguous subset is
//     covered: Go cannot distinguish "defaulted to open" from "explicitly
//     set to open" here, so — unlike the nix assert, which checks presence —
//     an explicit NETWORK_MODE=open plus a raw knob is left to raw-wins.
func checkNetworkModeRuntimeGate(c config) error {
	if c.networkMode == runner.NetworkModeNoHostLoopback && c.runnerKind == freshness.KindBwrap {
		return newLaunchGateConfigError("NETWORK_MODE=no-host-loopback is unsupported on RUNNER_KIND=bwrap -- it has no rendering distinct from the isolated-by-default NETWORK_MODE=open; use NETWORK_MODE=open instead, or RUNNER_KIND=oci for the docker/nerdctl inert-but-correct render")
	}
	if c.networkMode != runner.NetworkModeOpen && c.networkMode != "" && (c.podmanNetwork != "" || c.bwrapUnshareNet) {
		var rawKnobs []string
		if c.podmanNetwork != "" {
			rawKnobs = append(rawKnobs, "PODMAN_NETWORK")
		}
		if c.bwrapUnshareNet {
			rawKnobs = append(rawKnobs, "BWRAP_UNSHARE_NET")
		}
		return newLaunchGateConfigError("NETWORK_MODE=%s is set alongside raw network knob(s) %s -- there is no precedence rule between a runtime-overridden NETWORK_MODE and a raw knob; set only one", c.networkMode, strings.Join(rawKnobs, ", "))
	}
	return nil
}

// checkBwrapPastaGate refuses to launch rather than silently sharing the
// host network namespace when the bwrap runner needs pasta (own netns with
// working egress) and pasta is not on PATH. NetworkMode="host" (the
// documented opt-out) and "none" (fully offline) never invoke pasta. A raw
// BwrapUnshareNet knob paired with NetworkMode="host" would invoke pasta in
// bwrap.go's isolateNet computation, but checkNetworkModeRuntimeGate already
// rejects that combination first, so it is not a gap here.
func checkBwrapPastaGate(c config) error {
	if c.runnerKind != freshness.KindBwrap {
		return nil
	}
	if c.networkMode == runner.NetworkModeHost || c.networkMode == runner.NetworkModeNone {
		return nil
	}
	return runner.ValidatePasta()
}

// checkBwrapOverlayGate refuses to launch rather than let bwrap fail deep
// inside sandbox startup when the in-box /nix/store is made writable via an
// ephemeral tmpfs overlay (ADR 0042) but the host kernel/config does not
// allow an unprivileged user namespace to mount overlayfs. The three
// no-op conditions mirror bwrap.go's own AND-gate: without all three the
// overlay flags never render in buildArgs, leaving nothing to validate.
func checkBwrapOverlayGate(c config) error {
	if c.runnerKind != freshness.KindBwrap {
		return nil
	}
	if !c.nixStoreWritable {
		return nil
	}
	if c.nixConfigFile == "" {
		return nil
	}
	return runner.ValidateOverlay()
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
//	  binary, or both would be rebuilt against the current base-branch tip;
//	  in-flight Boxes finished, no new ones launched, and the driving loop
//	  should rebuild and re-invoke.
//	exit 5 (errImageHostTainted): CONTINUOUS_DISPATCH mode only — a stale
//	  divergence persisted after a rebuild to the base tip (a host-system
//	  derivation reached the image graph through a consumer flake); the
//	  driving loop must halt, not rebuild-and-retry.
//	exit 6 (errConfigInvalid, exitConfigInvalid): bootstrap()'s validate(c)
//	  step rejected the loaded config — distinguishes it from any other
//	  bootstrap failure, which still falls back to exit 1.
var errQueueEmpty = errors.New("queue empty")

// snapshotGeneration creates the nix-var store-DB snapshot generation a
// bwrap hot-swap (ADR 0043) is about to bind, once per successful swap under
// a nixInBox Consumer. A package-level var rather than a parameter threaded
// alongside eval/realize, following bwrap.go's execCommand/statHostNixDB
// convention: this seam only fires when c.nixConfigFile is set, so threading
// it would force a dozen unrelated runContinuousDispatch test call sites to
// pass a value they don't care about. Tests that do exercise the swap path
// reassign it to a fake so they never shell out to sqlite3.
var snapshotGeneration = runner.SnapshotGeneration

// exitConfigInvalid is the exit code for a bootstrap() failure wrapping
// errConfigInvalid -- see the exit-code doc comment above.
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
		return []issue{newIssue(fi)}, origin, nil
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
		issues = append(issues, newIssue(fi))
	}
	return issues, nil
}

// readinessFor resolves a waves.Batch from a raw issues batch. Ordering
// constraint: discover must call logDiscoveryPoll between its queryOpenIssues
// and this call, so the announcement isn't delayed behind the DepsOf fan-out.
func readinessFor(it forge.IssueTracker, issues []issue) (waves.Batch, error) {
	waveIssues := toWaveIssues(issues)
	result, err := waves.NewReadiness(it, waveIssues)
	if err != nil {
		return waves.Batch{}, err
	}
	return waves.Batch{Issues: waveIssues, Edges: result.Edges, Sources: result.Sources, Failed: result.Failed}, nil
}

// logDiscoveryPoll decides whether a continuous-dispatch refill poll should
// print the "==> querying open" announcement, then records this poll's issue
// numbers into seen. The first poll of a run always announces, regardless of
// what seen already holds — a continuous run's first discover establishes the
// baseline exactly once. Every later poll stays silent unless it surfaces an
// issue number not in seen, and then names only the new ones.
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

// recoverByNumber resolves the open PR for issueNum, draft or not, and drives
// it through the adopt-and-gate path: the sole way an agent-in-progress issue
// is ever adopted, gated on the operator's explicit agent-recover label
// rather than any automatic sweep (#600). With no open PR it falls back to a
// second adopt arm: recover the driver's last genuine self-report from the
// on-disk pass logs and, when that is a success and the prior run left a
// relayable finished branch in the outbox, open the PR on that relayed
// branch and drive it through the same merge gate. Returns an error when the
// issue cannot be fetched, or neither arm applies (labels untouched in that
// case); callers should treat those as non-success exits.
func recoverByNumber(c config, it forge.IssueTracker, cf forge.CodeForge, caps forge.Capabilities, pwd string, f *dispatch.Factory, s settle.WorkSettler, issueNum string) error {
	fi, err := it.Issue(issueNum)
	if err != nil {
		return recoverFailed(it, caps, issueNum, fmt.Errorf("issue %s: %w", issueNum, err))
	}
	iss := newIssue(fi)
	branch := cf.AgentBranch(iss.number)
	backoff := retry.LinearBackoff{Unit: time.Duration(c.transientBackoffSecs) * time.Second, Clock: retry.RealClock()}
	// transientRetryMax is a max-retries knob everywhere else (dispatch/retry.go),
	// so the first attempt is on top of it: maxAttempts = retries + 1.
	res, prErr := forge.ResolveOpenPRWithRetry(cf, iss.number, backoff, c.transientRetryMax+1)
	if prErr != nil {
		return recoverFailed(it, caps, issueNum, fmt.Errorf("issue %s: resolve PR: %w", issueNum, prErr))
	}
	if !res.Found {
		// The self-report walk runs even on a near-miss resolveErr (an
		// unparseable leading-token line propagates as Resolve's own error),
		// so a driver's genuine but unparseable success report stays
		// adoptable.
		//
		// SettleRelayedBranch is attempted even when the log carried no
		// self-report at all: for the local push-only shape it accepts a
		// bundle sitting in the outbox as sufficient evidence on its own — a
		// signal-killed Box never gets to print a self-report, and this
		// later, separate process cannot see the original run's in-memory
		// KilledBySignal bit. Its own gate decides whether anything is
		// recoverable; the "no open PR" exit below fires when it returns
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
		// SettleRelayedBranch's gate can reach CumulativeUsage/UsageReport/
		// Fix on this never-Run()'d Dispatch -- see the SettleAdopted arm's
		// identical call below for why.
		if err := d.EnsureRunLineage(); err != nil {
			fmt.Fprintf(os.Stderr, "    ?? #%s: ensure run lineage: %v\n", issueNum, err)
		}
		result := dispatch.Result{Resolved: resolved}
		sit := s.SituationFor(iss.number, res.Found, result)
		if s.SettleRelayedBranch(d, iss.number, 0, sit, result) {
			return nil
		}
		fmt.Printf("    #%s  status=skipped  note=no open PR on %s\n", issueNum, branch)
		return recoverFailed(it, caps, issueNum, fmt.Errorf("issue %s: no open PR", issueNum))
	}
	if err := os.MkdirAll(dispatch.HostLogDirFor(pwd), 0o755); err != nil {
		return fmt.Errorf("mkdir logs: %w", err)
	}
	d := f.New(iss.number, iss.title)
	defer d.Close()
	// This Dispatch adopts an already-open PR and never calls Run(), so it
	// never gets Run's quarantine-prior-run-logs guarantee for free.
	// EnsureRunLineage establishes it explicitly, once, before
	// CumulativeUsage/UsageReport/Fix ever read a pass log.
	if err := d.EnsureRunLineage(); err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: ensure run lineage: %v\n", issueNum, err)
	}
	s.SettleAdopted(d, iss.number, 0, res.URL)
	return nil
}

// recoverFailed is recoverByNumber's single terminal-failure exit: every
// error path funnels through it so a recover attempt that claimed an issue
// already in a successful terminal state can never downgrade it to
// agent-failed. The claim runs host-side ahead of this process
// (.github/workflows/agent-recover.yml) and strips the prior terminal label,
// so the pre-claim state has to be read back out of the issue's timeline via
// the optional PriorClaimStateReader surface (github only today). Anything
// else — no reader, no prior terminal label, or a prior agent-failed — falls
// straight through to origErr, preserving the park-to-agent-failed behavior
// the workflow's own "Park if nothing to recover" step depends on.
func recoverFailed(it forge.IssueTracker, caps forge.Capabilities, num string, origErr error) error {
	if caps.PriorClaimStateReader == nil {
		return origErr
	}
	prior, found, err := caps.PriorClaimStateReader.PriorClaimState(num)
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
// and drain/wave/fan-out dispatch.
func run(lc *launchContext) error {
	c, it, cf, f, s, pwd := lc.config, lc.issueTracker, lc.codeForge, lc.factory, lc.settle, lc.pwd
	caps := lc.capabilities
	lp := reconcile.NewFSProbe(pwd, lc.runner)

	fmt.Println(repoBanner(c))

	if err := checkAutoMergePreflight(c, caps); err != nil {
		return err
	}

	// A bare agent-in-progress issue is never adopted automatically here: it
	// carries no liveness signal, so it cannot be told apart from one a live
	// runner is actively committing to right now (#600). The only adopt path
	// is the explicit, operator-driven `spindrift recover <n>`.
	if resolveOrigin(c) == waves.OriginDiscovered && c.continuousDispatch {
		return runContinuousDispatch(c, it, cf, pwd, f, s, runner.NixEvaluator{}, runner.NixRealizer{}, lp)
	}

	issues, origin, err := discoverIssues(c, it)
	if err != nil {
		return err
	}

	if origin == waves.OriginDiscovered && len(issues) == 0 {
		fmt.Printf("no open '%s' issues — nothing to do.\n", c.label)
		if err := reconcileAfterDispatch(c, it, cf, lp, caps, pwd, os.Stdout); err != nil {
			return err
		}
		return errQueueEmpty
	}

	readiness, err := waves.NewReadiness(it, toWaveIssues(issues))
	if err != nil {
		return err
	}
	in := waves.NewInput(origin, readiness, toWaveIssues(issues))
	cfg := wavesConfig(c)
	cfg.SeedScopeOf = localloop.SeedScopeResolver(it, caps)
	claimer := waves.NewLabelClaimer(it, c.label, c.inProgressLabel)
	if err := waves.Dispatch(cfg, it, cf, pwd, f, s, in, claimer); err != nil {
		return err
	}

	fmt.Print(dispatchCompletionBanner(c))
	return reconcileAfterDispatch(c, it, cf, lp, caps, pwd, os.Stdout)
}

// continuousDispatchErr picks runContinuousDispatch's terminal error in
// priority order: ErrImageStale wins over a stashed firstQueryErr. No
// currently-reachable production path sets both at once, so this precedence
// is documented, tested intent (TestContinuousDispatchErr_* pin it against
// this helper in isolation) rather than a live guard — kept in case a future
// caller reintroduces a path that can set both.
func continuousDispatchErr(err, firstQueryErr error) error {
	if errors.Is(err, waves.ErrImageStale) {
		return waves.ErrImageStale
	}
	if firstQueryErr != nil {
		return firstQueryErr
	}
	return err
}

// runContinuousDispatch is the entry point for CONTINUOUS_DISPATCH, the
// opt-in slot-refill dispatch mode (#527). It hands off to
// waves.RunContinuous with a Discoverer that re-runs the label query and
// edge build on every refill, and a FreshnessChecker wired to
// freshness.Probe against the fetched base-branch tip. There is deliberately
// no empty-queue precheck: the discover closure's first call, made from
// RunContinuous's own bootstrap refill, is the only query a continuous run
// makes before its first dispatch.
//
// firstQueryEmpty, set by that same first call, records whether it found no
// open issues at all, as opposed to open ones that turned out blocked or
// deferred — only the former maps ErrOpenNoneDispatchable to errQueueEmpty/
// exit 2 below. It lives here rather than inside waves.RunContinuous because
// that sentinel is shared with the console package's Discoverer, which
// pre-filters claimed/dissolved picks, so a zero-issue result there doesn't
// mean the tracker is empty.
func runContinuousDispatch(c config, it forge.IssueTracker, cf forge.CodeForge, pwd string, f *dispatch.Factory, s settle.Settler, eval freshness.Evaluator, realize freshness.Realizer, lp reconcile.LivenessProbe) error {
	// Resolved fresh here rather than threaded in: unlike run's
	// lc.capabilities, this function's bare argument list carries no
	// aggregate context to take one from.
	forgeDesc, _ := backend.ByName(c.codeForge)
	trackerDesc, _ := backend.ByName(c.issueTracker)
	caps := forge.ResolveCapabilities(cf, it, forgeDesc, trackerDesc)

	firstQuery := true
	firstQueryEmpty := false
	var firstQueryErr error
	// logDiscoveryPoll's per-run dedupe state, so a long-running refill loop
	// doesn't repeat the "querying open" line every cycle once the queue has
	// settled.
	seenIssues := make(map[string]bool)
	// firstQuery/firstQueryEmpty/firstQueryErr need no locking: every
	// discover() call runs under RunContinuous's own mutex, so this closure
	// is never invoked concurrently with itself.
	discover := func() (waves.Batch, error) {
		wasFirst := firstQuery
		issues, err := queryOpenIssues(c, it)
		if firstQuery {
			firstQuery = false
			firstQueryErr = err
			firstQueryEmpty = err == nil && len(issues) == 0
		}
		// A non-first poll that errors passes an empty slice here, so
		// logDiscoveryPoll finds nothing new and this poll goes unannounced.
		//
		// Ordering: this runs before readinessFor's DepsOf fan-out on
		// purpose. The announcement is about the poll itself, so a slow
		// per-issue round-trip must never delay it.
		logDiscoveryPoll(c, issues, wasFirst, seenIssues)
		if err != nil {
			return waves.Batch{}, err
		}
		return readinessFor(it, issues)
	}
	guard := freshness.NewGuard(pwd)
	var staleResult freshness.Result
	// currentImageTag is the effective "loaded" baseline Probe compares
	// against -- it advances to res.TipTag after every successful hot-swap
	// below, so a later probe against an unchanged base tip converges to
	// fresh instead of re-detecting the same divergence forever (ADR 0043).
	currentImageTag := c.imageTag
	// swapClassified records that guard.Classify already ran against this
	// call's probe result inside fresh()'s hot-swap branch -- true whatever
	// disposition came back. Classify must never run twice on the same
	// Result: it records/clears persisted state, and a second call would read
	// its own write back as "the prior run's" state (see Guard.Classify).
	var swapClassified bool
	// swapHostTainted is that Classify call's disposition, and therefore also
	// whether the swap branch already printed HostTaintDiagnostic (so the
	// terminal switch must not print it again).
	var swapHostTainted bool

	fresh := func() (bool, bool, string) {
		res := freshness.Probe(freshness.ProbeSpec{
			RunnerKind:         c.runnerKind,
			Pwd:                pwd,
			BaseBranch:         c.baseBranch,
			FlakeImageAttr:     c.flakeImageAttr,
			ImageTag:           currentImageTag,
			FlakeLauncherAttr:  c.flakeLauncherAttr,
			LoadedLauncherHash: c.loadedLauncherHash,
		}, eval)

		if c.runnerKind == freshness.KindBwrap && res.Applicable && !res.ImageFresh && res.LauncherFresh && c.flakeLauncherAttr != "" {
			// drain records res as staleResult and reports not-fresh -- the
			// shared exit every failure branch below falls back to.
			drain := func() (bool, bool, string) {
				staleResult = res
				return res.Applicable, false, res.Message
			}

			// Box-only staleness under bwrap (ADR 0043): hot-swap instead of
			// draining. This branch is reached exactly when the image
			// dimension alone is stale -- res.LauncherFresh keeps a
			// both-moved verdict out ("when both moved, the launcher wins")
			// and KindBwrap keeps every OCI verdict out. c.flakeLauncherAttr
			// != "" requires the launcher dimension to have actually been
			// evaluated: Probe hard-codes LauncherFresh true when the attr is
			// unconfigured, which would otherwise let that case hot-swap
			// forever with no incidental restart to catch a launcher-side
			// change. An unconfigured launcher drains instead.
			//
			// guard.Classify decides Rebuild (realize and bind) vs
			// HostTainted (this divergence already failed to converge) —
			// the same halt the drain path gets, not a second mechanism.
			// Called exactly once per entry here; see swapClassified above.
			disposition := guard.Classify(res)
			swapClassified = true
			swapHostTainted = disposition == freshness.HostTainted
			if swapHostTainted {
				fmt.Fprintln(os.Stdout, freshness.HostTaintDiagnostic(c.runnerKind, c.baseBranch, res.Rev, c.flakeImageAttr, res.TipTag, currentImageTag))
				return drain()
			}
			if err := freshness.RealizeSync(realize, pwd, res, c.flakeImageAttr); err != nil {
				fmt.Fprintf(os.Stderr, "==> bwrap hot-swap: realize failed, draining instead: %v\n", err)
				return drain()
			}
			// res.TipTag becomes both a bind-mount source and a path
			// component (the snapshot generation's dir name) -- reject
			// anything that isn't a genuine nix store path before either
			// use, rather than trust Probe never to hand back a foreign host
			// directory.
			if !strings.HasPrefix(res.TipTag, "/nix/store/") {
				fmt.Fprintf(os.Stderr, "==> bwrap hot-swap: realized tip tag %q is not a nix store path, draining instead\n", res.TipTag)
				return drain()
			}
			// nixInBox Consumers (c.nixConfigFile != "", the same gate
			// bwrapAdapter uses to decide the /nix/var overlay is in play)
			// need a real store-DB snapshot generation on disk before any Box
			// can bind it: nothing else ever writes one for a hot-swapped
			// closure (ADR 0043). Drain on failure rather than bind a
			// generation whose snapshot dir doesn't exist, which would
			// otherwise surface later as every Box launch's own "no longer
			// exists" stat-guard failure.
			if c.nixConfigFile != "" {
				if err := snapshotGeneration(pwd, res.TipTag); err != nil {
					fmt.Fprintf(os.Stderr, "==> bwrap hot-swap: snapshot generation failed, draining instead: %v\n", err)
					return drain()
				}
			}
			gen := runner.NewAgentGeneration(res.TipTag)
			f.SetAgentGeneration(&gen)
			currentImageTag = res.TipTag
			fmt.Printf("==> hot-swapped bwrap agent closure to %s tip %s (%s)\n", c.baseBranch, res.Rev, res.TipTag)
			return res.Applicable, true, res.Message
		}

		// fresh() runs under RunContinuous's mutex, so this plain write is
		// serialized. A staleResult set here was NOT classified by the swap
		// branch above -- reset the two swap flags so the terminal switch
		// classifies this genuinely new Result normally.
		if res.Applicable && !res.Fresh {
			staleResult = res
			swapClassified = false
			swapHostTainted = false
		}
		freshness.RealizeTip(realize, pwd, res, c.flakeImageAttr)
		return res.Applicable, res.Fresh, res.Message
	}

	cfg := wavesConfig(c)
	cfg.SeedScopeOf = localloop.SeedScopeResolver(it, caps)
	// pending is the quiet, unlogged query waves.Queue.Pending uses for the
	// stale-drain report's heldBack number: discover's shape minus the
	// announcement and its firstQuery*/seenIssues bookkeeping, since a
	// reporting-only query is not a dispatch attempt. The count must go
	// through waves.CountReady, not a raw len(issues) — heldBack is
	// readiness-filtered, and the claimed set is dropped so a
	// still-eventually-consistent re-list doesn't double-count an issue this
	// run already dispatched.
	pending := func(claimed map[string]bool) (int, error) {
		issues, err := queryOpenIssues(c, it)
		if err != nil {
			return 0, err
		}
		batch, err := readinessFor(it, issues)
		if err != nil {
			return 0, err
		}
		return waves.CountReady(cfg, it, cf, batch, claimed), nil
	}
	queue := waves.NewHeadlessQueue(discover, waves.NewLabelClaimer(it, c.label, c.inProgressLabel), pending, pwd)
	if err := waves.RunContinuous(cfg, nil, it, cf, pwd, f, s, queue, fresh); err != nil {
		// No reachable path sets err and firstQueryErr both: refill's
		// bootstrap never releases its mutex to a second refill once the
		// first discover() has failed and aborted the run. See
		// continuousDispatchErr for why the precedence is kept anyway.
		switch terminal := continuousDispatchErr(err, firstQueryErr); {
		case errors.Is(terminal, waves.ErrImageStale):
			// swapClassified means fresh()'s hot-swap branch already ran
			// guard.Classify against this exact staleResult, on either
			// disposition. Classify must never run twice on the same Result
			// (record/clear side effect), so trust swapHostTainted instead of
			// re-classifying, and skip the diagnostic the swap branch has
			// already printed.
			hostTainted := swapHostTainted
			if !swapClassified {
				hostTainted = guard.Classify(staleResult) == freshness.HostTainted
				if hostTainted {
					fmt.Fprintln(os.Stdout, freshness.HostTaintDiagnostic(c.runnerKind, c.baseBranch, staleResult.Rev, c.flakeImageAttr, staleResult.TipTag, currentImageTag))
				}
			}
			if hostTainted {
				return errImageHostTainted
			}
			return waves.ErrImageStale
		case firstQueryErr != nil:
			// refill swallows every discover error to stderr and retries on
			// the next trigger — fine for refill 2+, but the first call has
			// no next trigger once nothing ever dispatches. Surface it here
			// rather than let it flatten into ErrOpenNoneDispatchable/exit 3.
			return firstQueryErr
		}
		if errors.Is(err, waves.ErrOpenNoneDispatchable) && firstQueryEmpty {
			fmt.Printf("no open '%s' issues — nothing to do.\n", c.label)
			if err := reconcileAfterDispatch(c, it, cf, lp, caps, pwd, os.Stdout); err != nil {
				return err
			}
			_ = guard.Reset()
			return errQueueEmpty
		}
		return err
	}
	fmt.Print(dispatchCompletionBanner(c))
	return reconcileAfterDispatch(c, it, cf, lp, caps, pwd, os.Stdout)
}

func cmdBuild() int {
	if err := build(); err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
		return 1
	}
	return 0
}

// cmdConsole is the `console` subcommand: the interactive picks-only driving
// loop. Fresh and RebuildFn wire the same freshness.Probe seam
// runContinuousDispatch uses for the headless exit-4 path into an in-session
// banner/hold plus a one-key rebuild instead of an exit. stdin/stdout are
// threaded explicitly rather than read from os directly, so a test can drive
// the real Bubble Tea program with a scripted reader instead of a live TTY.
func cmdConsole(lc *launchContext, stdin io.Reader, stdout io.Writer) int {
	defer lc.cleanup()
	// Bubble Tea owns the terminal in alt-screen/raw mode; a heartbeat
	// line's bare \n moves the cursor down but not back to column 0,
	// stairstepping across the screen. The sidebar activity feed already
	// re-renders the same lines from the pass log on disk, so the
	// live-terminal echo is both redundant and corrupting here.
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
			return recoverByNumber(lc.config, lc.issueTracker, lc.codeForge, lc.capabilities, lc.pwd, lc.factory, lc.workSettle(), issueNum)
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
// PR (draft or not) with no outcome line and drive it through the merge gate.
func cmdRecover(lc *launchContext, issueNum string) int {
	defer lc.cleanup()
	if err := recoverByNumber(lc.config, lc.issueTracker, lc.codeForge, lc.capabilities, lc.pwd, lc.factory, lc.workSettle(), issueNum); err != nil {
		if writeErr := writeGithubOutput("recover-reason", err.Error()); writeErr != nil {
			fmt.Fprintf(os.Stderr, "warning: writing recover-reason output: %v\n", writeErr)
		}
		fmt.Fprintf(os.Stderr, "%s\n", err)
		return 1
	}
	return 0
}

// cmdPreview reports what dispatch would do without launching any Box.
func cmdPreview(issueNums []string) int {
	if err := preview(issueNums); err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
		return 1
	}
	return 0
}

// selectiveDispatchExitCode translates selectiveListDispatch's result into
// the launcher's process exit code: 3 for open issues that exist but none
// are dispatchable — a selective wave can defer every listed issue just as a
// queue drain can — 1 for any other error, 0 on success.
func selectiveDispatchExitCode(lc *launchContext, nums []string, forceYes bool) int {
	err := selectiveListDispatch(lc.config, lc.issueTracker, lc.codeForge, lc.capabilities, lc.pwd, lc.factory, lc.settle, nums, forceYes, os.Stdin, os.Stdout)
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
// operator-supplied issue list that bypasses the label/barrier gates.
func cmdDispatchSelective(lc *launchContext, nums []string, forceYes bool) int {
	defer lc.cleanup()
	return selectiveDispatchExitCode(lc, nums, forceYes)
}

// exitCodeFor translates a run/runContinuousDispatch error into the
// launcher's process exit code: 2 for an empty dispatch queue, 3 for open
// issues that exist but none are dispatchable, 4 for CONTINUOUS_DISPATCH
// stopping on a stale image (a rebuild-and-retry signal), 5 for
// CONTINUOUS_DISPATCH halting on a non-converging, host-tainted stale image
// (a rebuild cannot fix it, so the driving loop must stop instead of looping
// on exit 4 forever), 1 for any other error, 0 on success.
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
// errConfigInvalid, 1 for any other error, 0 on success.
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

func runExitCode(lc *launchContext) int {
	err := run(lc)
	code := exitCodeFor(err)
	if code == 1 && err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
	}
	return code
}

// cmdDispatch is the `dispatch` subcommand: drain the labeled queue.
func cmdDispatch(lc *launchContext) int {
	defer lc.cleanup()
	return runExitCode(lc)
}

// flushAmbientWarnings writes snapshotted ambient-env deprecation warnings to
// stderr; every mainRun early-return site passes the same buffer (ADR 0020).
func flushAmbientWarnings(stderr io.Writer, warnings *bytes.Buffer) {
	stderr.Write(warnings.Bytes())
}

// verbHandler is the uniform shape every entry in verbHandlers implements,
// even though the underlying cmd* functions take different arguments: args
// is args[1:] (the subcommand's own arguments, subcommand name stripped).
type verbHandler func(args []string, stderr io.Writer) int

// verbHandlers is the single source of truth for "what subcommands actually
// exist" — a test enumerates its keys to prove that set programmatically. The
// hidden __complete-issues shell-completion verb is deliberately absent
// (mainRun dispatches it separately, ahead of the table lookup), since it
// isn't one of the documented verbs.
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
		// remaining is used directly, unfiltered — see
		// parseIssuePositionals (flags.go) for why recover's non-numeric IDs
		// must survive.
		_, _, selfContained, remaining := parseIssuePositionals(args)
		if selfContained {
			fmt.Fprintln(stderr, "flag --self-contained is only valid for the research subcommand")
			return 1
		}
		if len(remaining) < 1 {
			fmt.Fprintln(stderr, "usage: spindrift recover <issue-number>")
			return 1
		}
		lc, err := bootstrap(true, dispatchKindWork, false)
		if err != nil {
			fmt.Fprintf(stderr, "%s\n", err)
			return 1
		}
		return cmdRecover(lc, remaining[0])
	},
	"preview": func(args []string, stderr io.Writer) int {
		// Unlike dispatch/recover, preview never rejects --self-contained; it
		// is silently ignored, matching preview's long-standing behavior.
		_, _, _, remaining := parseIssuePositionals(args)
		return cmdPreview(remaining)
	},
	"dispatch": func(args []string, stderr io.Writer) int {
		noBuild, forceYes, selfContained, remaining := parseIssuePositionals(args)
		if selfContained {
			fmt.Fprintln(stderr, "flag --self-contained is only valid for the research subcommand")
			return 1
		}
		nums := remaining
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
		noBuild, forceYes, selfContained, remaining := parseIssuePositionals(args)
		nums := remaining
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
	// masks the ambient value the warning reports on (ADR 0020). Taken ahead
	// of every early return below so none of them silently drops it.
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

// Package main is the spindrift launcher: it orchestrates open issues into
// disposable containers. Nix-computed config reaches the binary as one
// Launcher input document via --input (ADR 0020); a CLI flag overrides the
// document, and an ambient knob env var still wins this release but draws a
// deprecation warning. Secrets and BOX_ENV_VARS plumbing stay env-only.
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
	"sync"
	"time"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/console"
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/local"
	"spindrift.dev/launcher/internal/freshness"
	"spindrift.dev/launcher/internal/inputdoc"
	"spindrift.dev/launcher/internal/localloop"
	"spindrift.dev/launcher/internal/reconcile"
	"spindrift.dev/launcher/internal/registryproxy"
	"spindrift.dev/launcher/internal/report"
	"spindrift.dev/launcher/internal/retry"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/settle"
	"spindrift.dev/launcher/internal/shutdown"
	"spindrift.dev/launcher/internal/stopsignal"
	"spindrift.dev/launcher/internal/terminate"
	"spindrift.dev/launcher/internal/waves"
)

type config struct {
	// One field per lib/env-schema.nix host-config entry (issue #2364, #2365),
	// loaded by loadSchemaConfig. Embedded by value so a copy-and-mutate helper
	// like applyDispatchKind can never alias the caller's config through it.
	schemaConfig

	// OCI image config baked by the nix wrapper. imageArchive/imageDrv/
	// nixBuilderImage/nixVolume are empty for bwrap. flakeImageAttr/imageTag are
	// dual-purpose (issue #2667): the OCI image's flake attr and content-hash
	// tag, or the bundled bwrap agent closure's flake attr and loaded output
	// path. flakeLauncherAttr is populated for both runtimes (issue #1364).
	imageArchive      string
	imageTag          string
	imageDrv          string
	nixBuilderImage   string
	nixVolume         string
	flakeImageAttr    string
	flakeLauncherAttr string

	// loadedLauncherHash is the bare 32-char nix store hash of the
	// launcher-currency flake package this binary was built from
	// (LAUNCHER_CURRENCY_HASH; issue #2677, issue #1364). freshness.Probe
	// compares it against a freshly-evaluated hash at the base-branch tip.
	loadedLauncherHash string

	// bwrap agent closure paths (bwrap only)
	agentFiles    string
	agentEnv      string
	agentFilesDrv string // .drv path, realized by `launcher build`
	agentEnvDrv   string // .drv path, realized by `launcher build`
	bakedPrefetch string
	passwdFile    string
	groupFile     string
	passwdFileDrv string // .drv path, realized by `launcher build`
	groupFileDrv  string // .drv path, realized by `launcher build`

	// nixConfigFile is the baked nix store path for /etc/nix/nix.conf (ADR
	// 0042, bwrap only); empty when the Consumer's nixInBox knob is off.
	nixConfigFile string
	// nixConfigFileDrv is its .drv path; `launcher build` realizes it and
	// snapshots the host nix store DB (ADR 0042).
	nixConfigFileDrv string

	// syscallFilterPath is the baked nix store path to the compiled BPF
	// syscall-filter file (issue #2670, bwrap only). Always populated, unlike
	// nixConfigFile: the filter is independent of the nixInBox knob.
	syscallFilterPath string
	// syscallFilterDrv is its .drv path, realized by `launcher build`.
	syscallFilterDrv string

	// nixStoreWritable gates whether the bwrap adapter overlays /nix/store as
	// an ephemeral tmpfs-backed writable layer instead of a plain read-only
	// bind (ADR 0042, bwrap only; issue #2665).
	nixStoreWritable bool

	// Runtime: podman | docker | rancher | bwrap (runner.ValidValues)
	runtime string

	// runnerKind selects the launcher's runner implementation: "bwrap" or "oci"
	// (issue #2538). Runner selection reads this, never runtime's raw value,
	// since runtime also carries operator-facing runtime names (podman, docker,
	// rancher) that aren't "oci" literally.
	runnerKind string

	// driver selects the Go Driver strategy (ADR 0009). Empty defaults to
	// "claude", matching the nix side's default.
	driver string

	// image is the OCI runtime image reference; defaults to imageTag, which for
	// bwrap holds the bundled agent closure's store path instead (issue #2667).
	// Only oci.go reads it, and oci.go is never reached for a bwrap runnerKind.
	image string

	// driverSessionCacheDir is the in-box mount target for the selected
	// Driver's session-state dir (ADR 0009), nix-baked at wrap time. Empty
	// when the Driver declares no session-state dir.
	driverSessionCacheDir string

	// registryProxyRoutes is the resolved registry-proxy route table (ADR 0045,
	// issue #3139). schemaConfig's registryProxyRoutesFile carries only a TOML
	// path, never a credential value; bootstrap resolves it exactly once via
	// buildRegistryProxyRoutes. Nil when the registry proxy is off entirely.
	registryProxyRoutes []registryproxy.Route

	// boxEnvVars is the space-separated list of env var names forwarded into
	// each Box container. The nix-rendered preamble sets it from the schema's
	// boxEnv=true entries, so the Go source never enumerates them by hand.
	boxEnvVars string

	// dispatchKind is "" (doctor, reconcile, preview, none of which dispatch)
	// or dispatchKindWork/dispatchKindResearch (ADR 0022). Set via
	// applyDispatchKind, never read from the environment directly: it is
	// operator intent carried by which subcommand launched, not a config knob.
	dispatchKind string

	// selfContained is the research kind's no-repo sub-mode (issue #2202,
	// --self-contained): the Box clones no repo and explores none, and startup
	// validation permits the no-REPO_SLUG/no-GH_TOKEN configuration. validate
	// rejects it for any other kind.
	selfContained bool

	// otherFamilyInProgressLabel is the other Dispatch kind's in-progress
	// label (issue #3541): set via applyDispatchKind alongside dispatchKind,
	// never read from the environment directly, same as dispatchKind above.
	// queryOpenIssues skips a discovered issue carrying it, so a researcher
	// and a worker never run on the same issue at once.
	otherFamilyInProgressLabel string
}

// The two Dispatch kinds (ADR 0022). Both share the four canonical
// DispatchState lifecycle states; research selects the fixed agent-research
// label family and a one-shot Settle instead of work's full merge gate.
const (
	dispatchKindWork     = "work"
	dispatchKindResearch = "research"
)

// applyDispatchKind sets c's dispatchKind and, for research, swaps the four
// lifecycle label fields to the fixed research family. Unlike the work labels
// these aren't operator-configurable, since the research CI workflow and
// prompt key off them directly. completeLabel is left blank: the
// verdict-carrying transition uses IssueTracker.CompleteVerdict instead.
func applyDispatchKind(c config, kind string) config {
	c.dispatchKind = kind
	if kind == dispatchKindResearch {
		// Read the configured work in-progress label before the swap below
		// overwrites c.inProgressLabel with the research family's — this is
		// the only chance to capture it (issue #3541).
		c.otherFamilyInProgressLabel = c.inProgressLabel
		rl := forge.ResearchDispatchLabels()
		c.label = rl.Dispatchable
		c.inProgressLabel = rl.InProgress
		c.completeLabel = rl.Complete
		c.failedLabel = rl.Failed
	} else {
		c.otherFamilyInProgressLabel = forge.ResearchDispatchLabels().InProgress
	}
	return c
}

type issue struct {
	number   string
	title    string
	priority forge.Priority
}

// newIssue is the sole conversion point from forge.Issue to the launcher's
// local issue type; build an issue through this rather than hand-copying
// fields (issue #2925).
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
// getenvSchema/atoiSchema/atoiNonnegSchema call resolves through this, so
// document precedence applies to all of them (issue #625).
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
// GIT_USER_EMAIL) via the normal document/flag/env chain, falling back to the
// host git config when none of those supply a value. The wrapper exports no
// knob env at all (ADR 0020), so this fallback has to live in process.
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
	// No runtime-name fallback: the nix pipeline always renders RUNNER_KIND
	// alongside RUNTIME (issue #2538). A direct binary invocation with no
	// --input document that also omits RUNNER_KIND gets "", which
	// runnerForKind/buildRunnerForKind treat as "oci".
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

// resolveCapabilitySignals returns the capability signals for the pairing in
// effect this run. The document's artifacts describe the pairing baked in at
// build time, so a runtime override, or no document, falls back to a registry
// lookup on the resolved names. The trust branch needs all four keys, so a
// partial render falls back too (issue #2527).
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

// trackerAxisSignals reads the tracker-axis facts off the matching
// backendRows entry rather than re-deriving them with its own name switch, so
// nix and Go can never drift (issue #2533). An empty TrackerAxisRead covers
// both an unregistered name and github/jira's own rows, whose resolved value
// is "GITHUB" but whose Go zero value leaves the field unset.
func trackerAxisSignals(issueTracker string) (read, write, filer string) {
	row, ok := backendByName(issueTracker)
	if !ok || row.TrackerAxisRead == "" {
		return "GITHUB", "GITHUB", "GH"
	}
	// TrackerAxisWrite is read as-is: unlike Read and Filer, "" is a
	// legitimate resolved value for a found row (local has no write-step axis),
	// not an unset-field placeholder. Filer's "GH" default mirrors
	// lib/mkHarness.nix's `issueTrackerRow.trackerAxisFiler or "GH"`.
	filer = row.TrackerAxisFiler
	if filer == "" {
		filer = "GH"
	}
	return row.TrackerAxisRead, row.TrackerAxisWrite, filer
}

// forgeBackendSignal mirrors lib/backends/default.nix's registry rows, the
// same registry-driven shape as trackerAxisSignals (issue #2533).
func forgeBackendSignal(codeForge string) string {
	row, ok := backendByName(codeForge)
	if !ok || row.ForgeBackend == "" {
		return "GH"
	}
	return row.ForgeBackend
}

// resolveTrackerAndForgeSignals returns the tracker-axis and forge-backend
// signals for the pairing in effect this run, with resolveCapabilitySignals's
// trust-then-fallback shape and for the same reason: an operator can override
// ISSUE_TRACKER/CODE_FORGE at dispatch time, away from what the document baked
// in at image-build time (issue #2527, issue #2533).
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

type agentPresence struct {
	filerEnabled, workerProvisioned, scoutProvisioned, reviewLoopInline, reviewLoopOrchestrator bool
}

// resolveAgentPresenceSignals returns the roster and orchestration gate
// signals for this run, with three independent trust gates rather than one
// spanning all five (issue #2527, #2533): the gates differ in override
// semantics, and one shared gate falling back would leave both review-loop
// bools false, breaking their exactly-one-true invariant.
func resolveAgentPresenceSignals(driver string) agentPresence {
	var filerEnabled, workerProvisioned, scoutProvisioned, reviewLoopInline, reviewLoopOrchestrator bool
	filerModel := getenvSchema("FILER_MODEL")
	workerModel := getenvSchema("WORKER_MODEL")
	scoutModel := getenvSchema("SCOUT_MODEL")
	orchestratorEnabled := getenvSchema("ORCHESTRATOR_ENABLED")

	// opencode provisions subagents from on-disk agents/*.md rather than
	// agentsJsonTemplate, so nix bakes both false for that Driver whatever the
	// configured models say (issue #2533). Scout skips this branch because
	// opencode does provision it too, so a non-empty SCOUT_MODEL decides
	// scout's fallback under every Driver — see lib/mkHarness.nix's
	// scoutProvisioned comment.
	if driver == "opencode" {
		filerEnabled, workerProvisioned = false, false
	} else {
		filerEnabled, workerProvisioned = filerModel != "", workerModel != ""
	}
	scoutProvisioned = scoutModel != ""
	if loadedDoc != nil {
		_, filerOK := loadedDoc.Artifacts["FILER_ENABLED"]
		_, workerOK := loadedDoc.Artifacts["WORKER_PROVISIONED"]
		if filerOK && workerOK {
			// Both come from agentsJsonTemplate, a fixed eval-time bake, so a
			// dispatch-time FILER_MODEL/WORKER_MODEL override cannot change the
			// roster the box actually gets. Trust them whenever present, without
			// checking the live models against the document's baked Settings.
			filerEnabled = docArtifact("FILER_ENABLED") == "true"
			workerProvisioned = docArtifact("WORKER_PROVISIONED") == "true"
		}
		// Its own gate, not folded into filerOK/workerOK: a document baked
		// before issue #3157 carries the roster pair but no SCOUT_PROVISIONED
		// key, and that skew must fall back for scout alone rather than defeat
		// trust in the still-present pair.
		if _, scoutOK := loadedDoc.Artifacts["SCOUT_PROVISIONED"]; scoutOK {
			scoutProvisioned = docArtifact("SCOUT_PROVISIONED") == "true"
		}
	}

	// A bool-kind schema knob's live value is "1" or "", never the literal
	// "true" the docArtifact reads above compare against.
	orchestratorOn := orchestratorEnabled != ""
	reviewLoopInline, reviewLoopOrchestrator = !orchestratorOn, orchestratorOn
	// ORCHESTRATOR_ENABLED is boxEnv=true, so a dispatch-time override really
	// does change box behavior: gate the artifacts on the live value matching.
	if loadedDoc != nil && orchestratorEnabled == loadedDoc.Settings["ORCHESTRATOR_ENABLED"] {
		_, inlineOK := loadedDoc.Artifacts["REVIEW_LOOP_INLINE"]
		_, orchOK := loadedDoc.Artifacts["REVIEW_LOOP_ORCHESTRATOR"]
		if inlineOK && orchOK {
			reviewLoopInline = docArtifact("REVIEW_LOOP_INLINE") == "true"
			reviewLoopOrchestrator = docArtifact("REVIEW_LOOP_ORCHESTRATOR") == "true"
		}
	}
	return agentPresence{
		filerEnabled:           filerEnabled,
		workerProvisioned:      workerProvisioned,
		scoutProvisioned:       scoutProvisioned,
		reviewLoopInline:       reviewLoopInline,
		reviewLoopOrchestrator: reviewLoopOrchestrator,
	}
}

func validate(c config) error {
	if c.selfContained && c.dispatchKind != dispatchKindResearch {
		return fmt.Errorf("--self-contained is only valid for the research dispatch kind")
	}
	// internal/launcherchecks' repoRequirementExempt holds the REPO_SLUG/
	// GH_TOKEN exemption logic.
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

// validateConfig runs the configuration-correctness checks validate gates
// dispatch on, minus doctor.RuntimeCheck (advisory, issue #2561; it must not
// fold into cmdDoctor's exit-2 classification, issue #2569) and minus the
// --self-contained check (cmdDoctor always passes selfContained=false). It
// runs every row and joins the failures so cmdDoctor names them all at once.
func validateConfig(c config) error {
	return validateConfigChecks(c, doctorExtraChecks(c))
}

// validateConfigChecks is validateConfig's body over a caller-supplied checks
// slice, so readContext.validation() can pass doctorCheckSets(c)'s memoized
// classify half (issue #3144) rather than a fresh doctorExtraChecks(c) whose
// Probes would Peek independently of runDoctor's. WithRemedy puts each failing
// row's Remedy into the joined error, not only its probe text (issue #2886).
// doctor.Blocking, not a bare Required-and-failing test, decides what counts
// as a failure here, so a degraded row stays the advisory doctor prints it as.
func validateConfigChecks(c config, checks []doctor.Check) error {
	var errs []error
	for _, r := range doctor.RunChecks(checks) {
		if doctor.Blocking(r) {
			errs = append(errs, doctor.WithRemedy(r))
		}
	}
	errs = append(errs, validateChoiceKnobsErrors(c, choiceKnobRegistry)...)
	if _, err := forge.ParseResearchVerdicts(c.researchVerdicts); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// repoBanner formats the "repo: ... merge-mode: ..." line preview and run
// print at the top of a dispatch. The repo segment is omitted when repoSlug is
// empty, the fully-local case where no target repo exists.
func repoBanner(c config) string {
	if c.repoSlug == "" {
		return fmt.Sprintf("merge-mode: %s", c.mergeMode)
	}
	return fmt.Sprintf("repo: %s  merge-mode: %s", c.repoSlug, c.mergeMode)
}

// dispatchLabels builds the DispatchLabels mapping from loaded config.
// Recoverable is a fixed literal, not env-sourced: it applies only to
// CODE_FORGE=local push-only runs and is stored as a local frontmatter marker,
// never a real GitHub label (#2254). Ambiguous is a real GitHub label but is
// fixed too, since issue #2275 does not ask for it to be configurable.
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

// researchVerdictLabels returns the configured verdict-label mapping
// (RESEARCH_VERDICTS) for the research kind, or the zero value for work. Only
// ResearchSettle calls CompleteVerdict, so a zero value is inert for work.
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
// (default "github"), carrying c.dispatchKind's label and verdict families
// (ADR 0022). An unregistered name or a row with no constructor is unreachable
// post-validate and falls back to github.
func newIssueTracker(c config) forge.IssueTracker {
	row, ok := backendByName(c.issueTracker)
	if !ok || row.newIssueTracker == nil {
		gh, _ := backendByName("github")
		return gh.newIssueTracker(c)
	}
	return row.newIssueTracker(c)
}

// newCodeForge returns the CodeForge adapter selected by CODE_FORGE: "github"
// (open PR, watch CI, merge), "git" (push-only, no merge gate), or "local"
// (host-mediated landing onto the Accumulation repo's Integration branch, ADR
// 0033). parent is only the local seam's Integration-branch key (issue #1734);
// every other forge ignores it. An unregistered name falls back to github.
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

// dispatchCompletionBanner returns the forge-aware end-of-dispatch line, so
// the single/wave and continuous paths share one wording (issue #1733).
func dispatchCompletionBanner(c config) string {
	switch c.codeForge {
	case "git", "forgejo":
		return fmt.Sprintf("==> all agents finished — branches pushed on %s.\n", c.repoSlug)
	case "local":
		return "==> all agents finished — seams landed host-side into their own Integration branches in the Accumulation repo.\n"
	default:
		// validate restricts c.codeForge to git, local, forgejo, or github, so
		// a future forge fails loud there rather than inheriting this wording.
		return fmt.Sprintf("==> all agents finished — branches pushed and PRs opened on %s.\n", c.repoSlug)
	}
}

// absCodeForgeAccumulationRepoDir resolves the Accumulation repo dir (ADR
// 0033) for CODE_FORGE=local to an absolute host path, defaulting an unset
// knob to .spindrift/accum.git under the process cwd (issue #1726). The
// read-only /repo Box mount and the landing forge's git subprocesses both need
// the same path, so it is resolved once here. Other forges leave dir alone.
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

// runnerConfig builds the runner.Config a runner adapter needs. Shared by the
// `run` and `build` entry points; build never calls Run, so leaving
// PromptDir/SkillsDir/PodmanNetwork populated is harmless there.
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
			PromptDir:              c.spindriftPromptDir,
			SkillsDir:              c.spindriftSkillsDir,
			DriverSessionCacheDir:  c.driverSessionCacheDir,
			HostMediatedRemote:     sig.hostMediatedRemote,
			AccumulationRepoDir:    c.codeForgeAccumulationRepoDir,
			OutboxRelayCapable:     sig.outboxRelayCapable,
			BoxForgeAndIssueAccess: c.boxForgeAndIssueAccess,
		},
	}
}

// runnerForKind selects the run-time runner adapter (bwrap or OCI). Keyed
// solely on c.runnerKind (issue #2538), never c.runtime, which also carries
// operator-facing runtime names that aren't "oci" literally.
func runnerForKind(c config, rc runner.Config, pwd string) runner.Runner {
	if c.runnerKind == freshness.KindBwrap {
		return runner.NewBwrap(rc, pwd)
	}
	return runner.NewOCI(rc, pwd)
}

// buildRunnerForKind is runnerForKind's `launcher build` counterpart: the
// bwrap arm selects NewBwrapBuild, which realizes store closures rather than
// running an agent.
func buildRunnerForKind(c config, rc runner.Config, pwd string) runner.Runner {
	if c.runnerKind == freshness.KindBwrap {
		return runner.NewBwrapBuild(rc, pwd)
	}
	return runner.NewOCI(rc, pwd)
}

// newDriver returns the Go Driver strategy selected by c.driver (ADR 0009).
// validate already rejects an unrecognised DRIVER, so the error path here
// falls back to the registry default.
func newDriver(c config) driver.Driver {
	d, err := driver.New(c.driver)
	if err != nil {
		d, _ = driver.New("")
	}
	return d
}

// localBaseBranchResolver returns resolveBoxEnvVar, except that under
// CODE_FORGE=local it forwards BASE_BRANCH as the seam's Integration branch
// (integration/<parent>, ADR 0033) once cf.BranchExists confirms it, so a Box
// builds on whatever has landed so far (issue #1700). That ref only appears
// once some seam lands, so it falls back to c.baseBranch until then.
func localBaseBranchResolver(c config, it forge.IssueTracker, lw *localloop.Wired, cf forge.CodeForge, caps forge.Capabilities) func(num, name string) string {
	if !caps.ForgeDescriptor.HostMediatedRemote {
		return func(_, name string) string { return resolveBoxEnvVar(name) }
	}
	return func(num, name string) string {
		if name == "BASE_BRANCH" {
			// Resolved fresh on every call, never cached at construction: a
			// later seam in the same continuous run must see
			// integration/<parent> as it stands then, and each num may resolve
			// to a different parent's branch entirely (issue #1734). lw seals
			// the parent value CodeForgeForIssue and Surface also read (#1810).
			integrationBranch := local.IntegrationBranch(lw.ResolveParent(num))
			exists, err := cf.BranchExists(integrationBranch)
			switch {
			case err == nil && exists:
				return integrationBranch
			case err != nil:
				fmt.Printf("!! BASE_BRANCH: checking %s: %v; falling back to %s\n", integrationBranch, err, c.baseBranch)
			default:
				// A blocker-free seam legitimately seeds from base on first
				// dispatch, so stay silent for it. A seam that HAS blockers
				// should have been held by the #2130 readiness gate until its
				// blocker landed onto this very branch, so reaching here means
				// one slipped through onto bare base. Say so loudly.
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

// boxTokenResolver wraps next, overriding a Box's bearer-token env var with
// the operator's BOX_<X>_TOKEN when the owning backend row sets one (ADR 0016
// two-actor separation, issue #380; ADR 0038 backend-prefixed knobs). The
// launcher's own os.Getenv(token) stays untouched for merges and labels. It
// runs ahead of next's CODE_FORGE fan-out, so it applies under every forge.
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
// settleConfig, and wavesConfig share one value instead of each converting the
// same three ints (issue #2928).
func retryPolicy(c config) retry.Policy {
	return retry.Policy{
		Max:    c.transientRetryMax,
		Unit:   time.Duration(c.transientBackoffSecs) * time.Second,
		Jitter: time.Duration(c.holdJitterSecs) * time.Second,
	}
}

// dispatchConfig builds the subset of config a dispatch.Factory needs. caps
// arrives pre-resolved, so this skips resolveCapabilitySignals's loaded-doc
// path; an --input document that contradicts the registry is an accepted gap
// (#3062, #2947).
func dispatchConfig(c config, it forge.IssueTracker, lw *localloop.Wired, cf forge.CodeForge, caps forge.Capabilities) dispatch.Config {
	trackerAxisRead, trackerAxisWrite, trackerAxisFiler, forgeBackend := resolveTrackerAndForgeSignals(c.codeForge, c.issueTracker)
	presence := resolveAgentPresenceSignals(c.driver)
	return dispatch.Config{
		BoxEnvVars: c.boxEnvVars,
		ResolveEnv: boxTokenResolver(localBaseBranchResolver(c, it, lw, cf, caps)),
		// Raw os.Getenv, not getenvSchema (issue #3171): these two carry only
		// what the operator set at dispatch time. The document/schema-default
		// chain must stay out, since those values already reached the baked
		// roster at eval time and re-forwarding them would override it.
		ReviewModelOverride:    os.Getenv("REVIEW_MODEL"),
		ReviewEffortOverride:   os.Getenv("REVIEW_EFFORT"),
		Kind:                   c.dispatchKind,
		SelfContained:          c.selfContained,
		ForgeDescriptor:        caps.ForgeDescriptor,
		TrackerDescriptor:      caps.TrackerDescriptor,
		BoxForgeAndIssueAccess: c.boxForgeAndIssueAccess,
		TrackerAxisRead:        trackerAxisRead,
		TrackerAxisWrite:       trackerAxisWrite,
		TrackerAxisFiler:       trackerAxisFiler,
		ForgeBackend:           forgeBackend,
		FilerEnabled:           presence.filerEnabled,
		WorkerProvisioned:      presence.workerProvisioned,
		ScoutProvisioned:       presence.scoutProvisioned,
		ReviewLoopInline:       presence.reviewLoopInline,
		ReviewLoopOrchestrator: presence.reviewLoopOrchestrator,
		Policy:                 retryPolicy(c),
		DriverSessionCacheDir:  c.driverSessionCacheDir,
		RegistryProxyRoutes:    c.registryProxyRoutes,
		// forge.ResolveOpenPR (issue #565) keeps a zero-exit rate-limited retry
		// from re-running a box whose work already landed a PR.
		OpenPRForIssue: func(number string) (bool, error) {
			res, err := forge.ResolveOpenPR(cf, number)
			return res.Found, err
		},
		IssueTextFor: memoizedIssueText(it),
	}
}

// memoizedIssueText resolves an issue's injected text at most once per issue
// number for the life of the closure. One dispatch builds up to three Boxes,
// and the injected block sits in the prompt's stable prefix (issue #3445): a
// comment landing mid-run would shift every byte after it and cost the
// prefix-cache hit. Errors are not cached, so a later dispatch retries fresh.
func memoizedIssueText(it forge.IssueTracker) func(string) (string, error) {
	var mu sync.Mutex
	cache := map[string]string{}
	return func(number string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if text, ok := cache[number]; ok {
			return text, nil
		}
		text, err := forge.IssueText(it, number)
		if err != nil {
			return "", err
		}
		cache[number] = text
		return text, nil
	}
}

// newDispatchFactory constructs the dispatch.Factory for one top-level
// dispatch entry point. A driver-cache failure degrades to no cache (fix boxes
// cold-start) rather than failing the dispatch: the cache is a resume
// optimization, not a correctness requirement (issue #427).
func newDispatchFactory(c config, pwd string, r runner.Runner, it forge.IssueTracker, lw *localloop.Wired, cf forge.CodeForge, caps forge.Capabilities) *dispatch.Factory {
	f, err := dispatch.NewFactory(dispatchConfig(c, it, lw, cf, caps), pwd, r, newDriver(c), dispatch.RealClock())
	if err != nil {
		fmt.Fprintf(os.Stderr, "==> driver cache unavailable (%v) -- fix boxes will cold-start\n", err)
	}
	return f
}

// settleConfig builds the subset of config a settle.Settle needs. Only a Code
// Forge implementing forge.BundleRelay consults OutboxDir (issue #1918).
// CodeForgeForIssue resolves a per-issue instance for CODE_FORGE=local (ADR
// 0033, issue #1734) and returns cf unchanged otherwise, so a substituted cf is
// honored. caps is not re-resolved with it: substitute both or neither.
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
		// Policy reuses dispatch's transient-retry tuning rather than a second
		// knob pair (issue #2095, #2325, #2928); it covers the rebase-push
		// backoff and the merge-transient retry cap, which is not
		// MaxRebaseAttempts, a merge-conflict budget. settle.New fills Clock.
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

// localloopConfig builds the subset of config a localloop.Wire needs. Every
// settleConfig and surfaceAfterDispatch site shares it, so they cannot drift on
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

// newSettle constructs the Settler for one dispatch entry point, reused across
// every issue in it: research's one-shot ResearchSettle, or work's merge gate.
func newSettle(c config, it forge.IssueTracker, lw *localloop.Wired, cf forge.CodeForge, caps forge.Capabilities) settle.Settler {
	if c.dispatchKind == dispatchKindResearch {
		vl := researchVerdictLabels(c)
		filerEnabled := resolveAgentPresenceSignals(c.driver).filerEnabled
		if c.boxForgeAndIssueAccess == "read-only" {
			return settle.NewResearchSettleReadOnly(it, vl, filerEnabled)
		}
		return settle.NewResearchSettle(it, vl, filerEnabled)
	}
	return settle.New(settleConfig(c, lw, cf, caps), it, cf)
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
		MaxParallel:    c.maxParallel,
		MaxJobs:        c.maxJobs,
		OverlapGate:    c.overlapGate,
		CompleteLabel:  c.completeLabel,
		FailedLabel:    c.failedLabel,
		IgnoreBlockers: c.dispatchKind == dispatchKindResearch,
		Verb:           verb,
		// The same tuning dispatch's exit-retry path and settleConfig thread
		// (issue #2866, #2928), here reaching RunContinuous's rate-limited
		// re-discover loop. RunContinuous fills Clock.
		Policy: retryPolicy(c),
	}
}

// selectiveWavesConfig builds the wave-engine config for `dispatch <nums>`.
// MAX_JOBS never applies to an explicit selection, since the operator already
// named the exact issues to run, so it is zeroed whatever the config says.
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
// auto-merge when MERGE_MODE=auto. It is a no-op for other modes.
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

// errLaunchGateConfigInvalid is the sentinel the launch gates below wrap
// their misconfiguration errors with. Kept distinct from bootstrap.go's
// errConfigInvalid because bootstrap, preview and recover also call these
// gates, and reusing it would silently move their exit code to 6. doctor.go
// checks it and classifies a gate failure as exit 2 (issue #2942).
var errLaunchGateConfigInvalid = errors.New("launch gate config invalid")

// launchGateConfigError is what the two launch gates return. Error() returns
// only the operator-facing text, never the sentinel's, so dispatch, recover
// and preview print it verbatim; Unwrap() still exposes the sentinel for
// doctorExitCodeFor's errors.Is check.
type launchGateConfigError struct {
	msg string
}

func (e *launchGateConfigError) Error() string { return e.msg }
func (e *launchGateConfigError) Unwrap() error { return errLaunchGateConfigInvalid }

func newLaunchGateConfigError(format string, args ...any) error {
	return &launchGateConfigError{msg: fmt.Sprintf(format, args...)}
}

// checkReadOnlyCapabilityGate enforces BOX_FORGE_AND_ISSUE_ACCESS=read-only's
// requirement (issue #1916): the Box may be denied a write token only when the
// Launcher can perform every write it would otherwise make, on both axes.
// mkHarness's readOnlyCapabilityOk assert (issue #2526) already proves this at
// build time, so this gate only backstops a runtime override of those knobs.
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

// checkNetworkModeRuntimeGate backstops two NETWORK_MODE combinations
// mkHarness's networkModeCoherenceOk assert cannot see, since NETWORK_MODE,
// PODMAN_NETWORK and BWRAP_UNSHARE_NET are all runtime-overridable: bwrap
// renders no-host-loopback no differently from open (issue #2562), and a
// non-open mode beside a raw knob hits networkArg's raw-wins full egress.
func checkNetworkModeRuntimeGate(c config) error {
	// Keys on runnerKind, never runtime: RUNNER_KIND=bwrap/RUNTIME=podman is a
	// supported pairing, and keying on runtime would both reject it and let it
	// reach bwrap.go's fail-open isolateNet=false (issue #2538).
	if c.networkMode == runner.NetworkModeNoHostLoopback && c.runnerKind == freshness.KindBwrap {
		return newLaunchGateConfigError("NETWORK_MODE=no-host-loopback is unsupported on RUNNER_KIND=bwrap -- it has no rendering distinct from the isolated-by-default NETWORK_MODE=open; use NETWORK_MODE=open instead, or RUNNER_KIND=oci for the docker/nerdctl inert-but-correct render")
	}
	// Only the detectable subset: Go sees the resolved value alone, so an
	// explicit NETWORK_MODE=open beside a raw knob is left to raw-wins.
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

// checkBwrapPastaGate refuses to launch rather than silently share the host
// network namespace when the bwrap runner needs pasta (issue #2666) and pasta
// is not on PATH. NetworkMode "host" and "none" never invoke pasta, matching
// bwrap.go's own isolateNet condition, so the gate is a no-op for them.
func checkBwrapPastaGate(c config) error {
	if c.runnerKind != freshness.KindBwrap {
		return nil
	}
	if c.networkMode == runner.NetworkModeHost || c.networkMode == runner.NetworkModeNone {
		return nil
	}
	return runner.ValidatePasta()
}

// checkBwrapOverlayGate refuses to launch rather than let bwrap fail deep in
// sandbox startup when the in-box /nix/store needs an ephemeral tmpfs overlay
// (ADR 0042, issue #2665) but the host kernel bars an unprivileged user
// namespace from mounting overlayfs. The three no-op arms mirror bwrap.go's
// own AND-gate, where the overlay flags never render at all.
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

// errQueueEmpty means discoverIssues found no open dispatchable issues. It and
// its sibling sentinels each map to a distinct exit code so a driving loop
// like dogfood.sh can tell terminations apart without a separate gh probe; see
// exitCodeFor and bootstrapExitCode for the full mapping.
var errQueueEmpty = errors.New("queue empty")

// snapshotGeneration creates the nix-var store-DB snapshot generation a bwrap
// hot-swap is about to bind (ADR 0043, issue #2682); nothing else writes one
// for a hot-swapped closure. It is a package-level test seam, like bwrap.go's
// execCommand, rather than a threaded parameter, so runContinuousDispatch's
// many nixInBox-indifferent test call sites need not pass it.
var snapshotGeneration = runner.SnapshotGeneration

// installStopSignal is a package-level test seam, like snapshotGeneration
// above, so tests drive waves.Config.Stop and waves.Config.Abort through
// fake channels instead of registering a real signal handler or sending a
// real SIGTERM/SIGINT to the test binary (#3520, #3521).
var installStopSignal = stopsignal.Notify

// exitConfigInvalid is the exit code for a bootstrap failure whose error wraps
// errConfigInvalid; see bootstrapExitCode.
const exitConfigInvalid = 6

// exitSignalledStop is the exit code for waves.ErrSignalledStop (issue
// #3520; see its doc for why a stop wins over a stale-image or empty-queue
// verdict in the same drain). A driving loop like dogfood.sh must stop on
// this code rather than rebuild-and-re-invoke the way it does on exit 4. It
// collides with no other dispatch exit code above; doctor's own exit-code
// table is a separate space where the same integers mean unrelated things,
// so this constant must never be read as doctor's.
const exitSignalledStop = 7

func containsLabel(labels []string, target string) bool {
	for _, l := range labels {
		if l == target {
			return true
		}
	}
	return false
}

// resolveOrigin is the one place c.issueNumber is read as the claimed-single
// versus discovered-batch sentinel; every other call site reads the derived
// Origin instead of re-checking the sentinel.
func resolveOrigin(c config) waves.Origin {
	if c.issueNumber != "" {
		return waves.OriginClaimed
	}
	return waves.OriginDiscovered
}

// discoverIssues resolves the batch of issues to dispatch and the Origin it
// came from. When ISSUE_NUMBER is set the workflow already claimed that issue,
// so it is targeted directly: a label query could otherwise pick up a
// different issue stranded on the same in-progress label by an earlier crash.
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

// queryOpenIssues fetches the dispatchable-labelled batch and prints nothing,
// so a caller that polls repeatedly can decide whether a poll is worth
// announcing; see logDiscoveryPoll.
func queryOpenIssues(c config, it forge.IssueTracker) ([]issue, error) {
	rawIssues, err := it.ListIssues(forge.Dispatchable)
	if err != nil {
		return nil, err
	}
	var issues []issue
	for _, fi := range rawIssues {
		// Filtering on the tracker-returned Labels, rather than adding a
		// -label: qualifier to the ListIssues query, is what makes this
		// tracker-derived: it sees in-progress work started by a Console
		// session, CI, or a human, not just this launcher's own claims. It
		// also keeps work-only operation working with no research labels
		// defined — an absent label is a label no issue carries, so the
		// filter is a no-op and containsLabel never has to special-case "".
		// Silent like the Dispatchable filter above: an issue held back by
		// the other family's in-progress label is no more newsworthy than
		// one that never had the dispatchable label to begin with.
		if c.otherFamilyInProgressLabel != "" && containsLabel(fi.Labels, c.otherFamilyInProgressLabel) {
			continue
		}
		issues = append(issues, newIssue(fi))
	}
	return issues, nil
}

// readinessFor resolves a waves.Batch from a raw issues batch. discover must
// call logDiscoveryPoll before this, ahead of the DepsOf fan-out below.
func readinessFor(it forge.IssueTracker, issues []issue) (waves.Batch, error) {
	waveIssues := toWaveIssues(issues)
	result, err := waves.NewReadiness(it, waveIssues)
	if err != nil {
		return waves.Batch{}, err
	}
	return waves.Batch{Issues: waveIssues, Edges: result.Edges, Sources: result.Sources, Failed: result.Failed}, nil
}

// logDiscoveryPoll decides whether a refill poll prints the "==> querying
// open" line, then records this poll's numbers into seen. The first poll of a
// run always announces, whatever seen holds (the #1645 invariant). Later polls
// stay silent unless they surface a number not in seen, and then name only
// those.
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

// registryFor returns s's own termination registry, or a fresh one when s owns
// none (settle.Fake, ResearchSettle). Getting one rather than installing one is
// the point: RunContinuous, both waves.Dispatch call sites, and recoverByNumber
// each reach the same registry as their settler, so a Console recover gesture
// cannot replace the session's registry and strand the operator's later marks
// where no settle goroutine looks (#3522). An abort (second signal) and a
// settle goroutine already polling CI therefore agree on an issue's fate:
// observeAbort's Reclaim marks it here and the settler checks that same mark at
// its next checkpoint, abandoning rather than driving the reclaimed issue to a
// terminal state out from under the abort (#3521).
func registryFor(s any) *terminate.Registry {
	if r, ok := s.(settle.Registrar); ok {
		return r.Registry()
	}
	return terminate.NewRegistry()
}

// recoverByNumber resolves the open PR for issueNum, draft or not, and drives
// it through the adopt-and-gate path: the sole way an agent-in-progress issue
// is adopted, gated on the operator's explicit agent-recover label rather than
// any automatic sweep (#600). With no open PR it falls back to adopting a
// relayed finished branch out of the outbox (issue #2225).
func recoverByNumber(c config, it forge.IssueTracker, cf forge.CodeForge, caps forge.Capabilities, pwd string, f *dispatch.Factory, s settle.WorkSettler, issueNum string) error {
	// Installed once, ahead of it.Issue, exactly as run() places its own
	// install ahead of discoverIssues (#3522): a signal that fired before this
	// call must win over recoverFailed below, not just over an adopt already
	// in flight.
	stopCh, abortCh, stopCleanup := installStopSignal()
	defer stopCleanup()

	terminated := registryFor(s)
	reaper := f.AsReaper()
	gate := shutdown.NewGate(stopCh, abortCh, it, cf, reaper, terminated, c.completeLabel)
	gate.Watch()
	// Settle is idempotent (gate.go), so deferring it here reaches every one
	// of this function's early returns, not just the two happy-path arms that
	// also call it inline before their own final gate.Signalled() check.
	// Registered right after Watch so this defer runs after d.Close()'s below
	// (LIFO) -- the watcher must outlive the Dispatch it might still need to
	// reclaim through.
	defer gate.Settle()

	// First checkpoint: nothing is adopted yet, so a signal here must not
	// reach recoverFailed below — a requested stop is not a recover failure
	// and must never park the issue on agent-failed.
	if !gate.Allowed(issueNum) {
		return waves.ErrSignalledStop
	}

	fi, err := it.Issue(issueNum)
	if err != nil {
		return recoverFailed(it, caps, issueNum, fmt.Errorf("issue %s: %w", issueNum, err))
	}
	iss := newIssue(fi)
	branch := cf.AgentBranch(iss.number)
	backoff := retry.LinearBackoff{Unit: time.Duration(c.transientBackoffSecs) * time.Second, Clock: retry.RealClock()}
	// transientRetryMax counts retries everywhere else, so the first attempt
	// sits on top of it: maxAttempts = retries + 1.
	res, prErr := forge.ResolveOpenPRWithRetry(cf, iss.number, backoff, c.transientRetryMax+1)
	if prErr != nil {
		return recoverFailed(it, caps, issueNum, fmt.Errorf("issue %s: resolve PR: %w", issueNum, prErr))
	}
	if !res.Found {
		// A resolveErr is logged, not returned: the self-report walk runs
		// alongside the genuine/synthetic tier (issue #2268), so an unparseable
		// report must stay adoptable. SettleRelayedBranch is attempted even
		// with no self-report at all, since for the local push-only shape a
		// bundle sitting in the outbox is evidence enough (issue #2378).
		resolved, resolveErr := dispatch.ResolveFromLogs(pwd, iss.number, "")
		if resolveErr != nil {
			fmt.Fprintf(os.Stderr, "    ?? #%s: resolve pass logs: %v\n", issueNum, resolveErr)
		}
		if err := os.MkdirAll(dispatch.HostLogDirFor(pwd), 0o755); err != nil {
			return fmt.Errorf("mkdir logs: %w", err)
		}
		// New arms this issue's kill latch, so it must run under Gate's own
		// lock and before the in-flight registration, or a concurrent abort
		// could snapshot the in-flight set without it (#3522).
		var d *dispatch.Dispatch
		if !gate.Launch(iss.number, func() { d = f.New(iss.number, iss.title) }) {
			// A signal arrived between Allowed and here. Recover never calls
			// gate.Hold -- the agent-recover workflow holds this issue's claim
			// and its PR may still be open -- so Launch's decline releases
			// nothing, leaving the issue in-progress for a later recover.
			return waves.ErrSignalledStop
		}
		defer d.Close()
		// Same reason as the SettleAdopted arm below: this Dispatch never calls
		// Run, so it needs the lineage guarantee stated explicitly (#2575).
		if err := d.EnsureRunLineage(); err != nil {
			fmt.Fprintf(os.Stderr, "    ?? #%s: ensure run lineage: %v\n", issueNum, err)
		}
		result := dispatch.Result{Resolved: resolved}
		sit := s.SituationFor(iss.number, res.Found, result)
		settled := s.SettleRelayedBranch(d, iss.number, 0, sit, result)
		// Leave must run before Settle's final abort re-check, or a signal
		// landing the instant after settling finishes would still find this
		// issue in-flight and reclaim it right back to Dispatchable (#3522).
		gate.Leave(iss.number)
		gate.Settle()
		// Second checkpoint: a mid-flight abort reclaims the in-flight issue
		// off in-progress on its own (the watcher), and the settle above
		// abandoned at its next checkpoint through the shared registry's
		// mark — this wins over the settle's own verdict, recoverFailed
		// included, exactly as run()'s signalledOr does for a wave (#3522).
		if gate.Signalled() {
			return waves.ErrSignalledStop
		}
		if settled {
			return nil
		}
		fmt.Printf("    #%s  status=skipped  note=no open PR on %s\n", issueNum, branch)
		return recoverFailed(it, caps, issueNum, fmt.Errorf("issue %s: no open PR", issueNum))
	}
	if err := os.MkdirAll(dispatch.HostLogDirFor(pwd), 0o755); err != nil {
		return fmt.Errorf("mkdir logs: %w", err)
	}
	var d *dispatch.Dispatch
	if !gate.Launch(iss.number, func() { d = f.New(iss.number, iss.title) }) {
		return waves.ErrSignalledStop
	}
	defer d.Close()
	// This Dispatch adopts an already-open PR and never calls Run, so it does
	// not get Run's quarantine-prior-run-logs guarantee for free.
	// EnsureRunLineage establishes it once, before any pass log is read
	// (issue #2575).
	if err := d.EnsureRunLineage(); err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: ensure run lineage: %v\n", issueNum, err)
	}
	s.SettleAdopted(d, iss.number, 0, res.URL)
	// Same reordering as the SettleRelayedBranch arm above, and for the same
	// reason (#3522): Leave before Settle, not after.
	gate.Leave(iss.number)
	gate.Settle()
	if gate.Signalled() {
		return waves.ErrSignalledStop
	}
	return nil
}

// recoverFailed is recoverByNumber's single terminal-failure exit, so a
// recover attempt on an issue already agent-complete can never downgrade it to
// agent-failed (issue #2477). The workflow's claim strips the prior terminal
// label before this process starts, so the pre-claim state has to be read back
// out of the issue timeline via the optional PriorClaimStateReader.
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
	// Emitted after the TransitionState above succeeded, not on every exit from
	// recoverFailed: a failed transition falls through to origErr and the
	// caller settles that failure itself. Inline rather than through
	// settle/gate.go's latch, which exists for a path that can reach a second,
	// contradicting terminal transition; this one reaches at most one.
	report.Settled(num, forge.Complete.String(), note)
	if commentErr := it.Comment(num, note); commentErr != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: could not post recover-declined comment: %v\n", num, commentErr)
	}
	fmt.Printf("    #%s  status=recover-declined  note=%v\n", num, origErr)
	return nil
}

// run is the orchestration logic for the `dispatch` subcommand: preflight,
// stranded-issue reconciliation, discovery, dependency-graph construction, and
// drain/wave dispatch. bootstrap wires lc in production; tests use fakes.
func run(lc *launchContext) error {
	c, it, cf, f, s, pwd := lc.config, lc.issueTracker, lc.codeForge, lc.factory, lc.settle, lc.pwd
	caps := lc.capabilities
	lp := reconcile.NewFSProbe(pwd, lc.runner)

	fmt.Println(repoBanner(c))

	if err := checkAutoMergePreflight(c, caps); err != nil {
		return err
	}

	// A bare agent-in-progress issue is never adopted automatically: it carries
	// no liveness signal, so it cannot be told apart from one a live runner is
	// committing to right now (#600). The only adopt path is the explicit
	// `spindrift recover <n>`.
	if resolveOrigin(c) == waves.OriginDiscovered && c.continuousDispatch {
		return runContinuousDispatch(c, it, cf, pwd, f, s, runner.NixEvaluator{}, runner.NixRealizer{}, lp)
	}

	// Installed once, ahead of discoverIssues, mirroring
	// runContinuousDispatch's own placement (#3522): a signal arriving before
	// any issue is even discovered must still win over errQueueEmpty and
	// ErrOpenNoneDispatchable below, not just over a wave already in flight.
	stopCh, abortCh, stopCleanup := installStopSignal()
	defer stopCleanup()

	issues, origin, err := discoverIssues(c, it)
	if err != nil {
		return signalledOr(stopCh, abortCh, err)
	}

	if origin == waves.OriginDiscovered && len(issues) == 0 {
		// A signal that already fired wins here too: the operator asked to
		// stop, so a reconcile sweep is more work, not teardown (mirrors the
		// completion path below).
		if waves.SignalledStopAlready(stopCh, abortCh) {
			return waves.ErrSignalledStop
		}
		fmt.Printf("no open '%s' issues — nothing to do.\n", c.label)
		if err := reconcileAfterDispatch(c, it, cf, lp, caps, pwd, os.Stdout); err != nil {
			return err
		}
		return errQueueEmpty
	}

	readiness, err := waves.NewReadiness(it, toWaveIssues(issues))
	if err != nil {
		return signalledOr(stopCh, abortCh, err)
	}
	in := waves.NewInput(origin, readiness, toWaveIssues(issues))
	cfg := wavesConfig(c)
	cfg.SeedScopeOf = localloop.SeedScopeResolver(it, caps)
	cfg.Stop = stopCh
	cfg.Abort = abortCh
	claimer := waves.NewLabelClaimer(it, c.label, c.inProgressLabel)
	terminated := registryFor(s)
	if err := waves.Dispatch(cfg, &waves.Session{Terminated: terminated}, it, cf, pwd, f, s, in, claimer); err != nil {
		return err
	}

	fmt.Print(dispatchCompletionBanner(c))
	return reconcileAfterDispatch(c, it, cf, lp, caps, pwd, os.Stdout)
}

// signalledOr returns waves.ErrSignalledStop when a stop or abort has already
// fired, and err unchanged otherwise. run and selectiveListDispatch apply it at
// every early return that precedes waves.Dispatch -- an empty queue, a
// discovery error, or a NewReadiness error never reaches waves.Dispatch at all,
// so without the check a signal that fired first would flatten into
// errQueueEmpty or another pre-Dispatch error instead of winning as
// ErrSignalledStop (#3522). Returns that follow waves.Dispatch need no such
// check: the engine applies the same override to its own return, covering every
// path through it including a bare nil.
func signalledOr(stop, abort <-chan struct{}, err error) error {
	if waves.SignalledStopAlready(stop, abort) {
		return waves.ErrSignalledStop
	}
	return err
}

// continuousDispatchErr picks runContinuousDispatch's terminal error: a
// signalled stop wins over both ErrImageStale and a stashed firstQueryErr —
// see waves.ErrSignalledStop's doc for why (#3520); short of that,
// ErrImageStale wins over firstQueryErr. That last pair is reachable from no
// path today — #2777 and #2780 saw to that — so it is documented, tested
// intent (the TestContinuousDispatchErr_* tests) rather than a live guard, in
// case a future caller reintroduces one. The stop precedence is live, though:
// a stop closed after the first discover already errored arrives here with
// both set.
func continuousDispatchErr(err, firstQueryErr error) error {
	if errors.Is(err, waves.ErrSignalledStop) {
		return waves.ErrSignalledStop
	}
	if errors.Is(err, waves.ErrImageStale) {
		return waves.ErrImageStale
	}
	if firstQueryErr != nil {
		return firstQueryErr
	}
	return err
}

// runContinuousDispatch is the entry point for CONTINUOUS_DISPATCH, the opt-in
// slot-refill mode (#527). There is no empty-queue precheck: the discover
// closure's first call is the only query a continuous run makes before its
// first dispatch (#1645). eval and realize are injected so tests substitute
// fakes rather than shelling out to nix (#2679).
func runContinuousDispatch(c config, it forge.IssueTracker, cf forge.CodeForge, pwd string, f *dispatch.Factory, s settle.Settler, eval freshness.Evaluator, realize freshness.Realizer, lp reconcile.LivenessProbe) error {
	// Resolved fresh rather than threaded in (issue #2946): unlike run's
	// lc.capabilities, this argument list carries no aggregate context.
	forgeDesc, _ := backend.ByName(c.codeForge)
	trackerDesc, _ := backend.ByName(c.issueTracker)
	caps := forge.ResolveCapabilities(cf, it, forgeDesc, trackerDesc)

	// firstQueryEmpty separates "no open issues at all" from "open issues that
	// turned out blocked or deferred"; only the former maps
	// ErrOpenNoneDispatchable to exit 2 below. Tracked here rather than in
	// RunContinuous, whose sentinel the console Discoverer shares (#1645).
	firstQuery := true
	firstQueryEmpty := false
	var firstQueryErr error
	// seenIssues carries logDiscoveryPoll's per-run dedupe state (#1666), so a
	// long-running refill loop stops repeating the "querying open" line once
	// the queue has settled.
	seenIssues := make(map[string]bool)
	// These four need no locking: every discover call runs under
	// RunContinuous's own mutex, so this closure never runs concurrently with
	// itself.
	discover := func() (waves.Batch, error) {
		wasFirst := firstQuery
		issues, err := queryOpenIssues(c, it)
		if firstQuery {
			firstQuery = false
			firstQueryErr = err
			firstQueryEmpty = err == nil && len(issues) == 0
		}
		// Runs before readinessFor's DepsOf fan-out on purpose: the
		// announcement is about the poll itself, so a slow per-issue DepsOf
		// round-trip must never delay it. An errored non-first poll passes an
		// empty slice, so nothing is new and the poll goes unannounced.
		logDiscoveryPoll(c, issues, wasFirst, seenIssues)
		if err != nil {
			return waves.Batch{}, err
		}
		return readinessFor(it, issues)
	}
	guard := freshness.NewGuard(pwd)
	var staleResult freshness.Result
	// currentImageTag is the effective loaded baseline Probe compares against.
	// It advances to res.TipTag after every successful hot-swap, so a later
	// probe against an unchanged base tip converges to fresh instead of
	// re-detecting the same divergence forever (ADR 0043, issue #2682).
	currentImageTag := c.imageTag
	// swapClassified records that the hot-swap branch already ran
	// guard.Classify on this staleResult. Classify must never run twice on one
	// Result: it records and clears persisted state, so a second call reads
	// its own write back as the prior run's.
	var swapClassified bool
	// swapHostTainted is the disposition that Classify call returned, and so
	// whether the swap branch already printed HostTaintDiagnostic.
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

		// Box-only staleness under bwrap hot-swaps instead of draining (ADR
		// 0043, issue #2682). These conditions admit exactly that case:
		// LauncherFresh keeps a both-moved verdict out (the launcher wins
		// then), KindBwrap keeps OCI out, and flakeLauncherAttr keeps an
		// unconfigured launcher out, since Probe hard-codes its freshness.
		if c.runnerKind == freshness.KindBwrap && res.Applicable && !res.ImageFresh && res.LauncherFresh && c.flakeLauncherAttr != "" {
			// drain is the shared fallback exit for every failure branch below.
			drain := func() (bool, bool, string) {
				staleResult = res
				return res.Applicable, false, res.Message
			}

			// Classify decides Rebuild (realize and bind) against HostTainted
			// (this divergence already failed to converge), as it does for the
			// drain path below. Both outcomes must record that it ran.
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
			// res.TipTag becomes both a bind-mount source and a path component
			// (the snapshot generation's dir name), so reject anything that is
			// not a genuine store path rather than trust a Probe-side
			// regression never to hand back a foreign host directory (#2682).
			if !strings.HasPrefix(res.TipTag, "/nix/store/") {
				fmt.Fprintf(os.Stderr, "==> bwrap hot-swap: realized tip tag %q is not a nix store path, draining instead\n", res.TipTag)
				return drain()
			}
			// nixInBox Consumers need a real nix-var store-DB snapshot
			// generation on disk before a Box can bind it, and nothing else
			// writes one for a hot-swapped closure (ADR 0043). Draining beats
			// binding a generation whose snapshot dir does not exist, which
			// would otherwise surface as every later Box launch failing to stat.
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

		// fresh runs under RunContinuous's mutex, so these plain writes are
		// serialized. A staleResult set here was not classified by the swap
		// branch above, so reset both flags and let the terminal switch
		// classify this genuinely new Result normally.
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
	// stopCleanup retires the relay goroutine only; the SIGTERM/SIGINT
	// disposition deliberately stays installed for the rest of the process
	// (#3520, see stopsignal.Notify).
	stopCh, abortCh, stopCleanup := installStopSignal()
	defer stopCleanup()
	cfg.Stop = stopCh
	cfg.Abort = abortCh
	// pending is the quiet query waves.Queue.Pending uses for the stale-drain
	// report's heldBack number (#2939). It shares no state with discover,
	// since a reporting-only query is not a dispatch attempt (#2777), and it
	// counts through waves.CountReady rather than len(issues), so a blocked,
	// deferred, or already-claimed issue is not counted as held back.
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
	terminated := registryFor(s)
	if err := waves.RunContinuous(cfg, &waves.Session{Terminated: terminated}, it, cf, f, s, queue, fresh); err != nil {
		// No reachable path leaves both err and firstQueryErr non-nil: #2780
		// proved a genuine first-discover error cannot reach a later staleness
		// detection, and #2777 moved the heldBack query onto queue.Pending,
		// which never touches firstQueryErr. See continuousDispatchErr's own
		// doc comment for why the precedence is kept anyway.
		switch terminal := continuousDispatchErr(err, firstQueryErr); {
		case errors.Is(terminal, waves.ErrSignalledStop):
			// Wins here per waves.ErrSignalledStop's doc; otherwise this would
			// flatten into exit 3 (ErrOpenNoneDispatchable), exit 2 (that same
			// error once firstQueryEmpty converts it below), or exit 4/5
			// (stale-image) below.
			return waves.ErrSignalledStop
		case errors.Is(terminal, waves.ErrImageStale):
			// swapClassified means the hot-swap branch already ran Classify on
			// this staleResult, on either disposition. Classify must never run
			// twice on one Result, so trust swapHostTainted instead of
			// re-classifying, and skip the diagnostic the swap branch printed.
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
			// refill swallows every discover error and retries on the next
			// trigger, but the first call has no next trigger once nothing
			// dispatches. Surface it here rather than let it flatten into
			// ErrOpenNoneDispatchable and exit 3.
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

// cmdBuild is the `build` subcommand: realize the sandbox image or store
// closures without running any agent.
func cmdBuild() int {
	if err := build(); err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
		return 1
	}
	return 0
}

// cmdConsole is the `console` subcommand: the interactive picks-only driving
// loop (#645, #646). Fresh and RebuildFn turn the freshness.Probe seam behind
// the headless exit-4 path into an in-session banner and one-key rebuild
// (issue #652). stdin/stdout are threaded so a test can drive the real Bubble
// Tea program with a scripted reader instead of a live TTY.
func cmdConsole(lc *launchContext, stdin io.Reader, stdout io.Writer) int {
	defer lc.cleanup()
	// Bubble Tea owns the terminal in alt-screen raw mode, where a heartbeat
	// line's bare \n moves the cursor down but not back to column 0,
	// stairstepping across the screen. The sidebar activity feed re-renders the
	// same lines from the pass log anyway (issue #1583).
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

// writeGithubOutput appends a "key=value\n" line to the file named by
// GITHUB_OUTPUT, GitHub Actions' step-output mechanism. It is a no-op when
// that is unset. Newlines in value become spaces, so one cannot corrupt the
// single-line format.
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

// cmdRecover is the `recover` subcommand: adopt an already-discovered open PR
// with no outcome line and drive it through the merge gate, honouring the
// two-stage operator-shutdown latch the same as every other Box-launching
// path (#3522) — a signalled stop maps to exitSignalledStop ahead of the
// blanket failure return below, writing neither the recover-reason output nor
// a stderr line, since a requested stop is not a recover failure. Tests pass
// a spy cleanup to exercise the cleanup-on-every-exit contract.
func cmdRecover(lc *launchContext, issueNum string) int {
	defer lc.cleanup()
	err := recoverByNumber(lc.config, lc.issueTracker, lc.codeForge, lc.capabilities, lc.pwd, lc.factory, lc.workSettle(), issueNum)
	if err == nil {
		return 0
	}
	if errors.Is(err, waves.ErrSignalledStop) {
		return exitSignalledStop
	}
	if writeErr := writeGithubOutput("recover-reason", err.Error()); writeErr != nil {
		fmt.Fprintf(os.Stderr, "warning: writing recover-reason output: %v\n", writeErr)
	}
	fmt.Fprintf(os.Stderr, "%s\n", err)
	return 1
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

// selectiveDispatchExitCode translates selectiveListDispatch's result into an
// exit code: 7 for an operator stop request (SIGTERM), ahead of every other
// verdict below (issue #3522, mirrors exitCodeFor/runExitCode -- not printed
// to stderr, since a requested stop is not a failure), 3 when open issues
// exist but none are dispatchable (a selective wave can defer every listed
// issue, just as a queue drain can), 1 for any other error, 0 on success.
// Split out so it is testable without bootstrap.
func selectiveDispatchExitCode(lc *launchContext, nums []string, forceYes bool) int {
	err := selectiveListDispatch(lc.config, lc.issueTracker, lc.codeForge, lc.capabilities, lc.pwd, lc.factory, lc.settle, nums, forceYes, os.Stdin, os.Stdout)
	if err == nil {
		return 0
	}
	if errors.Is(err, waves.ErrSignalledStop) {
		return exitSignalledStop
	}
	if errors.Is(err, waves.ErrOpenNoneDispatchable) {
		return 3
	}
	fmt.Fprintf(os.Stderr, "%s\n", err)
	return 1
}

// cmdDispatchSelective is the `dispatch <nums>` subcommand: an
// operator-supplied issue list that bypasses the label and barrier gates.
func cmdDispatchSelective(lc *launchContext, nums []string, forceYes bool) int {
	defer lc.cleanup()
	return selectiveDispatchExitCode(lc, nums, forceYes)
}

// exitCodeFor translates a run or runContinuousDispatch error into an exit
// code: 2 for an empty queue, 3 for open issues none of which are
// dispatchable, 4 for a stale image (rebuild and retry), 5 for a
// host-tainted stale image that no rebuild can fix, so the driving loop must
// stop rather than loop on exit 4 forever (issue #2113), 7 for an operator
// stop request (SIGTERM) that drained outstanding work and wants no rebuild
// (issue #3520) -- reachable from both run's one-shot dispatch and
// runContinuousDispatch, not a CONTINUOUS_DISPATCH-only verdict (#3522) --
// 1 otherwise.
func exitCodeFor(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, waves.ErrSignalledStop):
		return exitSignalledStop
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

// bootstrapExitCode translates a bootstrap error into an exit code:
// exitConfigInvalid (6) when it wraps errConfigInvalid, so a caller can tell a
// config-validation failure from any other bootstrap failure (issue #2568), 1
// otherwise.
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

// runExitCode translates run's result via exitCodeFor. Split out from
// cmdDispatch so it is testable without going through bootstrap.
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
// stderr. Every mainRun early return must call it (ADR 0020, issue #814).
func flushAmbientWarnings(stderr io.Writer, warnings *bytes.Buffer) {
	stderr.Write(warnings.Bytes())
}

// verbHandler is the uniform shape every verbHandlers entry implements. args
// is args[1:], with the subcommand name stripped.
type verbHandler func(args []string, stderr io.Writer) int

// verbHandlers is the single source of truth for which subcommands exist
// (issue #1574); a test enumerates its keys. The hidden __complete-issues
// completion verb stays out of it and is dispatched ahead of the table lookup,
// since it is not a documented verb.
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
		// noBuild and yes are dispatch/research knobs recover has no use for.
		// remaining is used unfiltered, since recover's non-numeric IDs must
		// survive; see parseIssuePositionals in flags.go.
		parsed := parseIssuePositionals(args)
		if parsed.selfContained {
			fmt.Fprintln(stderr, "flag --self-contained is only valid for the research subcommand")
			return 1
		}
		if len(parsed.remaining) < 1 {
			fmt.Fprintln(stderr, "usage: spindrift recover <issue-number>")
			return 1
		}
		lc, err := bootstrap(true, dispatchKindWork, false)
		if err != nil {
			fmt.Fprintf(stderr, "%s\n", err)
			return 1
		}
		return cmdRecover(lc, parsed.remaining[0])
	},
	"preview": func(args []string, stderr io.Writer) int {
		// remaining is the issue-ID list, unfiltered (issue #3054, #3055).
		// Unlike dispatch and recover, preview ignores --self-contained rather
		// than rejecting it, matching its earlier behavior.
		parsed := parseIssuePositionals(args)
		return cmdPreview(parsed.remaining)
	},
	"dispatch": func(args []string, stderr io.Writer) int {
		parsed := parseIssuePositionals(args)
		if parsed.selfContained {
			fmt.Fprintln(stderr, "flag --self-contained is only valid for the research subcommand")
			return 1
		}
		lc, err := bootstrap(!parsed.noBuild, dispatchKindWork, false)
		if err != nil {
			fmt.Fprintf(stderr, "%s\n", err)
			return bootstrapExitCode(err)
		}
		if len(parsed.remaining) > 0 {
			return cmdDispatchSelective(lc, parsed.remaining, parsed.yes)
		}
		return cmdDispatch(lc)
	},
	"research": func(args []string, stderr io.Writer) int {
		parsed := parseIssuePositionals(args)
		lc, err := bootstrap(!parsed.noBuild, dispatchKindResearch, parsed.selfContained)
		if err != nil {
			fmt.Fprintf(stderr, "%s\n", err)
			return bootstrapExitCode(err)
		}
		if len(parsed.remaining) > 0 {
			return cmdDispatchSelective(lc, parsed.remaining, parsed.yes)
		}
		return cmdDispatch(lc)
	},
	"registry": func(args []string, stderr io.Writer) int {
		if len(args) == 0 || args[0] != "discover" {
			fmt.Fprintln(stderr, "usage: spindrift registry discover <repo-dir> <routes-file> [--force]")
			return 1
		}
		// The handed stderr covers only this handler's own usage error;
		// cmdRegistryDiscover wires its own streams, like doctor and reconcile.
		return cmdRegistryDiscover(args[1:], os.Stdout, os.Stderr)
	},
}

// mainRun parses argv and dispatches to the selected subcommand, returning the
// process exit code. stdout and stderr are injected so tests can assert on
// help and error output without touching the real process streams.
func mainRun(argv []string, stdout, stderr io.Writer) int {
	// Installed before any subcommand handler runs, so SPINDRIFT_REPORT_FD is
	// close-on-exec before this process spawns its first Box, runtime, or
	// subprocess (issue #3627).
	report.Install(report.FromEnv(os.Getenv, stderr))
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
	// environment, so a flag setting the same var never masks the ambient value
	// the warning reports on (ADR 0020). Taken ahead of every early return
	// below so each of them can still surface it (issues #814, #1191).
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
		// Bare `spindrift` prints help rather than silently dispatching (issue
		// #555); `dispatch` is the sole way to drain the queue.
		flushAmbientWarnings(stderr, &ambientWarnings)
		printHelp(stdout)
		return 0
	}
	if inputPath != "" {
		doc, err := inputdoc.Load(inputPath)
		if err != nil {
			stderr.Write(ambientWarnings.Bytes())
			fmt.Fprintf(stderr, "%s\n", err)
			return 1
		}
		loadedDoc = doc
	}
	// Runs after loadedDoc is in place: CODE_FORGE and ISSUE_TRACKER may be set
	// through the document alone.
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
	// Unrecognized subcommand prints help rather than dispatching (issue #555).
	fmt.Fprintf(stderr, "unknown subcommand: %s\n\n", args[0])
	printHelp(stderr)
	return 1
}

func main() {
	os.Exit(mainRun(os.Args[1:], os.Stdout, os.Stderr))
}

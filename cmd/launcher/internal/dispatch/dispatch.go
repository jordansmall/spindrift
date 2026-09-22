// Package dispatch owns every Box launched for one issue, from claim to
// verdict (issue #441). No caller outside this package constructs a
// runner.Box, opens an issue log file for writing, or classifies a Driver
// exit.
package dispatch

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/registryproxy"
	"spindrift.dev/launcher/internal/retry"
)

// Config carries the subset of launcher config a Dispatch needs to build a
// Box's env and drive its retry policy.
type Config struct {
	// BoxEnvVars is a space-separated list of env var names forwarded into
	// every Box (schema boxEnv=true entries).
	BoxEnvVars string

	// ResolveEnv resolves one BoxEnvVars name for the issue being dispatched
	// (num), defaulting to a num-ignoring os.Getenv when nil. main.go wires
	// it to loadConfig()'s document/flag/env chain, so a document-baked value
	// reaches the Box even when the operator sets it nowhere (ADR 0020). num
	// lets each issue resolve its own BASE_BRANCH under local (issue #1734).
	ResolveEnv func(num, name string) string

	// Policy is the transient-retry tuning, built once by retryPolicy
	// (issue #2928).
	Policy retry.Policy

	// DriverSessionCacheDir is the selected Driver's declared in-box
	// session-cache mount target (ADR 0009). Empty when the Driver declares
	// none, and then the Factory creates no per-issue cache directory at
	// all, because there is nowhere in-box to mount it (issue #448).
	DriverSessionCacheDir string

	// RegistryProxyRoutes is the resolved registry-proxy route table (ADR
	// 0044/0045, issue #3139). Each Route's Credential is the resolved value
	// itself, never a file path or env var name: the launcher resolves those
	// at startup, before it builds this Config. An empty slice turns the
	// feature off, and runOnce then starts no proxy and mounts no socket.
	RegistryProxyRoutes []registryproxy.Route

	// Kind is the dispatch kind ("work" or "research", ADR 0022) forwarded as
	// DISPATCH_KIND, so the entrypoint selects its prompt and skips the
	// clone-branch/PR/CI phases for research. Empty defaults to "work".
	Kind string

	// SelfContained forwards the research kind's no-repo sub-mode as
	// SELF_CONTAINED=1 (issue #2202), so the entrypoint skips clone_repo and
	// repo exploration. Meaningful only when Kind == "research".
	SelfContained bool

	// ForgeDescriptor/TrackerDescriptor are the backend-registry rows for this
	// run's CODE_FORGE/ISSUE_TRACKER pairing, read off the forge.Capabilities
	// value ResolveCapabilities produced (issue #2945). Config carries only
	// these plain-data rows, never that whole value, because dispatch must
	// not hold its write-capable handles (issue #2947, narrowed by #3063).
	ForgeDescriptor   backend.Descriptor
	TrackerDescriptor backend.Descriptor

	// BoxForgeAndIssueAccess is the BOX_FORGE_AND_ISSUE_ACCESS knob value,
	// "read-write" or "read-only".
	BoxForgeAndIssueAccess string

	// SignalCarrier is the BOX_SIGNAL_CARRIER knob value (issue #3725). An
	// empty or unrecognised value means the "log" carrier (fail-closed,
	// exactly the way BoxForgeAndIssueAccess is matched exactly rather than
	// negated at buildBoxEnv below): only the exact string "socket" selects
	// the socket carrier.
	SignalCarrier string

	// NetworkMode is the NETWORK_MODE knob value, read by startSignalSocket's
	// transport-verdict gate inside the Dispatch (issue #3725).
	NetworkMode string

	// These are nix-resolved static prompt-gate values (issue #2533;
	// ScoutProvisioned added by #3157), forwarded into the Box unmodified.
	TrackerAxisRead        string
	TrackerAxisWrite       string
	TrackerAxisFiler       string
	ForgeBackend           string
	FilerEnabled           bool
	WorkerProvisioned      bool
	ScoutProvisioned       bool
	ReviewLoopInline       bool
	ReviewLoopOrchestrator bool

	// ReviewModelOverride/ReviewEffortOverride carry an operator's explicit
	// dispatch-time REVIEW_MODEL/REVIEW_EFFORT (issue #3171), which the review
	// pass binds over the baked roster entry. Empty forwards no var, skipping
	// the document/schema default chain BoxEnvVars uses: a baked default would
	// override the roster on every dispatch.
	ReviewModelOverride  string
	ReviewEffortOverride string

	// OpenPRForIssue reports whether an open PR already exists for the issue's
	// agent branch, so a box whose work already landed a PR is never held and
	// retried on a transient classification (issue #565). Callers may rely on
	// it being non-nil: a push-only Code Forge resolves to false inside
	// dispatchConfig's closure rather than leaving it nil.
	OpenPRForIssue func(number string) (bool, error)

	// IssueTextFor resolves the subject issue's body, plus its recent
	// comments when the tracker supports them, into the ISSUE_TEXT the Box
	// receives (issue #3445). Every tracker backend goes through this one
	// closure. A nil closure leaves ISSUE_TEXT absent; an error instead
	// fails the dispatch (see buildBoxEnv's doc).
	IssueTextFor func(number string) (string, error)

	// HeartbeatOut is the human-facing sink for every Box's heartbeat writer
	// and each dispatch-start announce line (issue #1829). Nil defaults to
	// os.Stdout in box.go. The console entry point sets io.Discard: Bubble Tea
	// owns the terminal in alt-screen raw mode, where a bare-\n heartbeat line
	// stairsteps down the screen instead of returning to column 0.
	HeartbeatOut io.Writer
}

// signalCarrierSocket reports whether c selects the socket signal carrier.
// The one spelling of the comparison inside this package, so no dispatch-side
// caller re-spells it. cmd/launcher's own startup gate
// (signalcarrier_gate.go) reads the value off its own config type across the
// package boundary and does re-spell it.
func (c Config) signalCarrierSocket() bool {
	return c.SignalCarrier == "socket"
}

// buildBoxEnv assembles the Box env from the schema boxEnv=true vars, the
// per-issue vars, and nonce as RUN_NONCE (issue #1937). An IssueTextFor error
// fails the dispatch rather than being dropped (issue #3445): the prompt
// templates promise the Box an injected ISSUE_TEXT section. It fires before any
// box runs, so the empty-log check surfaces it on Result.Err (issue #3119).
func buildBoxEnv(cfg Config, number, title string, fixPass int, ciFailureSummary string, nonce string) (map[string]string, error) {
	resolve := cfg.ResolveEnv
	if resolve == nil {
		resolve = func(_, name string) string { return os.Getenv(name) }
	}
	env := make(map[string]string)
	for _, name := range strings.Fields(cfg.BoxEnvVars) {
		env[name] = resolve(number, name)
	}
	env["ISSUE_NUMBER"] = number
	env["ISSUE_TITLE"] = title
	if cfg.IssueTextFor != nil {
		text, err := cfg.IssueTextFor(number)
		if err != nil {
			return nil, fmt.Errorf("issue text for #%s: %w", number, err)
		}
		if text != "" {
			env["ISSUE_TEXT"] = text
		}
	}
	kind := cfg.Kind
	if kind == "" {
		kind = "work"
	}
	env["DISPATCH_KIND"] = kind
	if cfg.SelfContained {
		env["SELF_CONTAINED"] = "1"
	}
	if fixPass > 0 {
		env["FIX_PASS"] = strconv.Itoa(fixPass)
	}
	if ciFailureSummary != "" {
		env["CI_FAILURE_SUMMARY"] = ciFailureSummary
	}
	env["RUN_NONCE"] = nonce
	// Exact match, never != "read-only" (issue #1951): an unset, typo'd, or
	// forwarding-glitched value must not fall open into the write-capable
	// prompt path.
	if cfg.BoxForgeAndIssueAccess == "read-write" {
		env["BOX_WRITE_ENABLED"] = "1"
	}
	// Forwarded so the in-box `driver-exec outcome-backstop` verb keys its
	// decision off explicit signals rather than a raw CODE_FORGE name
	// comparison (issue #2267).
	forgeHostMediatedRemote := cfg.ForgeDescriptor.HostMediatedRemote
	trackerInBoxUnreachable := cfg.TrackerDescriptor.InBoxUnreachableTracker
	if forgeHostMediatedRemote {
		env["BOX_HOST_MEDIATED_REMOTE"] = "1"
	}
	if cfg.ForgeDescriptor.OutboxRelayCapable {
		env["BOX_OUTBOX_RELAY_CAPABLE"] = "1"
	}
	// Both seams of this run are local (ADR 0033). main.go's
	// resolveCapabilitySignals computes the same boolean over the same two
	// fields for the FULLY_LOCAL prompt gate, so changing one without the
	// other silently desyncs BOX_FULLY_LOCAL from FULLY_LOCAL.
	if forgeHostMediatedRemote && trackerInBoxUnreachable {
		env["BOX_FULLY_LOCAL"] = "1"
	}
	if trackerInBoxUnreachable {
		env["BOX_IN_BOX_UNREACHABLE_TRACKER"] = "1"
	}
	// TrackerAxisWrite is legitimately empty for a local (read-only)
	// tracker, so the uniform empty-string guard correctly leaves
	// BOX_TRACKER_AXIS_WRITE absent there (issue #2533).
	if cfg.TrackerAxisRead != "" {
		env["BOX_TRACKER_AXIS_READ"] = cfg.TrackerAxisRead
	}
	if cfg.TrackerAxisWrite != "" {
		env["BOX_TRACKER_AXIS_WRITE"] = cfg.TrackerAxisWrite
	}
	if cfg.TrackerAxisFiler != "" {
		env["BOX_TRACKER_AXIS_FILER"] = cfg.TrackerAxisFiler
	}
	if cfg.ForgeBackend != "" {
		env["BOX_FORGE_BACKEND"] = cfg.ForgeBackend
	}
	// Per-route registry-proxy upstream hosts travel in
	// REGISTRY_PROXY_MANIFEST (ADR 0045, runOnce/box.go), not here.
	//
	// These prompt gates forward as a positive signal only: "1" when true,
	// absent rather than "0" when false (issue #2533).
	if cfg.FilerEnabled {
		env["BOX_FILER_ENABLED"] = "1"
	}
	if cfg.WorkerProvisioned {
		env["BOX_WORKER_PROVISIONED"] = "1"
	}
	if cfg.ScoutProvisioned {
		env["BOX_SCOUT_PROVISIONED"] = "1"
	}
	if cfg.ReviewLoopInline {
		env["BOX_REVIEW_LOOP_INLINE"] = "1"
	}
	if cfg.ReviewLoopOrchestrator {
		env["BOX_REVIEW_LOOP_ORCHESTRATOR"] = "1"
	}
	// Absent entirely when empty, so the Box reads presence as "the operator
	// said so at dispatch time" (issue #3171).
	if cfg.ReviewModelOverride != "" {
		env["BOX_REVIEW_MODEL_OVERRIDE"] = cfg.ReviewModelOverride
	}
	if cfg.ReviewEffortOverride != "" {
		env["BOX_REVIEW_EFFORT_OVERRIDE"] = cfg.ReviewEffortOverride
	}
	return env, nil
}

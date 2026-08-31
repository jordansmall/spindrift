// Package dispatch is the per-issue execution module (issue #441): every Box
// launched for one issue — initial run, fix passes, conflict-resolve — plus
// its results and its driver-cache entry, from claim to verdict. No caller
// outside this package constructs a runner.Box, opens an issue log file for
// writing, or classifies a Driver exit directly.
package dispatch

import (
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"

	"spindrift.dev/launcher/internal/forge"
)

// Config carries the subset of launcher config a Dispatch needs to build a
// Box's env and drive its retry policy.
type Config struct {
	// BoxEnvVars is a space-separated list of env var names forwarded into
	// every Box (schema boxEnv=true entries).
	BoxEnvVars string

	// ResolveEnv resolves one BoxEnvVars name to its forwarded value for the
	// issue being dispatched (num). Defaults to a num-ignoring os.Getenv
	// when nil (every pre-#625 caller and test). main.go wires this to the
	// same document/flag/env chain loadConfig() uses (getenvSchema), so a
	// boxEnv knob's document-baked value still reaches the Box even when
	// the operator sets it nowhere (ADR 0020: the wrapper exports no
	// per-var env any more). num lets CODE_FORGE=local resolve BASE_BRANCH
	// per seam (issue #1734): each dispatched issue may key its own
	// Integration branch off a different parent.
	ResolveEnv func(num, name string) string

	// TransientRetryMax caps both the hold-cycle count (429 with a known
	// reset) and the backoff-retry count (other transients) before a
	// dispatch gives up.
	TransientRetryMax int

	// TransientBackoffSecs is the linear backoff unit for non-hold
	// transients: attempt N waits TransientBackoffSecs*N.
	TransientBackoffSecs int

	// HoldJitterSecs is added to a rate-limit hold's wait, and is the whole
	// wait when the known reset time has already passed.
	HoldJitterSecs int

	// DriverSessionCacheDir is the selected Driver's declared in-box
	// session-cache mount target (ADR 0009). Empty when the Driver declares
	// none, in which case the Factory creates no per-issue cache directory
	// at all -- there is nowhere in-box to mount it (issue #448).
	DriverSessionCacheDir string

	// RegistryProxyUpstreamURL is the REGISTRY_PROXY_UPSTREAM_URL knob value
	// (ADR 0044, issue #2849): the upstream registry the launcher-side
	// Registry proxy forwards GET/HEAD requests to. Empty means the
	// registry-proxy feature is off, in which case runOnce starts no proxy
	// and mounts no socket into the Box.
	RegistryProxyUpstreamURL string

	// RegistryProxyCredential is the launcher-resolved plaintext credential
	// (ADR 0044, issue #2850) attached to every request the registry proxy
	// forwards to RegistryProxyUpstreamURL, as "Authorization: Bearer
	// <value>". It is the resolved value itself -- never a reference like a
	// file path or env var name, those are resolved once at launcher
	// startup before this Config is built. Empty means an unauthenticated
	// pass-through, matching RegistryProxyUpstreamURL's own on/off gate.
	RegistryProxyCredential string

	// Kind is the dispatch kind ("work" or "research", ADR 0022) forwarded
	// into every Box as DISPATCH_KIND, so the entrypoint can select its
	// prompt and skip clone-branch/PR/CI phases for research. Empty defaults
	// to "work" in buildBoxEnv, matching every pre-existing (kind-unaware)
	// construction site.
	Kind string

	// SelfContained forwards the research kind's no-repo sub-mode (issue #2202)
	// into the Box as SELF_CONTAINED=1, so the entrypoint skips clone_repo and
	// all repo exploration and selects the self-contained research prompt.
	// Meaningful only when Kind == "research"; false (the default) for every
	// pre-#2202 construction site leaves the env var unset.
	SelfContained bool

	// Capabilities is the resolved backend-capability value (forge.Capabilities,
	// issue #2945) for this run's CODE_FORGE/ISSUE_TRACKER pairing -- buildBoxEnv
	// and needsOutbox read it instead of Config carrying its own duplicate
	// booleans (issue #2947).
	Capabilities forge.Capabilities

	// BoxForgeAndIssueAccess is the BOX_FORGE_AND_ISSUE_ACCESS knob value
	// ("read-write" or "read-only"). See Capabilities.ForgeDescriptor's
	// HostMediatedRemote/OutboxRelayCapable fields, consulted alongside this
	// one by needsOutbox/buildBoxEnv below.
	BoxForgeAndIssueAccess string

	// TrackerAxisRead/TrackerAxisWrite/TrackerAxisFiler/ForgeBackend/
	// FilerEnabled/WorkerProvisioned/ReviewLoopInline/ReviewLoopOrchestrator
	// are each the nix-resolved static prompt-gate value (issue #2533),
	// forwarded into the Box unmodified.
	TrackerAxisRead        string
	TrackerAxisWrite       string
	TrackerAxisFiler       string
	ForgeBackend           string
	FilerEnabled           bool
	WorkerProvisioned      bool
	ReviewLoopInline       bool
	ReviewLoopOrchestrator bool

	// OpenPRForIssue reports whether an open PR already exists for the
	// issue's agent branch. Consulted before a zero-exit, no-outcome box is
	// held-and-retried on a transient classification (issue #565), so a box
	// whose work already landed a PR is never re-run -- the same guard
	// settle's status=missing path applies. Always set by the sole
	// production constructor (dispatchConfig); a push-only Code Forge with
	// no PR lookup is handled inside that closure (ResolveOpenPR resolves
	// to Found: false there), not by leaving this field nil -- callers may
	// rely on it being non-nil.
	OpenPRForIssue func(number string) (bool, error)

	// HeartbeatOut is the human-facing sink every Box's heartbeat writer
	// echoes to, alongside its unconditional pass-log file capture, and the
	// sink each dispatch-start announce line ("-> #NN: title" and its
	// fix-pass/conflict-resolve variants, box.go's humanOut) writes to as
	// well (issue #1829). Nil defaults to os.Stdout in box.go (every
	// pre-#1583 caller and test). The console entry point sets this to
	// io.Discard via Factory.SetHeartbeatOut -- Bubble Tea owns the terminal
	// in alt-screen/raw mode there, and a bare-\n heartbeat line or a raw
	// announce line both stairstep down the screen instead of returning to
	// column 0, while the sidebar activity feed and queue view already
	// reflect the same information from the pass log and dispatch state.
	HeartbeatOut io.Writer
}

// buildBoxEnv assembles the env map forwarded into a Box. It combines the
// schema boxEnv=true vars (read from the ambient env by name) with per-issue
// vars. nonce is the dispatching Dispatch's per-run nonce (issue #1937,
// empty in tests that don't need it), forwarded as RUN_NONCE so
// control-signal prompt fragments can reference it.
func buildBoxEnv(cfg Config, number, title string, fixPass int, ciFailureSummary string, nonce string) map[string]string {
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
	// The write-enabled-vs-not decision, resolved once here and forwarded as
	// a single explicit positive signal (issue #1951): present only when
	// cfg.BoxForgeAndIssueAccess is exactly "read-write", absent under
	// read-only or any other/malformed value, so an unset, typo'd, or
	// forwarding-glitched value inside the Box can never fall open into the
	// write-capable prompt path the way branching on
	// BOX_FORGE_AND_ISSUE_ACCESS with a `:-read-write` fallback did. A
	// `!= "read-only"` test would put the fallback right back here, just
	// moved host-side.
	if cfg.BoxForgeAndIssueAccess == "read-write" {
		env["BOX_WRITE_ENABLED"] = "1"
	}
	// HostMediatedRemote/OutboxRelayCapable forward the same two backend-
	// registry capability facts needsOutbox already consults (issue #2267),
	// so the in-box `driver-exec outcome-backstop` verb can key its no-
	// outcome backstop decision off explicit signals instead of re-deriving
	// them from a raw CODE_FORGE name comparison the way it did before.
	forgeHostMediatedRemote := cfg.Capabilities.ForgeDescriptor.HostMediatedRemote
	trackerInBoxUnreachable := cfg.Capabilities.TrackerDescriptor.InBoxUnreachableTracker
	if forgeHostMediatedRemote {
		env["BOX_HOST_MEDIATED_REMOTE"] = "1"
	}
	if cfg.Capabilities.ForgeDescriptor.OutboxRelayCapable {
		env["BOX_OUTBOX_RELAY_CAPABLE"] = "1"
	}
	// FullyLocal: both seams of this run are local (ADR 0033: CODE_FORGE=local
	// and ISSUE_TRACKER=local together). cmd/launcher/main.go's
	// resolveCapabilitySignals reaches this exact same "fully local"
	// conclusion independently, as hostMediatedRemote && inBoxUnreachableTracker,
	// for its own different callers (the FULLY_LOCAL doc artifact / prompt-gate
	// signal) -- the two computations are the same boolean over the same two
	// backend-registry fields and must stay in agreement; a change to one
	// without the other would silently desync BOX_FULLY_LOCAL from
	// FULLY_LOCAL.
	if forgeHostMediatedRemote && trackerInBoxUnreachable {
		env["BOX_FULLY_LOCAL"] = "1"
	}
	if trackerInBoxUnreachable {
		env["BOX_IN_BOX_UNREACHABLE_TRACKER"] = "1"
	}
	// TrackerAxisRead/TrackerAxisWrite/TrackerAxisFiler/ForgeBackend are
	// nix-resolved static prompt-gate values (issue #2533), forwarded
	// unmodified whenever non-empty. TrackerAxisWrite is legitimately empty
	// for a local (read-only) tracker, so this uniform empty-string guard
	// leaves BOX_TRACKER_AXIS_WRITE correctly absent in that case, same
	// effect as "absent" everywhere else.
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
	// REGISTRY_PROXY_UPSTREAM_HOST is the host[:port] portion of
	// RegistryProxyUpstreamURL, forwarded so an in-Box phase (issue #2851,
	// ADR 0044) can textually find-and-replace this exact host string in a
	// Target repo's own committed registry config (e.g. a cargo
	// .cargo/config.toml), redirecting it at the local registry-proxy
	// Forwarder instead of the real upstream. Non-secret -- ADR 0044
	// already treats the upstream URL itself as non-secret, only the
	// credential attached to it is. Set only when the URL is non-empty,
	// parses, and yields a non-empty host, so a malformed or unset knob
	// leaves the var absent rather than forwarding an empty string an
	// in-Box substitution could match against everything.
	if cfg.RegistryProxyUpstreamURL != "" {
		if u, err := url.Parse(cfg.RegistryProxyUpstreamURL); err == nil && u.Host != "" {
			env["REGISTRY_PROXY_UPSTREAM_HOST"] = u.Host
		}
	}
	// FilerEnabled/WorkerProvisioned/ReviewLoopInline/ReviewLoopOrchestrator
	// are nix-resolved static prompt-gate values (issue #2533), forwarded as
	// a single explicit positive signal matching BOX_FULLY_LOCAL's shape:
	// present only as "1" when true, absent (not "0") when false.
	if cfg.FilerEnabled {
		env["BOX_FILER_ENABLED"] = "1"
	}
	if cfg.WorkerProvisioned {
		env["BOX_WORKER_PROVISIONED"] = "1"
	}
	if cfg.ReviewLoopInline {
		env["BOX_REVIEW_LOOP_INLINE"] = "1"
	}
	if cfg.ReviewLoopOrchestrator {
		env["BOX_REVIEW_LOOP_ORCHESTRATOR"] = "1"
	}
	return env
}

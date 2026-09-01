// Package dispatch is the per-issue execution module: every Box launched for one
// issue — initial run, fix passes, conflict-resolve — plus its results and its
// driver-cache entry, from claim to verdict. No caller outside this package
// constructs a runner.Box, opens an issue log file for writing, or classifies a
// Driver exit directly.
package dispatch

import (
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/retry"
)

// Config carries the subset of launcher config a Dispatch needs to build a
// Box's env and drive its retry policy.
type Config struct {
	// BoxEnvVars is a space-separated list of env var names forwarded into
	// every Box (schema boxEnv=true entries).
	BoxEnvVars string

	// ResolveEnv resolves one BoxEnvVars name to its forwarded value for the
	// issue being dispatched (num). Nil defaults to a num-ignoring os.Getenv.
	// main.go wires this to the same document/flag/env chain loadConfig() uses,
	// so a boxEnv knob's document-baked value still reaches the Box even when
	// the operator sets it nowhere (ADR 0020: the wrapper exports no per-var env
	// any more). num lets CODE_FORGE=local resolve BASE_BRANCH per seam — each
	// dispatched issue may key its own Integration branch off a different parent.
	ResolveEnv func(num, name string) string

	// Policy is retry.Policy's transient-retry tuning.
	Policy retry.Policy

	// DriverSessionCacheDir is the selected Driver's declared in-box
	// session-cache mount target (ADR 0009). Empty when the Driver declares
	// none, in which case the Factory creates no per-issue cache directory at
	// all -- there is nowhere in-box to mount it.
	DriverSessionCacheDir string

	// RegistryProxyUpstreamURL is the upstream registry the launcher-side
	// Registry proxy forwards GET/HEAD requests to (ADR 0044). Empty turns the
	// feature off: runOnce starts no proxy and mounts no socket into the Box.
	RegistryProxyUpstreamURL string

	// RegistryProxyCredential is attached to every request the registry proxy
	// forwards to RegistryProxyUpstreamURL, as "Authorization: Bearer <value>".
	// It is the resolved plaintext value itself -- never a reference like a file
	// path or env var name, which are resolved at launcher startup before this
	// Config is built. Empty means an unauthenticated pass-through.
	RegistryProxyCredential string

	// Kind is the dispatch kind ("work" or "research", ADR 0022) forwarded into
	// every Box as DISPATCH_KIND, so the entrypoint can select its prompt and
	// skip clone-branch/PR/CI phases for research. Empty defaults to "work".
	Kind string

	// SelfContained forwards the research kind's no-repo sub-mode into the Box as
	// SELF_CONTAINED=1, so the entrypoint skips clone_repo and all repo
	// exploration. Meaningful only when Kind == "research"; false leaves the env
	// var unset.
	SelfContained bool

	// Capabilities is the resolved backend-capability value for this run's
	// CODE_FORGE/ISSUE_TRACKER pairing, read by buildBoxEnv and needsOutbox.
	Capabilities forge.Capabilities

	// BoxForgeAndIssueAccess is the BOX_FORGE_AND_ISSUE_ACCESS knob value
	// ("read-write" or "read-only"). See Capabilities.ForgeDescriptor's
	// HostMediatedRemote/OutboxRelayCapable fields, consulted alongside this
	// one by needsOutbox/buildBoxEnv below.
	BoxForgeAndIssueAccess string

	// Each of these is a nix-resolved static prompt-gate value, forwarded into
	// the Box unmodified.
	TrackerAxisRead        string
	TrackerAxisWrite       string
	TrackerAxisFiler       string
	ForgeBackend           string
	FilerEnabled           bool
	WorkerProvisioned      bool
	ReviewLoopInline       bool
	ReviewLoopOrchestrator bool

	// OpenPRForIssue reports whether an open PR already exists for the issue's
	// agent branch. Consulted before a zero-exit, no-outcome box is
	// held-and-retried on a transient classification, so a box whose work
	// already landed a PR is never re-run. Callers may rely on it being
	// non-nil: a push-only Code Forge with no PR lookup is handled inside the
	// production closure (resolving to false), not by leaving this field nil.
	OpenPRForIssue func(number string) (bool, error)

	// HeartbeatOut is the human-facing sink every Box's heartbeat writer echoes
	// to, alongside its unconditional pass-log file capture, and the sink each
	// dispatch-start announce line writes to. Nil defaults to os.Stdout. The
	// console entry point sets this to io.Discard -- Bubble Tea owns the
	// terminal in alt-screen/raw mode, where a bare-\n heartbeat or announce
	// line stairsteps down the screen instead of returning to column 0, and the
	// sidebar already reflects the same information.
	HeartbeatOut io.Writer
}

// buildBoxEnv assembles the env map forwarded into a Box, combining the schema
// boxEnv=true vars (read from the ambient env by name) with per-issue vars.
// nonce is forwarded as RUN_NONCE so control-signal prompt fragments can
// reference it.
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
	// A single explicit positive signal: present only on an exact "read-write"
	// match, absent under read-only or any malformed value, so an unset, typo'd,
	// or forwarding-glitched value in the Box can never fall open into the
	// write-capable prompt path. A `!= "read-only"` test would reintroduce that
	// fallback host-side.
	if cfg.BoxForgeAndIssueAccess == "read-write" {
		env["BOX_WRITE_ENABLED"] = "1"
	}
	// Forwarded so the in-box `driver-exec outcome-backstop` verb keys its
	// no-outcome decision off explicit signals rather than re-deriving them from
	// a raw CODE_FORGE name comparison.
	forgeHostMediatedRemote := cfg.Capabilities.ForgeDescriptor.HostMediatedRemote
	trackerInBoxUnreachable := cfg.Capabilities.TrackerDescriptor.InBoxUnreachableTracker
	if forgeHostMediatedRemote {
		env["BOX_HOST_MEDIATED_REMOTE"] = "1"
	}
	if cfg.Capabilities.ForgeDescriptor.OutboxRelayCapable {
		env["BOX_OUTBOX_RELAY_CAPABLE"] = "1"
	}
	// Both seams of this run are local (ADR 0033). main.go's
	// resolveCapabilitySignals computes the same boolean independently for the
	// FULLY_LOCAL prompt-gate signal; the two must stay in agreement or
	// BOX_FULLY_LOCAL silently desyncs from FULLY_LOCAL.
	if forgeHostMediatedRemote && trackerInBoxUnreachable {
		env["BOX_FULLY_LOCAL"] = "1"
	}
	if trackerInBoxUnreachable {
		env["BOX_IN_BOX_UNREACHABLE_TRACKER"] = "1"
	}
	// Forwarded unmodified whenever non-empty. TrackerAxisWrite is legitimately
	// empty for a local (read-only) tracker, where the uniform guard correctly
	// leaves BOX_TRACKER_AXIS_WRITE absent.
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
	// The host[:port] portion, forwarded so an in-Box phase can find-and-replace
	// this exact host string in a Target repo's committed registry config,
	// redirecting it at the local proxy (ADR 0044). Non-secret: only the
	// credential attached to the upstream URL is. Set only when the URL parses
	// to a non-empty host, so a malformed or unset knob leaves the var absent
	// rather than forwarding an empty string an in-Box substitution would match
	// against everything.
	if cfg.RegistryProxyUpstreamURL != "" {
		if u, err := url.Parse(cfg.RegistryProxyUpstreamURL); err == nil && u.Host != "" {
			env["REGISTRY_PROXY_UPSTREAM_HOST"] = u.Host
		}
	}
	// Positive-signal shape, matching BOX_FULLY_LOCAL: present as "1" when true,
	// absent (never "0") when false.
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

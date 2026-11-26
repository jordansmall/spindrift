// Package dispatch owns every Box launched for one issue, from claim to
// verdict (issue #441). No caller outside this package constructs a
// runner.Box, opens an issue log file for writing, or classifies a Driver
// exit.
package dispatch

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/branchrecovery"
	"spindrift.dev/launcher/internal/chore"
	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/gitexec"
	"spindrift.dev/launcher/internal/hostop"
	"spindrift.dev/launcher/internal/registryproxy"
	"spindrift.dev/launcher/internal/registryvocab"
	"spindrift.dev/launcher/internal/retry"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/signalsocket"
)

// Chore is a one-shot butler run's key (ADR 0056, issue #3875),
// Factory.NewChore's argument: no tracker number or issue text, only the
// Ledger Chore's name, its target branch, and the scan Scope.
type Chore struct {
	// Name is forwarded as CHORE_NAME and folded into the Dispatch's
	// log/lock/cache key as "butler-"+Name.
	Name string
	// Branch is forwarded as BASE_BRANCH directly rather than through
	// Config.ResolveEnv: a chore has no issue number for the local-forge
	// BASE_BRANCH resolver to key off.
	Branch string
	// Scope forwards as CHORE_HEAD, CHORE_DIFF_RANGE, and CHORE_SLICE.
	Scope chore.Scope
	// PromotionClasses is forwarded as CHORE_CLASSES (issue #3880),
	// informational only: the host re-checks a finding's class at settle
	// regardless. Empty whenever today's promotion room is spent or off, so
	// the Box never spends reviewer turns on candidates nothing can promote.
	PromotionClasses []string
	// ClassList is the Chore's closed finding-class list (issue #4766),
	// forwarded as CHORE_CLASS_LIST whatever the promotion budget.
	ClassList []string
	// Input is the host-rendered tuning digest (ADR 0062) a records-scoped
	// Chore reasons from, forwarded as CHORE_INPUT; empty for a code Chore.
	Input string
	// PatchClasses is forwarded as CHORE_PATCH_CLASSES (issue #4072, ADR
	// 0057), informational for the Box's prompt only: internal/butler's
	// patchBlocker re-checks a finding's class at settle whatever the Box was
	// told. Empty whenever
	// the rung is off or today's patch room is spent.
	PatchClasses []string
	// MaxFindings is this sweep's findings room (chore.Room.Findings): the
	// most findings the Box may relay this run. Zero means no host limit.
	MaxFindings int
	// ClaimTime, when non-zero, is this run's claim time: the Dispatch's
	// Record ID is minted from it at construction so the caller can read
	// RecordID() before Run (issue #4952).
	ClaimTime time.Time
}

// maxIssueIntents is the effective cap on the findings a Chore run's Box may
// relay: MaxFindings, else the signal socket's default. The socket and
// CHORE_MAX_FINDINGS both read it, so the Box is told the socket's own cap.
func (c Chore) maxIssueIntents() int {
	if c.MaxFindings > 0 {
		return c.MaxFindings
	}
	return signalsocket.DefaultMaxIssueIntents
}

// subject is what a Dispatch's Box works: a tracker issue or a one-shot
// butler Chore (ADR 0056), told apart by its Dispatch key. key and title
// supply the Dispatch's log/lock/cache key and its human-facing title
// (issue #3954); chore is set only when key.IsChore().
type subject struct {
	key   dispatchkey.Key
	title string
	chore Chore
}

// issueSubject builds the tracker-issue arm of subject.
func issueSubject(number, title string) subject {
	return subject{key: dispatchkey.Issue(number), title: title}
}

// choreSubject builds the butler-Chore arm of subject (ADR 0056).
func choreSubject(c Chore) subject {
	return subject{key: dispatchkey.Chore(c.Name), title: "butler: " + c.Name, chore: c}
}

// Config carries the subset of launcher config a Dispatch needs to build a
// Box's env and drive its retry policy.
type Config struct {
	// Drivers is the Driver set the host reads Pass logs through; the Record
	// the Dispatch settles into is ingested with it.
	Drivers *driver.Set

	// BoxEnvVars is a space-separated list of env var names forwarded into
	// every Box (schema boxEnv=true entries).
	BoxEnvVars string

	// ResolveEnv resolves one BoxEnvVars name for the issue being dispatched
	// (num), defaulting to os.Getenv when nil. main.go wires it to the
	// document/flag/env chain, so a document-baked value reaches the Box even
	// when the operator sets it nowhere (ADR 0020). num lets each issue
	// resolve its own BASE_BRANCH under local (issue #1734).
	ResolveEnv func(num, name string) string

	// SeedMirror, when non-nil, runs before every Pass and returns the host
	// path of the freshly seeded mirror to mount at /repo; an error fails the
	// Pass. Nil (self-contained research never clones) gives the Box no /repo.
	// release is non-nil on success and runs once the Pass's Box has exited.
	// The hook may hand each Pass its own mirror (github, forgejo: release
	// removes only that Pass's) or re-seed one shared mirror (local, git:
	// release is a no-op, and the hook must serialize itself).
	SeedMirror func() (dir string, release func(), err error)

	// Policy is the transient-retry tuning (issue #2928).
	Policy retry.Policy

	// DriverSessionCacheDir is the selected Driver's declared in-box
	// session-cache mount target (ADR 0009). Empty means no per-issue cache
	// directory at all: there is nowhere in-box to mount it (issue #448).
	DriverSessionCacheDir string

	// RegistryProxyRoutes is the resolved registry-proxy route table (ADR
	// 0044/0045, issue #3139). Each Route's Credential is the resolved value
	// itself, never a file path or env var name. Empty turns the proxy off.
	RegistryProxyRoutes []registryproxy.Route

	// RegistryProxyRewriteRows are the response-rewrite rows the proxy
	// applies, in the registered Strategies' order; the launcher fills it so
	// dispatch never reads the registry.
	RegistryProxyRewriteRows []registryvocab.RewriteRow

	// Kind is the dispatch kind, a name from dispatchkind.All. Its
	// descriptor's axes reach the Box as DISPATCH_KEYING and
	// DISPATCH_ANNOUNCE_VERB (issue #3996), the name itself as DISPATCH_KIND
	// (issue #3901). Empty defaults to "work".
	Kind string

	// Stamp carries the deployment facts the launcher records in every Pass
	// log's dispatch_start stamp (issue #4783): only Revision, RoleModels,
	// DriverVersion and Knobs are read; the Dispatch fills in the rest.
	Stamp hostop.DispatchStart

	// SelfContained forwards the research kind's no-repo sub-mode as
	// SELF_CONTAINED=1 (issue #2202), so box skips the clone. Meaningful only
	// when Kind == "research".
	SelfContained bool

	// ForgeDescriptor/TrackerDescriptor are the backend-registry rows for this
	// run's CODE_FORGE/ISSUE_TRACKER pairing (issue #2945). Config carries
	// only these plain-data rows, never the whole forge.Capabilities value,
	// because dispatch must not hold its write-capable handles (issues #2947,
	// #3063).
	ForgeDescriptor   backend.Descriptor
	TrackerDescriptor backend.Descriptor

	// SignalCarrier is the BOX_SIGNAL_CARRIER knob value (issue #3725). Only
	// the exact string "socket" selects the socket carrier (fail-closed).
	SignalCarrier string

	// NetworkMode gates startSignalSocket's transport verdict (issue #3725).
	NetworkMode runner.NetworkMode

	// Nix-resolved static prompt-gate values (issues #2533, #3157), forwarded
	// into the Box unmodified.
	TrackerAxisRead   string
	TrackerAxisWrite  string
	TrackerAxisFiler  string
	ForgeBackend      string
	FilerEnabled      bool
	IssueRouted       bool
	WorkerProvisioned bool
	ScoutProvisioned  bool

	// ReviewModelOverride/ReviewEffortOverride carry an operator's explicit
	// dispatch-time REVIEW_MODEL/REVIEW_EFFORT (issue #3171), bound over the
	// baked roster entry. Empty forwards no var, skipping the document/schema
	// default chain BoxEnvVars uses: a baked default would override the roster
	// on every dispatch.
	ReviewModelOverride  string
	ReviewEffortOverride string

	// OpenPRForIssue reports whether an open PR already exists for the agent
	// branch of a dispatch key, so a box whose work already landed a PR is
	// never held and retried on a transient classification (issue #565), and
	// answers BOX_HOST_OPEN_PR (issues #7146, #6900). Callers may rely on it
	// being non-nil: dispatchConfig's closure resolves a push-only Code Forge
	// to false rather than leaving it nil.
	OpenPRForIssue func(number string) (bool, error)

	// IssueTextFor resolves the subject issue's body, plus its recent
	// comments when the tracker supports them, into the Box's ISSUE_TEXT
	// (issue #3445). The Factory memoizes it per Dispatch. Nil leaves
	// ISSUE_TEXT absent; an error fails the dispatch.
	IssueTextFor func(number string) (string, error)

	// IssueReader answers the Signal socket's issue read verb from this
	// Dispatch's tracker (issue #6894). Nil, as for a tracker with no
	// resolvable scope, makes the route answer unsupported.
	IssueReader signalsocket.IssueReader

	// IssueSearcher answers the issue-search verb from the same tracker. Nil
	// whenever IssueReader is nil, or the tracker cannot search.
	IssueSearcher signalsocket.IssueSearcher
}

// signalCarrierSocket reports whether c selects the socket signal carrier.
// cmd/launcher's signalcarrier_gate.go re-spells this comparison against its
// own config type.
func (c Config) signalCarrierSocket() bool {
	return c.SignalCarrier == "socket"
}

// kindName is Config.Kind, defaulting to the default kind.
func (c Config) kindName() string {
	if c.Kind == "" {
		return dispatchkind.Default.Name
	}
	return c.Kind
}

// buildBoxEnv assembles the Box env from the schema boxEnv=true vars, the
// per-issue vars, and nonce as RUN_NONCE (issue #1937). An IssueTextFor error
// fails the dispatch rather than being dropped (issue #3445): the prompt
// templates promise the Box an injected ISSUE_TEXT section. It fires before any
// box runs, so the empty-log check surfaces it on Result.Err (issue #3119).
//
// A Chore subject (ADR 0056) skips ISSUE_NUMBER/ISSUE_TITLE/ISSUE_TEXT and the
// ResolveEnv BASE_BRANCH lookup -- localBaseBranchResolver would run an
// issue-tracker lookup against a key that names no issue -- and forwards
// CHORE_* plus BASE_BRANCH from the Chore.
func buildBoxEnv(cfg Config, subj subject, fixPass int, ciFailureSummary string, nonce string) (map[string]string, error) {
	resolve := cfg.ResolveEnv
	if resolve == nil {
		resolve = func(_, name string) string { return os.Getenv(name) }
	}
	isChore := subj.key.IsChore()
	key := subj.key.String()
	env := make(map[string]string)
	for _, name := range strings.Fields(cfg.BoxEnvVars) {
		if isChore && name == "BASE_BRANCH" {
			continue
		}
		env[name] = resolve(key, name)
	}
	// Absent, not blank: an empty value still reaches the Box as a set name.
	for _, d := range backend.Registry {
		delete(env, d.TokenEnvVar)
	}
	if cfg.ForgeDescriptor.HostSeededMirror {
		// The Box clones the mirror and nothing in it reads the remote, so its
		// userinfo (commonly a token) stays on the host.
		if u, ok := env["CODE_FORGE_REMOTE_URL"]; ok {
			env["CODE_FORGE_REMOTE_URL"], _ = gitexec.SplitCredential(u)
		}
	}
	if isChore {
		env["CHORE_NAME"] = subj.chore.Name
		env["BASE_BRANCH"] = subj.chore.Branch
		if subj.chore.Scope.Head != "" {
			env["CHORE_HEAD"] = subj.chore.Scope.Head
		}
		if subj.chore.Scope.DiffRange != "" {
			env["CHORE_DIFF_RANGE"] = subj.chore.Scope.DiffRange
		}
		if len(subj.chore.Scope.Slice) > 0 {
			env["CHORE_SLICE"] = strings.Join(subj.chore.Scope.Slice, "\n")
		}
		if len(subj.chore.PromotionClasses) > 0 {
			env["CHORE_CLASSES"] = strings.Join(subj.chore.PromotionClasses, " ")
		}
		if len(subj.chore.ClassList) > 0 {
			env["CHORE_CLASS_LIST"] = strings.Join(subj.chore.ClassList, " ")
		}
		if subj.chore.Input != "" {
			env["CHORE_INPUT"] = subj.chore.Input
		}
		if len(subj.chore.PatchClasses) > 0 {
			env["CHORE_PATCH_CLASSES"] = strings.Join(subj.chore.PatchClasses, " ")
		}
		env["CHORE_MAX_FINDINGS"] = strconv.Itoa(subj.chore.maxIssueIntents())
	} else {
		env["ISSUE_NUMBER"] = key
		env["ISSUE_TITLE"] = subj.title
		if cfg.IssueTextFor != nil {
			text, err := cfg.IssueTextFor(key)
			if err != nil {
				return nil, fmt.Errorf("issue text for #%s: %w", key, err)
			}
			if text != "" {
				env["ISSUE_TEXT"] = text
			}
		}
	}
	kind := cfg.kindName()
	env["DISPATCH_KIND"] = kind
	// The Box rejects an unrecognized DISPATCH_KIND (see Kind), but its env
	// guard first requires DISPATCH_KEY, DISPATCH_KEYING and
	// DISPATCH_ANNOUNCE_VERB. This host-side Default fallback keeps the latter
	// two set, so an unknown kind reaches that clearer error (issue #3996).
	d, ok := dispatchkind.ByName(kind)
	if !ok {
		d = dispatchkind.Default
	}
	env["DISPATCH_KEY"] = key
	env["DISPATCH_KEYING"] = d.Keying.String()
	env["DISPATCH_ANNOUNCE_VERB"] = d.AnnounceVerb
	if cfg.SelfContained {
		env["SELF_CONTAINED"] = "1"
	}
	// The Box has no forge token to ask for its open PR itself (issue #7146);
	// self-contained and advise-only Boxes never recover a branch. A failed
	// lookup must reach the Box as "unknown", never "none".
	if !cfg.SelfContained && !d.AdviseOnly {
		env[branchrecovery.HostOpenPREnv] = hostOpenPR(cfg, key)
	}
	if fixPass > 0 {
		env["FIX_PASS"] = strconv.Itoa(fixPass)
	}
	if ciFailureSummary != "" {
		env["CI_FAILURE_SUMMARY"] = ciFailureSummary
	}
	env["RUN_NONCE"] = nonce
	// Forwarded so box's outcome backstop keys off explicit signals rather
	// than a raw CODE_FORGE name comparison (issue #2267).
	forgeHostMediatedRemote := cfg.ForgeDescriptor.HostMediatedRemote
	trackerInBoxUnreachable := cfg.TrackerDescriptor.InBoxUnreachableTracker
	if forgeHostMediatedRemote {
		env["BOX_HOST_MEDIATED_REMOTE"] = "1"
	}
	if cfg.ForgeDescriptor.OutboxRelayCapable {
		env["BOX_OUTBOX_RELAY_CAPABLE"] = "1"
	}
	// Both seams are local (ADR 0033). main.go's resolveCapabilitySignals
	// computes the same boolean for the FULLY_LOCAL prompt gate; changing one
	// without the other desyncs BOX_FULLY_LOCAL from FULLY_LOCAL.
	if forgeHostMediatedRemote && trackerInBoxUnreachable {
		env["BOX_FULLY_LOCAL"] = "1"
	}
	if trackerInBoxUnreachable {
		env["BOX_IN_BOX_UNREACHABLE_TRACKER"] = "1"
	}
	// TrackerAxisWrite is legitimately empty for a local (read-only) tracker,
	// so its guard leaves BOX_TRACKER_AXIS_WRITE absent there (issue #2533).
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
	// REGISTRY_PROXY_MANIFEST (ADR 0045, box.go), not here.
	//
	// Prompt gates forward "1" when true, absent rather than "0" when false
	// (issue #2533).
	if cfg.FilerEnabled {
		env["BOX_FILER_ENABLED"] = "1"
	}
	if cfg.IssueRouted {
		env["BOX_ISSUE_ROUTED"] = "1"
	}
	// The prompt offers each issue verb only where the socket serves it
	// (issue #6924); the log carrier has no socket.
	if cfg.signalCarrierSocket() {
		if cfg.IssueReader != nil {
			env["BOX_SIGNAL_ISSUE_READ"] = "1"
		}
		if cfg.IssueSearcher != nil {
			env["BOX_SIGNAL_ISSUE_SEARCH"] = "1"
		}
	}
	if cfg.WorkerProvisioned {
		env["BOX_WORKER_PROVISIONED"] = "1"
	}
	if cfg.ScoutProvisioned {
		env["BOX_SCOUT_PROVISIONED"] = "1"
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

// hostOpenPR renders the open-PR lookup as the Box's BOX_HOST_OPEN_PR value.
func hostOpenPR(cfg Config, key string) string {
	if cfg.OpenPRForIssue == nil {
		return branchrecovery.HostOpenPRUnknown
	}
	open, err := cfg.OpenPRForIssue(key)
	switch {
	case err != nil:
		fmt.Fprintf(os.Stderr, "    ?? %s: open-PR lookup failed: %v; the Box will abort branch recovery\n", key, err)
		return branchrecovery.HostOpenPRUnknown
	case open:
		return branchrecovery.HostOpenPROpen
	}
	return branchrecovery.HostOpenPRNone
}

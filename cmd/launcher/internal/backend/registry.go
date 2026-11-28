// Package backend holds the config-independent metadata for the launcher's
// registered backends, so a pre-CLI consumer such as Quickstart can read it
// without pulling in the launcher's full config machinery. The
// config-dependent constructor closures stay in package main
// (cmd/launcher/backend.go).
package backend

import "sort"

// Descriptor is the config-independent metadata for one registered backend.
type Descriptor struct {
	Name string

	// DisplayName is the backend's human-facing name ("GitHub", "Forgejo",
	// "local"), bound to the prompt's ${FORGE_DISPLAY_NAME} (issue #5647).
	DisplayName string

	ValidAsTracker   bool
	ValidAsCodeForge bool

	// ConnectorOnly marks a backend whose only implementation is its Connector:
	// it has no native adapter, so the Bundle's no-native-network-adapter check
	// (ADR 0073) lets its Strategy stay registered.
	ConnectorOnly bool

	// TokenEnvVar names this backend's bearer-token env var; it is empty when
	// the backend carries no bearer token (git, local).
	TokenEnvVar string

	// DoctorSlugHint names the knob doctor points an operator at when this
	// backend is the active ISSUE_TRACKER and its repo is not found. Empty
	// means the github-shaped default. Doctor's token hint is TokenEnvVar.
	DoctorSlugHint string

	// TrackerScopeKnob names the knob that scopes this tracker when it is not
	// REPO_SLUG (jira's project key); empty means the repo slug scopes it.
	TrackerScopeKnob string

	// HostMediatedRemote is true only for a backend with no writable remote to
	// push to at all (ADR 0033: "local"): the branch returns to the host as an
	// outbox bundle. It says nothing about where the Box clones from; see
	// HostSeededMirror.
	HostMediatedRemote bool

	// HostSeededMirror is true for a backend whose Box clones from a mirror the
	// host seeds fresh before each Pass and mounts at /repo: local (ADR 0033)
	// and git (issue #6898), whose Box holds no credential for the remote. Read
	// only host-side, to pick the Strategy's seeding hook. github and forgejo
	// instead get a host-fetched mirror via forge.MirrorSeeder (issue #6897).
	HostSeededMirror bool

	// OutboxRelayCapable is true for a backend whose CODE_FORGE has pull
	// requests: the promptassembly IF BLOCKED PR gates and recover-kind
	// enablement (RelaysOutbox) read it. The outbox mount and relay follow
	// RelaysBundle instead (issues #1918, #6898).
	// TestRelayCapableMatchesForgeInterfaces (cmd/launcher) fails when it
	// diverges from the forge implementing DraftPRCreator.
	OutboxRelayCapable bool

	// InBoxUnreachableTracker is true only for a tracker with no in-box
	// reachability at all (ADR 0032: "local").
	InBoxUnreachableTracker bool

	// InBoxUnwritableTracker is true only for a tracker the Box has no client
	// for ("jira"), so it can neither write to the tracker nor read its linked
	// issues; "local" is covered by InBoxUnreachableTracker and its empty
	// write axis instead. It overrides the jira row's TrackerAxisWrite="GITHUB"
	// for the research verdict (#5245) and the work IF BLOCKED comment
	// (#5263), which relay through the host, and for the linked-issue read
	// steps (#6344).
	InBoxUnwritableTracker bool

	// SlugIssueRefTracker is true for a tracker whose issues carry no number a
	// PR body can auto-close ("local"): the body names the issue by slug, and
	// only when LOCAL_ISSUE_REFERENCE opts in, instead of `Closes #N`.
	SlugIssueRefTracker bool

	// RelayCapable is true for a CODE_FORGE backend with every host-mediation
	// seam needed under BOX_FORGE_AND_ISSUE_ACCESS=read-only: bundle relay
	// always, plus draft-PR-create and commit-subjects when the backend has a
	// PR concept (git relays a bundle only, issue #6898). Wider than
	// OutboxRelayCapable (#5276). cmd/launcher's
	// TestRelayCapableMatchesForgeInterfaces checks it (issue #2628).
	RelayCapable bool

	// HostPostingCapable is true for an ISSUE_TRACKER backend whose comments
	// and issue filing can be host-mediated under
	// BOX_FORGE_AND_ISSUE_ACCESS=read-only.
	HostPostingCapable bool

	// LabelRegistry is true for an ISSUE_TRACKER backend whose labels are
	// registered objects that ListLabels and CreateLabel manage, so a doctor
	// preflight can require one and create it when missing. local and jira
	// have no registry: CreateLabel is a no-op and ListLabels reports only the
	// dispatch markers (local) or labels already on an issue (jira).
	LabelRegistry bool

	// IssuesWearLabels is true for an ISSUE_TRACKER backend whose issue labels
	// are reliable provenance, so a label's absence is authoritative too
	// (issue #6347): a label on an issue is a fact about it, whether or not
	// labels can be listed or created ahead of time, so jira wears labels
	// without a LabelRegistry. False for local: its frontmatter labels are
	// still read, but a body marker can stand in for a missing one.
	IssuesWearLabels bool

	// TrackerAxisRead is this tracker's read-step axis value ("GITHUB",
	// "LOCAL", "FORGEJO", or "JIRA"); empty means "GITHUB", both for github's
	// row and an unregistered name's zero Descriptor.
	TrackerAxisRead string

	// TrackerAxisWrite is this tracker's write-step axis value ("GITHUB",
	// "FORGEJO", or ""); empty means "GITHUB" for an unregistered lookup, but
	// is local's real resolved value, as it has no write axis.
	TrackerAxisWrite string

	// TrackerAxisFiler is this tracker's filer write-mechanism axis value ("GH"
	// or "FORGEJO"); empty means "GH".
	TrackerAxisFiler string

	// ForgeBackend is this code-forge's backend suffix ("GH" or "FORGEJO");
	// empty means "GH".
	ForgeBackend string

	// Quickstart is the backend's opt-in to the Quickstart wizard (ADR 0068
	// decision 8); nil means hand-edit only, never offered by the wizard.
	Quickstart *QuickstartBlock

	// RouteKinds lists the Issue route kinds this tracker translates (ADR 0075
	// decision 1). Each declared kind has both a filter op and a stamp op;
	// empty means the tracker takes no route at all.
	RouteKinds []string
}

// QuickstartBlock is a row's quickstart declaration: Hosts are the git remote
// hosts that identify the backend, Axes the axes it sets ("tracker", "forge").
// renderBackendRegistryGo guarantees Axes includes "tracker".
type QuickstartBlock struct {
	Hosts []string
	Axes  []string
}

// Registry and its named per-backend vars are generated into registry_gen.go
// from lib/backends/default.nix (issue #2521). Its order (github, git, local,
// jira, forgejo) is load-bearing: env-schema.nix derives issueTracker.choices
// and codeForge.choices from that same Nix list as an order-preserving filter,
// so each axis's pinned choice order falls out of one filter.

// ByName looks up the descriptor for name (an ISSUE_TRACKER or CODE_FORGE knob
// value). ok is false for an unregistered name.
func ByName(name string) (Descriptor, bool) {
	for _, d := range Registry {
		if d.Name == name {
			return d, true
		}
	}
	return Descriptor{}, false
}

// TrackerAxisSignals reads the tracker-axis facts off the matching Registry
// entry rather than a name switch, so nix and Go can never drift (issue
// #2533). renderBackendRegistryGo rejects a row that sets write/filer without
// read, so the early return never drops a declared axis (issue #4183), and
// read without write, so past it "" is a declared empty write axis (issue
// #2673).
func TrackerAxisSignals(issueTracker string) (read, write, filer string) {
	row, ok := ByName(issueTracker)
	if !ok || row.TrackerAxisRead == "" {
		return "GITHUB", "GITHUB", "GH"
	}
	// The "GITHUB" fallback above and filer's "GH" default mirror
	// lib/mkHarness.nix's `or "GITHUB"` / `or "GH"`.
	filer = row.TrackerAxisFiler
	if filer == "" {
		filer = "GH"
	}
	return row.TrackerAxisRead, row.TrackerAxisWrite, filer
}

// ForgeBackendSignal mirrors lib/backends/default.nix's registry rows, the
// same registry-driven shape as TrackerAxisSignals (issue #2533).
func ForgeBackendSignal(codeForge string) string {
	row, ok := ByName(codeForge)
	if !ok || row.ForgeBackend == "" {
		return "GH"
	}
	return row.ForgeBackend
}

// ClosesRef reports whether a `Closes #N` in a PR body on forge auto-closes
// tracker's issue N: forge has pull requests and is the same backend as the
// tracker, so the two share an issue namespace. Any other pairing names a
// foreign tracker's number, or a key the forge never parses. An unknown
// name's zero row is false. Settle's host append (closeOnlyOwnIssue), the
// prompt's PR_BODY_CLOSES gate, and the closing-keyword defuse of the
// Box-authored PR body (#7079) all read it.
func ClosesRef(forge, tracker Descriptor) bool {
	return forge.OutboxRelayCapable && tracker.ValidAsTracker && forge.Name == tracker.Name
}

// OutboxRelayBackends lists, sorted, the CODE_FORGE names RelaysOutbox accepts.
func OutboxRelayBackends() []string {
	var names []string
	for _, d := range Registry {
		if d.OutboxRelayCapable {
			names = append(names, d.Name)
		}
	}
	sort.Strings(names)
	return names
}

// RelaysBundle reports whether a Box on forge d hands its finished branch back
// as an outbox bundle the host relays (ADR 0033, issues #1918, #6898). The
// launcher's outbox mount and the Box's relay, push guard, and outcome
// backstop all decide through this.
func RelaysBundle(d Descriptor) bool {
	return d.HostMediatedRemote || d.RelayCapable
}

// RelaysOutbox reports whether a Box under codeForge is a pull-request forge's
// Box, whose outbox bundle the host relays into a PR (issue #4656). It gates
// the recover kind, so `git`, which relays a bundle but opens no PR, is false.
// An unset CODE_FORGE is not defaulted here: "" relays nothing.
func RelaysOutbox(codeForge string) bool {
	d, ok := ByName(codeForge)
	return ok && d.OutboxRelayCapable
}

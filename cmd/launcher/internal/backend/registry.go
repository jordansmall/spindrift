// Package backend holds the lean, config-independent metadata for the
// launcher's registered backends -- everything a pre-CLI consumer (e.g.
// Quickstart) needs without pulling in the launcher's full config
// machinery. cmd/launcher's own backendRow registry (cmd/launcher/backend.go)
// carries the config-dependent constructor closures and stays in package
// main; this package is the shared, importable subset both sides pull from.
package backend

// Descriptor is the config-independent metadata for one registered backend
// -- everything a pre-CLI consumer (Quickstart) needs without the full
// launcher config machinery.
type Descriptor struct {
	Name string

	ValidAsTracker   bool
	ValidAsCodeForge bool

	// TokenEnvVar is this backend's bearer-token knob name; empty when the
	// backend carries no bearer token (git, local).
	TokenEnvVar string

	// DoctorTokenHint/DoctorSlugHint name the env vars `doctor` points an
	// operator at when this backend is the active ISSUE_TRACKER. Empty
	// means "use the github-shaped default".
	DoctorTokenHint, DoctorSlugHint string

	// HostMediatedRemote is true only for a backend with no writable
	// remote to push to at all (ADR 0033: "local").
	HostMediatedRemote bool

	// OutboxRelayCapable is true for a backend whose CODE_FORGE selection
	// gets the outbox mount/relay treatment under read-only (issue #1918).
	// dispatch/box.go's needsOutbox and dispatch.buildBoxEnv read this
	// field via forge.Capabilities.ForgeDescriptor rather than asserting a
	// backend name (#2947), which is what let #2927 close forgejo's
	// asymmetry (formerly true for github, false for forgejo) with a
	// one-field flip.
	OutboxRelayCapable bool

	// InBoxUnreachableTracker is true only for a tracker with no in-box
	// reachability at all (ADR 0032: "local"), gating the read-only
	// /issues mount.
	InBoxUnreachableTracker bool

	// RelayCapable is true for a CODE_FORGE backend that, under
	// BOX_FORGE_AND_ISSUE_ACCESS=read-only, has every real host-mediation
	// seam needed (bundle-relay always; draft-PR-create + commit-subjects
	// too when the backend has a PR concept). True for github, forgejo,
	// local (trivially, no PR concept); false for git. Distinct from
	// OutboxRelayCapable, which is a narrower concern (outbox mount
	// treatment, issue #1918/#2267/#2927).
	RelayCapable bool

	// HostPostingCapable is true for an ISSUE_TRACKER backend that, under
	// BOX_FORGE_AND_ISSUE_ACCESS=read-only, can have its comments/
	// issue-filing host-mediated (host-posted comments + issue-filing).
	// True for github, forgejo, local; false for jira.
	HostPostingCapable bool

	// TrackerAxisRead is this tracker's ISSUE_TRACKER_GITHUB/LOCAL/FORGEJO
	// read-step axis value ("GITHUB", "LOCAL", or "FORGEJO"); empty means
	// "GITHUB" (the default arm, shared by github and jira -- their
	// registry rows below leave this field at its Go zero value rather
	// than spelling out the literal "GITHUB"). Because an unregistered
	// name's zero-value Descriptor also reads back as TrackerAxisRead ==
	// "", trackerAxisSignals (main.go) uses that same check to cover both
	// cases at once: either way, the caller falls back to the GITHUB/
	// GITHUB/GH defaults, which is the correct resolved value for github
	// and jira anyway.
	TrackerAxisRead string

	// TrackerAxisWrite is this tracker's write-step axis value ("GITHUB",
	// "FORGEJO", or "" for a tracker with no direct write-step path);
	// empty means "GITHUB" for an unregistered/no-row lookup, but "local"
	// sets this to the literal empty string as its real, legitimate
	// resolved value -- it has no write axis at all.
	TrackerAxisWrite string

	// TrackerAxisFiler is this tracker's filer write-mechanism axis value
	// ("GH" or "FORGEJO"); empty means "GH".
	TrackerAxisFiler string

	// ForgeBackend is this code-forge's backend suffix ("GH" or
	// "FORGEJO"); empty means "GH".
	ForgeBackend string
}

// Registry (every registered backend descriptor) and its named GitHub/
// Forgejo/Jira/Local/Git vars are generated into registry_gen.go from
// lib/backends/default.nix (issue #2521) -- not hand-declared here. Its
// order is lib/backends/default.nix's declaration order (github, git,
// local, jira, forgejo), not the order cmd/launcher's own backendRows
// registers them: env-schema.nix's issueTracker.choices/codeForge.choices
// derive from that same Nix list as an order-preserving filter, and this
// declaration order is chosen to reproduce both axes' existing pinned
// choice orders via nothing but a single filter each.

// ByName looks up the descriptor for name (an ISSUE_TRACKER or CODE_FORGE
// knob value). ok is false for an unregistered name.
func ByName(name string) (Descriptor, bool) {
	for _, d := range Registry {
		if d.Name == name {
			return d, true
		}
	}
	return Descriptor{}, false
}

// QuickstartEligible returns the descriptors Quickstart's wizard can drive
// end-to-end today: backends valid as both tracker and code forge with a
// real remote to push to (excludes jira: tracker-only; local: host-mediated
// remote; git: code-forge-only).
func QuickstartEligible() []Descriptor {
	var out []Descriptor
	for _, d := range Registry {
		if d.ValidAsTracker && d.ValidAsCodeForge && !d.HostMediatedRemote {
			out = append(out, d)
		}
	}
	return out
}

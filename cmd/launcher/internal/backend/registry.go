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
	// gets the outbox mount/relay treatment under read-only today (issue
	// #1918: "github" only). NOTE: forgejo also has its own read-only
	// CodeForge constructor (NewReadOnlyForgejoCodeForge) but is NOT
	// included in this today -- that's a pre-existing asymmetry in the
	// current code (mount.go / dispatch/box.go's needsOutbox / the
	// outcome-backstop switch all check CodeForge=="github" specifically,
	// never "forgejo", for this one capability), and #2267 is explicitly
	// behavior-preserving, so this field must reproduce that asymmetry
	// (true for github, false for forgejo) rather than "fixing" it.
	OutboxRelayCapable bool

	// InBoxUnreachableTracker is true only for a tracker with no in-box
	// reachability at all (ADR 0032: "local"), gating the read-only
	// /issues mount.
	InBoxUnreachableTracker bool
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

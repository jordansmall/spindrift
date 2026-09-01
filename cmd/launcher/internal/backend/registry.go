// Package backend holds the config-independent metadata for the launcher's
// registered backends -- everything a pre-CLI consumer (e.g. Quickstart)
// needs without pulling in the launcher's full config machinery.
// cmd/launcher's own backendRow registry (cmd/launcher/backend.go) carries
// the config-dependent constructor closures and stays in package main.
package backend

// Descriptor is the config-independent metadata for one registered backend.
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
	OutboxRelayCapable bool

	// InBoxUnreachableTracker is true only for a tracker with no in-box
	// reachability at all (ADR 0032: "local"), gating the read-only
	// /issues mount.
	InBoxUnreachableTracker bool

	// RelayCapable is true for a CODE_FORGE backend that, under read-only,
	// has every host-mediation seam needed (bundle-relay always;
	// draft-PR-create + commit-subjects too when it has a PR concept).
	// Distinct from OutboxRelayCapable, the narrower outbox-mount concern.
	RelayCapable bool

	// HostPostingCapable is true for an ISSUE_TRACKER backend whose comments
	// and issue-filing can be host-mediated under read-only. False for jira.
	HostPostingCapable bool

	// TrackerAxisRead is this tracker's read-step axis value ("GITHUB",
	// "LOCAL", or "FORGEJO"); empty means "GITHUB", so github and jira leave
	// it at the Go zero value — as does an unregistered name's zero-value
	// Descriptor, which is why one emptiness check covers both.
	TrackerAxisRead string

	// TrackerAxisWrite is this tracker's write-step axis value ("GITHUB",
	// "FORGEJO", or ""). Empty means "GITHUB" for an unregistered lookup,
	// but for "local" it is the real resolved value -- no write axis at all.
	TrackerAxisWrite string

	// TrackerAxisFiler is this tracker's filer write-mechanism axis value
	// ("GH" or "FORGEJO"); empty means "GH".
	TrackerAxisFiler string

	// ForgeBackend is this code-forge's backend suffix ("GH" or
	// "FORGEJO"); empty means "GH".
	ForgeBackend string
}

// Registry and its named per-backend vars are generated into registry_gen.go
// from lib/backends/default.nix. Its order (github, git, local, jira,
// forgejo) is chosen so env-schema.nix's issueTracker.choices/codeForge.choices
// reproduce their pinned orders as nothing but an order-preserving filter each.

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

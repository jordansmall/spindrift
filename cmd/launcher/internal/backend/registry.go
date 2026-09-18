// Package backend holds the config-independent metadata for the launcher's
// registered backends, so a pre-CLI consumer such as Quickstart can read it
// without pulling in the launcher's full config machinery. The
// config-dependent constructor closures stay in package main
// (cmd/launcher/backend.go).
package backend

// Descriptor is the config-independent metadata for one registered backend.
type Descriptor struct {
	Name string

	ValidAsTracker   bool
	ValidAsCodeForge bool

	// TokenEnvVar names this backend's bearer-token env var; it is empty when
	// the backend carries no bearer token (git, local).
	TokenEnvVar string

	// DoctorTokenHint/DoctorSlugHint name the env vars doctor points an
	// operator at when this backend is the active ISSUE_TRACKER. Empty means
	// the github-shaped default.
	DoctorTokenHint, DoctorSlugHint string

	// HostMediatedRemote is true only for a backend with no writable remote to
	// push to at all (ADR 0033: "local").
	HostMediatedRemote bool

	// OutboxRelayCapable is true for a backend whose CODE_FORGE selection gets
	// the outbox mount/relay treatment under read-only (issue #1918).
	// Consumers read this field rather than asserting a backend name (#2947),
	// which is what let #2927 close forgejo's asymmetry with a one-field flip.
	OutboxRelayCapable bool

	// InBoxUnreachableTracker is true only for a tracker with no in-box
	// reachability at all (ADR 0032: "local"). Issue #3471 retired the /issues
	// mount, so grep the field name for the live set of consumers that
	// compensate.
	InBoxUnreachableTracker bool

	// RelayCapable is true for a CODE_FORGE backend with every host-mediation
	// seam needed under BOX_FORGE_AND_ISSUE_ACCESS=read-only: bundle relay
	// always, plus draft-PR-create and commit-subjects when the backend has a
	// PR concept. True for github, forgejo, local; false for git. Wider than
	// OutboxRelayCapable (#1918/#2267/#2927), which is the outbox mount alone.
	RelayCapable bool

	// HostPostingCapable is true for an ISSUE_TRACKER backend whose comments
	// and issue filing can be host-mediated under
	// BOX_FORGE_AND_ISSUE_ACCESS=read-only. True for github, forgejo, local;
	// false for jira.
	HostPostingCapable bool

	// TrackerAxisRead is this tracker's read-step axis value ("GITHUB",
	// "LOCAL", or "FORGEJO"); empty means "GITHUB", so github and jira leave it
	// at the Go zero value. An unregistered name's zero-value Descriptor reads
	// back the same way, which is why trackerAxisSignals (main.go) tests for ""
	// to cover both cases at once.
	TrackerAxisRead string

	// TrackerAxisWrite is this tracker's write-step axis value ("GITHUB",
	// "FORGEJO", or ""); empty means "GITHUB" for an unregistered lookup, but
	// "local" sets it empty as its real resolved value, having no write axis.
	TrackerAxisWrite string

	// TrackerAxisFiler is this tracker's filer write-mechanism axis value ("GH"
	// or "FORGEJO"); empty means "GH".
	TrackerAxisFiler string

	// ForgeBackend is this code-forge's backend suffix ("GH" or "FORGEJO");
	// empty means "GH".
	ForgeBackend string
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

// QuickstartEligible returns the descriptors Quickstart's wizard can drive
// end-to-end today: valid as both tracker and code forge, with a real remote to
// push to.
func QuickstartEligible() []Descriptor {
	var out []Descriptor
	for _, d := range Registry {
		if d.ValidAsTracker && d.ValidAsCodeForge && !d.HostMediatedRemote {
			out = append(out, d)
		}
	}
	return out
}

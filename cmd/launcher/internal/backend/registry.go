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
}

// GitHub is the descriptor for the "github" backend.
var GitHub = Descriptor{
	Name:             "github",
	ValidAsTracker:   true,
	ValidAsCodeForge: true,
	TokenEnvVar:      "GH_TOKEN",
}

// Forgejo is the descriptor for the "forgejo" backend.
var Forgejo = Descriptor{
	Name:             "forgejo",
	ValidAsTracker:   true,
	ValidAsCodeForge: true,
	TokenEnvVar:      "FORGEJO_TOKEN",
	DoctorTokenHint:  "FORGEJO_TOKEN",
	DoctorSlugHint:   "FORGEJO_BASE_URL",
}

// Jira is the descriptor for the "jira" backend.
var Jira = Descriptor{
	Name:             "jira",
	ValidAsTracker:   true,
	ValidAsCodeForge: false,
	TokenEnvVar:      "JIRA_TOKEN",
	DoctorTokenHint:  "JIRA_TOKEN",
	DoctorSlugHint:   "JIRA_BASE_URL / JIRA_PROJECT_KEY",
}

// Local is the descriptor for the "local" backend.
var Local = Descriptor{
	Name:               "local",
	ValidAsTracker:     true,
	ValidAsCodeForge:   true,
	HostMediatedRemote: true,
}

// Git is the descriptor for the "git" backend.
var Git = Descriptor{
	Name:             "git",
	ValidAsTracker:   false,
	ValidAsCodeForge: true,
}

// Registry is every registered backend descriptor, in the same order the
// launcher's own backendRows registers them.
var Registry = []Descriptor{GitHub, Forgejo, Jira, Local, Git}

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

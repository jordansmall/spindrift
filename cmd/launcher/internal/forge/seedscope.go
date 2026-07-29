package forge

// SeedScope is the opaque seed-branch scope a dependent's blocker gate is
// resolved against under CODE_FORGE=local (issue #2150, spec #2144 D2). It
// lives in the forge package (not waves) because a forge-package interface
// (LandingContainmentQuery) and the forge.Fake both need to name it — the
// wave engine still treats it opaquely: it hands the whole scope to the
// local Code Forge's containment query and prints it in hold diagnostics, but
// never constructs or parses a Code Forge's own Integration-branch ref
// grammar. A zero SeedScope means "no scope" — every non-local forge — for
// which the seed-branch containment gate never fires; such a blocker is
// judged solely by its PR/issue state.
type SeedScope struct {
	parent string // sanitized parent token the containment query keys on
	branch string // operator-facing branch label, rendered by the local adapter
}

// NewSeedScope pairs a resolver's sanitized parent token with the
// adapter-rendered branch label the wave engine prints opaquely. Built only by
// the local loop's resolver; the wave engine never constructs one.
func NewSeedScope(parent, branch string) SeedScope { return SeedScope{parent: parent, branch: branch} }

// String renders the adapter-supplied branch label for hold diagnostics, so
// the wave engine names the seam's Integration branch without knowing the
// Code Forge's ref grammar.
func (s SeedScope) String() string { return s.branch }

// Parent returns the sanitized parent token, so the local adapter can build
// the Integration branch its containment query checks the scope against.
func (s SeedScope) Parent() string { return s.parent }

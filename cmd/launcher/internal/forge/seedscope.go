package forge

// SeedScope is the opaque seed-branch scope a dependent's blocker gate is
// resolved against under CODE_FORGE=local (issue #2150, spec #2144 D2). It sits
// in the forge package because LandingContainmentQuery and forge.Fake name it;
// the wave engine never parses its ref grammar. A zero SeedScope means no scope,
// as on every non-local forge, so the blocker is judged by PR/issue state alone.
type SeedScope struct {
	parent string // sanitized parent token the containment query keys on
	branch string // operator-facing branch label, rendered by the local adapter
}

// NewSeedScope pairs a resolver's sanitized parent token with the
// adapter-rendered branch label. Only the local loop's resolver builds one.
func NewSeedScope(parent, branch string) SeedScope { return SeedScope{parent: parent, branch: branch} }

// String renders the adapter-supplied branch label for hold diagnostics.
func (s SeedScope) String() string { return s.branch }

// Parent returns the sanitized parent token the local adapter builds its
// containment query's Integration branch from.
func (s SeedScope) Parent() string { return s.parent }

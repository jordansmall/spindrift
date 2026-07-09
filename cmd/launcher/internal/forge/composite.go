package forge

// composite pairs an independently-selected IssueTracker with an
// independently-selected CodeForge into a single Client, keeping the two axes
// freely combinable (ADR 0013) — e.g. the github IssueTracker with the
// push-only git CodeForge.
type composite struct {
	IssueTracker
	CodeForge
}

// NewComposite returns a Client that routes IssueTracker calls to it and
// CodeForge calls to cf.
func NewComposite(it IssueTracker, cf CodeForge) Client {
	return composite{IssueTracker: it, CodeForge: cf}
}

// Probe disambiguates the two embedded Probe methods (IssueTracker and
// CodeForge both declare one with an identical signature). It is never called
// through the combined Client in practice — doctor probes IssueTracker and
// CodeForge separately via their narrower types — so this exists only to
// satisfy the Client interface; it defers to the IssueTracker.
func (c composite) Probe() (string, error) {
	return c.IssueTracker.Probe()
}

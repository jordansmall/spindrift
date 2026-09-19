package localloop

import (
	"fmt"

	"spindrift.dev/launcher/internal/forge/local"
)

// VerdictKind discriminates Verdict's two shapes (issue #1811).
type VerdictKind int

const (
	// VerdictSurfaced reports a broad ticket whose Integration branch Surface
	// just confirmed current in the operator's checkout.
	VerdictSurfaced VerdictKind = iota
	// VerdictHeld reports a broad ticket Surface could not surface this sweep,
	// naming the first unmet gate it hit.
	VerdictHeld
)

// Verdict is Surface's one-line-per-broad-ticket report (issue #1811, campaign
// #1803 C4). Kind selects which fields apply: Branch and SeamCount for
// VerdictSurfaced, Held for VerdictHeld.
type Verdict struct {
	// Parent is always the sanitized slug, never the title-derived surfaced
	// name, so an edited title cannot shift a ticket's identity (issue #1811).
	Parent local.SanitizedParent
	Kind   VerdictKind
	// Branch is the sanitized parent for a parented ticket, or the sanitized
	// ticket title (falling back to Parent) for a parentless one.
	Branch    string
	SeamCount int
	// Held names the first unmet gate, from the closed set Surface always
	// checks in this order: open seam, stuck landing, target branch checked
	// out, diverged, never landed.
	Held string
}

// String renders v as Surface's one printed line.
func (v Verdict) String() string {
	if v.Kind == VerdictSurfaced {
		return fmt.Sprintf("surface: %s surfaced → branch %s (%d seams)", v.Parent, v.Branch, v.SeamCount)
	}
	return fmt.Sprintf("surface: %s held — %s", v.Parent, v.Held)
}

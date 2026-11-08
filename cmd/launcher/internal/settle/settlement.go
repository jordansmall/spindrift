package settle

import (
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/forge"
)

// Settlement is what one Settle yields (CONTEXT.md): the lifecycle state it
// drove the Dispatch to and its Settle reason (the embedded Verdict), the PR it
// worked if any, and a note. The zero Settlement means the settle reached no
// decision (abandoned mid-settle, reclaimed, or an unclaimed issue left
// alone); its state is forge.Dispatchable and callers fold nothing for it.
type Settlement struct {
	Verdict
	PRURL, Note string
}

// Delivered reports whether s earns the all-delivered banner line. Only a
// Complete counts, so a Failed dispatch with its PR open (ci-red etc.) is still
// named; a merge-blocked Complete counts only if its PR opened (push-only
// forges never latch a PR URL, so their clean Completes count). An
// already-resolved Complete opened nothing, so it is named too.
func (s Settlement) Delivered() bool {
	if s.State() != forge.Complete {
		return false
	}
	switch dispatchrecord.ReasonDelivery(s.Reason()) {
	case dispatchrecord.DeliversNothing:
		return false
	case dispatchrecord.DeliversWithPR:
		return s.PRURL != ""
	default:
		return true
	}
}

package chore

import (
	"cmp"
	"slices"

	"spindrift.dev/launcher/internal/dispatchrecord"
)

// NewSettledRecords returns the host-settled Records after cursor, a Record
// ID, in settle order (SettledSeq, then ID), for a records-scoped Chore (ADR
// 0062). Not claim order: a long Dispatch claimed before the cursor can settle
// after it. An empty cursor, or an unknown one (a pruned store), yields every
// settled Record, so the Chore is never stranded.
func NewSettledRecords(records []dispatchrecord.Record, cursor string) []dispatchrecord.Record {
	var settled []dispatchrecord.Record
	for _, r := range records {
		if r.OutcomeSource == dispatchrecord.OutcomeSourceSettled {
			settled = append(settled, r)
		}
	}
	slices.SortStableFunc(settled, func(a, b dispatchrecord.Record) int {
		return cmp.Or(cmp.Compare(a.SettledSeq, b.SettledSeq), cmp.Compare(a.ID, b.ID))
	})
	if cursor != "" {
		if i := slices.IndexFunc(settled, func(r dispatchrecord.Record) bool { return r.ID == cursor }); i >= 0 {
			settled = settled[i+1:]
		}
	}
	if len(settled) == 0 {
		return nil
	}
	return settled
}

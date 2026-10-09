package chore

import (
	"cmp"
	"slices"

	"spindrift.dev/launcher/internal/dispatchrecord"
)

// NewSettledRecords returns the host-settled Records that come after cursor in
// settle order (SettledSeq, then ID), for a records-scoped Chore (ADR 0062)
// whose ledger cursor is a Record ID. Settle order, not Store.Records()'
// claim order: a long-running Dispatch claimed before the cursor can settle
// after it and must still land in a later window. An empty or unknown cursor
// yields every settled Record: an unknown ID (a pruned store) must not strand
// the Chore on a cursor it can never pass.
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

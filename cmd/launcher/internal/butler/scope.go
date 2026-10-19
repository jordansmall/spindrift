package butler

import (
	"time"

	"spindrift.dev/launcher/internal/chore"
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/ledger"
)

// scopeSource is a Chore's Scope source (ADR 0070 decision 1): the one place
// that knows what a Chore scans and when there is something new to scan. The
// butler picks the adapter from the catalog row and asks it four things --
// whether the Chore is Due, when it next could be, what to Stage before the
// claim, and, once claimed, what to Prepare -- so Sweep never branches on how
// a Chore is scoped.
type scopeSource interface {
	Due(tip ledger.Tip, recent []ledger.Entry, now time.Time, room chore.Room, cfg chore.DueConfig) chore.NotDue
	NextDue(tip ledger.Tip, recent []ledger.Entry, now time.Time, room chore.Room, cfg chore.DueConfig) (at time.Time, onTipMove bool)
	// Stage runs once the Chore is due and before the claim. An error leaves
	// the Ledger unclaimed, so the next sweep retries instead of waiting out a
	// stale takeover.
	Stage() error
	Prepare(claim ledger.Tip) (prepared, error)
}

// prepared is what a scopeSource hands run once the Chore is claimed.
type prepared struct {
	scope     chore.Scope
	input     string
	claimTime time.Time
	// arm is the settle hook: run calls it, when non-nil, once the Box is
	// built and before it runs. An error fails closed with the claim standing.
	arm func(d dispatch.Dispatcher, step *settleRun) error
}

// treeSource scopes a Chore to the repo checkout: it owns the head-keyed due
// check, the tracked-file listing and the commit/tree-walk cursor slice.
type treeSource struct {
	tree Tree
	head string
	// files is the listing Stage took, for Prepare to slice.
	files []string
}

func (s *treeSource) Due(tip ledger.Tip, recent []ledger.Entry, now time.Time, room chore.Room, cfg chore.DueConfig) chore.NotDue {
	return chore.Check(tip, recent, s.head, now, room, cfg)
}

func (s *treeSource) NextDue(tip ledger.Tip, recent []ledger.Entry, now time.Time, room chore.Room, cfg chore.DueConfig) (time.Time, bool) {
	return chore.NextDue(tip, recent, s.head, now, room, cfg)
}

// Stage lists the tracked files before the claim; listing only for a due Chore
// keeps the not-due sweeps free of a git call per Chore.
func (s *treeSource) Stage() error {
	files, err := s.tree.TrackedFiles(s.head)
	if err != nil {
		return err
	}
	s.files = files
	return nil
}

func (s *treeSource) Prepare(claim ledger.Tip) (prepared, error) {
	return prepared{scope: chore.NextScope(claim.State, s.head, s.files, chore.DefaultSliceSize)}, nil
}

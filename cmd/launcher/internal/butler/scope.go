package butler

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"time"

	"spindrift.dev/launcher/internal/chore"
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/ledger"
	"spindrift.dev/launcher/internal/recordsdigest"
	"spindrift.dev/launcher/internal/settle"
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

// scopeSourceFor picks c's Scope source adapter from its catalog row. It is the
// only place that reads c.Records. whenNow is Sweep's instant, the one run
// later stamps the claim with.
func (r *Runner) scopeSourceFor(c chore.Chore, head string, whenNow time.Time) (scopeSource, error) {
	if c.Records {
		return newRecordsSource(r, c.Name, head, whenNow)
	}
	return &treeSource{tree: r.tree, head: head}, nil
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

// recordsSource scopes a Chore to the Dispatch Records store (ADR 0062): it
// owns the Record ID cursor, the settled-Records count that makes the Chore
// due, and the digest of the Records past the cursor.
//
// A records-scoped Chore has no file slice: its input is that digest, and the
// next cursor is the latest Record ID it covered, carried in Scope.NextCursor
// so settle's Done commit is unchanged.
type recordsSource struct {
	policy Policy
	tree   Tree
	it     forge.IssueTracker
	warn   io.Writer
	// merged is built once per source: its rate-limit state is per digest.
	merged recordsdigest.MergeLookup
	digest recordsdigest.Strategy
	chore  string
	head   string
	// records is read once per candidate, so the digest renders the very
	// Records the Chore was judged due on.
	records   []dispatchrecord.Record
	claimedAt time.Time
}

func newRecordsSource(r *Runner, choreName, head string, claimedAt time.Time) (recordsSource, error) {
	digest, ok := recordsdigest.ByName(choreName)
	if !ok {
		return recordsSource{}, recordsdigest.NotRegistered(choreName)
	}
	records, err := r.policy.loadRecords()
	if err != nil {
		return recordsSource{}, err
	}
	return recordsSource{
		policy: r.policy, tree: r.tree, it: r.it, warn: r.warn, merged: r.mergeLookup(choreName), digest: digest,
		chore: choreName, head: head, records: records, claimedAt: claimedAt,
	}, nil
}

func (s recordsSource) newSettled(tip ledger.Tip) int {
	return len(chore.NewSettledRecords(s.records, tip.State.Cursor))
}

func (s recordsSource) Due(tip ledger.Tip, recent []ledger.Entry, now time.Time, room chore.Room, cfg chore.DueConfig) chore.NotDue {
	return chore.CheckRecords(tip, recent, s.newSettled(tip), s.policy.TuningMinRecords, now, room, cfg)
}

func (s recordsSource) NextDue(tip ledger.Tip, recent []ledger.Entry, now time.Time, room chore.Room, cfg chore.DueConfig) (time.Time, bool) {
	return chore.NextDueRecords(tip, recent, s.newSettled(tip), s.policy.TuningMinRecords, now, room, cfg)
}

// Stage is a no-op: the Records were loaded when the source was built.
func (s recordsSource) Stage() error { return nil }

func (s recordsSource) Prepare(claim ledger.Tip) (prepared, error) {
	var prior []any
	if s.digest.ParsePrior != nil {
		findings, err := settle.PriorFindings(s.it, s.chore)
		if err != nil {
			fmt.Fprintf(s.warn, "    ?? %s: prior findings: %v\n", s.chore, err)
		}
		prior = make([]any, 0, len(findings))
		for _, f := range findings {
			prior = append(prior, s.digest.ParsePrior(f))
		}
	}
	out := s.digest.Render(recordsdigest.Input{
		Records: s.records, Cursor: claim.State.Cursor, Now: s.claimedAt,
		Limits: s.policy.digestLimits(),
		Prior:  prior, Merged: s.merged,
	})
	return prepared{
		scope: chore.Scope{Head: s.head, NextCursor: out.Latest},
		input: out.Text,
		// The sweep's Record ID keys the stored digest, so it is pinned to the
		// claim instant to be known before the Box runs.
		claimTime: s.claimedAt,
		arm:       func(d dispatch.Dispatcher, step *settleRun) error { return s.armSnapshot(d, step, out.Text) },
	}, nil
}

func (s recordsSource) armSnapshot(d dispatch.Dispatcher, step *settleRun, digest string) error {
	// Fail closed: a Box launched without a stored digest could report
	// findings nothing can validate. The claim stands until stale takeover.
	id := d.RecordID()
	if id == "" {
		return fmt.Errorf("butler: %s: dispatch has no record ID to key the snapshot", s.chore)
	}
	sum := sha256.Sum256([]byte(digest))
	ref := ledger.Snapshot{Sweep: id, SHA256: hex.EncodeToString(sum[:])}
	if err := s.policy.storeSnapshot(dispatchrecord.Snapshot{Chore: s.chore, RecordID: id, SHA256: ref.SHA256, Rendered: digest, CreatedAt: s.claimedAt}); err != nil {
		return fmt.Errorf("butler: %s: %w", s.chore, err)
	}
	step.tuning = &tuningSnapshot{chore: s.chore, ref: ref, digest: s.digest, policy: s.policy, tree: s.tree}
	return nil
}

package settle

import (
	"errors"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/passmanifest"
)

// ErrNothingToRecover means Recover found no open PR and no relayable
// evidence: either it declined under Strict before Begin, or Begin ran and the
// Lander relayed nothing without reporting an error.
var ErrNothingToRecover = errors.New("nothing to recover")

// RecoverInput is the evidence spindrift recover gathered for one issue.
type RecoverInput struct {
	Num      string
	OpenPR   string // the issue's open PR URL; "" when none was found
	Resolved outcome.Resolved
	Passes   []passmanifest.Entry
	// Strict is queue mode's eligibility: a relayed branch needs a bundle in
	// the outbox and a success self-report, else Recover declines before Begin.
	Strict bool
	// Begin claims the issue and starts its Dispatch once Recover commits to
	// landing. Its error is returned unchanged.
	Begin func() (dispatch.Dispatcher, error)
}

// RecoverGen is the terminate.Registry generation Recover settles at: no Begin
// bumps it, so a caller checking for a Mark on a recovered issue must match.
const RecoverGen uint64 = 0

// Recover is spindrift recover's single entry (issues #2225, #2408), so the
// relay-vs-adopt-vs-decline decision lives here rather than in the caller. It
// returns, in order of precedence:
//   - a Settlement with ReasonUnreviewed, when the run's review loop stopped
//     before its land pass finished and the evidence is not an open PR without
//     a success self-report (issues #5973, #5984); the caller prints and fails
//     it, since nothing was claimed or recorded;
//   - ErrNothingToRecover, per its doc;
//   - Begin's error unchanged, or the relay's or draft-PR's wrapped;
//   - the landing's Settlement, zero when a stop abandoned it.
func (s *Settle) Recover(in RecoverInput) (Settlement, error) {
	sit := s.situationFor(in.Num, in.OpenPR != "", dispatch.Result{Resolved: in.Resolved, Passes: in.Passes})
	// Queue mode skips an unreviewed run silently: settle already commented, so
	// a decline here would repeat on every daemon sweep.
	if in.Strict && in.OpenPR == "" && (!sit.BundlePresent || !sit.SelfReportSuccess || sit.Unreviewed) {
		return Settlement{}, ErrNothingToRecover
	}
	// An open PR does not vouch for the run (issue #5973), but a run without a
	// success self-report has no stale ready to judge and is left to the adopt
	// arm's merge gate.
	if sit.Unreviewed && (in.OpenPR == "" || sit.SelfReportSuccess) {
		return Settlement{Verdict: ForReason(dispatchrecord.ReasonUnreviewed), Note: dispatchrecord.UnreviewedNote}, nil
	}
	d, err := in.Begin()
	if err != nil {
		return Settlement{}, err
	}
	if in.OpenPR != "" {
		return s.settleAdopted(d, in.Num, RecoverGen, in.OpenPR), nil
	}
	return s.recoverRelay(d, in.Num, sit, in.Resolved)
}

// recoverRelay lands num's relayed branch. Its Result carries no Passes, so a
// recover landing still clears the recorded landing pass rather than
// inheriting the run's (they feed only the Situation).
func (s *Settle) recoverRelay(d dispatch.Dispatcher, num string, sit Situation, resolved outcome.Resolved) (st Settlement, err error) {
	defer func() { st = s.flushSettled(d, num) }()
	relayed, err := s.lander.landRelayed(d, num, RecoverGen, sit, dispatch.Result{Resolved: resolved})
	if err == nil && !relayed {
		err = ErrNothingToRecover
	}
	return st, err
}

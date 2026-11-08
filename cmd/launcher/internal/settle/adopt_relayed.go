package settle

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/passmanifest"
	"spindrift.dev/launcher/internal/seambundle"
)

// SettleRelayedBranch is spindrift recover's adopt-a-relayed-branch arm
// (#2225); sit comes from the caller (#2501). Recover is read-write, so it
// needs neither synthetic provenance nor a read-only Box. An open PR is
// SettleAdopted's job, so sit.OpenPRFound returns false; every production
// caller passes false today, so this guard only protects a future caller.
// The Lander lands the relayed branch: a PR forge opens a PR on it, while
// local push-only has no PR to open (ADR 0039, #2254), where a bundle alone
// is evidence (#2378). The error is non-nil only when relaying the branch or
// opening its draft PR failed.
func (s *Settle) SettleRelayedBranch(d dispatch.Dispatcher, num string, gen uint64, sit Situation, result dispatch.Result) (st Settlement, relayed bool, err error) {
	defer func() { st = s.flushSettled(d, num) }()
	// An unreviewed run is not adoption evidence, push-only included (issues #5973, #5984).
	if sit.OpenPRFound || sit.Unreviewed {
		return Settlement{}, false, nil
	}
	relayed, err = s.lander.landRelayed(d, num, gen, sit, result)
	return Settlement{}, relayed, err
}

// bundlePresent reports whether num's outbox holds a relayable bundle file. It
// stats rather than calling RelayBundle: detecting Recoverable must never
// import or land anything.
func (s *Settle) bundlePresent(num string) bool {
	path, ok := s.bundlePath(num)
	if !ok {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// bundlePath is num's seam.bundle path, or false when no outbox is configured.
func (s *Settle) bundlePath(num string) (string, bool) {
	if s.cfg.OutboxDir == nil {
		return "", false
	}
	return filepath.Join(s.cfg.OutboxDir(num), seambundle.FileName), true
}

// removeLandedBundle deletes num's seam.bundle once its work has merged. A
// bundle left behind reads as unlanded work to recover's queue mode and the
// outbox Demand (issue #4652). Only the bundle goes: the outbox also holds the
// pass manifest. Best-effort, since a leftover file must never fail a landing
// that already merged (issue #4653).
func (s *Settle) removeLandedBundle(num string) {
	path, ok := s.bundlePath(num)
	if !ok {
		return
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		s.cfg.Progress.Step(Step{Key: num, Status: StatusBundleRemoveFailed, Detail: fmt.Sprintf("!! %v", err)})
	}
}

// isSuccessSelfReport reports whether a driver self-report's Status means the
// run succeeded. Only outcome.StatusReady counts: the bare word "success" was
// never in outcome.WorkStatuses, so per #2981 it is no longer accepted here,
// deliberately narrowing the self-report side of #2223's adoption path.
func isSuccessSelfReport(status string) bool {
	return status == outcome.StatusReady
}

// gateAdopted is the tail shared by both Landers' relayed-branch landings:
// print status=adopted for landing, record it with the run's passes, drive the
// merge gate, and post the usage comment.
func (s *Settle) gateAdopted(d dispatch.Dispatcher, num string, gen uint64, landing, note string, passes []passmanifest.Entry) {
	s.cfg.Progress.Step(Step{Key: num, Landing: landing, Status: StatusAdopted, Detail: "note=" + note})
	s.recordLanding(num, landing, passes)
	result, reason, err := s.selfHeal(d, num, gen, landing, passes)
	if errors.Is(err, errAbandoned) {
		// Another actor owns the issue, so a usage comment here would be noise.
		return
	}
	if result == landingFailed {
		s.cfg.Progress.Step(Step{Key: num, Landing: landing, Status: StatusFailed, Detail: "!! " + reason})
	}
	s.postUsageComment(num, d)
}

package main

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"time"

	"spindrift.dev/launcher/internal/daemon"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/report"
)

// checkoutEvents reads the Daemon's Events file for root, or nothing when root
// is not itself the top of a git checkout. Requiring the top level to be root
// keeps a scratch directory inside some other repo from borrowing that repo's
// events.
func checkoutEvents(root string, stderr io.Writer) []daemon.Event {
	top, err := isCheckoutTop(root)
	if err != nil {
		fmt.Fprintf(stderr, "warning: not reading daemon events: %v\n", err)
		return nil
	}
	if !top {
		return nil
	}
	gitDir, err := gitOutput(root, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return nil
	}
	events, err := daemon.ReadEvents(gitDir)
	if err != nil {
		// Without events every inferred Record lands in (none); say why
		// rather than let that pass for a real answer.
		fmt.Fprintf(stderr, "warning: not reading daemon events: %v\n", err)
		return nil
	}
	return events
}

// isCheckoutTop reports whether root is the top of its own git checkout. A
// root that is merely not a checkout is ordinary; a missing git is not, and
// would otherwise pass for "not a checkout" on every root, so it is an error.
func isCheckoutTop(root string) (bool, error) {
	top, err := gitOutput(root, "rev-parse", "--show-toplevel")
	if errors.Is(err, exec.ErrNotFound) {
		return false, err
	}
	return err == nil && sameDir(top, root), nil
}

func sameDir(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

// fillInferredRevisions sets Revision on each inferred Record from the Events
// file, where the Record's own log predates the dispatch_start stamp. The Box
// event is announced before the Box runs, so the Dispatch's event is the
// latest initial-phase box event for its kind and key at or before its claim
// time. An event followed by another Record of that kind and key claiming
// before this one belongs to that earlier Dispatch instead: a manual dispatch
// writes no event, and must not inherit a stale one. Nothing is persisted.
func fillInferredRevisions(records []dispatchrecord.Record, events []daemon.Event) {
	type boxEvent struct {
		at  time.Time
		rev string
	}
	type dispatch struct{ kind, key string }
	boxes := map[dispatch][]boxEvent{}
	for _, ev := range events {
		if ev.Event != report.EventBox || ev.Phase != report.PhaseInitial || ev.Revision == "" {
			continue
		}
		d, ok := dispatchkind.ByVerb(string(ev.Kind))
		at, err := time.Parse(time.RFC3339, ev.Time)
		if !ok || err != nil {
			continue
		}
		k := dispatch{d.Name, ev.Key.String()}
		boxes[k] = append(boxes[k], boxEvent{at, ev.Revision})
	}
	if len(boxes) == 0 {
		return
	}
	for i := range records {
		r := &records[i]
		if r.Attribution != dispatchrecord.AttributionInferred || r.Revision != "" {
			continue
		}
		k := dispatch{r.Kind, r.DispatchKey}
		var best *boxEvent
		for j, b := range boxes[k] {
			if !b.at.After(r.ClaimTime) && (best == nil || !b.at.Before(best.at)) {
				best = &boxes[k][j]
			}
		}
		if best == nil {
			continue
		}
		earlier := false
		for _, o := range records {
			if o.ID != r.ID && o.Kind == k.kind && o.DispatchKey == k.key &&
				!o.ClaimTime.Before(best.at) && o.ClaimTime.Before(r.ClaimTime) {
				earlier = true
				break
			}
		}
		if !earlier {
			r.Revision = best.rev
		}
	}
}

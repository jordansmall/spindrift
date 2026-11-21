package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
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
		warnEach(stderr, "not reading daemon events", err)
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
		warnEach(stderr, "not reading daemon events", err)
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

// fillInferredRevisions sets Revision on each inferred Record (whose log
// predates the dispatch_start stamp) from the Events file. The Box event is
// announced before the Box runs, so a Dispatch's event is the latest
// initial-phase box event for its kind and key at or before its claim time.
// If another Record of that kind and key claimed in between, that event is the
// earlier Dispatch's and this Record gets none: a manual dispatch writes no
// event and must not inherit a stale one. Nothing is persisted.
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

// gitOutput runs `git -C pwd args...` and returns its trimmed stdout,
// surfacing git's own stderr on failure.
func gitOutput(pwd string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", pwd}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

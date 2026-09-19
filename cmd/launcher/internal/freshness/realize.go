package freshness

import (
	"fmt"
	"os"
)

// Realizer builds a flake attribute's derivation at a specific git rev into
// the local nix store. The real implementation shells out to `nix build`;
// tests substitute a RealizerFake so no nix round-trip is required.
type Realizer interface {
	// Start forks a build of attr in the flake rooted at pwd, at rev, a
	// fetched commit-ish rather than the working tree, so nothing touches
	// pwd's checkout. It blocks only long enough to fork, so the realize
	// survives a caller that exits immediately afterward, including via
	// os.Exit, which does not wait for goroutines. wait blocks for the outcome.
	Start(pwd, rev, attr string) (wait func() error, err error)
}

// RealizeTip starts realizing the base-tip image artifact res describes and
// waits for it in a background goroutine. It calls Start in the caller's own
// goroutine so the build process is forked before RealizeTip returns: the
// caller reaches main()'s os.Exit right after, and os.Exit would otherwise win
// the race, leaving the realize silently undone.
func RealizeTip(r Realizer, pwd string, res Result, flakeImageAttr string) {
	wait, attr, skipped, err := startRealize(r, pwd, res, flakeImageAttr)
	if skipped {
		return
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "background realize of %s tip %s (%s) failed to start: %v\n", res.TipTag, res.Rev, attr, err)
		return
	}
	go func() {
		if err := wait(); err != nil {
			fmt.Fprintf(os.Stderr, "background realize of %s tip %s (%s) failed: %v\n", res.TipTag, res.Rev, attr, err)
		}
	}()
}

// startRealize holds the guard, attr trimming, and Start call that RealizeTip
// and RealizeSync share, so the two cannot drift apart. Callers must check
// skipped, which marks a genuine no-op, before err: a Start implementation
// that returns (nil, nil) on success would otherwise read as a skip
// (issue #2682 review findings).
func startRealize(r Realizer, pwd string, res Result, flakeImageAttr string) (wait func() error, attr string, skipped bool, err error) {
	// A non-empty TipTag is Probe's only genuine "rebuild needed, tag differs"
	// verdict. Its error branches and its launcher-only-stale verdict all leave
	// TipTag empty, and none of them have anything to realize.
	if !res.Applicable || res.Fresh || res.TipTag == "" {
		return nil, "", true, nil
	}
	// Probe applies this same trim before its own eval.Eval call, so both paths
	// address the same flake attribute.
	attr = trimFlakeAttrPrefix(flakeImageAttr)
	wait, err = r.Start(pwd, res.Rev, attr)
	return wait, attr, false, err
}

// RealizeSync blocks on the realize and returns its outcome, for a caller that
// must know the build succeeded before it acts on the result: the bwrap
// Box-only staleness hot-swap (issue #2682) binds the new closure for
// subsequent Box launches. Its caller handles the error, so unlike RealizeTip
// it never writes to stderr.
func RealizeSync(r Realizer, pwd string, res Result, flakeImageAttr string) error {
	wait, attr, skipped, err := startRealize(r, pwd, res, flakeImageAttr)
	if skipped {
		return nil
	}
	if err != nil {
		return fmt.Errorf("realize of %s tip %s (%s) failed to start: %w", res.TipTag, res.Rev, attr, err)
	}
	if err := wait(); err != nil {
		return fmt.Errorf("realize of %s tip %s (%s) failed: %w", res.TipTag, res.Rev, attr, err)
	}
	return nil
}

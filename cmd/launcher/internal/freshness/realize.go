package freshness

import (
	"fmt"
	"os"
)

// Realizer builds a flake attribute's derivation at a specific git rev,
// landing the output in the local nix store. The real implementation shells
// out to `nix build`; tests substitute a RealizerFake.
//
// The realize is split into a synchronous Start and an asynchronous wait on
// purpose: RealizeTip's caller ultimately reaches main()'s os.Exit, which
// terminates the process without waiting for any goroutine — not even one that
// has yet to fork its `nix build` child. If Start launched the whole realize
// inside a background goroutine, os.Exit could win the race and the realize
// would silently never happen. Blocking only long enough to fork means the
// realize is durably underway by the time Start returns, while the slow part
// still runs in the background via the returned wait function.
type Realizer interface {
	// Start begins realizing (building) attr's derivation in the flake rooted
	// at pwd, at rev — a fetched commit-ish, never the working tree — so the
	// output lands in the local nix store without touching pwd's checkout. It
	// blocks only long enough to fork the underlying process, never until the
	// realize completes, so by the time it returns the realize is durably
	// underway and survives the caller exiting immediately afterward. It
	// returns a wait function the caller runs asynchronously to block for
	// completion and learn the outcome.
	Start(pwd, rev, attr string) (wait func() error, err error)
}

// RealizeTip kicks off realizing (building) the base-tip image artifact res
// describes. It calls Start synchronously, in the caller's own goroutine, so
// the underlying process is durably forked (see the Realizer doc) before
// RealizeTip returns — the caller, and hence the process via os.Exit, can then
// return immediately without racing the realize out of existence. Only the
// wait for completion moves into a background goroutine, so a slow or failed
// realize never blocks the caller.
//
// It is a no-op unless res is Probe's one genuine "rebuild needed, tag
// differs" verdict — the only Result variant with a non-empty TipTag, since
// Probe sets TipTag only once it has fetched, evaluated, and tagged the base
// tip AND that tag differs from the loaded one. !res.Applicable, res.Fresh, or
// TipTag == "" all skip the realize; the last also catches every one of
// Probe's error and no-divergence branches, which report Applicable=true with
// no genuine image-tag mismatch to realize.
//
// flakeImageAttr is trimmed of its ".#" prefix via the same
// trimFlakeAttrPrefix helper Probe uses before its own eval.Eval call, so the
// two paths always address the same flake attribute.
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

// startRealize centralizes RealizeTip's and RealizeSync's shared no-op guard,
// attr-trimming, and Start call so the two can't drift apart. skipped true
// means the guard skipped the realize entirely (a genuine no-op, not a
// failure); callers must check it before err rather than inferring a skip from
// wait == nil && err == nil, which a Start implementation wrongly returning
// (nil, nil) on success would read identically.
func startRealize(r Realizer, pwd string, res Result, flakeImageAttr string) (wait func() error, attr string, skipped bool, err error) {
	if !res.Applicable || res.Fresh || res.TipTag == "" {
		return nil, "", true, nil
	}
	attr = trimFlakeAttrPrefix(flakeImageAttr)
	wait, err = r.Start(pwd, res.Rev, attr)
	return wait, attr, false, err
}

// RealizeSync is RealizeTip's synchronous counterpart, for a caller that
// cannot fire-and-forget: the bwrap Box-only staleness hot-swap must know
// whether the realize succeeded before binding the newly-realized closure for
// subsequent Box launches, so it blocks on wait() and returns the outcome
// instead of forking the wait into a background goroutine. It shares
// RealizeTip's no-op guard and attr-trimming, and, since its caller handles
// the error itself, never writes to stderr.
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

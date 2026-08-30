package freshness

import (
	"fmt"
	"os"
)

// Realizer builds a flake attribute's derivation at a specific git rev,
// landing the output in the local nix store. The real implementation shells
// out to `nix build`; tests substitute a RealizerFake so no nix round-trip
// is required.
//
// The realize is split into a synchronous Start and an asynchronous wait on
// purpose: RealizeTip's caller ultimately reaches main()'s os.Exit, which
// terminates the process immediately and does not wait for any other
// goroutine to run, let alone for a goroutine to have gotten as far as
// forking a `nix build` child process. If Start merely launched the whole
// realize (fork *and* wait for completion) inside a background goroutine,
// os.Exit could win the race and the realize would silently never happen.
// Requiring Start to block only long enough to fork the underlying process
// means that by the time Start (and therefore RealizeTip, and therefore its
// caller) returns, the realize is durably underway and will survive the
// caller exiting immediately afterward — while the slow part, the actual
// build, still runs to completion in the background via the returned wait
// function, never blocking the caller.
type Realizer interface {
	// Start begins realizing (building) attr's derivation in the flake
	// rooted at pwd, at rev — a fetched commit-ish, never the working tree
	// — so the output lands in the local nix store without touching pwd's
	// checkout. It blocks only long enough to fork the underlying process
	// — never until the realize completes — so that by the time it
	// returns, the realize is durably underway and will survive the caller
	// exiting immediately afterward, including via os.Exit (which does not
	// wait for goroutines). It returns a wait function the caller runs
	// asynchronously to block for completion and learn the outcome.
	Start(pwd, rev, attr string) (wait func() error, err error)
}

// RealizeTip kicks off realizing (building) the base-tip image artifact res
// describes. It calls Start synchronously, in the caller's own goroutine,
// so the underlying process is durably forked (see the Realizer doc comment)
// before RealizeTip returns — the whole point being that the caller (and
// hence the process, via os.Exit) can safely return/exit right after
// RealizeTip does, without racing the realize out of existence. Only the
// wait for completion moves into a background goroutine, so a slow or
// failed realize never blocks or changes the caller's own behavior. It is a
// no-op — Start is never called — unless res is Probe's one genuine
// "rebuild needed, tag differs" verdict: the only Result variant with a
// non-empty TipTag, since Probe only sets TipTag once it has successfully
// fetched, evaluated, and tagged the base tip AND that tag genuinely differs
// from the loaded one. !res.Applicable, res.Fresh, or res.TipTag == "" all
// skip the realize entirely — the last of which also catches every one of
// Probe's error/no-divergence branches that report Applicable=true without a
// genuine image-tag mismatch to realize: the two image-side branches that
// carry a real, non-empty res.Rev but never reach tag derivation
// (eval-error and tag-derive-error), the one that doesn't (fetch-failure),
// the two launcher-side error branches (launcher eval-error and
// hash-derive-error — the image itself succeeded, but there's still no
// genuine image-tag divergence to realize), and a launcher-only-stale
// verdict (the image matched; only the launcher dimension is stale, so
// TipTag is left empty even though Fresh is false). None of them have
// anything meaningful to realize.
// flakeImageAttr is trimmed of its ".#" prefix via the same
// trimFlakeAttrPrefix helper Probe uses immediately before its own
// eval.Eval call, so the two paths always address the same flake attribute.
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

// startRealize centralizes RealizeTip's and RealizeSync's shared no-op
// guard, attr-trimming, and Start call, so the two can no longer silently
// drift apart despite their doc comments promising they behave "the exact
// same" up to this point (issue #2682 review finding). skipped true means
// the guard skipped the realize entirely (a genuine no-op, not a failure) --
// callers must check it before err, rather than inferring a skip from
// wait == nil && err == nil, which a Start implementation returning (nil,
// nil) on success (a Realizer contract violation, but not one this package
// enforces) would read identically (issue #2682 review finding).
func startRealize(r Realizer, pwd string, res Result, flakeImageAttr string) (wait func() error, attr string, skipped bool, err error) {
	if !res.Applicable || res.Fresh || res.TipTag == "" {
		return nil, "", true, nil
	}
	attr = trimFlakeAttrPrefix(flakeImageAttr)
	wait, err = r.Start(pwd, res.Rev, attr)
	return wait, attr, false, err
}

// RealizeSync is RealizeTip's synchronous counterpart, for a caller that
// cannot fire-and-forget: the upcoming bwrap Box-only staleness hot-swap
// (issue #2682) must know whether the realize succeeded before it binds the
// newly-realized closure for subsequent Box launches, so unlike RealizeTip
// (whose caller reaches os.Exit right after and must never block on the
// build) it blocks on wait() itself and returns the outcome instead of
// forking the wait into a background goroutine. It shares the exact same
// no-op guard and attr-trimming as RealizeTip — see RealizeTip's doc comment
// for why those checks are what they are — and, since its caller is
// expected to handle/log the error itself, it never writes to stderr the way
// RealizeTip's background goroutine does.
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

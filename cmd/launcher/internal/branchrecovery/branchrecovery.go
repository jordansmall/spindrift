// Package branchrecovery is the Box's pre-work branch positioning: it decides
// whether a prior run's agent branch is adopted or force-reset, checks the
// branch out, rebases it onto the latest base, and owns the single decision of
// how a rebased branch is published. Git runs for real in Config.WorkDir; the
// one question git cannot answer, whether the branch has an open PR, is
// injected.
package branchrecovery

import (
	"fmt"
	"io"
	"os/exec"
	"strings"

	"spindrift.dev/launcher/internal/bundleout"
)

// Config is what the decision reads from the Box's environment.
type Config struct {
	WorkDir    string
	Branch     string
	BaseBranch string
	// CodeForge is CODE_FORGE, defaulted to github by the caller; narration only.
	CodeForge string
	// QueryOpenPR is set when the Box can ask CODE_FORGE about open PRs
	// (backend.Descriptor.InBoxOpenPRQueryable, which records why the rest cannot).
	QueryOpenPR bool
	// Push is BOX_WRITE_ENABLED: the Box holds a push-capable token. A
	// read-only Box relays through the outbox bundle instead (issue #1979:
	// a force-push there 403s before the agent ever runs), behind the same
	// fail-closed gate the OPEN A PULL REQUEST contract uses (issue #1918).
	Push      bool
	OutboxDir string
}

// Outcome is what the caller needs after Recover.
type Outcome struct {
	// Conflict: the pre-work rebase stopped and is left in progress for the
	// conflict-resolve pass.
	Conflict bool
	// Adopted: prior work on an open PR was checked out, so its rebased
	// history is owed a Publish once any conflict is resolved.
	Adopted bool
}

// Recover positions cfg.Branch for the agent: fresh from origin/base, adopted
// from an open PR, or force-reset when stale. It then rebases onto
// origin/base and publishes at once when nothing is left to resolve. Narration
// goes to w. An error means the Box must abort.
//
// openPR is consulted only when cfg.QueryOpenPR, and only when origin/Branch
// exists. Its error must abort: a silent empty answer (network/auth failure)
// is indistinguishable from "no PR" and must not trigger the force-reset.
func Recover(cfg Config, openPR func() (bool, error), w io.Writer) (Outcome, error) {
	var out Outcome
	base := "origin/" + cfg.BaseBranch
	if err := cfg.seedRemoteReflog(w); err != nil {
		return out, err
	}
	// A leftover origin/<branch> is recorded in the new branch's reflog before
	// the reset: the push's --force-if-includes refuses a remote tip the local
	// reflog never held.
	fresh := func() error {
		if cfg.git(io.Discard, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+cfg.Branch) != nil {
			return cfg.git(w, "checkout", "-b", cfg.Branch, base)
		}
		if err := cfg.git(w, "branch", "--no-track", cfg.Branch, "origin/"+cfg.Branch); err != nil {
			return err
		}
		return cfg.git(w, "checkout", "-B", cfg.Branch, base)
	}

	switch {
	case !cfg.QueryOpenPR:
		// A stale refs/remotes/origin/<branch> is superseded by a fresh checkout.
		fmt.Fprintf(w, "==> CODE_FORGE=%s: starting %s fresh from %s\n", cfg.CodeForge, cfg.Branch, base)
		if err := fresh(); err != nil {
			return out, err
		}
	case cfg.git(io.Discard, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+cfg.Branch) != nil:
		if err := fresh(); err != nil {
			return out, err
		}
	default:
		open, err := openPR()
		if err != nil {
			return out, fmt.Errorf("open-PR query failed on %s; aborting to protect any open PR: %w", cfg.Branch, err)
		}
		if open {
			fmt.Fprintf(w, "==> open PR exists on %s; skipping force-reset — checking out prior work for pre-work rebase\n", cfg.Branch)
			if err := cfg.git(w, "checkout", "-b", cfg.Branch, "origin/"+cfg.Branch); err != nil {
				return out, err
			}
			out.Adopted = true
		} else {
			fmt.Fprintf(w, "==> stale remote branch %s found (no open PR); force-resetting to %s\n", cfg.Branch, cfg.BaseBranch)
			if err := fresh(); err != nil {
				return out, err
			}
			if err := Publish(cfg, w); err != nil {
				return out, fmt.Errorf("publishing reset branch failed on %s; concurrent Box may be ahead: %w", cfg.Branch, err)
			}
		}
	}

	fmt.Fprintf(w, "==> rebasing %s onto latest %s\n", cfg.Branch, base)
	// Any rebase failure is handed on as a conflict, as the shell did: the
	// caller's resolve pass checks whether a rebase is actually in progress.
	out.Conflict = cfg.git(w, "rebase", base) != nil
	// Only the adoption path rewrote history already on the remote. A
	// conflict defers publication until the resolve pass has run.
	if out.Adopted && !out.Conflict {
		fmt.Fprintf(w, "==> publishing rebased %s\n", cfg.Branch)
		if err := Publish(cfg, w); err != nil {
			return out, fmt.Errorf("publishing rebased branch failed after pre-work rebase on %s: %w", cfg.Branch, err)
		}
	}
	return out, nil
}

// Publish lands the just-rebased branch so a later step never sees the stale
// pre-rebase state: a lease-guarded force-push when the Box is push-capable,
// else the outbox bundle (issue #1808), which no-ops with no commits ahead.
func Publish(cfg Config, w io.Writer) error {
	if cfg.Push {
		return cfg.git(w, "push", "--force-with-lease", "--force-if-includes", "origin", cfg.Branch)
	}
	return bundleout.Run(bundleout.Config{
		Repo:      cfg.WorkDir,
		Base:      "origin/" + cfg.BaseBranch,
		Branch:    cfg.Branch,
		OutboxDir: cfg.OutboxDir,
	}, w)
}

// seedRemoteReflog gives a cloned origin/<branch> the reflog entry clone never
// writes. --force-if-includes stops its walk of the local reflog at the
// remote-tracking ref's newest entry, and with no entry that cutoff is an
// uninitialized stack value (git remote.c is_reachable_in_reflog), so the push
// is refused at random — reliably on aarch64. The seed predates every local
// branch entry, so the walk always reaches them.
func (cfg Config) seedRemoteReflog(w io.Writer) error {
	ref := "refs/remotes/origin/" + cfg.Branch
	if cfg.git(io.Discard, "reflog", "exists", ref) == nil {
		return nil
	}
	cmd := exec.Command("git", "rev-parse", "--verify", "--quiet", ref)
	cmd.Dir = cfg.WorkDir
	tip, err := cmd.Output()
	if err != nil {
		return nil // no remote branch, so no lease to check
	}
	oid := strings.TrimSpace(string(tip))
	return cfg.git(w, "reflog", "write", ref, oid, oid, "branchrecovery: observed at clone")
}

func (cfg Config) git(w io.Writer, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = cfg.WorkDir
	cmd.Stdout = w
	cmd.Stderr = w
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %v: %w", args, err)
	}
	return nil
}

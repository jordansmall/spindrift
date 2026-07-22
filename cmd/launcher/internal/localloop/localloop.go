// Package localloop assembles CODE_FORGE=local's per-issue wiring — Code
// Forge construction, outbox resolution, parent resolution, and the
// reconcile/surface hookup — behind one Wire constructor (issue #1806,
// campaign #1803 T1), so the launcher's command path and the composed loop
// test drive the exact same composition instead of two independently
// maintained copies of it.
package localloop

import (
	"errors"
	"fmt"
	"io"
	"os"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/local"
)

// Config carries the subset of launcher config Wire needs to construct
// CODE_FORGE=local's per-issue Code Forge instances and surface completed
// broad tickets.
type Config struct {
	// AccumulationRepoDir is the bare Accumulation repo's host path (ADR
	// 0033).
	AccumulationRepoDir string
	// BaseBranch is the operator's real base branch — what
	// SeedAccumulationRepo seeds the Accumulation repo with, distinct from
	// any parent's Integration branch.
	BaseBranch string
	// GitUserName/GitUserEmail configure the merge commit identity a local
	// Code Forge's Merge creates.
	GitUserName, GitUserEmail string
	// BranchPrefix is baked into each per-issue Code Forge's AgentBranch
	// output.
	BranchPrefix string
}

// Wired bundles one Config + IssueTracker's resolved local-loop wiring —
// returned by Wire, and the seam both the launcher's command path and the
// composed loop test call.
type Wired struct {
	cfg Config
	it  forge.IssueTracker
}

// Wire returns cfg and it's resolved local-loop wiring.
func Wire(cfg Config, it forge.IssueTracker) *Wired {
	return &Wired{cfg: cfg, it: it}
}

// ResolveParent resolves num's own Integration-branch key through it: its
// parent: frontmatter, sanitized, or its own slug when unset (local.
// ResolveParent, issue #1734) — logged rather than silent on a lookup
// failure. A package-level function, not a Wired method, since resolving a
// parent needs only an IssueTracker, not a Config. Shared by every caller
// (BASE_BRANCH forwarding, per-issue Code Forge construction, surface
// grouping), so the diagnostic names the operation, not any one caller.
func ResolveParent(it forge.IssueTracker, num string) string {
	iss, err := it.Issue(num)
	if err != nil {
		fmt.Printf("!! localloop: resolving issue %s's parent: %v; falling back to its own slug\n", num, err)
		return local.ResolveParent(num, "")
	}
	return local.ResolveParent(num, iss.Parent)
}

// ResolveParent resolves num's own Integration-branch key through w's own
// IssueTracker (see the package-level ResolveParent).
func (w *Wired) ResolveParent(num string) string {
	return ResolveParent(w.it, num)
}

// CodeForgeForIssue returns num's own CodeForge instance, keyed to its
// resolved parent's Integration branch (ADR 0033, issue #1734) — a mixed-
// parent batch merges each seam through its own resolved instance, never a
// single shared one.
func (w *Wired) CodeForgeForIssue(num string) forge.CodeForge {
	return local.NewLocalCodeForge(w.cfg.AccumulationRepoDir, w.cfg.BaseBranch, w.ResolveParent(num), w.cfg.GitUserName, w.cfg.GitUserEmail, w.cfg.BranchPrefix)
}

// OutboxDir resolves num to its Box's writable outbox directory, read via
// os.Getwd() rather than a threaded pwd so every construction site (test and
// production) sees the process's own working directory at call time — a
// Getwd failure is surprising and worth a loud diagnostic, but degrades
// safely (RelayBundle then reports it as a missing bundle and the seam
// blocks, same as any other bundle-relay failure) rather than panicking.
func (w *Wired) OutboxDir(num string) string {
	pwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "==> outbox dir: os.Getwd failed: %v\n", err)
		return ""
	}
	return dispatch.OutboxDirFor(pwd, num)
}

// Surface surfaces every completed broad ticket's Integration branch into
// pwd as a local branch named after its resolved parent, once every one of
// its seam issues is closed — CODE_FORGE=local's auto-surface exit (ADR
// 0033, issue #1730). Each issue keys its own broad ticket from its own
// parent: frontmatter, or its own slug when unset (ResolveParent), so a
// mixed-parent batch may complete several broad tickets in the same sweep —
// this iterates every distinct resolved parent among the tracker's issues
// instead of a single run-wide parent. It is a no-op for a tracker with no
// SeamLister surface (every tracker but local); a resolved parent with any
// seam still open is skipped, not an error.
func (w *Wired) Surface(pwd string, out io.Writer) error {
	sl, ok := w.it.(forge.SeamLister)
	if !ok {
		return nil
	}
	issues, err := sl.AllIssues()
	if err != nil {
		return fmt.Errorf("surface: list issues: %w", err)
	}
	groups := map[string][]forge.Issue{}
	var order []string
	for _, iss := range issues {
		parent := local.ResolveParent(iss.Number, iss.Parent)
		if _, seen := groups[parent]; !seen {
			order = append(order, parent)
		}
		groups[parent] = append(groups[parent], iss)
	}
	var errs []error
	neverLanded := 0
	for _, parent := range order {
		allClosed := true
		for _, s := range groups[parent] {
			if s.State != forge.IssueClosed {
				allClosed = false
				break
			}
		}
		if !allClosed {
			continue
		}
		surfaced, skipped, err := local.SurfaceIntegrationBranch(w.cfg.AccumulationRepoDir, pwd, parent)
		if err != nil {
			// Recorded, not returned immediately: one parent's genuine
			// surface failure must not stop the sweep from attempting every
			// other completed broad ticket in the same batch.
			errs = append(errs, fmt.Errorf("surface %s: %w", parent, err))
			continue
		}
		if skipped != "" {
			// The "never landed" reason is the expected, permanent shape for
			// any closed parentless issue that never went through
			// CODE_FORGE=local (issue #1739): as a tracker's closed-issue
			// history grows, printing one line per such parent on every
			// sweep, forever, drowns out the two other skip reasons
			// (checked-out / diverged) that are transient and operator-
			// actionable. Those still print individually below; this one
			// collapses into a single end-of-sweep count instead.
			if skipped == local.NeverLandedSkip(parent) {
				neverLanded++
				continue
			}
			fmt.Fprintf(out, "surface: %s skipped — %s\n", parent, skipped)
			continue
		}
		if surfaced {
			fmt.Fprintf(out, "surface: broad ticket %s complete — %s's Integration branch is ready in the checkout as local branch %q.\n",
				parent, local.IntegrationBranch(parent), parent)
		}
	}
	if neverLanded > 0 {
		fmt.Fprintf(out, "surface: %d broad ticket(s) skipped — no seam has landed yet\n", neverLanded)
	}
	return errors.Join(errs...)
}

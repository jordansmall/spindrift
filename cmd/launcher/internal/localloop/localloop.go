// Package localloop assembles CODE_FORGE=local's per-issue wiring (Code Forge
// construction, outbox resolution, parent resolution, and the reconcile/surface
// hookup) behind one Wire constructor, so the launcher's command path and the
// composed loop test drive the same composition (issue #1806, campaign #1803).
package localloop

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/local"
)

// Config carries the launcher settings Wire needs to construct per-issue Code
// Forge instances and surface completed broad tickets.
type Config struct {
	// AccumulationRepoDir is the bare Accumulation repo's host path (ADR 0033).
	AccumulationRepoDir string
	// BaseBranch is the operator's real base branch, what SeedAccumulationRepo
	// seeds the Accumulation repo with, distinct from any parent's Integration
	// branch.
	BaseBranch string
	// GitUserName and GitUserEmail are the identity on the merge commits a local
	// Code Forge's Merge creates.
	GitUserName, GitUserEmail string
	// BranchPrefix prefixes each per-issue Code Forge's AgentBranch output.
	BranchPrefix string
}

// Wired is one Config and IssueTracker's resolved local-loop wiring.
type Wired struct {
	cfg Config
	it  forge.IssueTracker

	mu       sync.Mutex
	resolved map[string]local.SanitizedParent
}

// Wire builds the local-loop wiring from cfg and an issue tracker.
func Wire(cfg Config, it forge.IssueTracker) *Wired {
	return &Wired{cfg: cfg, it: it, resolved: map[string]local.SanitizedParent{}}
}

// ResolveParent resolves num's own Integration-branch key from its parent:
// frontmatter, sanitized, or from its own slug when that is unset (issue
// #1734). A failed lookup logs rather than falling back silently.
func ResolveParent(it forge.IssueTracker, num string) local.SanitizedParent {
	iss, err := it.Issue(num)
	if err != nil {
		fmt.Printf("!! localloop: resolving issue %s's parent: %v; falling back to its own slug\n", num, err)
		return local.ResolveParent(num, "")
	}
	return local.ResolveParent(num, iss.Parent)
}

// SeedScopeOf resolves num to the opaque forge.SeedScope its blocker gate is
// checked against under CODE_FORGE=local (issue #2150). The dispatch command
// path and the Console both consume this seam, so they cannot disagree about
// which blocker landing gates a dependent.
func SeedScopeOf(it forge.IssueTracker, num string) forge.SeedScope {
	return seedScopeFor(ResolveParent(it, num))
}

// SeedScopeOf is the package SeedScopeOf over w's memoized parent cache.
func (w *Wired) SeedScopeOf(num string) forge.SeedScope {
	return seedScopeFor(w.ResolveParent(num))
}

// seedScopeFor is the single NewSeedScope construction site, so the package
// SeedScopeOf and its memoized method stay in lockstep.
func seedScopeFor(p local.SanitizedParent) forge.SeedScope {
	return forge.NewSeedScope(p.String(), local.IntegrationBranch(p))
}

// SeedScopeResolver returns the waves.Config.SeedScopeOf resolver for the local
// blocker gate (#2130, #2150). It is non-nil only when caps carries a
// LandingContainmentQuery handle (issue #2946); every other forge has no
// seed-branch containment gate and judges a blocker by its PR/issue state alone.
func SeedScopeResolver(it forge.IssueTracker, caps forge.Capabilities) func(string) forge.SeedScope {
	if caps.LandingContainmentQuery == nil {
		return nil
	}
	return func(num string) forge.SeedScope { return SeedScopeOf(it, num) }
}

// ResolveParent resolves num's own Integration-branch key through w's
// IssueTracker, memoized so every caller sharing w reuses one resolved value
// (issue #1810). w.mu serializes every call, including each cache miss's own
// it.Issue lookup, which keeps dispatch's concurrent BASE_BRANCH resolution
// across Boxes safe at the cost of a little concurrency.
func (w *Wired) ResolveParent(num string) local.SanitizedParent {
	return w.cached(num, func() local.SanitizedParent { return ResolveParent(w.it, num) })
}

// cached returns num's memoized parent, computing and storing it via resolve on
// a cache miss. Surface populates the same cache through it from an issue it
// already holds, avoiding a second read of a file it just fetched.
func (w *Wired) cached(num string, resolve func() local.SanitizedParent) local.SanitizedParent {
	w.mu.Lock()
	defer w.mu.Unlock()
	if p, ok := w.resolved[num]; ok {
		return p
	}
	p := resolve()
	w.resolved[num] = p
	return p
}

// CodeForgeForIssue returns num's own CodeForge instance, keyed to its resolved
// parent's Integration branch (ADR 0033, issue #1734), so a mixed-parent batch
// merges each seam through its own instance and never a single shared one.
func (w *Wired) CodeForgeForIssue(num string) forge.CodeForge {
	return local.NewLocalCodeForge(w.cfg.AccumulationRepoDir, w.cfg.BaseBranch, w.ResolveParent(num), w.cfg.GitUserName, w.cfg.GitUserEmail, w.cfg.BranchPrefix)
}

// OutboxDir resolves num to its Box's writable outbox directory. It reads
// os.Getwd rather than a threaded pwd so every construction site sees the
// process's own working directory at call time. A Getwd failure degrades to an
// empty path, which RelayBundle reports as a missing bundle and the seam blocks.
func (w *Wired) OutboxDir(num string) string {
	pwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "==> outbox dir: os.Getwd failed: %v\n", err)
		return ""
	}
	return dispatch.OutboxDirFor(pwd, num)
}

// seamGroup bundles one broad ticket's member seams for Surface's grouping
// pass. Surface derives the surfaced branch name from title only when the group
// is parentless; a parented ticket keeps ADR 0033's sanitized-parent name
// (issue #1811).
type seamGroup struct {
	issues     []forge.Issue
	parentless bool
	title      string
}

// Surface surfaces every completed broad ticket's Integration branch into pwd
// as a local branch, once all of its seam issues are closed (ADR 0033, issues
// #1730, #1811). A mixed-parent batch may complete several tickets in one
// sweep, so this iterates every distinct resolved parent, printing one Verdict
// line per ticket touched. stuck maps an issue number to its stuck branch name.
func (w *Wired) Surface(pwd string, out io.Writer, stuck map[string]string, caps forge.Capabilities) error {
	if caps.SeamLister == nil {
		return nil
	}
	issues, err := caps.SeamLister.AllIssues()
	if err != nil {
		return fmt.Errorf("surface: list issues: %w", err)
	}
	groups := map[local.SanitizedParent]*seamGroup{}
	var order []local.SanitizedParent
	for _, iss := range issues {
		// w.cached, not w.ResolveParent: iss.Parent is already in hand from
		// AllIssues, so a cache miss resolves from it directly instead of
		// re-fetching the issue file, and still populates the shared cache.
		parent := w.cached(iss.Number, func() local.SanitizedParent { return local.ResolveParent(iss.Number, iss.Parent) })
		g, seen := groups[parent]
		if !seen {
			order = append(order, parent)
			g = &seamGroup{}
			groups[parent] = g
		}
		g.issues = append(g.issues, iss)
	}
	// A broad ticket whose own key collides with its group's key is a member of
	// its own group, not one of its seams. Dropping it before parentless, title,
	// and SeamCount are derived keeps those correct with no filtering of their
	// own (issue #3439). Scoped to the collision: a three-level chain's middle
	// issue resolves into its grandparent's group and must keep gating it.
	for _, parent := range order {
		g := groups[parent]
		var kept []forge.Issue
		for _, iss := range g.issues {
			if local.ResolveParent(iss.Number, "") == parent {
				continue
			}
			kept = append(kept, iss)
		}
		// When every member collides (two issue filenames sanitizing to the same
		// token, "foo bar.md" and "foo-bar.md"), drop none rather than surface a
		// group with zero real seams left.
		if len(kept) > 0 {
			g.issues = kept
		}
		// local.SanitizeParent, not a bare Parent == "" check: a parent: value
		// made entirely of non-[a-z0-9] characters sanitizes to empty, and
		// ResolveParent already treats that as unset (ADR 0033, issue #1734).
		// g.issues[0] carries no ordering requirement: a parentless member of
		// group P has slug P, so a non-degenerate exclusion above already dropped it.
		g.parentless = local.SanitizeParent(g.issues[0].Parent) == ""
		g.title = g.issues[0].Title
	}
	var errs []error
	neverLanded := 0
	for _, parent := range order {
		v, err := w.verdictFor(pwd, parent, groups[parent], stuck)
		if err != nil {
			// Recorded, not returned immediately: one parent's surface failure
			// must not stop the sweep from attempting every other completed
			// broad ticket in the same batch.
			errs = append(errs, fmt.Errorf("surface %s: %w", parent, err))
			continue
		}
		// A closed parentless issue that never went through CODE_FORGE=local
		// holds permanently (issue #1739), so this reason alone collapses into
		// one end-of-sweep count instead of drowning out actionable ones.
		if v.Kind == VerdictHeld && v.Held == local.NeverLandedSkip(parent) {
			neverLanded++
			continue
		}
		fmt.Fprintln(out, v)
	}
	if neverLanded > 0 {
		fmt.Fprintf(out, "surface: %d broad ticket(s) skipped — no seam has landed yet\n", neverLanded)
	}
	return errors.Join(errs...)
}

// verdictFor builds parent's Verdict: held on the group's first still-open seam,
// naming a known-stuck branch when stuck has one, else the outcome of surfacing
// its Integration branch. A parentless group surfaces under its sanitized title,
// falling back to parent's own slug when that sanitizes empty (issue #1811).
func (w *Wired) verdictFor(pwd string, parent local.SanitizedParent, g *seamGroup, stuck map[string]string) (Verdict, error) {
	for _, s := range g.issues {
		if s.State == forge.IssueClosed {
			continue
		}
		if branch, ok := stuck[s.Number]; ok {
			return Verdict{Parent: parent, Kind: VerdictHeld,
				Held: fmt.Sprintf("stuck landing — branch %s not merged into %s", branch, local.IntegrationBranch(parent))}, nil
		}
		return Verdict{Parent: parent, Kind: VerdictHeld, Held: "open seam #" + s.Number}, nil
	}

	branchName := parent.String()
	if g.parentless {
		if sanitized := local.SanitizeParent(g.title); sanitized != "" {
			branchName = sanitized
		}
	}
	_, skipped, err := local.SurfaceIntegrationBranch(w.cfg.AccumulationRepoDir, pwd, parent, branchName)
	if err != nil {
		return Verdict{}, err
	}
	if skipped != "" {
		return Verdict{Parent: parent, Kind: VerdictHeld, Held: skipped}, nil
	}
	return Verdict{Parent: parent, Kind: VerdictSurfaced, Branch: branchName, SeamCount: len(g.issues)}, nil
}

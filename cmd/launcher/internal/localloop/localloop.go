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
	"sync"

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

	mu       sync.Mutex
	resolved map[string]local.SanitizedParent
}

// Wire returns cfg and it's resolved local-loop wiring.
func Wire(cfg Config, it forge.IssueTracker) *Wired {
	return &Wired{cfg: cfg, it: it, resolved: map[string]local.SanitizedParent{}}
}

// ResolveParent resolves num's own Integration-branch key through it: its
// parent: frontmatter, sanitized, or its own slug when unset (local.
// ResolveParent, issue #1734) — logged rather than silent on a lookup
// failure. A package-level function, not a Wired method, since resolving a
// parent needs only an IssueTracker, not a Config. Shared by every caller
// (BASE_BRANCH forwarding, per-issue Code Forge construction, surface
// grouping), so the diagnostic names the operation, not any one caller.
func ResolveParent(it forge.IssueTracker, num string) local.SanitizedParent {
	iss, err := it.Issue(num)
	if err != nil {
		fmt.Printf("!! localloop: resolving issue %s's parent: %v; falling back to its own slug\n", num, err)
		return local.ResolveParent(num, "")
	}
	return local.ResolveParent(num, iss.Parent)
}

// ResolveParent resolves num's own Integration-branch key through w's own
// IssueTracker (see the package-level ResolveParent), memoized so num's
// parent is resolved exactly once per Wired: CodeForgeForIssue, Surface, and
// any external caller sharing w (e.g. the launcher's BASE_BRANCH forwarding)
// all consume that one resolved value instead of independently re-deriving
// it (issue #1810). Safe under dispatch's concurrent BASE_BRANCH resolution
// across Boxes: w.mu serializes every call, including each cache miss's own
// it.Issue() lookup, trading a little concurrency for a lock this simple.
func (w *Wired) ResolveParent(num string) local.SanitizedParent {
	return w.cached(num, func() local.SanitizedParent { return ResolveParent(w.it, num) })
}

// cached returns num's memoized parent, computing and storing it via resolve
// on a cache miss. Factored out of ResolveParent so Surface can populate the
// same cache from an issue it already has in hand (rawParent straight off
// AllIssues' result) instead of resolve's it.Issue(num) re-fetching a file
// Surface just read.
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

// seamGroup bundles one broad ticket's member seams for Surface's grouping
// pass: its seam issues in tracker order, whether it is parentless (its own
// broad ticket, keyed on its own slug — local.ResolveParent), and — only
// when parentless — the title Surface derives its surfaced branch name from
// (issue #1811). A parented ticket keeps ADR 0033's sanitized-parent name
// unchanged, so title is unused for it.
type seamGroup struct {
	issues     []forge.Issue
	parentless bool
	title      string
}

// Surface surfaces every completed broad ticket's Integration branch into
// pwd as a local branch, once every one of its seam issues is closed —
// CODE_FORGE=local's auto-surface exit (ADR 0033, issue #1730). Each issue
// keys its own broad ticket from its own parent: frontmatter, or its own
// slug when unset (ResolveParent), so a mixed-parent batch may complete
// several broad tickets in the same sweep — this iterates every distinct
// resolved parent among the tracker's issues instead of a single run-wide
// parent. It prints exactly one Verdict line per broad ticket it touches —
// surfaced or held, naming the first unmet gate — so no touched ticket is
// ever silent (issue #1811); stuck maps an issue number to its stuck
// LandingBranchRef branch name (reconcile.Result.Stuck), letting a held
// ticket's gate read "stuck landing" instead of the generic "open seam"
// without Surface redoing reconcile's own ancestry check. It is a no-op for
// a tracker with no SeamLister surface (every tracker but local).
func (w *Wired) Surface(pwd string, out io.Writer, stuck map[string]string) error {
	sl, ok := w.it.(forge.SeamLister)
	if !ok {
		return nil
	}
	issues, err := sl.AllIssues()
	if err != nil {
		return fmt.Errorf("surface: list issues: %w", err)
	}
	groups := map[local.SanitizedParent]*seamGroup{}
	var order []local.SanitizedParent
	for _, iss := range issues {
		// w.cached, not w.ResolveParent: iss.Parent is already in hand from
		// AllIssues above, so resolving from it directly (matching the
		// package-level ResolveParent's own it.Issue+sanitize shape) avoids
		// re-fetching the issue file a second time on a cache miss, while
		// still sharing and populating the same memoized value CodeForgeForIssue
		// and any other caller of w.ResolveParent(iss.Number) will reuse.
		parent := w.cached(iss.Number, func() local.SanitizedParent { return local.ResolveParent(iss.Number, iss.Parent) })
		g, seen := groups[parent]
		if !seen {
			order = append(order, parent)
			// local.SanitizeParent, not a bare iss.Parent == "" check: a
			// parent: value made entirely of non-[a-z0-9] characters
			// sanitizes to empty too, and ResolveParent already treats that
			// the same as unset — its own broad ticket, keyed on its own
			// slug (ADR 0033, issue #1734) — so title-derived naming must
			// recognize it the same way.
			g = &seamGroup{parentless: local.SanitizeParent(iss.Parent) == "", title: iss.Title}
			groups[parent] = g
		}
		g.issues = append(g.issues, iss)
	}
	var errs []error
	neverLanded := 0
	for _, parent := range order {
		v, err := w.verdictFor(pwd, parent, groups[parent], stuck)
		if err != nil {
			// Recorded, not returned immediately: one parent's genuine
			// surface failure must not stop the sweep from attempting every
			// other completed broad ticket in the same batch.
			errs = append(errs, fmt.Errorf("surface %s: %w", parent, err))
			continue
		}
		// The "never landed" reason is the expected, permanent shape for any
		// closed parentless issue that never went through CODE_FORGE=local
		// (issue #1739): as a tracker's closed-issue history grows, printing
		// one line per such parent on every sweep, forever, drowns out every
		// other, operator-actionable held reason. It alone collapses into a
		// single end-of-sweep count instead of Verdict's usual one-line-per-
		// ticket rendering.
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

// verdictFor builds parent's Verdict: held on the group's first still-open
// seam (naming a known-stuck LandingBranchRef specifically, else the seam
// generically), else the outcome of actually surfacing its Integration
// branch — surfaced under g's title-derived name when g is parentless
// (sanitized the same ref-safe way as a parent, falling back to parent's own
// slug when the title sanitizes empty), or under parent unchanged otherwise
// (ADR 0033, issue #1811).
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

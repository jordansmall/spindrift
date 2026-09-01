// Package localloop assembles CODE_FORGE=local's per-issue wiring — Code Forge
// construction, outbox resolution, parent resolution, and the
// reconcile/surface hookup — behind one Wire constructor, so the launcher's
// command path and the composed loop test drive the exact same composition.
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
// CODE_FORGE=local's per-issue Code Forges and surface completed broad tickets.
type Config struct {
	// AccumulationRepoDir is the bare Accumulation repo's host path (ADR 0033).
	AccumulationRepoDir string
	// BaseBranch is the operator's real base branch, distinct from any
	// parent's Integration branch — what SeedAccumulationRepo seeds with.
	BaseBranch string
	// GitUserName/GitUserEmail are the merge commit identity Merge creates.
	GitUserName, GitUserEmail string
	// BranchPrefix is baked into each per-issue Code Forge's AgentBranch.
	BranchPrefix string
}

// Wired bundles one Config + IssueTracker's resolved local-loop wiring — the
// seam both the launcher's command path and the composed loop test call.
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
// parent: frontmatter, sanitized, or its own slug when unset — logged rather
// than silent on a lookup failure. A package-level function, not a Wired
// method, since resolving a parent needs only an IssueTracker, not a Config;
// the diagnostic therefore names the operation, not any one caller.
func ResolveParent(it forge.IssueTracker, num string) local.SanitizedParent {
	iss, err := it.Issue(num)
	if err != nil {
		fmt.Printf("!! localloop: resolving issue %s's parent: %v; falling back to its own slug\n", num, err)
		return local.ResolveParent(num, "")
	}
	return local.ResolveParent(num, iss.Parent)
}

// SeedScopeOf resolves num to the opaque forge.SeedScope its blocker gate is
// checked against under CODE_FORGE=local. The single seam both the dispatch
// command path and the Console consume, so the two can never disagree about
// which blocker landing gates a dependent.
func SeedScopeOf(it forge.IssueTracker, num string) forge.SeedScope {
	return seedScopeFor(ResolveParent(it, num))
}

// SeedScopeOf resolves num's dependent SeedScope through w's memoized
// ResolveParent — the Wired-scoped twin of the package SeedScopeOf.
func (w *Wired) SeedScopeOf(num string) forge.SeedScope {
	return seedScopeFor(w.ResolveParent(num))
}

// seedScopeFor builds the opaque SeedScope for an already-resolved parent —
// the single NewSeedScope construction site both SeedScopeOf and
// (*Wired).SeedScopeOf share, so the two stay in lockstep.
func seedScopeFor(p local.SanitizedParent) forge.SeedScope {
	return forge.NewSeedScope(p.String(), local.IntegrationBranch(p))
}

// SeedScopeResolver returns the waves.Config.SeedScopeOf resolver for the
// local blocker gate: a dependent num -> the opaque forge.SeedScope its
// blocker gate is checked against. Non-nil only when caps'
// LandingContainmentQuery handle is set; nil for every other forge, where the
// seed-branch containment gate never fires and a blocker is judged solely by
// its PR/issue state.
func SeedScopeResolver(it forge.IssueTracker, caps forge.Capabilities) func(string) forge.SeedScope {
	if caps.LandingContainmentQuery == nil {
		return nil
	}
	return func(num string) forge.SeedScope { return SeedScopeOf(it, num) }
}

// ResolveParent resolves num's own Integration-branch key through w's own
// IssueTracker, memoized so num's parent is resolved exactly once per Wired
// and every caller sharing w consumes that one value. Safe under dispatch's
// concurrent BASE_BRANCH resolution across Boxes: w.mu serializes every call,
// including each cache miss's own it.Issue() lookup, trading a little
// concurrency for a lock this simple.
func (w *Wired) ResolveParent(num string) local.SanitizedParent {
	return w.cached(num, func() local.SanitizedParent { return ResolveParent(w.it, num) })
}

// cached returns num's memoized parent, computing and storing it via resolve
// on a cache miss. Factored out of ResolveParent so Surface can populate the
// same cache from an issue it already has in hand, instead of resolve's
// it.Issue(num) re-fetching a file Surface just read.
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
// resolved parent's Integration branch (ADR 0033) — a mixed-parent batch
// merges each seam through its own instance, never a single shared one.
func (w *Wired) CodeForgeForIssue(num string) forge.CodeForge {
	return local.NewLocalCodeForge(w.cfg.AccumulationRepoDir, w.cfg.BaseBranch, w.ResolveParent(num), w.cfg.GitUserName, w.cfg.GitUserEmail, w.cfg.BranchPrefix)
}

// OutboxDir resolves num to its Box's writable outbox directory, read via
// os.Getwd() rather than a threaded pwd so every construction site sees the
// process's own working directory at call time. A Getwd failure is surprising
// enough to warrant a loud diagnostic but degrades safely: RelayBundle then
// reports a missing bundle and the seam blocks, rather than panicking.
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
// broad ticket, keyed on its own slug), and — only when parentless — the title
// Surface derives its surfaced branch name from. A parented ticket keeps
// ADR 0033's sanitized-parent name, so title is unused for it.
type seamGroup struct {
	issues     []forge.Issue
	parentless bool
	title      string
}

// Surface surfaces every completed broad ticket's Integration branch into pwd
// as a local branch, once every one of its seam issues is closed —
// CODE_FORGE=local's auto-surface exit (ADR 0033). Each issue keys its own
// broad ticket from its own parent, so a mixed-parent batch may complete
// several broad tickets in one sweep. Exactly one Verdict line is printed per
// broad ticket touched — surfaced or held, naming the first unmet gate — so no
// touched ticket is ever silent. stuck maps an issue number to its stuck
// LandingBranchRef branch name, letting a held ticket read "stuck landing"
// without redoing reconcile's ancestry check. A no-op for a tracker with no
// SeamLister surface (every tracker but local).
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
		// AllIssues, so resolving from it directly avoids re-fetching the issue
		// file on a cache miss while still populating the same memoized value
		// every other w.ResolveParent caller reuses.
		parent := w.cached(iss.Number, func() local.SanitizedParent { return local.ResolveParent(iss.Number, iss.Parent) })
		g, seen := groups[parent]
		if !seen {
			order = append(order, parent)
			// local.SanitizeParent, not a bare iss.Parent == "" check: a
			// parent: value made entirely of non-[a-z0-9] characters sanitizes
			// to empty too, and ResolveParent already treats that as unset, so
			// title-derived naming must recognize it the same way (ADR 0033).
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
			// Recorded, not returned immediately: one parent's surface failure
			// must not stop the sweep attempting the rest of the batch.
			errs = append(errs, fmt.Errorf("surface %s: %w", parent, err))
			continue
		}
		// "never landed" is the expected, permanent shape for any closed
		// parentless issue that never went through CODE_FORGE=local, and a
		// tracker's closed-issue history only grows — one line per such parent
		// on every sweep, forever, would drown out every operator-actionable
		// held reason. It alone collapses into an end-of-sweep count.
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
// generically), else the outcome of surfacing its Integration branch —
// surfaced under g's title-derived name when g is parentless (sanitized the
// same ref-safe way as a parent, falling back to parent's own slug when the
// title sanitizes empty), or under parent unchanged otherwise (ADR 0033).
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

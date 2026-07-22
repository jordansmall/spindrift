// Package reconcile implements ADR 0029's reconcile sweep: the sole
// authority that closes a local issue, reflecting Code Forge reality (a
// merged landing PR) into the local issue's closed: axis. It is
// observational — it never lands code.
package reconcile

import (
	"fmt"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/local"
)

// Result reports what a Run swept.
type Result struct {
	// Closed lists the issue numbers Run closed this sweep, in the order
	// ListOpenIssues returned them.
	Closed []string
	// Abandoned lists the issue numbers Run flagged abandoned this sweep —
	// their recorded landing PR was closed without merging.
	Abandoned []string
	// Reset lists the issue numbers Run reset from InProgress to
	// Dispatchable this sweep, in the order ListIssues returned them.
	Reset []string
}

// LivenessProbe is reconcile's injected death-signal seam (#600, ADR 0029):
// whether an InProgress issue's Box is still alive. Run never touches
// os.Stat or the container runtime itself — every liveness fact comes
// through this seam, so it is fakeable in tests.
type LivenessProbe interface {
	// LogStale reports whether issue num's Box log has gone stale beyond
	// reconcile's threshold — the log-side half of the death signal.
	LogStale(num string) bool
	// ContainerLive reports whether issue num's Box container/sandbox is
	// currently running. reachable is false when the container runtime
	// itself could not be queried (e.g. the runtime is unreachable
	// on-host); Run treats that as no evidence of a live container, not as
	// proof of one, so it never blocks a reset on an unreachable runtime.
	ContainerLive(num string) (live, reachable bool)
}

// Run sweeps every open issue it reports: an issue carrying a recorded
// landing whose PR (per cf's PRForge surface) has merged is closed; one with
// no landing, or whose landing PR is still open, is left untouched. Against a
// CODE_FORGE=local Code Forge — no PR concept at all — Run instead checks
// each recorded landing through cf's LandingVerifier surface (ADR 0033) and
// closes only once that reports the landing merged into the adapter's own
// Integration branch, no network call either way. Run never merges, opens,
// or pushes — cf is queried read-only and it is only ever transitioned to
// closed.
//
// Run is a no-op, not an error, when it has no IssueCloser surface (every
// tracker but local) or cf has neither a PRForge nor a LandingVerifier
// surface — there is nothing to check or nowhere to write in either case.
//
// After closing, Run sweeps every InProgress issue and resets it to
// Dispatchable when lp reports the composite death signal: no PR (in any
// state — open, closed, or merged) exists for its agent branch, its Box log
// is stale, and (when the container runtime is reachable) its Box container
// is absent. This qualifies #600: a bare InProgress label is never enough to
// reset on its own, only the composite evidence from lp is. This sweep is
// PRForge-specific — a local Code Forge has no PR/branch signal to key an
// orphan reset off, so Run skips it entirely when cf has no PRForge surface.
func Run(it forge.IssueTracker, cf forge.CodeForge, lp LivenessProbe) (Result, error) {
	closer, ok := it.(forge.IssueCloser)
	if !ok {
		return Result{}, nil
	}
	pr, hasPR := cf.(forge.PRForge)
	verifier, hasVerifier := cf.(forge.LandingVerifier)
	if !hasPR && !hasVerifier {
		return Result{}, nil
	}
	lr, _ := it.(forge.LandingRecorder)
	flagger, _ := it.(forge.AbandonedFlagger)
	repair, _ := cf.(forge.LandingRepair)

	issues, err := it.ListOpenIssues()
	if err != nil {
		return Result{}, fmt.Errorf("reconcile: list open issues: %w", err)
	}

	var res Result
	prc := prReconciler{closer: closer, pr: pr, cf: cf, lr: lr, flagger: flagger}
	llc := localLandingReconciler{closer: closer, verifier: verifier, repair: repair, lr: lr}
	for _, iss := range issues {
		if hasPR {
			if err := prc.reconcile(&res, iss); err != nil {
				return res, err
			}
			continue
		}
		if err := llc.reconcile(&res, iss); err != nil {
			return res, err
		}
	}

	if !hasPR {
		return res, nil
	}
	inProgress, err := it.ListIssues(forge.InProgress)
	if err != nil {
		return res, fmt.Errorf("reconcile: list in-progress issues: %w", err)
	}
	for _, iss := range inProgress {
		orphaned, err := isOrphaned(pr, cf, lp, iss.Number)
		if err != nil {
			return res, fmt.Errorf("reconcile issue %s: liveness check: %w", iss.Number, err)
		}
		if !orphaned {
			continue
		}
		if err := it.TransitionState(iss.Number, forge.InProgress, forge.Dispatchable); err != nil {
			return res, fmt.Errorf("reconcile issue %s: reset: %w", iss.Number, err)
		}
		res.Reset = append(res.Reset, iss.Number)
	}
	return res, nil
}

// prReconciler bundles the seams reconcile's remote-PR path needs per issue —
// grouped into one value so passing them through Run's per-issue loop isn't
// a five-plus-parameter argument list.
type prReconciler struct {
	closer  forge.IssueCloser
	pr      forge.PRForge
	cf      forge.CodeForge
	lr      forge.LandingRecorder
	flagger forge.AbandonedFlagger
}

// reconcile checks a single open issue against the PRForge's live PR state,
// closing it on a merged landing PR, discovering an unrecorded landing by
// agent branch, and flagging an abandoned issue whose landing PR closed
// unmerged — the remote-PR half of Run's per-issue sweep, unchanged from
// before Run also supported LandingVerifier's no-PR path.
func (p prReconciler) reconcile(res *Result, iss forge.Issue) error {
	landing := iss.Landing
	if landing == "" {
		if p.lr == nil {
			return nil
		}
		url, found, err := p.pr.PRForBranch(p.cf.AgentBranch(iss.Number))
		if err != nil {
			return fmt.Errorf("reconcile issue %s: resolve branch PR: %w", iss.Number, err)
		}
		if !found {
			return nil
		}
		if err := p.lr.RecordLanding(iss.Number, url); err != nil {
			return fmt.Errorf("reconcile issue %s: record landing: %w", iss.Number, err)
		}
		landing = url
	}
	state, err := p.pr.PRState(landing)
	if err != nil {
		return fmt.Errorf("reconcile issue %s: PR state for %s: %w", iss.Number, landing, err)
	}
	switch state {
	case forge.PRMerged:
		if err := p.closer.CloseIssue(iss.Number); err != nil {
			return fmt.Errorf("reconcile issue %s: close: %w", iss.Number, err)
		}
		res.Closed = append(res.Closed, iss.Number)
	case forge.PRClosed:
		if p.flagger == nil || iss.Abandoned {
			return nil
		}
		if err := p.flagger.FlagAbandoned(iss.Number); err != nil {
			return fmt.Errorf("reconcile issue %s: flag abandoned: %w", iss.Number, err)
		}
		res.Abandoned = append(res.Abandoned, iss.Number)
	}
	return nil
}

// localLandingReconciler bundles the seams reconcile's local-landing path
// needs per issue (mirroring prReconciler for the PRForge path). repair is
// nil for a Code Forge with no forge.LandingRepair surface (every adapter
// but local, though (localLandingReconciler).reconcile's caller never
// reaches that case today since Run only takes this path when cf implements
// LandingVerifier, which only local does too) — a LandingBranchRef then
// prints a loud "no repair surface" line rather than the pre-#1809 silent
// no-op, since there is no ancestor check to run.
type localLandingReconciler struct {
	closer   forge.IssueCloser
	verifier forge.LandingVerifier
	repair   forge.LandingRepair
	lr       forge.LandingRecorder
}

// reconcile checks a single open issue's recorded landing, parsed into its
// typed forge.Landing (issue #1809) so this switches on meaning instead of
// re-deriving the string grammar itself:
//
//   - LandingIntegrationRef (the post-merge form) is checked via
//     LandingVerifier exactly as before: merged closes the issue through the
//     normal close path, not-yet-merged (a conflicting land, ADR 0033) leaves
//     it open, blocked — there is no separate "blocked" axis to set.
//   - LandingBranchRef (settle's pre-merge record) is Reconcile's healing
//     path: BranchMergedIntoIntegration checks it against the ticket's own
//     Integration branch. An ancestor means the merge landed but the
//     post-merge upgrade never ran — repair upgrades the recorded landing to
//     the rich IntegrationRef form and closes the seam through the same
//     normal close path a fresh merge would have. Not an ancestor prints a
//     loud stuck verdict naming the branch (issue #1809: the silent
//     stuck-open cluster this replaces) and leaves the issue open.
//   - Any other shape (e.g. a PR URL reaching this local-only path) prints a
//     distinct, loud "unverifiable" line rather than being silently folded
//     into "not merged yet".
func (l localLandingReconciler) reconcile(res *Result, iss forge.Issue) error {
	if iss.Landing == "" {
		return nil
	}
	landing, err := forge.ParseLanding(iss.Landing)
	if err != nil {
		fmt.Printf("    #%s  landing=%s  status=landing-unverifiable  !! %v\n", iss.Number, iss.Landing, err)
		return nil
	}
	switch landing.Kind {
	case forge.LandingIntegrationRef:
		merged, err := l.verifier.VerifyLanding(iss.Landing)
		if err != nil {
			return fmt.Errorf("reconcile issue %s: verify landing %s: %w", iss.Number, iss.Landing, err)
		}
		if !merged {
			return nil
		}
		return l.close(res, iss.Number)
	case forge.LandingBranchRef:
		return l.reconcileBranchRef(res, iss, landing)
	default:
		fmt.Printf("    #%s  landing=%s  status=landing-unverifiable  !! landing does not verify through the local Code Forge\n", iss.Number, iss.Landing)
		return nil
	}
}

// reconcileBranchRef is reconcile's healing path for a LandingBranchRef —
// see (localLandingReconciler).reconcile's doc for the full behavior.
func (l localLandingReconciler) reconcileBranchRef(res *Result, iss forge.Issue, landing forge.Landing) error {
	if l.repair == nil {
		fmt.Printf("    #%s  landing=%s  status=landing-unverifiable  !! Code Forge has no repair surface to check branch %s against\n", iss.Number, iss.Landing, landing.Branch)
		return nil
	}
	parent := local.ResolveParent(iss.Number, iss.Parent)
	merged, err := l.repair.BranchMergedIntoIntegration(landing.Branch, parent)
	if err != nil {
		return fmt.Errorf("reconcile issue %s: check branch %s ancestry: %w", iss.Number, landing.Branch, err)
	}
	if !merged {
		fmt.Printf("    #%s  landing=%s  status=stuck  !! branch %s not merged into %s\n", iss.Number, iss.Landing, landing.Branch, local.IntegrationBranch(parent))
		return nil
	}
	if l.lr == nil {
		// No LandingRecorder to persist the upgrade through: closing anyway
		// would leave the issue closed with a stale BranchRef forever, worse
		// than leaving it open for a later sweep with a working tracker.
		// Unreachable today (LocalTracker, the only IssueTracker this path
		// ever runs against, always implements LandingRecorder).
		fmt.Printf("    #%s  landing=%s  status=landing-unverifiable  !! branch %s merged but no LandingRecorder to persist the repaired landing\n", iss.Number, iss.Landing, landing.Branch)
		return nil
	}
	tip, err := l.repair.IntegrationTip(parent)
	if err != nil {
		return fmt.Errorf("reconcile issue %s: resolve integration tip for %s: %w", iss.Number, parent, err)
	}
	if err := l.lr.RecordLanding(iss.Number, tip); err != nil {
		return fmt.Errorf("reconcile issue %s: record repaired landing: %w", iss.Number, err)
	}
	fmt.Printf("    #%s  landing=%s  status=landing-repaired  repaired-landing=%s\n", iss.Number, iss.Landing, tip)
	return l.close(res, iss.Number)
}

// close closes num through the normal close path and records it in res —
// shared by both the fresh-merge and the healing-repair close.
func (l localLandingReconciler) close(res *Result, num string) error {
	if err := l.closer.CloseIssue(num); err != nil {
		return fmt.Errorf("reconcile issue %s: close: %w", num, err)
	}
	res.Closed = append(res.Closed, num)
	return nil
}

// isOrphaned reports whether num's InProgress issue shows the full
// composite death signal: no PR of any state for its agent branch, no
// branch pushed for it either, a stale Box log, and — only when the
// container runtime answered — no live container. A PR of any state (not
// just open/merged) counts as evidence a runner touched this branch, so a
// closed-unmerged PR withholds the reset rather than silently re-dispatching
// what a human or CI already rejected; flagging that case as abandoned is a
// separate reconcile concern. The bare branch check catches the narrower
// die-after-push-before-PR window a PR-only check would miss.
func isOrphaned(pr forge.PRForge, cf forge.CodeForge, lp LivenessProbe, num string) (bool, error) {
	branch := cf.AgentBranch(num)
	if _, found, err := pr.PRForBranch(branch); err != nil {
		return false, err
	} else if found {
		return false, nil
	}
	if exists, err := cf.BranchExists(branch); err != nil {
		return false, err
	} else if exists {
		return false, nil
	}
	if !lp.LogStale(num) {
		return false, nil
	}
	if live, reachable := lp.ContainerLive(num); reachable && live {
		return false, nil
	}
	return true, nil
}

// Package reconcile implements ADR 0029's reconcile sweep: the sole
// authority that closes a local issue, reflecting Code Forge reality (a
// merged landing PR) into the local issue's closed: axis. It is
// observational — it never lands code.
package reconcile

import (
	"fmt"

	"spindrift.dev/launcher/internal/forge"
)

// Result reports what a Run swept.
type Result struct {
	// Closed lists what Run closed, in ListOpenIssues' order.
	Closed []string
	// Abandoned lists issues whose landing PR closed without merging.
	Abandoned []string
	// Reset lists issues Run moved InProgress -> Dispatchable, in
	// ListIssues' order.
	Reset []string
	// Stuck maps an open issue's number to its recorded LandingBranchRef
	// branch name when the healing path found it not yet merged into the
	// ticket's Integration branch — nil when Run found no such issue. Surface
	// reads this to name a held gate "stuck landing" instead of the generic
	// "open seam", without redoing the ancestry check itself.
	Stuck map[string]string
}

// LivenessProbe is reconcile's injected death-signal seam (ADR 0029):
// whether an InProgress issue's Box is still alive. Run never touches
// os.Stat or the container runtime itself — every liveness fact arrives
// through this seam.
type LivenessProbe interface {
	// LogStale reports whether issue num's Box log has gone stale beyond
	// reconcile's threshold — the log-side half of the death signal.
	LogStale(num string) bool
	// ContainerLive reports whether issue num's Box container/sandbox is
	// running. reachable is false when the runtime could not be queried;
	// Run treats that as no evidence of a live container, not proof of one,
	// so an unreachable runtime never blocks a reset.
	ContainerLive(num string) (live, reachable bool)
}

// Run sweeps every open issue it reports: one carrying a recorded landing
// whose PR has merged is closed; one with no landing, or whose landing PR is
// still open, is left untouched. Against a CODE_FORGE=local Code Forge — no
// PR concept at all — Run instead checks each recorded landing through cf's
// LandingContainmentQuery surface (ADR 0033) and closes only once that
// reports it contained in the Integration branch, with no network call
// either way. Run never merges, opens, or pushes: cf is queried read-only
// and issues are only ever transitioned to closed.
//
// Run is a no-op, not an error, when it has no IssueCloser surface or cf has
// neither a PRForge nor a LandingContainmentQuery surface.
//
// After closing, Run resets an InProgress issue to Dispatchable only on the
// full composite death signal: no PR in any state for its agent branch, a
// stale Box log, and (when the runtime is reachable) no Box container. A
// bare InProgress label is never enough on its own. This sweep needs a
// PRForge, so Run skips it entirely when cf has none.
//
// scopeFor resolves an issue number to its broad ticket's opaque
// forge.SeedScope (ADR 0033); taking it as a callback keeps reconcile from
// importing forge/local. Unused outside the local healing/discovery path.
// caps carries it's and cf's resolved optional-interface surfaces, resolved
// once by the caller and threaded through every consumer.
func Run(it forge.IssueTracker, cf forge.CodeForge, lp LivenessProbe, caps forge.Capabilities, scopeFor func(num string) forge.SeedScope) (Result, error) {
	closer := caps.IssueCloser
	if closer == nil {
		return Result{}, nil
	}
	pr := caps.PRForge
	container := caps.LandingContainmentQuery
	if pr == nil && container == nil {
		return Result{}, nil
	}
	lr := caps.LandingRecorder
	flagger := caps.AbandonedFlagger
	repair := caps.LandingRepair

	issues, err := it.ListOpenIssues()
	if err != nil {
		return Result{}, fmt.Errorf("reconcile: list open issues: %w", err)
	}

	var res Result
	prc := prReconciler{closer: closer, pr: pr, cf: cf, lr: lr, flagger: flagger}
	llc := localLandingReconciler{closer: closer, container: container, repair: repair, lr: lr, cf: cf, scopeFor: scopeFor}
	for _, iss := range issues {
		if pr != nil {
			if err := prc.reconcile(&res, iss); err != nil {
				return res, err
			}
			continue
		}
		if err := llc.reconcile(&res, iss); err != nil {
			return res, err
		}
	}

	if pr == nil {
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

// prReconciler bundles the seams reconcile's remote-PR path needs per issue.
type prReconciler struct {
	closer  forge.IssueCloser
	pr      forge.PRForge
	cf      forge.CodeForge
	lr      forge.LandingRecorder
	flagger forge.AbandonedFlagger
}

// reconcile checks a single open issue against the PRForge's live PR state:
// closing it on a merged landing PR, discovering an unrecorded landing by
// agent branch, and flagging an issue whose landing PR closed unmerged.
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
// needs per issue, mirroring prReconciler. repair is nil for a Code Forge
// with no forge.LandingRepair surface, in which case a LandingBranchRef
// prints a loud "no repair surface" line rather than silently no-oping.
type localLandingReconciler struct {
	closer    forge.IssueCloser
	container forge.LandingContainmentQuery
	repair    forge.LandingRepair
	lr        forge.LandingRecorder
	cf        forge.CodeForge
	scopeFor  func(num string) forge.SeedScope
}

// reconcile checks a single open issue's recorded landing, parsed into a
// typed forge.Landing so this switches on meaning, not string grammar:
//
//   - No recorded landing discovers one by agent branch (see discover).
//   - LandingIntegrationRef (post-merge) closes the issue once
//     LandingContained reports it contained; not-yet-contained (a
//     conflicting land, ADR 0033) leaves it open and blocked — there is no
//     separate "blocked" axis to set.
//   - LandingBranchRef (settle's pre-merge record) is the healing path.
//     Contained means the merge landed but the post-merge upgrade never
//     ran, so repair rewrites the landing to the IntegrationRef form and
//     closes normally. Not contained prints a stuck verdict naming the
//     branch and leaves the issue open.
//   - Any other shape prints a loud "unverifiable" line rather than folding
//     silently into "not merged yet".
func (l localLandingReconciler) reconcile(res *Result, iss forge.Issue) error {
	if iss.Landing == "" {
		return l.discover(res, iss)
	}
	landing, err := forge.ParseLanding(iss.Landing)
	if err != nil {
		fmt.Printf("    #%s  landing=%s  status=landing-unverifiable  !! %v\n", iss.Number, iss.Landing, err)
		return nil
	}
	switch landing.Kind {
	case forge.LandingIntegrationRef:
		contained, err := l.container.LandingContained(landing, l.scopeFor(iss.Number))
		if err != nil {
			return fmt.Errorf("reconcile issue %s: check landing %s containment: %w", iss.Number, iss.Landing, err)
		}
		if !contained {
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
	scope := l.scopeFor(iss.Number)
	contained, err := l.container.LandingContained(landing, scope)
	if err != nil {
		return fmt.Errorf("reconcile issue %s: check branch %s containment: %w", iss.Number, landing.Branch, err)
	}
	if !contained {
		fmt.Printf("    #%s  landing=%s  status=stuck  !! branch %s not merged into %s's integration branch\n", iss.Number, iss.Landing, landing.Branch, scope.Parent())
		if res.Stuck == nil {
			res.Stuck = map[string]string{}
		}
		res.Stuck[iss.Number] = landing.Branch
		return nil
	}
	if l.lr == nil {
		// No LandingRecorder to persist the upgrade through: closing anyway
		// would leave the issue closed with a stale BranchRef forever, worse
		// than leaving it open for a later sweep with a working tracker.
		fmt.Printf("    #%s  landing=%s  status=landing-unverifiable  !! branch %s merged but no LandingRecorder to persist the repaired landing\n", iss.Number, iss.Landing, landing.Branch)
		return nil
	}
	tip, err := l.repair.IntegrationTip(scope.Parent())
	if err != nil {
		return fmt.Errorf("reconcile issue %s: resolve integration tip for %s: %w", iss.Number, scope.Parent(), err)
	}
	if err := l.lr.RecordLanding(iss.Number, tip); err != nil {
		return fmt.Errorf("reconcile issue %s: record repaired landing: %w", iss.Number, err)
	}
	fmt.Printf("    #%s  landing=%s  status=landing-repaired  repaired-landing=%s\n", iss.Number, iss.Landing, tip)
	return l.close(res, iss.Number)
}

// discover is the local-forge counterpart of prReconciler's branch-discovery
// fallback: an issue with no recorded landing (the box died before its
// outcome line was parsed) is checked by wrapping its agent branch as a raw
// BranchRef Landing. It stays silent when there is no repair surface or the
// branch isn't contained yet — the common case. Contained records the
// resolved IntegrationTip and closes through the normal close path.
func (l localLandingReconciler) discover(res *Result, iss forge.Issue) error {
	if l.lr == nil || l.repair == nil {
		return nil
	}
	branch := l.cf.AgentBranch(iss.Number)
	landing := forge.Landing{Kind: forge.LandingBranchRef, Branch: branch}
	scope := l.scopeFor(iss.Number)
	contained, err := l.container.LandingContained(landing, scope)
	if err != nil {
		return fmt.Errorf("reconcile issue %s: check discovered branch %s containment: %w", iss.Number, branch, err)
	}
	if !contained {
		return nil
	}
	tip, err := l.repair.IntegrationTip(scope.Parent())
	if err != nil {
		return fmt.Errorf("reconcile issue %s: resolve integration tip for %s: %w", iss.Number, scope.Parent(), err)
	}
	if err := l.lr.RecordLanding(iss.Number, tip); err != nil {
		return fmt.Errorf("reconcile issue %s: record discovered landing: %w", iss.Number, err)
	}
	fmt.Printf("    #%s  landing=%s  status=landing-discovered  discovered-landing=%s\n", iss.Number, branch, tip)
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
// branch pushed either, a stale Box log, and — only when the runtime
// answered — no live container. A PR in *any* state counts as evidence a
// runner touched this branch, so a closed-unmerged PR withholds the reset
// rather than re-dispatching what a human or CI already rejected. The bare
// branch check catches the die-after-push-before-PR window.
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

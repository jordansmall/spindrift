// Package reconcile implements ADR 0029's reconcile sweep: the sole authority
// that closes a local issue once Code Forge reality (a merged landing PR)
// says the work landed. The sweep only observes, it never lands code.
package reconcile

import (
	"fmt"

	"spindrift.dev/launcher/internal/forge"
)

// Result reports what a Run swept.
type Result struct {
	// Closed holds the numbers closed this sweep, in ListOpenIssues order.
	Closed []string
	// Abandoned holds the numbers whose landing PR closed without merging.
	Abandoned []string
	// Reset holds the numbers moved from InProgress back to Dispatchable,
	// in ListIssues order.
	Reset []string
	// Stuck maps an open issue's number to its recorded branch when the
	// healing path's ancestry check (issue #1809) found that branch not yet
	// merged into the ticket's integration branch. Surface (issue #1811)
	// reads it instead of redoing the ancestry check.
	Stuck map[string]string
}

// LivenessProbe reports whether an InProgress issue's Box is still alive
// (#600, ADR 0029). Every liveness fact reaches Run through this seam, which
// never touches os.Stat or the container runtime, so tests can fake it.
type LivenessProbe interface {
	// LogStale reports whether num's Box log has gone stale beyond
	// reconcile's threshold.
	LogStale(num string) bool
	// ContainerLive reports whether num's Box container is running.
	// reachable is false when the container runtime could not be queried at
	// all, which Run reads as no evidence of a live container rather than
	// proof of one, so an unreachable runtime never blocks a reset.
	ContainerLive(num string) (live, reachable bool)
}

// Run closes every open issue whose recorded landing has merged, queried
// read-only through caps (issue #2946): PRForge, or LandingContainmentQuery on
// a local Code Forge with no PR (ADR 0033, issue #2151). It then resets a dead
// InProgress issue (#600), skipped on local, which has no PR to key that off.
// With no IssueCloser or query seam, Run returns empty, not an error.
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

// prReconciler bundles the seams the remote-PR path needs per issue.
type prReconciler struct {
	closer  forge.IssueCloser
	pr      forge.PRForge
	cf      forge.CodeForge
	lr      forge.LandingRecorder
	flagger forge.AbandonedFlagger
}

// reconcile checks one open issue against live PR state: it closes the issue
// on a merged landing PR, discovers an unrecorded landing by agent branch, and
// flags one whose landing PR closed unmerged.
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

// localLandingReconciler bundles the seams the local-landing path needs per
// issue. A nil repair (a Code Forge without forge.LandingRepair) leaves no
// ancestor check to run, so a branch ref prints a loud "no repair surface"
// line rather than the silent no-op it got before issue #1809. scopeFor is a
// caller-supplied callback so reconcile imports no adapter (issue #1819).
type localLandingReconciler struct {
	closer    forge.IssueCloser
	container forge.LandingContainmentQuery
	repair    forge.LandingRepair
	lr        forge.LandingRecorder
	cf        forge.CodeForge
	scopeFor  func(num string) forge.SeedScope
}

// reconcile checks one open issue's recorded landing, parsed into a typed
// forge.Landing (issue #1809) so the switch reads meaning, not string grammar:
// no landing discovers one by agent branch, an integration ref closes the
// issue once contained (issue #2151), a branch ref takes the healing path, and
// any other shape prints a loud unverifiable line, never a not-merged-yet pass.
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

// reconcileBranchRef heals a LandingBranchRef: a branch already contained in
// the ticket's integration branch means the merge landed but the post-merge
// upgrade never ran, so repair rewrites the record to the integration-ref form
// and closes the seam. A branch not contained prints a stuck verdict naming it
// and leaves the issue open (issue #1809).
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
		// Closing with no LandingRecorder to persist the upgrade would strand
		// the issue closed on a stale BranchRef forever, so leave it open for
		// a later sweep. Unreachable while LocalTracker, the only tracker on
		// this path, implements LandingRecorder.
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

// discover is the local-forge counterpart of the PR branch-discovery fallback
// (issue #2151): when an issue has no recorded landing, because the Box died
// before the launcher parsed its outcome line, discover checks the agent
// branch for containment instead. It stays silent when no repair seam can
// persist the result, or when the branch is not contained, the common case.
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

func (l localLandingReconciler) close(res *Result, num string) error {
	if err := l.closer.CloseIssue(num); err != nil {
		return fmt.Errorf("reconcile issue %s: close: %w", num, err)
	}
	res.Closed = append(res.Closed, num)
	return nil
}

// isOrphaned reports the full composite death signal for num: no PR in any
// state for its agent branch, no pushed branch, a stale Box log, and, only
// when the container runtime answered, no live container. A closed unmerged PR
// withholds the reset rather than re-dispatching rejected work; the branch
// check catches the die-after-push-before-PR window a PR-only check misses.
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

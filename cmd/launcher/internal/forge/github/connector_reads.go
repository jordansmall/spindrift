package github

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/connectorforge"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectorrecord"
	"spindrift.dev/launcher/internal/connectorruntime"
	"spindrift.dev/launcher/internal/connectortracker"
	"spindrift.dev/launcher/internal/forge"
)

// errNoCapability marks a Connector whose handshake lacks a capability a read
// needs.
var errNoCapability = errors.New("connector does not declare the capability")

// routedRow is the github backend row of rt, when it holds one.
func routedRow(rt *connectorruntime.Runtime) (backendrows.Row, bool) {
	return rt.Row(backend.GitHub.Name)
}

// ErrNoRoute is wrapped by every call that has no Connector row to go through,
// and by NewLedger at construction (the tracker and Code Forge build without a
// row and fail per call). The Connector is github's only implementation, so
// the call fails rather than falling back to gh.
var ErrNoRoute = errors.New("github has no Connector route")

// routeRow is routedRow for a call that needs one: the routed row, or
// ErrNoRoute naming which of the two causes left none.
func routeRow(rt *connectorruntime.Runtime) (backendrows.Row, error) {
	if row, ok := routedRow(rt); ok {
		return row, nil
	}
	if host := Host(); !strings.EqualFold(host, DefaultHost) {
		return backendrows.Row{}, fmt.Errorf("%w: GH_HOST=%s is not %s and the github Connector's routes pin api.github.com, so GitHub Enterprise is not served", ErrNoRoute, host, DefaultHost)
	}
	return backendrows.Row{}, fmt.Errorf("%w: no github backend row in the runtime; import spindrift.flakeModules.default (the root module), not core's module directly", ErrNoRoute)
}

// noteRouted records the error a routed read or write returned and returns
// it unchanged.
func noteRouted(op string, err error) error {
	return connectorrecord.Note(backend.GitHub.Name, op, err)
}

// connectorOp runs fn over e's session with the routed row, and records a
// failure under a routed row. A Connector lacking what the operation needs (the
// missing sentinel, or a capability fn names through errNoCapability) is
// unsupported, never a reason to fall back to gh. A viaRead, or trackerOp's
// capability check, can hold a Connector another call then kills and see
// connectorhost.ErrUnusable from it; the next call reloads.
func (e *execClient) connectorOp(row backendrows.Row, missing error, verb string, fn func(row backendrows.Row, spec connectorruntime.SessionSpec) error) error {
	routed, spec, err := e.sessionSpec(row)
	if err == nil {
		err = unsupportedWithout(fn(routed, spec), verb, missing, errNoCapability)
	}
	return noteRouted(verb, err)
}

// sessionSpec is the session both axes open, and row narrowed to the
// repository's routes. github tolerates a missing capability per operation (it
// is unsupported), so nothing is required at load.
func (e *execClient) sessionSpec(row backendrows.Row) (backendrows.Row, connectorruntime.SessionSpec, error) {
	if e.connectors == nil {
		return row, connectorruntime.SessionSpec{}, connectorruntime.ErrNoRuntime
	}
	row, target, err := routeTarget(row, e.repo)
	if err != nil {
		return row, connectorruntime.SessionSpec{}, err
	}
	return row, connectorruntime.SessionSpec{
		Target:  target,
		Record:  backend.GitHub.Name,
		Options: connectorOptions,
		// connectorOp notes each failed operation by its own verb, and some guest
		// failures are benign to it (an already-deleted branch).
		NotesOperations: true,
	}, nil
}

// requireRemoteOnGHHost returns the host of the row's gitRemote, failing closed
// when that host is not GH_HOST's. Core derives the clone URL and PR web host
// from the remote, but the row's Routes and gate see only GH_HOST, so a remote
// elsewhere would send git and the GH_TOKEN credential where the reads do not
// look; this runs before any credential read.
func requireRemoteOnGHHost(row backendrows.Row) (string, error) {
	u, err := url.Parse(row.GitRemote)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("%w: the %s backend row's gitRemote %q does not parse to a host", ErrNoRoute, row.Name, row.GitRemote)
	}
	if want := Host(); !strings.EqualFold(u.Host, want) {
		return "", fmt.Errorf("%w: the %s backend row's gitRemote %q is on host %q, not GH_HOST %q", ErrNoRoute, row.Name, row.GitRemote, u.Host, want)
	}
	return u.Host, nil
}

// routeTarget is row narrowed to repo's routes, and the Target on its first.
func routeTarget(row backendrows.Row, repo string) (backendrows.Row, connectortracker.Target, error) {
	if len(row.Routes) == 0 {
		return row, connectortracker.Target{}, fmt.Errorf("the %s backend row has no Route, so its API host is unknown", row.Name)
	}
	row.Routes = connectortracker.Routes(row.Routes, repo)
	return row, connectortracker.Target{API: "https://" + row.Routes[0].Host, Repo: repo}, nil
}

// memo returns *slot, building it under mu until built reports it set. A failed
// build stores nothing, so the next call builds again.
func memo[T any](mu *sync.Mutex, slot *T, built func() bool, build func() (T, error)) (T, error) {
	mu.Lock()
	defer mu.Unlock()
	var zero T
	if built() {
		return *slot, nil
	}
	v, err := build()
	if err != nil {
		return zero, err
	}
	*slot = v
	return v, nil
}

// trackerView is the tracker over the session, built by the first operation
// that needs it.
func (e *execClient) trackerView(row backendrows.Row, spec connectorruntime.SessionSpec) (forge.IssueTracker, error) {
	return memo(&e.mu, &e.tracker, func() bool { return e.tracker != nil }, func() (forge.IssueTracker, error) {
		return e.connectors.Tracker(row, connectorruntime.TrackerSpec{
			SessionSpec: spec,
			Lifecycle:   connectortracker.Lifecycle{Labels: e.labels, VerdictLabels: e.verdictLabels, Route: e.route, WebHost: Host()},
		})
	})
}

// readerView is the read-only PR forge view over the session, with its
// optional interfaces resolved. Its web host is the one the PR URLs carry
// (the row's Git remote host, the one writes use).
func (e *execClient) readerView(row backendrows.Row, spec connectorruntime.SessionSpec) (forge.CodeForge, forge.Capabilities, error) {
	r, err := memo(&e.mu, &e.reader, func() bool { return e.reader.cf != nil }, func() (readerForge, error) {
		host, err := requireRemoteOnGHHost(row)
		if err != nil {
			return readerForge{}, err
		}
		cf, err := e.connectors.ForgeReader(row, spec, host)
		if err != nil {
			return readerForge{}, err
		}
		return readerForge{cf: cf, caps: forge.ResolveCodeForge(cf)}, nil
	})
	return r.cf, r.caps, err
}

// coreTracker as trackerOp's capName names an operation of the core tracker
// contract, which every tracker Connector serves.
const coreTracker = ""

// trackerOp runs fn over the tracker view, which must serve the capability
// capName names (or coreTracker) and each of alsoCaps. A capability the
// Connector does not serve fails with errNoCapability.
func (e *execClient) trackerOp(row backendrows.Row, capName, verb string, fn func(forge.IssueTracker) error, alsoCaps ...string) error {
	return e.connectorOp(row, connectortracker.ErrNotTracker, verb, func(routed backendrows.Row, spec connectorruntime.SessionSpec) error {
		if caps := slices.DeleteFunc(append([]string{capName}, alsoCaps...), func(c string) bool { return c == coreTracker }); len(caps) > 0 {
			conn, err := e.connectors.Connector(routed, spec)
			if err != nil {
				return err
			}
			for _, c := range caps {
				if !slices.Contains(conn.Capabilities, c) {
					return fmt.Errorf("%w: %s", errNoCapability, c)
				}
			}
		}
		tr, err := e.trackerView(routed, spec)
		if err != nil {
			return err
		}
		return fn(tr)
	})
}

// trackerCapOp is trackerOp for an operation through the optional interface T
// that the capability capName puts on the tracker view.
func trackerCapOp[T any](e *execClient, row backendrows.Row, capName, verb string, fn func(T) error) error {
	return e.trackerOp(row, capName, verb, func(tr forge.IssueTracker) error {
		v, ok := forge.As[T](tr)
		if !ok {
			return fmt.Errorf("%w: %s", errNoCapability, capName)
		}
		return fn(v)
	})
}

// forgeRead runs fn over the read-only PR forge view, with its PR-side and
// optional-interface handles resolved; a read the Connector does not serve
// fails.
func (e *execClient) forgeRead(row backendrows.Row, verb string, fn func(forge.CodeForge, forge.Capabilities) error) error {
	return e.connectorOp(row, connectorforge.ErrNotForge, verb, func(routed backendrows.Row, spec connectorruntime.SessionSpec) error {
		cf, caps, err := e.readerView(routed, spec)
		if err != nil {
			return err
		}
		return fn(cf, caps)
	})
}

// prRead is forgeRead for the reads on forge.PRForge.
func (e *execClient) prRead(row backendrows.Row, verb string, fn func(forge.PRForge) error) error {
	return e.forgeRead(row, verb, func(_ forge.CodeForge, caps forge.Capabilities) error { return fn(caps.PRForge) })
}

// The via* helpers are the connectorOp, trackerOp, trackerCapOp, forgeRead and
// prRead entry points for a read that returns one value; the reads that return
// two (a PR and whether it was found) call those directly.

func viaRead[R any](e *execClient, row backendrows.Row, missing error, verb string, fn func(*connectorhost.Connector, connectorforge.Target) (R, error)) (v R, err error) {
	err = e.connectorOp(row, missing, verb, func(routed backendrows.Row, spec connectorruntime.SessionSpec) error {
		conn, cerr := e.connectors.Connector(routed, spec)
		if cerr != nil {
			return cerr
		}
		var rerr error
		v, rerr = fn(conn, connectorforge.Target{API: spec.Target.API, Repo: e.repo})
		return rerr
	})
	return v, err
}

func viaTracker[R any](e *execClient, row backendrows.Row, capName, verb string, fn func(forge.IssueTracker) (R, error)) (v R, err error) {
	err = e.trackerOp(row, capName, verb, func(tr forge.IssueTracker) (rerr error) {
		v, rerr = fn(tr)
		return rerr
	})
	return v, err
}

func viaTrackerCap[T, R any](e *execClient, row backendrows.Row, capName, verb string, fn func(T) (R, error)) (v R, err error) {
	err = trackerCapOp(e, row, capName, verb, func(t T) (rerr error) {
		v, rerr = fn(t)
		return rerr
	})
	return v, err
}

func viaForge[R any](e *execClient, row backendrows.Row, verb string, fn func(forge.CodeForge, forge.Capabilities) (R, error)) (v R, err error) {
	err = e.forgeRead(row, verb, func(cf forge.CodeForge, caps forge.Capabilities) (rerr error) {
		v, rerr = fn(cf, caps)
		return rerr
	})
	return v, err
}

func viaPR[R any](e *execClient, row backendrows.Row, verb string, fn func(forge.PRForge) (R, error)) (v R, err error) {
	err = e.prRead(row, verb, func(pf forge.PRForge) (rerr error) {
		v, rerr = fn(pf)
		return rerr
	})
	return v, err
}

func (e *execClient) probe(row backendrows.Row) (string, error) {
	slug, err := viaRead(e, row, connectorforge.ErrNoProbe, "resolve the repository", func(conn *connectorhost.Connector, target connectorforge.Target) (string, error) {
		return connectorforge.Probe(conn, target)
	})
	if err != nil {
		return "", classifyProbeErr(err, e.repo)
	}
	return slug, nil
}

func (e *execClient) branchProtected(row backendrows.Row, branch string) (bool, error) {
	return viaRead(e, row, connectorforge.ErrNoBranchProtection, "read branch protection", func(conn *connectorhost.Connector, target connectorforge.Target) (bool, error) {
		return connectorforge.BranchProtected(conn, target, branch)
	})
}

func (e *execClient) canAutoMerge(row backendrows.Row) (bool, error) {
	return viaRead(e, row, connectorforge.ErrNoAutoMerge, "read the auto-merge setting", func(conn *connectorhost.Connector, target connectorforge.Target) (bool, error) {
		return connectorforge.AutoMergeAllowed(conn, target)
	})
}

// countReady forwards fresh so the Connector can bypass any
// conditional-request cache it keeps.
func (e *execClient) countReady(row backendrows.Row, fresh bool) (int, error) {
	return viaTrackerCap(e, row, connectortracker.CapCount, "count demand", func(dc forge.DemandCounter) (int, error) { return dc.CountReady(fresh) })
}

func (e *execClient) priorClaimState(row backendrows.Row, num string) (state forge.DispatchState, found bool, err error) {
	err = trackerCapOp(e, row, connectortracker.CapRemovedLabels, "read an issue's removed labels", func(pr forge.PriorClaimStateReader) error {
		var rerr error
		state, found, rerr = pr.PriorClaimState(num)
		return rerr
	})
	return state, found, err
}

func (e *execClient) listIssues(row backendrows.Row, state forge.DispatchState) ([]forge.Issue, error) {
	return viaTracker(e, row, coreTracker, "list issues", func(tr forge.IssueTracker) ([]forge.Issue, error) { return tr.ListIssues(state) })
}

func (e *execClient) listOpenIssues(row backendrows.Row) ([]forge.Issue, error) {
	return viaTracker(e, row, coreTracker, "list open issues", func(tr forge.IssueTracker) ([]forge.Issue, error) { return tr.ListOpenIssues() })
}

func (e *execClient) issue(row backendrows.Row, num string) (forge.Issue, error) {
	return viaTracker(e, row, coreTracker, "read an issue", func(tr forge.IssueTracker) (forge.Issue, error) { return tr.Issue(num) })
}

func (e *execClient) openPRForBranch(row backendrows.Row, branch string) (pr forge.PR, found bool, err error) {
	err = e.prRead(row, "find a branch's open PR", func(pf forge.PRForge) error {
		var rerr error
		pr, found, rerr = pf.OpenPRForBranch(branch)
		return rerr
	})
	return pr, found, err
}

func (e *execClient) prForBranch(row backendrows.Row, branch string) (url string, found bool, err error) {
	err = e.prRead(row, "find a branch's PR", func(pf forge.PRForge) error {
		var rerr error
		url, found, rerr = pf.PRForBranch(branch)
		return rerr
	})
	return url, found, err
}

func (e *execClient) prState(row backendrows.Row, prURL string) (forge.PRState, error) {
	return viaPR(e, row, "read a PR's state", func(pf forge.PRForge) (forge.PRState, error) { return pf.PRState(prURL) })
}

func (e *execClient) checkState(row backendrows.Row, prURL string) (forge.RollupState, error) {
	return viaPR(e, row, "read a PR's check state", func(pf forge.PRForge) (forge.RollupState, error) { return pf.CheckState(prURL) })
}

func (e *execClient) failureDetail(row backendrows.Row, prURL string) (string, error) {
	return viaPR(e, row, "read a PR's failing checks", func(pf forge.PRForge) (string, error) { return pf.FailureDetail(prURL) })
}

// The tracker writes. Each fails with errNoCapability when the Connector lacks
// the primitive behind it; a transition or verdict is the transition layer,
// which needs the label-edit primitive for its stale-label strip.

func (e *execClient) transition(row backendrows.Row, num string, from, to forge.DispatchState) error {
	return e.trackerOp(row, connectortracker.CapTransition, "transition an issue", func(tr forge.IssueTracker) error {
		return tr.TransitionState(num, from, to)
	}, connectortracker.CapEditLabels)
}

func (e *execClient) completeVerdict(row backendrows.Row, num string, verdict forge.Verdict) error {
	return e.trackerOp(row, connectortracker.CapTransition, "settle a research verdict", func(tr forge.IssueTracker) error {
		return tr.CompleteVerdict(num, verdict)
	}, connectortracker.CapEditLabels)
}

func (e *execClient) addLabels(row backendrows.Row, num string, labels []string) error {
	return trackerCapOp(e, row, connectortracker.CapEditLabels, "add labels to an issue", func(l forge.IssueLabeler) error {
		return l.AddLabels(num, labels)
	})
}

func (e *execClient) comment(row backendrows.Row, num, body string) error {
	return trackerCapOp(e, row, connectortracker.CapComment, "comment on an issue", func(c forge.HostPostedCommenter) error {
		return c.Comment(num, body)
	})
}

func (e *execClient) closeMergedIssue(row backendrows.Row, num string) error {
	return trackerCapOp(e, row, connectortracker.CapCloseIssue, "close a merged issue", func(c forge.MergeCloser) error {
		return c.CloseMergedIssue(num)
	})
}

func (e *execClient) fileIssue(row backendrows.Row, title, body string, labels []string) (string, error) {
	return viaTrackerCap(e, row, connectortracker.CapFileIssue, "file an issue", func(f forge.HostPostedIssueFiler) (string, error) {
		return f.PostIssue(title, body, labels)
	})
}

func (e *execClient) createLabel(row backendrows.Row, name, description, color string) error {
	return e.trackerOp(row, connectortracker.CapLabels, "create a label", func(tr forge.IssueTracker) error {
		return tr.CreateLabel(name, description, color)
	})
}

func (e *execClient) listLabels(row backendrows.Row) ([]string, error) {
	return viaTracker(e, row, connectortracker.CapLabels, "list labels", func(tr forge.IssueTracker) ([]string, error) { return tr.ListLabels() })
}

// classifyProbeErr gives a failed probe the sentinels callers match on:
// anything but an auth failure, a rate limit or an unsupported Connector means
// the repository is unreachable.
func classifyProbeErr(err error, repoSlug string) error {
	if errors.Is(err, forge.ErrAuthFailure) || errors.Is(err, forge.ErrRateLimit) || errors.Is(err, backendkit.ErrUnsupported) {
		return err
	}
	return fmt.Errorf("%w: probe of %s: %w", forge.ErrRepoNotFound, repoSlug, err)
}

// unsupportedWithout maps an err matching any of missing, the sentinels of a
// Connector that does not declare the capability a read needs, to
// backendkit.ErrUnsupported, and passes any other error through: a refused read is
// a failed one, not an unsupported one.
func unsupportedWithout(err error, verb string, missing ...error) error {
	for _, m := range missing {
		if errors.Is(err, m) {
			return fmt.Errorf("the %s connector cannot %s: %w: %w", backend.GitHub.Name, verb, err, backendkit.ErrUnsupported)
		}
	}
	return err
}

func (e *execClient) nativeDeps(row backendrows.Row, num string) ([]string, error) {
	return viaTrackerCap(e, row, connectortracker.CapNativeDeps, "read an issue's native dependencies", func(nr forge.NativeDepsReader) ([]string, error) { return nr.NativeDepsOf(num) })
}

func (e *execClient) branchExists(row backendrows.Row, branch string) (bool, error) {
	return viaForge(e, row, "check a branch", func(cf forge.CodeForge, _ forge.Capabilities) (bool, error) { return cf.BranchExists(branch) })
}

func (e *execClient) mergeCommit(row backendrows.Row, prURL string) (string, error) {
	return viaForge(e, row, "read a PR's merge commit", func(_ forge.CodeForge, caps forge.Capabilities) (string, error) { return mergeCommitFrom(caps, prURL) })
}

// mergeCommitFrom reads the merge commit through the optional reader, which a
// forge view may lack.
func mergeCommitFrom(caps forge.Capabilities, prURL string) (string, error) {
	if caps.MergeCommitReader == nil {
		return "", fmt.Errorf("%w: merge commit read", errNoCapability)
	}
	return caps.MergeCommitReader.MergeCommit(prURL)
}

func (e *execClient) headCommitSHA(row backendrows.Row, prURL string) (string, error) {
	return viaPR(e, row, "read a PR's head commit", func(pf forge.PRForge) (string, error) { return pf.HeadCommitSHA(prURL) })
}

func (e *execClient) mergeable(row backendrows.Row, prURL string) (forge.MergeableState, error) {
	return viaPR(e, row, "read a PR's mergeability", func(pf forge.PRForge) (forge.MergeableState, error) { return pf.Mergeable(prURL) })
}

func (e *execClient) needsUpdate(row backendrows.Row, prURL string) (bool, error) {
	return viaPR(e, row, "read how far a PR is behind", func(pf forge.PRForge) (bool, error) { return pf.NeedsUpdate(prURL) })
}

func (e *execClient) listPRFiles(row backendrows.Row, prURL string) ([]string, error) {
	return viaPR(e, row, "list a PR's files", func(pf forge.PRForge) ([]string, error) { return pf.ListPRFiles(prURL) })
}

// checkRun reads the rollup state and the in-flight run link in one
// view; the forge view lacks the optional reporter when the Connector does not
// declare forge.ciRun.
func (e *execClient) checkRun(row backendrows.Row, prURL string) (state forge.RollupState, run string, err error) {
	err = e.forgeRead(row, "read a PR's CI run", func(_ forge.CodeForge, caps forge.Capabilities) error {
		if caps.CIRunReporter == nil {
			return fmt.Errorf("%w: CI run read", errNoCapability)
		}
		var rerr error
		state, run, rerr = caps.CIRunReporter.CheckRun(prURL)
		return rerr
	})
	return state, run, err
}

func (e *execClient) blocksOf(row backendrows.Row, num string) ([]forge.Dependency, error) {
	return viaTrackerCap(e, row, connectortracker.CapBlocks, "read the issues an issue blocks", func(bl forge.BlockersLister) ([]forge.Dependency, error) { return bl.BlocksOf(num) })
}

// matchesRoute gates on the route, not the handshake, here: an
// unrouted client matches every issue even on a Connector that lacks
// tracker.route, and a routed one is refused by the tracker view itself.
func (e *execClient) matchesRoute(row backendrows.Row, num string) (bool, error) {
	return viaTrackerCap(e, row, coreTracker, "match an issue against the route", func(rm forge.RouteMatcher) (bool, error) { return rm.MatchesRoute(num) })
}

func (e *execClient) searchIssues(row backendrows.Row, text string, state forge.IssueState, label string, limit int) ([]forge.Issue, error) {
	return viaTrackerCap(e, row, connectortracker.CapSearchIssues, "search issues", func(s forge.IssueSearcher) ([]forge.Issue, error) { return s.SearchIssues(text, state, label, limit) })
}

func (e *execClient) listIssuesWithLabels(row backendrows.Row, state forge.IssueState, labels []string) ([]forge.Issue, error) {
	return viaTrackerCap(e, row, connectortracker.CapLabeled, "scan labeled issues", func(l forge.LabeledBacklogLister) ([]forge.Issue, error) {
		return l.ListIssuesWithLabels(state, labels)
	})
}

// comments keeps the Connector's attestation that each comment's
// minimized state is faithful; the tracker view serves no comments without it.
func (e *execClient) comments(row backendrows.Row, num string) ([]forge.Comment, error) {
	return viaTrackerCap(e, row, connectortracker.CapComments, "read an issue's comments", func(cl forge.CommentLister) ([]forge.Comment, error) { return cl.Comments(num) })
}

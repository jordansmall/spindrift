package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/connectorforge"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectortracker"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/primaryrecord"
)

// loadRowConnector loads a backend row's Connector; a test seam.
var loadRowConnector = backendrows.Load

// errNoCapability marks a Connector whose handshake lacks a capability a read
// needs.
var errNoCapability = errors.New("connector does not declare the capability")

// sharedConnectors holds one loaded Connector per repository and row, so the
// doctor, the daemon's demand probe and recover share a compile. The row is
// process-global (backendrows.Use) and a Connector serialises its own calls.
// A Connector closed by a failed call is reloaded on the next read.
var sharedConnectors struct {
	mu       sync.Mutex
	loaded   map[string]*sharedConnector
	failures map[string]loadFailures
}

// A failed load is retried by the next read until maxLoadFailures have failed
// in a row; after that a Connector that cannot load is retried once per
// loadRetryAfter rather than recompiled on every read. The cache is shared
// with the long-lived daemon, so a transient failure must heal without a
// restart.
const (
	maxLoadFailures = 3
	loadRetryAfter  = time.Minute
)

// loadClock is a test seam for the load-retry cooldown.
var loadClock = time.Now

type sharedConnector struct {
	conn *connectorhost.Connector
	api  string
}

// loadFailures counts a key's consecutive failed loads and keeps the last error
// and when it happened.
type loadFailures struct {
	n    int
	err  error
	last time.Time
}

// sharedConnectorFor returns the Connector loaded for row and repoSlug, loading
// it on first use. It reads with the host's own credential.
func sharedConnectorFor(row backendrows.Row, repoSlug string) (*sharedConnector, error) {
	raw, err := json.Marshal([]any{repoSlug, row})
	if err != nil {
		return nil, err
	}
	key := string(raw)
	sharedConnectors.mu.Lock()
	defer sharedConnectors.mu.Unlock()
	if sc, ok := sharedConnectors.loaded[key]; ok {
		deadErr := sc.conn.Err()
		if deadErr == nil {
			return sc, nil
		}
		// Only a failed call leaves a closed Connector cached (closeSharedConnectors
		// deletes what it closes). The call that failed is not retried; this read
		// loads a fresh one.
		dropShared(key, sc)
		noteRouted("reload the Connector", deadErr)
	}
	if f := sharedConnectors.failures[key]; f.n >= maxLoadFailures && loadClock().Sub(f.last) < loadRetryAfter {
		return nil, fmt.Errorf("connector load failed %d times in a row; retried %s after the last failure: %w", f.n, loadRetryAfter, f.err)
	}
	conn, api, err := loadConnector(row, backendrows.Options{Client: connectorClient}, repoSlug)
	if err != nil {
		if sharedConnectors.failures == nil {
			sharedConnectors.failures = map[string]loadFailures{}
		}
		sharedConnectors.failures[key] = loadFailures{n: sharedConnectors.failures[key].n + 1, err: err, last: loadClock()}
		return nil, err
	}
	delete(sharedConnectors.failures, key)
	sc := &sharedConnector{conn: conn, api: api}
	if sharedConnectors.loaded == nil {
		sharedConnectors.loaded = map[string]*sharedConnector{}
	}
	sharedConnectors.loaded[key] = sc
	return sc, nil
}

// dropShared closes sc and forgets it. The caller holds sharedConnectors.mu.
func dropShared(key string, sc *sharedConnector) {
	_ = sc.conn.Close(context.Background())
	delete(sharedConnectors.loaded, key)
}

// closeSharedConnectors closes every shared Connector, for tests that swap the
// row under them.
func closeSharedConnectors() {
	sharedConnectors.mu.Lock()
	defer sharedConnectors.mu.Unlock()
	for k, sc := range sharedConnectors.loaded {
		dropShared(k, sc)
	}
	clear(sharedConnectors.failures)
}

// routedRow is the github backend row when one is configured.
func routedRow() (backendrows.Row, bool) {
	return backendrows.Lookup(backend.GitHub.Name)
}

// ErrNoRoute is wrapped by every call that has no Connector row to go through,
// and by NewLedger at construction (the tracker and Code Forge build without a
// row and fail per call). The Connector is github's only implementation, so
// the call fails rather than falling back to gh.
var ErrNoRoute = errors.New("github has no Connector route")

// routeRow is routedRow for a call that needs one: the routed row, or
// ErrNoRoute naming which of the two causes left none.
func routeRow() (backendrows.Row, error) {
	if row, ok := routedRow(); ok {
		return row, nil
	}
	if host := Host(); !strings.EqualFold(host, DefaultHost) {
		return backendrows.Row{}, fmt.Errorf("%w: GH_HOST=%s is not %s and the github Connector's routes pin api.github.com, so GitHub Enterprise is not served", ErrNoRoute, host, DefaultHost)
	}
	return backendrows.Row{}, fmt.Errorf("%w: no github backend row is applied; import spindrift.flakeModules.default (the root module), not core's module directly", ErrNoRoute)
}

// noteRouted records the error a routed read or write returned and returns
// it unchanged. A lost claim race records nothing: forge.ErrAlreadyClaimed is
// core policy, not a Connector fault, and an empty log is what marks a clean
// run.
func noteRouted(op string, err error) error {
	if errors.Is(err, forge.ErrAlreadyClaimed) {
		return err
	}
	return primaryrecord.Note(backend.GitHub.Name, op, err)
}

// connectorOp runs fn over the shared Connector for e's repository, and records
// a failure under a routed row. A Connector lacking what the operation needs
// (the missing sentinel, or a capability fn names through errNoCapability) is
// unsupported, never a reason to fall back to gh. Readers run concurrently, so
// one can hold a Connector another reader's call then kills and see
// connectorhost.ErrUnusable from it; the next read reloads.
func (e *execClient) connectorOp(row backendrows.Row, missing error, verb string, fn func(conn *connectorhost.Connector, api string) error) error {
	sc, err := sharedConnectorFor(row, e.repo)
	if err == nil {
		err = unsupportedWithout(fn(sc.conn, sc.api), verb, missing, errNoCapability)
	}
	return noteRouted(verb, err)
}

// coreTracker as trackerOp's capName names an operation of the core tracker
// contract, which every tracker Connector serves.
const coreTracker = ""

// trackerOp runs fn over a tracker view of conn that serves the capability
// capName names (or coreTracker) and each of alsoCaps. The view has no native
// tracker under it: a capability it does not serve must fail with
// errNoCapability, since nothing sits under the view for it to reach.
func (e *execClient) trackerOp(row backendrows.Row, capName, verb string, fn func(forge.IssueTracker) error, alsoCaps ...string) error {
	return e.connectorOp(row, connectortracker.ErrNotTracker, verb, func(conn *connectorhost.Connector, api string) error {
		for _, c := range append([]string{capName}, alsoCaps...) {
			if c != coreTracker && !slices.Contains(conn.Capabilities, c) {
				return fmt.Errorf("%w: %s", errNoCapability, c)
			}
		}
		lc := connectortracker.Lifecycle{Labels: e.labels, VerdictLabels: e.verdictLabels, Route: e.route}
		tr, err := connectortracker.New(conn, nil, lc, connectortracker.Target{API: api, Repo: e.repo})
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

// forgeRead runs fn over a PR forge view of conn, with its PR-side and
// optional-interface handles resolved. Its web host is the one the PR URLs carry
// (GH_HOST, else github.com); a read the Connector does not serve fails.
func (e *execClient) forgeRead(row backendrows.Row, verb string, fn func(forge.CodeForge, forge.Capabilities) error) error {
	return e.connectorOp(row, connectorforge.ErrNotForge, verb, func(conn *connectorhost.Connector, api string) error {
		cf, err := connectorforge.NewReader(conn, connectorforge.Target{API: api, Repo: e.repo}, Host())
		if err != nil {
			return err
		}
		return fn(cf, forge.ResolveCodeForge(cf))
	})
}

// prRead is forgeRead for the reads on forge.PRForge.
func (e *execClient) prRead(row backendrows.Row, verb string, fn func(forge.PRForge) error) error {
	return e.forgeRead(row, verb, func(_ forge.CodeForge, caps forge.Capabilities) error { return fn(caps.PRForge) })
}

// The via* helpers are the connectorOp, trackerOp, trackerCapOp, forgeRead and
// prRead entry points for a read that returns one value; the reads that return
// two (a PR and whether it was found) call those directly.

func viaRead[R any](e *execClient, row backendrows.Row, missing error, verb string, fn func(*connectorhost.Connector, string) (R, error)) (v R, err error) {
	err = e.connectorOp(row, missing, verb, func(conn *connectorhost.Connector, api string) (rerr error) {
		v, rerr = fn(conn, api)
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

func (e *execClient) probeViaConnector(row backendrows.Row) (string, error) {
	slug, err := viaRead(e, row, connectorforge.ErrNoProbe, "resolve the repository", func(conn *connectorhost.Connector, api string) (string, error) {
		return connectorforge.Probe(conn, connectorforge.Target{API: api, Repo: e.repo})
	})
	if err != nil {
		return "", classifyProbeErr(err, e.repo)
	}
	return slug, nil
}

func (e *execClient) branchProtectedViaConnector(row backendrows.Row, branch string) (bool, error) {
	return viaRead(e, row, connectorforge.ErrNoBranchProtection, "read branch protection", func(conn *connectorhost.Connector, api string) (bool, error) {
		return connectorforge.BranchProtected(conn, connectorforge.Target{API: api, Repo: e.repo}, branch)
	})
}

func (e *execClient) canAutoMergeViaConnector(row backendrows.Row) (bool, error) {
	return viaRead(e, row, connectorforge.ErrNoAutoMerge, "read the auto-merge setting", func(conn *connectorhost.Connector, api string) (bool, error) {
		return connectorforge.AutoMergeAllowed(conn, connectorforge.Target{API: api, Repo: e.repo})
	})
}

// countReadyViaConnector forwards fresh so the Connector can bypass any
// conditional-request cache it keeps.
func (e *execClient) countReadyViaConnector(row backendrows.Row, fresh bool) (int, error) {
	return viaTrackerCap(e, row, connectortracker.CapCount, "count demand", func(dc forge.DemandCounter) (int, error) { return dc.CountReady(fresh) })
}

func (e *execClient) priorClaimStateViaConnector(row backendrows.Row, num string) (state forge.DispatchState, found bool, err error) {
	err = trackerCapOp(e, row, connectortracker.CapRemovedLabels, "read an issue's removed labels", func(pr forge.PriorClaimStateReader) error {
		var rerr error
		state, found, rerr = pr.PriorClaimState(num)
		return rerr
	})
	return state, found, err
}

func (e *execClient) listIssuesViaConnector(row backendrows.Row, state forge.DispatchState) ([]forge.Issue, error) {
	return viaTracker(e, row, coreTracker, "list issues", func(tr forge.IssueTracker) ([]forge.Issue, error) { return tr.ListIssues(state) })
}

func (e *execClient) listOpenIssuesViaConnector(row backendrows.Row) ([]forge.Issue, error) {
	return viaTracker(e, row, coreTracker, "list open issues", func(tr forge.IssueTracker) ([]forge.Issue, error) { return tr.ListOpenIssues() })
}

func (e *execClient) issueViaConnector(row backendrows.Row, num string) (forge.Issue, error) {
	return viaTracker(e, row, coreTracker, "read an issue", func(tr forge.IssueTracker) (forge.Issue, error) { return tr.Issue(num) })
}

func (e *execClient) openPRForBranchViaConnector(row backendrows.Row, branch string) (pr forge.PR, found bool, err error) {
	err = e.prRead(row, "find a branch's open PR", func(pf forge.PRForge) error {
		var rerr error
		pr, found, rerr = pf.OpenPRForBranch(branch)
		return rerr
	})
	return pr, found, err
}

func (e *execClient) prForBranchViaConnector(row backendrows.Row, branch string) (url string, found bool, err error) {
	err = e.prRead(row, "find a branch's PR", func(pf forge.PRForge) error {
		var rerr error
		url, found, rerr = pf.PRForBranch(branch)
		return rerr
	})
	return url, found, err
}

func (e *execClient) prStateViaConnector(row backendrows.Row, prURL string) (forge.PRState, error) {
	return viaPR(e, row, "read a PR's state", func(pf forge.PRForge) (forge.PRState, error) { return pf.PRState(prURL) })
}

func (e *execClient) checkStateViaConnector(row backendrows.Row, prURL string) (forge.RollupState, error) {
	return viaPR(e, row, "read a PR's check state", func(pf forge.PRForge) (forge.RollupState, error) { return pf.CheckState(prURL) })
}

func (e *execClient) failureDetailViaConnector(row backendrows.Row, prURL string) (string, error) {
	return viaPR(e, row, "read a PR's failing checks", func(pf forge.PRForge) (string, error) { return pf.FailureDetail(prURL) })
}

// The tracker writes. Each fails with errNoCapability when the Connector lacks
// the primitive behind it; a transition or verdict is the transition layer,
// which needs the label-edit primitive for its stale-label strip.

func (e *execClient) transitionViaConnector(row backendrows.Row, num string, from, to forge.DispatchState) error {
	return e.trackerOp(row, connectortracker.CapTransition, "transition an issue", func(tr forge.IssueTracker) error {
		return tr.TransitionState(num, from, to)
	}, connectortracker.CapEditLabels)
}

func (e *execClient) completeVerdictViaConnector(row backendrows.Row, num string, verdict forge.Verdict) error {
	return e.trackerOp(row, connectortracker.CapTransition, "settle a research verdict", func(tr forge.IssueTracker) error {
		return tr.CompleteVerdict(num, verdict)
	}, connectortracker.CapEditLabels)
}

func (e *execClient) addLabelsViaConnector(row backendrows.Row, num string, labels []string) error {
	return trackerCapOp(e, row, connectortracker.CapEditLabels, "add labels to an issue", func(l forge.IssueLabeler) error {
		return l.AddLabels(num, labels)
	})
}

func (e *execClient) commentViaConnector(row backendrows.Row, num, body string) error {
	return trackerCapOp(e, row, connectortracker.CapComment, "comment on an issue", func(c forge.HostPostedCommenter) error {
		return c.Comment(num, body)
	})
}

func (e *execClient) closeMergedIssueViaConnector(row backendrows.Row, num string) error {
	return trackerCapOp(e, row, connectortracker.CapCloseIssue, "close a merged issue", func(c forge.MergeCloser) error {
		return c.CloseMergedIssue(num)
	})
}

func (e *execClient) fileIssueViaConnector(row backendrows.Row, title, body string, labels []string) (string, error) {
	return viaTrackerCap(e, row, connectortracker.CapFileIssue, "file an issue", func(f forge.HostPostedIssueFiler) (string, error) {
		return f.PostIssue(title, body, labels)
	})
}

func (e *execClient) createLabelViaConnector(row backendrows.Row, name, description, color string) error {
	return e.trackerOp(row, connectortracker.CapLabels, "create a label", func(tr forge.IssueTracker) error {
		return tr.CreateLabel(name, description, color)
	})
}

func (e *execClient) listLabelsViaConnector(row backendrows.Row) ([]string, error) {
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

func (e *execClient) nativeDepsViaConnector(row backendrows.Row, num string) ([]string, error) {
	return viaTrackerCap(e, row, connectortracker.CapNativeDeps, "read an issue's native dependencies", func(nr forge.NativeDepsReader) ([]string, error) { return nr.NativeDepsOf(num) })
}

func (e *execClient) branchExistsViaConnector(row backendrows.Row, branch string) (bool, error) {
	return viaForge(e, row, "check a branch", func(cf forge.CodeForge, _ forge.Capabilities) (bool, error) { return cf.BranchExists(branch) })
}

func (e *execClient) mergeCommitViaConnector(row backendrows.Row, prURL string) (string, error) {
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

func (e *execClient) headCommitSHAViaConnector(row backendrows.Row, prURL string) (string, error) {
	return viaPR(e, row, "read a PR's head commit", func(pf forge.PRForge) (string, error) { return pf.HeadCommitSHA(prURL) })
}

func (e *execClient) mergeableViaConnector(row backendrows.Row, prURL string) (forge.MergeableState, error) {
	return viaPR(e, row, "read a PR's mergeability", func(pf forge.PRForge) (forge.MergeableState, error) { return pf.Mergeable(prURL) })
}

func (e *execClient) needsUpdateViaConnector(row backendrows.Row, prURL string) (bool, error) {
	return viaPR(e, row, "read how far a PR is behind", func(pf forge.PRForge) (bool, error) { return pf.NeedsUpdate(prURL) })
}

func (e *execClient) listPRFilesViaConnector(row backendrows.Row, prURL string) ([]string, error) {
	return viaPR(e, row, "list a PR's files", func(pf forge.PRForge) ([]string, error) { return pf.ListPRFiles(prURL) })
}

// checkRunViaConnector reads the rollup state and the in-flight run link in one
// view; the forge view lacks the optional reporter when the Connector does not
// declare forge.ciRun.
func (e *execClient) checkRunViaConnector(row backendrows.Row, prURL string) (state forge.RollupState, run string, err error) {
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

func (e *execClient) blocksOfViaConnector(row backendrows.Row, num string) ([]forge.Dependency, error) {
	return viaTrackerCap(e, row, connectortracker.CapBlocks, "read the issues an issue blocks", func(bl forge.BlockersLister) ([]forge.Dependency, error) { return bl.BlocksOf(num) })
}

// matchesRouteViaConnector gates on the route, not the handshake, here: an
// unrouted client matches every issue even on a Connector that lacks
// tracker.route, and a routed one is refused by the tracker view itself.
func (e *execClient) matchesRouteViaConnector(row backendrows.Row, num string) (bool, error) {
	return viaTrackerCap(e, row, coreTracker, "match an issue against the route", func(rm forge.RouteMatcher) (bool, error) { return rm.MatchesRoute(num) })
}

func (e *execClient) searchIssuesViaConnector(row backendrows.Row, text string, state forge.IssueState, label string, limit int) ([]forge.Issue, error) {
	return viaTrackerCap(e, row, connectortracker.CapSearchIssues, "search issues", func(s forge.IssueSearcher) ([]forge.Issue, error) { return s.SearchIssues(text, state, label, limit) })
}

func (e *execClient) listIssuesWithLabelsViaConnector(row backendrows.Row, state forge.IssueState, labels []string) ([]forge.Issue, error) {
	return viaTrackerCap(e, row, connectortracker.CapLabeled, "scan labeled issues", func(l forge.LabeledBacklogLister) ([]forge.Issue, error) {
		return l.ListIssuesWithLabels(state, labels)
	})
}

// commentsViaConnector keeps the Connector's attestation that each comment's
// minimized state is faithful; the tracker view serves no comments without it.
func (e *execClient) commentsViaConnector(row backendrows.Row, num string) ([]forge.Comment, error) {
	return viaTrackerCap(e, row, connectortracker.CapComments, "read an issue's comments", func(cl forge.CommentLister) ([]forge.Comment, error) { return cl.Comments(num) })
}

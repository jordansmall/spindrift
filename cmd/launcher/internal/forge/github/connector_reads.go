package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"sync"
	"time"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/connectorforge"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectortracker"
	"spindrift.dev/launcher/internal/forge"
)

// readsAuditLog records the requests of the shared Connector the host-side
// reads go through, beside the other Connector logs.
const readsAuditLog = "connector-github-reads.log"

// loadRowConnector loads a backend row's Connector; a test seam.
var loadRowConnector = backendrows.Load

// errNoCapability marks a Connector whose handshake lacks a capability a read
// needs.
var errNoCapability = errors.New("connector does not declare the capability")

// sharedConnectors holds one loaded Connector per repository and row, so the
// doctor, the daemon's demand probe and recover share a compile rather than
// repeating it per call or per client. The row is process-global
// (backendrows.Use) and a Connector serialises its own calls. A failed load
// is not stored, so the next read retries it.
var sharedConnectors struct {
	mu     sync.Mutex
	loaded map[string]*sharedConnector
}

type sharedConnector struct {
	conn       *connectorhost.Connector
	api        string
	closeAudit func()
}

// sharedConnectorFor returns the Connector loaded for row and repoSlug, loading
// it on first use. It reads with the host's own credential.
func sharedConnectorFor(row backendrows.Row, repoSlug string) (*sharedConnector, error) {
	key, err := json.Marshal([]any{repoSlug, row})
	if err != nil {
		return nil, err
	}
	sharedConnectors.mu.Lock()
	defer sharedConnectors.mu.Unlock()
	if sc, ok := sharedConnectors.loaded[string(key)]; ok {
		return sc, nil
	}
	auditName := readsAuditLog
	if row.Primary {
		auditName = primaryRecordName(".audit.jsonl")
	}
	audit, closeAudit := openAudit(auditName)
	conn, api, err := loadConnector(row, backendrows.Options{Audit: audit, Client: connectorClient}, repoSlug)
	if err != nil {
		closeAudit()
		return nil, err
	}
	sc := &sharedConnector{conn: conn, api: api, closeAudit: closeAudit}
	if sharedConnectors.loaded == nil {
		sharedConnectors.loaded = map[string]*sharedConnector{}
	}
	sharedConnectors.loaded[string(key)] = sc
	return sc, nil
}

// closeSharedConnectors closes every shared Connector, for tests that swap the
// row under them.
func closeSharedConnectors() {
	sharedConnectors.mu.Lock()
	defer sharedConnectors.mu.Unlock()
	for k, sc := range sharedConnectors.loaded {
		_ = sc.conn.Close(context.Background())
		sc.closeAudit()
		delete(sharedConnectors.loaded, k)
	}
}

// routedRow is the github backend row when one is configured and not in shadow
// mode. Backend rows are opt-in: without one, or under a shadow row (native
// stays authoritative), the host's gh answers.
func routedRow() (backendrows.Row, bool) {
	row, ok := backendrows.Lookup(backend.GitHub.Name)
	return row, ok && !row.Shadow
}

// primaryRow is the github backend row when it is configured primary: the
// issue and PR reads (listing and reading issues, the dedup scan, comments,
// native dependencies, the issues one blocks, issue search, the PR, branch and
// check reads) all go through it, with no gh fallback; writes stay native. It
// is deliberately narrower than routedRow, which any non-shadow row satisfies,
// so a plain row keeps gh answering those reads.
func primaryRow() (backendrows.Row, bool) {
	row, ok := backendrows.Lookup(backend.GitHub.Name)
	return row, ok && row.Primary
}

// primaryRecordStem names this process's primary-mode record files, stamped
// once so a human can count clean primary runs: an empty .log and no refused
// entry in the .audit.jsonl is a clean one.
var primaryRecordStem = sync.OnceValue(func() string {
	return "primary-github-" + time.Now().UTC().Format("20060102T150405Z") + "-" + strconv.Itoa(os.Getpid())
})

func primaryRecordName(suffix string) string { return primaryRecordStem() + suffix }

// notePrimary appends the error a primary-routed read returned to the run's
// primary log and returns err unchanged. A plain row records nothing. A record
// that cannot be written goes to stderr instead, so the failure is not lost.
func notePrimary(row backendrows.Row, op string, err error) error {
	if err == nil || !row.Primary {
		return err
	}
	line, _ := json.Marshal(struct {
		Op    string `json:"op"`
		Error string `json:"error"`
	}{op, err.Error()})
	w, closeLog, oerr := openAuditFile(primaryRecordName(".log"))
	defer closeLog()
	if oerr == nil {
		_, oerr = w.Write(append(line, '\n'))
	}
	if oerr != nil {
		fmt.Fprintf(os.Stderr, "primary read %q failed (%v) and its record was not written: %v\n", op, err, oerr)
	}
	return err
}

// readVia runs fn over the shared Connector for e's repository, and records a
// failure under a primary row. A Connector lacking what the read needs (the
// missing sentinel, or a capability fn names through errNoCapability) is
// unsupported, never a reason to fall back to gh.
func (e *execClient) readVia(row backendrows.Row, missing error, verb string, fn func(conn *connectorhost.Connector, api string) error) error {
	sc, err := sharedConnectorFor(row, e.repo)
	if err == nil {
		err = unsupportedWithout(fn(sc.conn, sc.api), verb, missing, errNoCapability)
	}
	return notePrimary(row, verb, err)
}

// coreTracker as trackerRead's capName names a read of the core tracker
// contract, which every tracker Connector serves.
const coreTracker = ""

// trackerRead runs fn over a tracker view of conn that serves the read capName
// names, or coreTracker. The view has no native tracker under it: a
// capability it does not serve must fail rather than reach gh.
func (e *execClient) trackerRead(row backendrows.Row, capName, verb string, fn func(forge.IssueTracker) error) error {
	return e.readVia(row, connectortracker.ErrNotTracker, verb, func(conn *connectorhost.Connector, api string) error {
		if capName != coreTracker && !slices.Contains(conn.Capabilities, capName) {
			return fmt.Errorf("%w: %s", errNoCapability, capName)
		}
		tr, err := connectortracker.New(conn, nil, connectortracker.Lifecycle{Labels: e.labels}, connectortracker.Target{API: api, Repo: e.repo})
		if err != nil {
			return err
		}
		return fn(tr)
	})
}

// trackerCapRead is trackerRead for a read through the optional interface T that
// the capability capName puts on the tracker view.
func trackerCapRead[T any](e *execClient, row backendrows.Row, capName, verb string, fn func(T) error) error {
	return e.trackerRead(row, capName, verb, func(tr forge.IssueTracker) error {
		v, ok := forge.As[T](tr)
		if !ok {
			return fmt.Errorf("%w: %s", errNoCapability, capName)
		}
		return fn(v)
	})
}

// forgeRead runs fn over a PR forge view of conn, with its PR-side and
// optional-interface handles resolved. Its web host is the one the native
// adapter's own PR URLs carry (GH_HOST, else github.com), and nothing native
// sits under it: a read the Connector does not serve must fail rather than
// reach gh.
func (e *execClient) forgeRead(row backendrows.Row, verb string, fn func(forge.CodeForge, forge.Capabilities) error) error {
	return e.readVia(row, connectorforge.ErrNotForge, verb, func(conn *connectorhost.Connector, api string) error {
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

// The via* helpers are the readVia, trackerRead, trackerCapRead, forgeRead and
// prRead entry points for a read that returns one value; the reads that return
// two (a PR and whether it was found) call those directly.

func viaRead[R any](e *execClient, row backendrows.Row, missing error, verb string, fn func(*connectorhost.Connector, string) (R, error)) (v R, err error) {
	err = e.readVia(row, missing, verb, func(conn *connectorhost.Connector, api string) (rerr error) {
		v, rerr = fn(conn, api)
		return rerr
	})
	return v, err
}

func viaTracker[R any](e *execClient, row backendrows.Row, capName, verb string, fn func(forge.IssueTracker) (R, error)) (v R, err error) {
	err = e.trackerRead(row, capName, verb, func(tr forge.IssueTracker) (rerr error) {
		v, rerr = fn(tr)
		return rerr
	})
	return v, err
}

func viaTrackerCap[T, R any](e *execClient, row backendrows.Row, capName, verb string, fn func(T) (R, error)) (v R, err error) {
	err = trackerCapRead(e, row, capName, verb, func(t T) (rerr error) {
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

// countReadyViaConnector has no fresh flag: the Connector keeps no validator
// cache, so every count reads the tracker.
func (e *execClient) countReadyViaConnector(row backendrows.Row) (int, error) {
	return viaTrackerCap(e, row, connectortracker.CapCount, "count demand", func(dc forge.DemandCounter) (int, error) { return dc.CountReady(false) })
}

func (e *execClient) priorClaimStateViaConnector(row backendrows.Row, num string) (state forge.DispatchState, found bool, err error) {
	err = trackerCapRead(e, row, connectortracker.CapRemovedLabels, "read an issue's removed labels", func(pr forge.PriorClaimStateReader) error {
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

func (e *execClient) listLabelsViaConnector(row backendrows.Row) ([]string, error) {
	return viaTracker(e, row, connectortracker.CapLabels, "list labels", func(tr forge.IssueTracker) ([]string, error) { return tr.ListLabels() })
}

// classifyProbeErr gives a failed probe the sentinels the native Probe does:
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

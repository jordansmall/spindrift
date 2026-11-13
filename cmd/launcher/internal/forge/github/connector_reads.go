package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"

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
	audit, closeAudit := openAudit(readsAuditLog)
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

// readVia runs fn over the shared Connector for e's repository. A Connector
// lacking what the read needs (the missing sentinel, or a capability fn names
// through errNoCapability) is unsupported, never a reason to fall back to gh.
func (e *execClient) readVia(row backendrows.Row, missing error, verb string, fn func(conn *connectorhost.Connector, api string) error) error {
	sc, err := sharedConnectorFor(row, e.repo)
	if err != nil {
		return err
	}
	return unsupportedWithout(fn(sc.conn, sc.api), verb, missing, errNoCapability)
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

func (e *execClient) probeViaConnector(row backendrows.Row) (slug string, err error) {
	err = e.readVia(row, connectorforge.ErrNoProbe, "resolve the repository", func(conn *connectorhost.Connector, api string) error {
		var rerr error
		slug, rerr = connectorforge.Probe(conn, connectorforge.Target{API: api, Repo: e.repo})
		return rerr
	})
	if err != nil {
		return "", classifyProbeErr(err, e.repo)
	}
	return slug, nil
}

func (e *execClient) branchProtectedViaConnector(row backendrows.Row, branch string) (protected bool, err error) {
	err = e.readVia(row, connectorforge.ErrNoBranchProtection, "read branch protection", func(conn *connectorhost.Connector, api string) error {
		var rerr error
		protected, rerr = connectorforge.BranchProtected(conn, connectorforge.Target{API: api, Repo: e.repo}, branch)
		return rerr
	})
	return protected, err
}

func (e *execClient) canAutoMergeViaConnector(row backendrows.Row) (allowed bool, err error) {
	err = e.readVia(row, connectorforge.ErrNoAutoMerge, "read the auto-merge setting", func(conn *connectorhost.Connector, api string) error {
		var rerr error
		allowed, rerr = connectorforge.AutoMergeAllowed(conn, connectorforge.Target{API: api, Repo: e.repo})
		return rerr
	})
	return allowed, err
}

// countReadyViaConnector has no fresh flag: the Connector keeps no validator
// cache, so every count reads the tracker.
func (e *execClient) countReadyViaConnector(row backendrows.Row) (count int, err error) {
	err = trackerCapRead(e, row, connectortracker.CapCount, "count demand", func(dc forge.DemandCounter) error {
		var rerr error
		count, rerr = dc.CountReady(false)
		return rerr
	})
	return count, err
}

func (e *execClient) priorClaimStateViaConnector(row backendrows.Row, num string) (state forge.DispatchState, found bool, err error) {
	err = trackerCapRead(e, row, connectortracker.CapRemovedLabels, "read an issue's removed labels", func(pr forge.PriorClaimStateReader) error {
		var rerr error
		state, found, rerr = pr.PriorClaimState(num)
		return rerr
	})
	return state, found, err
}

func (e *execClient) listIssuesViaConnector(row backendrows.Row, state forge.DispatchState) (issues []forge.Issue, err error) {
	err = e.trackerRead(row, coreTracker, "list issues", func(tr forge.IssueTracker) error {
		var rerr error
		issues, rerr = tr.ListIssues(state)
		return rerr
	})
	return issues, err
}

func (e *execClient) listLabelsViaConnector(row backendrows.Row) (labels []string, err error) {
	err = e.trackerRead(row, connectortracker.CapLabels, "list labels", func(tr forge.IssueTracker) error {
		var rerr error
		labels, rerr = tr.ListLabels()
		return rerr
	})
	return labels, err
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

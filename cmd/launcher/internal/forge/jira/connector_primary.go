package jira

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectortracker"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/primaryrecord"
)

// connectorClient is the transport the host-side Connector loads send with; nil
// is the default client. Tests swap it through ConnectorClientForTest.
var connectorClient *http.Client

// loadRowConnector loads a backend row's Connector; a test seam.
var loadRowConnector = backendrows.Load

// errPrimaryCapabilities is a Connector whose handshake omits a method
// core drives with no native adapter behind it.
var errPrimaryCapabilities = errors.New("jira primary Connector lacks required capabilities")

// primaryCapabilities are the capabilities the jira Connector must advertise:
// without native writes, a missing one would reach a nil interface, and a
// missing CapLabeled would silently degrade the finding dedup to open issues.
var primaryCapabilities = []string{
	connectortracker.CapTransition, connectortracker.CapEditLabels, connectortracker.CapLabels,
	connectortracker.CapComment, connectortracker.CapProbe, connectortracker.CapFileIssue,
	connectortracker.CapLabeled,
}

func missingPrimaryCapabilities(have []string) []string {
	var missing []string
	for _, c := range primaryCapabilities {
		if !slices.Contains(have, c) {
			missing = append(missing, c)
		}
	}
	return missing
}

func checkPrimaryCapabilities(have []string) error {
	if missing := missingPrimaryCapabilities(have); len(missing) > 0 {
		return fmt.Errorf("%w: %s", errPrimaryCapabilities, strings.Join(missing, ", "))
	}
	return nil
}

// primaryTracker keeps jira's native-only body grammar, which the Connector
// layers cannot declare; Unwrap leaves their capabilities reachable.
type primaryTracker struct{ forge.IssueTracker }

func (p primaryTracker) Unwrap() forge.IssueTracker { return p.IssueTracker }

// BodyGrammar mirrors the native adapter: links are authoritative.
func (primaryTracker) BodyGrammar() forge.BodyGrammar { return forge.BodyGrammarNativeDepsOnly }

// newConnectorTracker builds jira's tracker from the jira row's Connector.
// Lifecycle policy stays core's; the Connector only performs the operations.
// There are no native writes (nil), so nothing falls back to the adapter.
func newConnectorTracker(row backendrows.Row, s backendkit.Shared, k Knobs) (forge.IssueTracker, error) {
	wl, err := WorkLifecycle(s.Tracker, k.JiraStatusMapping, k.JiraLifecycle)
	if err != nil {
		return nil, err
	}
	dialect := "server"
	if k.JiraEmail != "" {
		dialect = "cloud"
	}
	row.Routes = connectortracker.Routes(row.Routes, k.JiraProjectKey)
	it, err := connectortracker.NewReloading(func() (*connectorhost.Connector, error) {
		conn, err := loadRowConnector(context.Background(), row, backendrows.Options{Audit: primaryAudit.writer(), Client: connectorClient})
		if err != nil {
			return nil, err
		}
		if err := checkPrimaryCapabilities(conn.Capabilities); err != nil {
			_ = conn.Close(context.Background())
			return nil, err
		}
		return conn, nil
	}, recordPrimaryFailure, nil, connectortracker.Lifecycle{
		Labels:        s.Labels,
		VerdictLabels: s.VerdictLabels,
		IssueType:     k.JiraIssueType,
		Statuses:      wl.Statuses,
		Resolutions:   wl.Resolutions,
		StatusOnly:    wl.StatusOnly,
	}, connectortracker.Target{
		API:     strings.TrimRight(k.JiraBaseURL, "/"),
		Repo:    k.JiraProjectKey,
		Dialect: dialect,
	})
	if err != nil {
		recordPrimaryFailure("load the Connector", err)
		return nil, err
	}
	return primaryTracker{it}, nil
}

// primaryAudit is the process's Connector audit file, opened on first use and
// held until exit: a tracker is short-lived (completion, readcontext) or one of
// several daemon kinds sharing the process, so none owns the handle. A record
// that cannot be written must not fail the tracker, so an unopenable file
// discards.
var primaryAudit = &auditFile{}

type auditFile struct {
	once  sync.Once
	w     io.Writer
	close func()
}

func (a *auditFile) writer() io.Writer {
	a.once.Do(func() {
		a.w, a.close, _ = primaryrecord.OpenFile(primaryrecord.Name(backend.Jira.Name, ".audit.jsonl"))
	})
	return a.w
}

// release closes the file if it was opened and stops it opening later. Only
// the test seam calls it; production holds the handle until exit.
func (a *auditFile) release() {
	a.once.Do(func() {})
	if a.close != nil {
		a.close()
	}
}

// recordPrimaryFailure records a Connector failure in jira's primary .log.
func recordPrimaryFailure(op string, err error) {
	_ = primaryrecord.Note(backend.Jira.Name, op, err)
}

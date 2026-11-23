package jira

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectortracker"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/primaryrecord"
	"spindrift.dev/launcher/internal/retry"
)

// connectorClient is the transport the host-side Connector loads send with; nil
// is the default client. Tests swap it through ConnectorClientForTest.
var connectorClient *http.Client

// connectorClock paces the retry backoff of the Connector tracker; the zero
// value sleeps for real. ConnectorClientForTest swaps it for one that does not.
var connectorClock retry.Clock

// loadRowConnector loads a backend row's Connector; a test seam.
var loadRowConnector = backendrows.Load

// errConnectorCapabilities is a Connector whose handshake omits a method
// core drives, with nothing behind it to fall back to.
var errConnectorCapabilities = errors.New("jira Connector lacks required capabilities")

// connectorCapabilities are the capabilities the jira Connector must advertise:
// a missing one would reach a nil interface, and a
// missing CapLabeled would silently degrade the finding dedup to open issues.
var connectorCapabilities = []string{
	connectortracker.CapTransition, connectortracker.CapEditLabels, connectortracker.CapLabels,
	connectortracker.CapComment, connectortracker.CapProbe, connectortracker.CapFileIssue,
	connectortracker.CapLabeled,
}

func missingConnectorCapabilities(have []string) []string {
	var missing []string
	for _, c := range connectorCapabilities {
		if !slices.Contains(have, c) {
			missing = append(missing, c)
		}
	}
	return missing
}

func checkConnectorCapabilities(have []string) error {
	if missing := missingConnectorCapabilities(have); len(missing) > 0 {
		return fmt.Errorf("%w: %s", errConnectorCapabilities, strings.Join(missing, ", "))
	}
	return nil
}

// connectorTracker keeps jira's body grammar, which the Connector
// layers cannot declare; Unwrap leaves their capabilities reachable.
type connectorTracker struct{ forge.IssueTracker }

func (p connectorTracker) Unwrap() forge.IssueTracker { return p.IssueTracker }

// BodyGrammar: Jira's native links are authoritative.
func (connectorTracker) BodyGrammar() forge.BodyGrammar { return forge.BodyGrammarNativeDepsOnly }

// newConnectorTracker builds jira's tracker from the jira row's Connector.
// Lifecycle policy stays core's; the Connector only performs the operations.
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
		conn, err := loadRowConnector(context.Background(), row, backendrows.Options{Client: connectorClient})
		if err != nil {
			return nil, err
		}
		if err := checkConnectorCapabilities(conn.Capabilities); err != nil {
			_ = conn.Close(context.Background())
			return nil, err
		}
		return conn, nil
	}, recordPrimaryFailure, nil, connectortracker.Lifecycle{
		Labels:        s.Labels,
		VerdictLabels: s.VerdictLabels,
		IssueType:     k.JiraIssueType,
		WebHost:       connectortracker.HostOf(k.JiraBaseURL),
		Statuses:      wl.Statuses,
		Resolutions:   wl.Resolutions,
		StatusOnly:    wl.StatusOnly,
		Route:         s.Route,
	}, connectortracker.Target{
		API:     strings.TrimRight(k.JiraBaseURL, "/"),
		Repo:    k.JiraProjectKey,
		Dialect: dialect,
	}, connectorClock)
	if err != nil {
		recordPrimaryFailure("load the Connector", err)
		return nil, err
	}
	return connectorTracker{it}, nil
}

// recordPrimaryFailure records a Connector failure in the primaryrecord .log.
func recordPrimaryFailure(op string, err error) {
	_ = primaryrecord.Note(backend.Jira.Name, op, err)
}

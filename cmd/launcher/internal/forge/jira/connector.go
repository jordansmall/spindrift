package jira

import (
	"net/http"
	"strings"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/connectorruntime"
	"spindrift.dev/launcher/internal/connectortracker"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/retry"
)

// connectorClient is the transport the host-side Connector loads send with; nil
// is the default client. Tests swap it through ConnectorClientForTest.
var connectorClient *http.Client

// connectorClock paces the retry backoff of the Connector tracker; the zero
// value sleeps for real. ConnectorClientForTest swaps it for one that does not.
var connectorClock retry.Clock

// connectorCapabilities are the capabilities the jira Connector must advertise:
// a missing one would reach a nil interface, and a
// missing CapLabeled would silently degrade the finding dedup to open issues.
var connectorCapabilities = []string{
	connectortracker.CapTransition, connectortracker.CapEditLabels, connectortracker.CapLabels,
	connectortracker.CapComment, connectortracker.CapProbe, connectortracker.CapFileIssue,
	connectortracker.CapLabeled,
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
	row.Routes = connectortracker.Routes(row.Routes, k.JiraProjectKey)
	it, err := s.Connectors.Tracker(row, connectorruntime.TrackerSpec{
		SessionSpec: connectorruntime.SessionSpec{
			Target: connectortracker.Target{
				API:     strings.TrimRight(k.JiraBaseURL, "/"),
				Repo:    k.JiraProjectKey,
				Dialect: k.JiraDeployment,
			},
			Requires: connectorCapabilities,
			Record:   backend.Jira.Name,
			Options:  backendrows.Options{Client: connectorClient},
		},
		Lifecycle: connectortracker.Lifecycle{
			Labels:        s.Labels,
			VerdictLabels: s.VerdictLabels,
			IssueType:     k.JiraIssueType,
			WebHost:       connectortracker.HostOf(k.JiraBaseURL),
			Statuses:      wl.Statuses,
			Resolutions:   wl.Resolutions,
			StatusOnly:    wl.StatusOnly,
			Route:         s.Route,
		},
		Clock: connectorClock,
	})
	if err != nil {
		return nil, err
	}
	return connectorTracker{it}, nil
}

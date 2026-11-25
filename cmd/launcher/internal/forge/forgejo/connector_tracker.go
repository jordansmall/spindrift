package forgejo

import (
	"strings"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/connectorruntime"
	"spindrift.dev/launcher/internal/connectortracker"
	"spindrift.dev/launcher/internal/forge"
)

// connectorCapabilities are what a forgejo Connector must advertise for either
// axis, so none silently disappears: the session keeps the first opener's check,
// so a forge-only forgejo Connector is refused at load. A missing one would
// reach a nil layer, and a missing CapLabeled would degrade the finding dedup
// to open issues.
var connectorCapabilities = []string{
	connectortracker.CapTransition, connectortracker.CapEditLabels, connectortracker.CapLabels,
	connectortracker.CapComment, connectortracker.CapFileIssue, connectortracker.CapCloseIssue,
	connectortracker.CapComments, connectortracker.CapNativeDeps, connectortracker.CapBlocks,
	connectortracker.CapProbe, connectortracker.CapCount, connectortracker.CapLabeled,
	connectortracker.CapSearchIssues,
}

// sessionSpec is the session both axes open, the same fields for each so a row
// serving both loads one Connector.
func sessionSpec(s backendkit.Shared, k Knobs) connectorruntime.SessionSpec {
	return connectorruntime.SessionSpec{
		Target: connectortracker.Target{
			API:  strings.TrimSuffix(k.ForgejoBaseURL, "/"),
			Repo: s.RepoSlug,
		},
		Requires: connectorCapabilities,
		Record:   backend.Forgejo.Name,
		Options:  backendrows.Options{Client: connectorClient},
	}
}

// newConnectorTracker builds the issue tracker from row's Connector alone:
// every read and write goes through it.
// Lifecycle policy stays core's. The runtime reloads a dead Connector, so the
// tracker may live as long as a daemon.
func newConnectorTracker(row backendrows.Row, s backendkit.Shared, k Knobs) (forge.IssueTracker, error) {
	row.Routes = connectortracker.Routes(row.Routes, s.RepoSlug)
	return s.Connectors.Tracker(row, connectorruntime.TrackerSpec{
		SessionSpec: sessionSpec(s, k),
		Lifecycle: connectortracker.Lifecycle{
			Labels:        s.Labels,
			Route:         s.Route,
			VerdictLabels: s.VerdictLabels,
			WebHost:       connectortracker.HostOf(k.ForgejoBaseURL),
		},
	})
}

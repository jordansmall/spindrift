package forgejo

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectortracker"
	"spindrift.dev/launcher/internal/forge"
)

// errConnectorCapabilities is a Connector whose handshake omits a method the
// native tracker serves, with nothing behind it to fall back to.
var errConnectorCapabilities = errors.New("forgejo Connector lacks required capabilities")

// connectorCapabilities are what the native forgejo tracker serves, so none
// silently disappears under a primary row. A missing one would reach a nil
// native layer, and a missing CapLabeled would degrade the finding dedup to
// open issues.
var connectorCapabilities = []string{
	connectortracker.CapTransition, connectortracker.CapEditLabels, connectortracker.CapLabels,
	connectortracker.CapComment, connectortracker.CapFileIssue, connectortracker.CapCloseIssue,
	connectortracker.CapComments, connectortracker.CapNativeDeps, connectortracker.CapBlocks,
	connectortracker.CapProbe, connectortracker.CapCount, connectortracker.CapLabeled,
	connectortracker.CapSearchIssues,
}

func checkConnectorCapabilities(have []string) error {
	var missing []string
	for _, c := range connectorCapabilities {
		if !slices.Contains(have, c) {
			missing = append(missing, c)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: %s", errConnectorCapabilities, strings.Join(missing, ", "))
	}
	return nil
}

// newConnectorTracker builds the issue tracker from row's Connector alone:
// every read and write goes through it, with no native tracker behind it.
// Lifecycle policy stays core's. The tracker reloads a dead Connector, so it
// may live as long as a daemon; the Connector and its audit file live for the
// process.
func newConnectorTracker(row backendrows.Row, s backendkit.Shared, k Knobs) (forge.IssueTracker, error) {
	row.Routes = connectortracker.Routes(row.Routes, s.RepoSlug)
	audit, closeAudit := openAudit()
	// The nil reload-failure note is deliberate: connector_forge.go records no
	// primaryrecord trace either, unlike the jira tracker.
	it, err := connectortracker.NewReloading(func() (*connectorhost.Connector, error) {
		conn, err := loadRowConnector(context.Background(), row, backendrows.Options{Audit: audit, Client: connectorClient, Getenv: credentialGetenv(row, k)})
		if err != nil {
			return nil, err
		}
		if err := checkConnectorCapabilities(conn.Capabilities); err != nil {
			_ = conn.Close(context.Background())
			return nil, err
		}
		return conn, nil
	}, nil, nil, connectortracker.Lifecycle{
		Labels:        s.Labels,
		VerdictLabels: s.VerdictLabels,
	}, connectortracker.Target{
		API:  strings.TrimSuffix(k.ForgejoBaseURL, "/"),
		Repo: s.RepoSlug,
	})
	if err != nil {
		closeAudit()
		return nil, err
	}
	return it, nil
}

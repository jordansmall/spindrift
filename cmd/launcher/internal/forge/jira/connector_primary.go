package jira

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectortracker"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/hostpaths"
)

// connectorClient is the transport the host-side Connector loads send with; nil
// is the default client. Tests swap it through ConnectorClientForTest.
var connectorClient *http.Client

// loadRowConnector loads a backend row's Connector; a test seam.
var loadRowConnector = backendrows.Load

// errPrimaryCapabilities is a primary Connector whose handshake omits a method
// core drives with no native adapter behind it.
var errPrimaryCapabilities = errors.New("jira primary Connector lacks required capabilities")

// primaryCapabilities are the capabilities a primary Connector must advertise:
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

// primaryRow is the jira backend row when it is configured primary: every
// tracker operation goes through its Connector, with no native fallback.
func primaryRow() (backendrows.Row, bool) {
	row, ok := backendrows.Lookup(backend.Jira.Name)
	return row, ok && row.Primary
}

// newConnectorTracker builds jira's tracker from a primary row's Connector.
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
	// The audit file stays open for the tracker's life, which is the process's.
	audit, closeAudit := openAudit("connector-jira.audit.jsonl")
	it, err := connectortracker.NewReloading(func() (*connectorhost.Connector, error) {
		conn, err := loadRowConnector(context.Background(), row, backendrows.Options{Audit: audit, Client: connectorClient})
		if err != nil {
			return nil, err
		}
		if err := checkPrimaryCapabilities(conn.Capabilities); err != nil {
			_ = conn.Close(context.Background())
			return nil, err
		}
		return conn, nil
	}, nil, connectortracker.Lifecycle{
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
		closeAudit()
		return nil, err
	}
	return primaryTracker{it}, nil
}

// openAudit opens the Connector's audit file for appending. A record that
// cannot be written must not fail the tracker, so an unopenable file discards.
func openAudit(name string) (io.Writer, func()) {
	pwd, err := os.Getwd()
	if err != nil {
		return io.Discard, func() {}
	}
	dir := hostpaths.LogDir(pwd)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return io.Discard, func() {}
	}
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return io.Discard, func() {}
	}
	return f, func() { _ = f.Close() }
}

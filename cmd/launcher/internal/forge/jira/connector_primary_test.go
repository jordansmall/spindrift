package jira_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/forge/jira"
)

func useJiraRow(t *testing.T, adjust func(*backendrows.Row)) {
	t.Helper()
	row := backendrows.Row{
		Name:          "jira",
		Module:        filepath.Join(t.TempDir(), "missing.wasm"),
		Routes:        []connectorhost.Route{{Method: "GET", Host: "jira.example", Path: "/rest"}},
		CredentialEnv: "JIRA_TOKEN",
	}
	adjust(&row)
	doc, err := json.Marshal([]backendrows.Row{row})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(backendrows.Snapshot())
	if err := backendrows.Use(doc, func(string) string { return "" }); err != nil {
		t.Fatal(err)
	}
}

func newJiraTracker(t *testing.T) error {
	t.Helper()
	t.Chdir(t.TempDir()) // the audit file lands under the host log dir
	env := map[string]string{"JIRA_BASE_URL": "https://jira.example", "JIRA_PROJECT_KEY": "PROJ", "JIRA_TOKEN": "tok"}
	_, err := jira.Strategy.NewIssueTracker(backendkit.Shared{}, func(k string) (string, bool) {
		v, ok := env[k]
		return v, ok
	})
	return err
}

// Under primary the Connector is the only tracker: one that cannot load fails
// the constructor with its error rather than dropping to the native adapter.
func TestPrimaryRowFailsWithTheLoadErrorAndNeverFallsBackToNative(t *testing.T) {
	useJiraRow(t, func(r *backendrows.Row) { r.Primary = true })
	if err := newJiraTracker(t); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want the module read error", err)
	}
}

func TestPlainRowKeepsTheNativeAdapter(t *testing.T) {
	useJiraRow(t, func(*backendrows.Row) {})
	if err := newJiraTracker(t); err != nil {
		t.Fatalf("a non-primary row must not load its Connector, got %v", err)
	}
}

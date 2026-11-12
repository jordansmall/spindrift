// Package backendrows applies the backend rows of the launcher input document
// (ADR 0073, issue #6805): a network Connector's compiled module, Route table
// and credential knob. A backend without a row here is built from the generated
// registry as before. It is a package of its own because it imports
// connectorhost, which must not import the registry.
package backendrows

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/registryvocab"
)

// Row is one backend row of the input document.
type Row struct {
	Name string `json:"name"`
	// Module is the path of the Connector's compiled wasm.
	Module string `json:"module"`
	// Routes is the only traffic the Connector may send.
	Routes []connectorhost.Route `json:"routes"`
	// CredentialEnv names the host env var holding the bearer token the host
	// attaches to each admitted request. The Connector never sees it.
	CredentialEnv string `json:"credentialEnv"`
	// CredentialKnob is the Consumer knob that names CredentialEnv; doctor shows it.
	CredentialKnob string `json:"credentialKnob,omitempty"`
	// AcceptedRoutes is the Consumer's accepted baseline of Routes, which doctor
	// diffs Routes against. Unlike Routes it may be empty or absent: nothing
	// accepted yet, so every live route counts as added.
	AcceptedRoutes []connectorhost.Route `json:"acceptedRoutes,omitempty"`
}

// Widening returns the live Routes no accepted Route covers. An accepted Route
// covers one with the same method and host, compared literally, and a path
// equal to or inside the accepted path's subtree; so a new host, method or
// path, or a broader path pattern, is a widening and a narrower one is not.
// The subtree test is PathSet.Admits' request-path match, sound here because Use
// validates both tables as canonical patterns (no blanket "/").
func (r Row) Widening() []connectorhost.Route {
	var added []connectorhost.Route
	for _, live := range r.Routes {
		covered := slices.ContainsFunc(r.AcceptedRoutes, func(a connectorhost.Route) bool {
			return a.Method == live.Method && a.Host == live.Host &&
				registryvocab.PathSet{a.Path}.Admits(live.Path)
		})
		if !covered {
			added = append(added, live)
		}
	}
	return added
}

// coreBackends are the built-in backends a row may not replace. It mirrors the
// `core = true` rows of lib/backends/ (ADR 0068, decision 7).
var coreBackends = map[string]bool{"git": true, "local": true}

var (
	mu      sync.RWMutex
	applied = map[string]Row{}
)

// Use applies the rows in raw, a JSON array of Row, replacing any rows an
// earlier call applied; empty raw applies none, so it clears them. A row with
// no name, a repeated name, a row missing its module, credentialEnv or
// routes, a name that is a built-in backend and an invalid Route table are
// errors, as is an invalid non-empty accepted baseline. Rows are validated
// whole before any is stored.
func Use(raw json.RawMessage) error {
	var rows []Row
	if len(bytes.TrimSpace(raw)) != 0 {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&rows); err != nil {
			return fmt.Errorf("backend rows: %w", err)
		}
	}
	seen := map[string]bool{}
	for _, row := range rows {
		if err := validate(row, seen); err != nil {
			return fmt.Errorf("backend rows: %w", err)
		}
	}
	next := make(map[string]Row, len(rows))
	for _, row := range rows {
		next[row.Name] = row
	}
	mu.Lock()
	defer mu.Unlock()
	applied = next
	return nil
}

func validate(row Row, seen map[string]bool) error {
	switch {
	case row.Name == "":
		return errors.New("a backend row has no name")
	case seen[row.Name]:
		return fmt.Errorf("backend %q has more than one row", row.Name)
	case coreBackends[row.Name]:
		return fmt.Errorf("backend %q is built in, so a row cannot replace it", row.Name)
	case row.Module == "":
		return fmt.Errorf("backend %q has no module", row.Name)
	case row.CredentialEnv == "":
		return fmt.Errorf("backend %q has no credentialEnv", row.Name)
	case len(row.Routes) == 0:
		return fmt.Errorf("backend %q has an empty route table", row.Name)
	}
	seen[row.Name] = true
	if err := connectorhost.ValidateRoutes(row.Routes); err != nil {
		return fmt.Errorf("backend %q: %w", row.Name, err)
	}
	if len(row.AcceptedRoutes) != 0 {
		if err := connectorhost.ValidateRoutes(row.AcceptedRoutes); err != nil {
			return fmt.Errorf("backend %q: accepted routes: %w", row.Name, err)
		}
	}
	return nil
}

// Lookup returns the document's row for the backend name. ok is false when the
// document has none, and the caller builds the native backend from the
// generated registry.
func Lookup(name string) (Row, bool) {
	mu.RLock()
	defer mu.RUnlock()
	row, ok := applied[name]
	return row, ok
}

// All returns every applied row, sorted by name.
func All() []Row {
	mu.RLock()
	defer mu.RUnlock()
	rows := slices.Collect(maps.Values(applied))
	slices.SortFunc(rows, func(a, b Row) int { return strings.Compare(a.Name, b.Name) })
	return rows
}

// Options is what Load takes from its host process.
type Options struct {
	// Getenv resolves the credential; nil means os.Getenv.
	Getenv func(string) string
	// Audit receives one line per request; required.
	Audit io.Writer
	// Client is the transport; nil means a default client.
	Client *http.Client
}

// Load compiles the row's module as a network Connector bound to its Route
// table. The credential is read at each request, not at load, and a missing one
// fails the request before it is sent.
func Load(ctx context.Context, row Row, o Options) (*connectorhost.Connector, error) {
	if o.Audit == nil {
		return nil, fmt.Errorf("backend %q: no audit writer", row.Name)
	}
	getenv := o.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	wasm, err := os.ReadFile(row.Module)
	if err != nil {
		return nil, fmt.Errorf("backend %q: read module: %w", row.Name, err)
	}
	return connectorhost.LoadNetwork(ctx, wasm, connectorhost.Network{
		Name:   row.Name,
		Routes: row.Routes,
		Authorize: func(r *http.Request) error {
			token := getenv(row.CredentialEnv)
			if token == "" {
				return errors.New(row.CredentialEnv + " is not set")
			}
			r.Header.Set("Authorization", "Bearer "+token)
			return nil
		},
		Client: o.Client,
		Audit:  connectorhost.NewAuditLog(o.Audit),
	})
}

// Snapshot captures the applied rows and returns the function that restores
// them, for a test that applies rows to the process-global set. It is not in
// export_test.go because cmd/launcher's own tests need it.
func Snapshot() (restore func()) {
	mu.Lock()
	defer mu.Unlock()
	saved := maps.Clone(applied)
	return func() {
		mu.Lock()
		defer mu.Unlock()
		applied = saved
	}
}

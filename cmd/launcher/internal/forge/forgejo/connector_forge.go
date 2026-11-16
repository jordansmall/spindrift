package forgejo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/connectorforge"
	"spindrift.dev/launcher/internal/connectortracker"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/git"
	"spindrift.dev/launcher/internal/gitexec"
	"spindrift.dev/launcher/internal/hostpaths"
)

// ErrPrimaryMergeMethod is why a primary forgejo row refuses a MERGE_METHOD
// other than rebase: the Connector always merges by rebase, so honouring the
// knob is impossible and ignoring it would be silent.
var ErrPrimaryMergeMethod = errors.New("MERGE_METHOD must be rebase or unset: the forgejo Connector merges by rebase only")

// connectorAuditLog is the primary Connector's request record, in the host's
// log directory beside the other Connector logs.
const connectorAuditLog = "connector-forgejo.audit.jsonl"

// loadRowConnector loads a backend row's Connector; a test seam.
var loadRowConnector = backendrows.Load

// connectorClient is the transport the Connector's requests go over; nil means
// the default client. A test seam, as a real host reaches the real instance.
var connectorClient *http.Client

// primaryRow is the forgejo backend row when it is configured primary: the
// Code Forge is then built from the row's Connector, never the native adapter.
func primaryRow() (backendrows.Row, bool) {
	row, ok := backendrows.Lookup(backend.Forgejo.Name)
	return row, ok && row.Primary
}

// boxMode names newConnectorCodeForge's Box access: a read-only Box relays its
// bundle instead of pushing.
type boxMode bool

const (
	boxWritable boxMode = false
	boxReadOnly boxMode = true
)

// newConnectorCodeForge composes the Code Forge from row's Connector and the
// capabilities its handshake advertises, with core's own git doing every git
// operation. Every failure is returned as is: a primary row never falls back to
// the native adapter. The Connector authorizes with the row's credential; git
// keeps the native token. The Connector and its audit file live for the
// process.
func newConnectorCodeForge(row backendrows.Row, s backendkit.Shared, k Knobs, mode boxMode) (forge.CodeForge, error) {
	if MergeStyle(s.MergeMethod) != "rebase" {
		return nil, fmt.Errorf("%w (got %q)", ErrPrimaryMergeMethod, s.MergeMethod)
	}
	row.Routes = connectortracker.Routes(row.Routes, s.RepoSlug)
	audit, closeAudit := openAudit()
	conn, err := loadRowConnector(context.Background(), row, backendrows.Options{Audit: audit, Client: connectorClient})
	if err != nil {
		closeAudit()
		return nil, err
	}
	cf, _, err := connectorforge.Compose(conn, connectorforge.Target{API: strings.TrimSuffix(k.ForgejoBaseURL, "/"), Repo: s.RepoSlug}, connectorforge.Git{
		Credential:  GitCredential(k.ForgejoToken),
		ReadOnlyBox: mode == boxReadOnly,
		Plumbing: func(cloneURL string, cred gitexec.Credential) forge.CodeForge {
			return git.NewGitClient(cloneURL, s.BaseBranch, s.GitUserName, s.GitUserEmail, git.WithCredential(cred))
		},
	})
	if err != nil {
		_ = conn.Close(context.Background())
		closeAudit()
		return nil, err
	}
	return cf, nil
}

// openAudit opens the Connector's audit file for appending. A record that
// cannot be written must not fail the run, so an unopenable file discards, with
// one stderr line because primary mode exists to collect that evidence.
func openAudit() (io.Writer, func()) {
	lost := func(err error) (io.Writer, func()) {
		fmt.Fprintf(os.Stderr, "forgejo: Connector audit record disabled: %v\n", err)
		return io.Discard, func() {}
	}
	pwd, err := os.Getwd()
	if err != nil {
		return lost(err)
	}
	dir := hostpaths.LogDir(pwd)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return lost(err)
	}
	f, err := os.OpenFile(filepath.Join(dir, connectorAuditLog), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return lost(err)
	}
	return f, func() { _ = f.Close() }
}

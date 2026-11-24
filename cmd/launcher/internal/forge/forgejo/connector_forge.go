package forgejo

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/connectorforge"
	"spindrift.dev/launcher/internal/connectorrecord"
	"spindrift.dev/launcher/internal/connectortracker"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/git"
	"spindrift.dev/launcher/internal/gitexec"
)

// ErrMergeMethod is why a forgejo row refuses a MERGE_METHOD
// other than rebase: the Connector always merges by rebase, so honouring the
// knob is impossible and ignoring it would be silent.
var ErrMergeMethod = errors.New("MERGE_METHOD must be rebase or unset: the forgejo Connector merges by rebase only")

// loadRowConnector loads a backend row's Connector; a test seam.
var loadRowConnector = backendrows.Load

// connectorClient is the transport the Connector's requests go over; nil means
// the default client. A test seam, as a real host reaches the real instance.
var connectorClient *http.Client

// ErrNoConnectorRow is why neither forgejo's Code Forge nor its issue tracker
// can be built without a forgejo backend row: both run only through the
// Connector.
var ErrNoConnectorRow = errors.New("forgejo runs only through its Connector, which needs a forgejo backend row with a usable base")

// connectorRow is the forgejo backend row both halves are built from.
// Otherwise it returns ErrNoConnectorRow, wrapping the row set's base error
// when that is why the row is absent.
func connectorRow() (backendrows.Row, error) {
	if row, ok := backendrows.Lookup(backend.Forgejo.Name); ok {
		return row, nil
	}
	if baseErr := backendrows.BaseError(backend.Forgejo.Name); baseErr != nil {
		return backendrows.Row{}, fmt.Errorf("%w: %w", ErrNoConnectorRow, baseErr)
	}
	return backendrows.Row{}, ErrNoConnectorRow
}

// newConnectorCodeForge composes the Code Forge from row's Connector and the
// capabilities its handshake advertises, with core's own git doing every git
// operation. Every failure is returned as is: the forge never falls back to a
// native adapter. The Connector authorizes with the row's credential; git
// keeps the native token. The Connector and its audit file live for the
// process. A failed load or compose, and each failed Connector call of the
// built forge, is recorded in the connectorrecord .log.
func newConnectorCodeForge(row backendrows.Row, s backendkit.Shared, k Knobs) (forge.CodeForge, error) {
	if MergeStyle(s.MergeMethod) != "rebase" {
		return nil, fmt.Errorf("%w (got %q)", ErrMergeMethod, s.MergeMethod)
	}
	row.Routes = connectortracker.Routes(row.Routes, s.RepoSlug)
	conn, err := loadRowConnector(context.Background(), row, backendrows.Options{Client: connectorClient, Getenv: credentialGetenv(row, k)})
	if err != nil {
		recordFailure("load the Connector", err)
		return nil, err
	}
	cf, _, err := connectorforge.Compose(conn, connectorforge.Target{API: strings.TrimSuffix(k.ForgejoBaseURL, "/"), Repo: s.RepoSlug}, connectorforge.Git{
		Credential: GitCredential(k.ForgejoToken),
		Note:       recordFailure,
		Plumbing: func(cloneURL string, cred gitexec.Credential) forge.CodeForge {
			return git.NewGitClient(cloneURL, s.BaseBranch, s.GitUserName, s.GitUserEmail, git.WithCredential(cred))
		},
	})
	if err != nil {
		recordFailure("compose the Code Forge", err)
		_ = conn.Close(context.Background())
		return nil, err
	}
	return cf, nil
}

// credentialGetenv resolves FORGEJO_TOKEN from k, the Lookup the forge was
// built from, so a token the process env never held (the quickstart wizard's)
// still authorizes. Every other name falls through to the process env.
func credentialGetenv(row backendrows.Row, k Knobs) func(string) string {
	return func(name string) string {
		if name == row.CredentialEnv && name == ForgejoTokenEnv && k.ForgejoToken != "" {
			return k.ForgejoToken
		}
		return os.Getenv(name)
	}
}

// recordFailure records a Connector failure in the connectorrecord .log.
func recordFailure(op string, err error) {
	_ = connectorrecord.Note(backend.Forgejo.Name, op, err)
}

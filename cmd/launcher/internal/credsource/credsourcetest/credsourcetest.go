// Package credsourcetest builds credential sources for tests whose token comes
// from the test's own environment.
package credsourcetest

import (
	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/credsource"
)

// EnvRows gives each of rows a source reading its credential (and identity)
// env at each use, as a test's t.Setenv rotates it.
func EnvRows(rows ...backendrows.Row) map[string]credsource.Row {
	out := make(map[string]credsource.Row, len(rows))
	for _, row := range rows {
		out[row.Name] = credsource.EnvRow(row.CredentialEnv, row.IdentityEnv)
	}
	return out
}

// Applied is EnvRows over the applied backend rows.
func Applied() map[string]credsource.Row { return EnvRows(backendrows.All()...) }

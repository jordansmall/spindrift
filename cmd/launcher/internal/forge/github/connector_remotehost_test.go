package github

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/backendkit"
)

// A routed row whose gitRemote names a host other than GH_HOST's would pin PR
// reads to one host and git, with the GH_TOKEN credential, to another. Every
// PR read, PR write, and the Ledger fail closed before any credential read, git
// process or request, naming both hosts.
func TestRoutedRow_GitRemoteOffGHHostFailsClosed(t *testing.T) {
	const remoteHost = "ghe.example.com"
	for _, tc := range []struct {
		name string
		call func(c *execClient) error
	}{
		{"read", func(c *execClient) error { _, err := c.PRState(readPRURL); return err }},
		{"write", func(c *execClient) error { return c.MarkReady(readPRURL) }},
		{"Ledger", func(c *execClient) error {
			_, err := Strategy.NewLedger(backendkit.Shared{RepoSlug: connectorRepo, Labels: testLabels, Connectors: c.connectors}, nil)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, c, calls := harnessWithRow(t, func(t *testing.T, host string) {
				decodeGithubRow(t, host, func(r *backendrows.Row) { r.GitRemote = "https://" + remoteHost })
			})
			// An unset credential would fail by name, so reaching GitCredential
			// shows as a non-ErrNoRoute error.
			t.Setenv(tokenCredentialEnv, "")
			t.Setenv("GH_HOST", "") // after the harness, which points it at the loopback host
			gitCalls := fakeGit(t)

			err := tc.call(c)

			if !errors.Is(err, ErrNoRoute) {
				t.Fatalf("error = %v, want ErrNoRoute", err)
			}
			for _, want := range []string{`on host "` + remoteHost + `"`, `GH_HOST "github.com"`} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %q, want it to contain %s", err, want)
				}
			}
			if n := h.backend.hits; n != 0 {
				t.Errorf("backend saw %d requests", n)
			}
			if n := gitCalls(); n != 0 {
				t.Errorf("ran git %d times", n)
			}
			if n := calls(); n != 0 {
				t.Errorf("ran gh %d times", n)
			}
		})
	}
}

// fakeGit puts a git on PATH that records its invocations and fails.
func fakeGit(t *testing.T) (calls func() int) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n: > \"" + dir + "/call-$$\"\nexit 99\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() int {
		m, _ := filepath.Glob(filepath.Join(dir, "call-*"))
		return len(m)
	}
}

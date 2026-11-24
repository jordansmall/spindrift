package github

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/tokenrefresh"
)

// ghTokenRotation is a routed github row whose credential is core's GH_TOKEN,
// loaded through backendrows.Load with its production Authorize, and the
// refresh file the shared watcher polls.
type ghTokenRotation struct {
	h    *connectorHarness
	c    *execClient
	file string
}

func newGHTokenRotation(t *testing.T) *ghTokenRotation {
	t.Helper()
	h, c, _, _ := routedLogHarness(t, func(t *testing.T, host string) {
		applyGithubRow(t, host, func(r *backendrows.Row) { r.CredentialEnv = "GH_TOKEN" })
	})
	t.Setenv("GH_TOKEN", connectorToken)
	r := &ghTokenRotation{h: h, c: c, file: filepath.Join(t.TempDir(), "token")}
	r.write(t, connectorToken)

	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		tokenrefresh.WatchGHToken(r.file, 10*time.Millisecond, stop)
	}()
	// Registered after the Setenv above, so the watcher is gone before GH_TOKEN
	// is restored.
	t.Cleanup(func() { close(stop); <-done })
	return r
}

// write replaces the refresh file the way the minter does: beside it, then
// rename.
func (r *ghTokenRotation) write(t *testing.T, token string) {
	t.Helper()
	tmp := r.file + ".tmp"
	if err := os.WriteFile(tmp, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, r.file); err != nil {
		t.Fatal(err)
	}
}

// rotate writes token to the refresh file and waits for the watcher to carry
// it into GH_TOKEN.
func (r *ghTokenRotation) rotate(t *testing.T, token string) {
	t.Helper()
	r.write(t, token)
	for deadline := time.Now().Add(10 * time.Second); os.Getenv("GH_TOKEN") != token; {
		if time.Now().After(deadline) {
			t.Fatalf("the watcher never applied the rotated token; GH_TOKEN = %q", os.Getenv("GH_TOKEN"))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The github row's credential is GH_TOKEN, which the shared refresh watcher
// rewrites in place: the next Connector request after a rotation carries the
// new token with no restart.
func TestConnectorRow_RequestCarriesTheGHTokenAfterTheWatcherRotatesIt(t *testing.T) {
	r := newGHTokenRotation(t)
	const rotated = "rotated-token"
	r.h.SeedIssue(forge.Issue{Number: "1", Title: "one"})
	r.h.backend.mu.Lock()
	r.h.backend.tokens = map[string]bool{"Bearer " + rotated: true}
	start := len(r.h.backend.auths)
	r.h.backend.mu.Unlock()

	if _, err := r.c.Issue("1"); err != nil {
		t.Fatalf("Issue before rotation: %v", err)
	}
	r.rotate(t, rotated)
	if _, err := r.c.Issue("1"); err != nil {
		t.Fatalf("Issue after rotation: %v", err)
	}

	r.h.backend.mu.Lock()
	auths := slices.Clone(r.h.backend.auths[start:])
	r.h.backend.mu.Unlock()
	if want := []string{"Bearer " + connectorToken, "Bearer " + rotated}; !slices.Equal(auths, want) {
		t.Errorf("requests carried %q, want %q", auths, want)
	}
}

// A core git push after the watcher rotates GH_TOKEN presents the new token:
// the Ledger site reads the row's credential at each push.
func TestConnectorRow_LedgerPushPresentsTheGHTokenAfterTheWatcherRotatesIt(t *testing.T) {
	r := newGHTokenRotation(t)
	requireLedgerPushReadsRotatedToken(t, func() { r.rotate(t, "rotated-token") })
}

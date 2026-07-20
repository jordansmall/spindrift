package tokenrefresh

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestReadIfChanged_NewContentReportsChanged verifies that a file whose
// trimmed contents differ from prev is reported as changed, with the fresh
// value returned.
func TestReadIfChanged_NewContentReportsChanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("fresh-token\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	next, changed := ReadIfChanged(path, "stale-token")

	if !changed {
		t.Fatal("ReadIfChanged: changed = false, want true")
	}
	if next != "fresh-token" {
		t.Fatalf("ReadIfChanged: next = %q, want %q", next, "fresh-token")
	}
}

// TestReadIfChanged_SameContentReportsUnchanged verifies that a file whose
// trimmed contents match prev exactly is reported as unchanged.
func TestReadIfChanged_SameContentReportsUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("same-token\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	next, changed := ReadIfChanged(path, "same-token")

	if changed {
		t.Fatal("ReadIfChanged: changed = true, want false")
	}
	if next != "same-token" {
		t.Fatalf("ReadIfChanged: next = %q, want %q", next, "same-token")
	}
}

// TestReadIfChanged_EmptyFileReportsUnchanged verifies that a file trimming
// down to an empty string never reports a change, even when prev was empty
// too — an empty read is always treated as "nothing minted yet", not a
// token to adopt.
func TestReadIfChanged_EmptyFileReportsUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	next, changed := ReadIfChanged(path, "stale-token")

	if changed {
		t.Fatal("ReadIfChanged: changed = true, want false for an empty file")
	}
	if next != "stale-token" {
		t.Fatalf("ReadIfChanged: next = %q, want prev %q preserved", next, "stale-token")
	}
}

// TestWatch_AppliesInitialTokenImmediately verifies that Watch calls setenv
// with the file's starting content right away, without waiting for the
// first tick — an external refresher's initial mint must take effect
// immediately, not after a full interval.
func TestWatch_AppliesInitialTokenImmediately(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("initial-token"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	applied := make(chan string, 10)
	stop := make(chan struct{})
	defer close(stop)

	go Watch(path, time.Hour, stop, func(v string) error {
		applied <- v
		return nil
	})

	select {
	case v := <-applied:
		if v != "initial-token" {
			t.Fatalf("Watch: setenv called with %q, want %q", v, "initial-token")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Watch: setenv was not called with the initial token")
	}
}

// TestWatch_AppliesLaterRewrite verifies that after the initial read, Watch
// picks up a subsequent rewrite of the file on a later poll — the refresher
// re-minting the token partway through a run must actually reach GH_TOKEN.
func TestWatch_AppliesLaterRewrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("initial-token"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	applied := make(chan string, 10)
	stop := make(chan struct{})
	defer close(stop)

	go Watch(path, 10*time.Millisecond, stop, func(v string) error {
		applied <- v
		return nil
	})

	if v := <-applied; v != "initial-token" {
		t.Fatalf("Watch: first setenv call = %q, want %q", v, "initial-token")
	}

	if err := os.WriteFile(path, []byte("refreshed-token"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	select {
	case v := <-applied:
		if v != "refreshed-token" {
			t.Fatalf("Watch: second setenv call = %q, want %q", v, "refreshed-token")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Watch: setenv was not called again after the file changed")
	}
}

// TestReadIfChanged_MissingFileReportsUnchanged verifies that a read error
// (e.g. the refresher hasn't written the file yet) leaves prev untouched
// instead of surfacing an error or clearing the current token.
func TestReadIfChanged_MissingFileReportsUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist")

	next, changed := ReadIfChanged(path, "stale-token")

	if changed {
		t.Fatal("ReadIfChanged: changed = true, want false for a missing file")
	}
	if next != "stale-token" {
		t.Fatalf("ReadIfChanged: next = %q, want prev %q preserved", next, "stale-token")
	}
}

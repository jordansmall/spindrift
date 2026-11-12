package tokenrefresh

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

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

// An empty read means the refresher has not minted a token yet, so it never
// counts as a change and never replaces the current token.
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

// Watch must apply the starting content before the first tick, because the
// refresher's initial mint cannot wait a full interval to take effect.
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

// A token the refresher re-mints partway through a run must reach GH_TOKEN,
// so a later poll has to pick up the rewrite.
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

// A read error usually means the refresher has not written the file yet, so
// ReadIfChanged returns prev rather than clearing the current token.
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

func TestFollow_NoPathReturnsStatic(t *testing.T) {
	if got := Follow("static-token", "")(); got != "static-token" {
		t.Fatalf("Follow: token = %q, want %q", got, "static-token")
	}
}

// rotate replaces path with token the way the minter does: write beside it,
// then rename.
func rotate(t *testing.T, path, token string) {
	t.Helper()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(token+"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatalf("Rename: %v", err)
	}
}

// The minter replaces the file by rename, so a rotation must show on the very
// next call rather than waiting for a poll interval.
func TestFollow_SeesRotationOnNextCall(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte("first-token\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	token := Follow("static-token", path)

	if got := token(); got != "first-token" {
		t.Fatalf("Follow: token = %q, want %q", got, "first-token")
	}

	rotate(t, path, "second-token")

	if got := token(); got != "second-token" {
		t.Fatalf("Follow: token after rotation = %q, want %q", got, "second-token")
	}
}

// A caller that once had a token must never be handed "" or the static
// fallback again because the file went missing or was emptied mid-rotation.
func TestFollow_KeepsLastGoodToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("good-token"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	token := Follow("static-token", path)
	if got := token(); got != "good-token" {
		t.Fatalf("Follow: token = %q, want %q", got, "good-token")
	}

	if err := os.WriteFile(path, []byte(" \n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if got := token(); got != "good-token" {
		t.Fatalf("Follow: token after emptying = %q, want %q", got, "good-token")
	}

	if err := os.Remove(path); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if got := token(); got != "good-token" {
		t.Fatalf("Follow: token after removal = %q, want %q", got, "good-token")
	}
}

func TestFollow_FileNotYetWrittenReturnsStatic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	token := Follow("static-token", path)

	if got := token(); got != "static-token" {
		t.Fatalf("Follow: token = %q, want %q", got, "static-token")
	}

	if err := os.WriteFile(path, []byte("minted-token"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if got := token(); got != "minted-token" {
		t.Fatalf("Follow: token after first mint = %q, want %q", got, "minted-token")
	}
}

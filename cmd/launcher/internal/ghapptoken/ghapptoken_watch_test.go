package ghapptoken

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// counterMintScript increments a counter persisted in FAKE_COUNTER_FILE on
// every invocation and echoes a token derived from the new count, so tests
// can distinguish successive mints without any real network call.
const counterMintScript = `#!/usr/bin/env bash
set -euo pipefail
: "${FAKE_COUNTER_FILE:?FAKE_COUNTER_FILE must be set}"
count=0
if [ -f "$FAKE_COUNTER_FILE" ]; then
  count=$(cat "$FAKE_COUNTER_FILE")
fi
count=$((count + 1))
echo "$count" > "$FAKE_COUNTER_FILE"
printf 'token-%d' "$count"
`

// failOnceThenCounterMintScript behaves like counterMintScript, except the
// very first periodic re-mint (count == 2 -- count == 1 is the synchronous
// initial mint in watchWithScript) fails instead of succeeding, using a
// marker file (FAKE_FAILED_ONCE_FILE) to make sure it only fails that one
// invocation and succeeds on every other one, including the retry after the
// failure-backoff fires.
const failOnceThenCounterMintScript = `#!/usr/bin/env bash
set -euo pipefail
: "${FAKE_COUNTER_FILE:?FAKE_COUNTER_FILE must be set}"
: "${FAKE_FAILED_ONCE_FILE:?FAKE_FAILED_ONCE_FILE must be set}"
count=0
if [ -f "$FAKE_COUNTER_FILE" ]; then
  count=$(cat "$FAKE_COUNTER_FILE")
fi
count=$((count + 1))
echo "$count" > "$FAKE_COUNTER_FILE"
if [ "$count" -eq 2 ] && [ ! -f "$FAKE_FAILED_ONCE_FILE" ]; then
  touch "$FAKE_FAILED_ONCE_FILE"
  echo "simulated mint failure" >&2
  exit 1
fi
printf 'token-%d' "$count"
`

func waitForFileContent(t *testing.T, path, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && string(data) == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s to contain %q", path, want)
}

func TestWatch_WritesFirstMintBeforeReturning(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "key.pem")
	if err := os.WriteFile(keyFile, []byte("fake-key"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	tokenFile := filepath.Join(t.TempDir(), "token")
	cfg := Config{AppID: "123", PrivateKeyFile: keyFile, InstallationID: "456"}

	stop := make(chan struct{})
	defer close(stop)

	token, err := watchWithScript(context.Background(), []byte(fakeMintScript), cfg, tokenFile, time.Hour, stop)
	if err != nil {
		t.Fatalf("watchWithScript returned error: %v", err)
	}

	const want = "minted-123-456"
	if token != want {
		t.Errorf("watchWithScript returned token = %q, want %q", token, want)
	}

	data, err := os.ReadFile(tokenFile)
	if err != nil {
		t.Fatalf("ReadFile(tokenFile): %v", err)
	}
	if string(data) != want {
		t.Errorf("tokenFile content = %q, want %q", string(data), want)
	}
}

func TestWatch_FirstMintFailureReturnsErrorAndWritesNoFile(t *testing.T) {
	cfg := Config{
		AppID:          "123",
		PrivateKeyFile: filepath.Join(t.TempDir(), "does-not-exist.pem"),
		InstallationID: "456",
	}
	tokenFile := filepath.Join(t.TempDir(), "token")

	stop := make(chan struct{})
	defer close(stop)

	token, err := watchWithScript(context.Background(), []byte(fakeMintScript), cfg, tokenFile, time.Hour, stop)
	if err == nil {
		t.Fatal("watchWithScript returned nil error, want an error from the failed first mint")
	}
	if token != "" {
		t.Errorf("watchWithScript returned token = %q on a failed first mint, want empty", token)
	}

	if _, statErr := os.Stat(tokenFile); !os.IsNotExist(statErr) {
		t.Errorf("tokenFile exists after a failed first mint, want no file (stat err = %v)", statErr)
	}
}

func TestWatch_PeriodicallyRemintsAndStopsOnClose(t *testing.T) {
	counterFile := filepath.Join(t.TempDir(), "counter")
	t.Setenv("FAKE_COUNTER_FILE", counterFile)

	keyFile := filepath.Join(t.TempDir(), "key.pem")
	if err := os.WriteFile(keyFile, []byte("fake-key"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	tokenFile := filepath.Join(t.TempDir(), "token")
	cfg := Config{AppID: "123", PrivateKeyFile: keyFile, InstallationID: "456"}

	stop := make(chan struct{})

	if _, err := watchWithScript(context.Background(), []byte(counterMintScript), cfg, tokenFile, 20*time.Millisecond, stop); err != nil {
		t.Fatalf("watchWithScript returned error: %v", err)
	}

	// First mint happened synchronously; wait for the periodic loop to
	// remint at least once more.
	waitForFileContent(t, tokenFile, "token-2", time.Second)

	close(stop)

	// Give any in-flight tick a moment to land, then snapshot the counter
	// and confirm it stops moving.
	time.Sleep(50 * time.Millisecond)
	countAfterStop, err := os.ReadFile(counterFile)
	if err != nil {
		t.Fatalf("ReadFile(counterFile): %v", err)
	}

	time.Sleep(100 * time.Millisecond)
	countLater, err := os.ReadFile(counterFile)
	if err != nil {
		t.Fatalf("ReadFile(counterFile): %v", err)
	}
	if string(countAfterStop) != string(countLater) {
		t.Errorf("counter kept increasing after stop was closed: %q -> %q", countAfterStop, countLater)
	}
}

// TestWatch_PeriodicRemintFailureBacksOffLogsAndRecovers exercises a periodic
// re-mint that fails exactly once (the first tick after the synchronous
// initial mint) and confirms the loop: (a) doesn't get stuck waiting out the
// full interval before retrying -- it recovers via a short failure backoff
// -- and (b) logs the failure to stderr, mirroring CI's sibling loop
// (.github/actions/gh-token-refresher/action.yml) which drops to a 5-minute
// retry and logs "gh-token-refresher: mint attempt failed, retrying in 5m"
// on a failed re-mint.
func TestWatch_PeriodicRemintFailureBacksOffLogsAndRecovers(t *testing.T) {
	origBackoff := remintFailureBackoff
	remintFailureBackoff = 15 * time.Millisecond
	t.Cleanup(func() { remintFailureBackoff = origBackoff })

	counterFile := filepath.Join(t.TempDir(), "counter")
	t.Setenv("FAKE_COUNTER_FILE", counterFile)
	t.Setenv("FAKE_FAILED_ONCE_FILE", filepath.Join(t.TempDir(), "failed-once"))

	keyFile := filepath.Join(t.TempDir(), "key.pem")
	if err := os.WriteFile(keyFile, []byte("fake-key"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	tokenFile := filepath.Join(t.TempDir(), "token")
	cfg := Config{AppID: "123", PrivateKeyFile: keyFile, InstallationID: "456"}

	// Capture stderr for the duration of the test so we can assert the
	// failed re-mint gets logged.
	origStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = origStderr })

	stop := make(chan struct{})

	if _, err := watchWithScript(context.Background(), []byte(failOnceThenCounterMintScript), cfg, tokenFile, 500*time.Millisecond, stop); err != nil {
		t.Fatalf("watchWithScript returned error: %v", err)
	}

	// count=1 is the synchronous initial mint; count=2 is the periodic
	// re-mint that fails; count=3 is the recovered re-mint (via the short
	// failure backoff, not the full interval). The 750ms deadline is chosen
	// so only the correct short-backoff path (interval + backoff ≈ 515ms)
	// lands token-3 in time; the broken "always wait interval" path
	// (interval + interval ≈ 1000ms) times out and fails this test.
	waitForFileContent(t, tokenFile, "token-3", 750*time.Millisecond)

	close(stop)

	if closeErr := w.Close(); closeErr != nil {
		t.Fatalf("close stderr pipe writer: %v", closeErr)
	}
	captured, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read captured stderr: %v", err)
	}
	got := string(captured)
	if !strings.Contains(got, "ghapptoken") {
		t.Errorf("captured stderr = %q, want it to name \"ghapptoken\"", got)
	}
	if !strings.Contains(got, remintFailureBackoff.String()) {
		t.Errorf("captured stderr = %q, want it to mention the backoff duration %s", got, remintFailureBackoff)
	}
}

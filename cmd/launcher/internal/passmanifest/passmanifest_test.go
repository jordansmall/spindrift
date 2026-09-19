package passmanifest

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/landdelta"
	"spindrift.dev/launcher/internal/usage"
)

// Read's degrade-not-error contract (issue #2983): a path that never
// existed (no outbox mounted, or a non-orchestrator box) returns (nil, nil)
// so callers treat "no manifest" like "empty manifest" without checking
// os.IsNotExist themselves.
func TestReadMissingFileReturnsNilNil(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")

	entries, err := Read(path)
	if err != nil {
		t.Fatalf("Read: got err %v, want nil", err)
	}
	if entries != nil {
		t.Errorf("Read: got %+v, want nil", entries)
	}
}

func TestReadEmptyArrayReturnsEmptySlice(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(path, []byte("[]"), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, err := Read(path)
	if err != nil {
		t.Fatalf("Read: got err %v, want nil", err)
	}
	if len(entries) != 0 {
		t.Errorf("Read: got %+v, want empty", entries)
	}
}

func TestWriteThenReadRoundTrips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	want := []Entry{
		{Pass: 1, Kind: "implement", Verdict: "", OutcomeFound: false, Usage: usage.Usage{InputTokens: 10}},
		{Pass: 2, Kind: "review", Verdict: "BLOCK", OutcomeFound: false, Usage: usage.Usage{InputTokens: 20}},
		{Pass: 3, Kind: "land", Verdict: "", OutcomeFound: true, Usage: usage.Usage{InputTokens: 30}},
	}

	Write(path, want)

	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Read: got %+v, want %+v", got, want)
	}
}

// Pins the on-disk key names of a land entry's LandDelta (issue #3244
// review finding): landdelta.Delta once serialized as bare Go field names
// ("Known"), breaking the snake_case convention every other manifest.json
// field follows. The three cases are the three shapes Delta takes on a land
// entry, so a regression in any one of them shows up here.
func TestWriteLandEntryUsesSnakeCaseDeltaKeys(t *testing.T) {
	cases := []struct {
		name       string
		delta      landdelta.Delta
		wantSubstr string
	}{
		{
			name:       "known counted",
			delta:      landdelta.Delta{Known: true, Files: 2, Insertions: 41, Deletions: 3},
			wantSubstr: `"land_delta":{"known":true,"files":2,"insertions":41,"deletions":3}`,
		},
		{
			name:       "known zero",
			delta:      landdelta.Delta{Known: true},
			wantSubstr: `"land_delta":{"known":true}`,
		},
		{
			name:       "unknown with reason",
			delta:      landdelta.Delta{Known: false, Reason: "no reviewed-commit anchor"},
			wantSubstr: `"land_delta":{"known":false,"reason":"no reviewed-commit anchor"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "manifest.json")
			entries := []Entry{
				{Pass: 1, Kind: "land", OutcomeFound: true, LandDelta: &tc.delta},
			}

			Write(path, entries)

			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}
			got := string(b)
			if !strings.Contains(got, tc.wantSubstr) {
				t.Errorf("manifest JSON = %s, want it to contain %s (snake_case land_delta keys)", got, tc.wantSubstr)
			}
			for _, pascal := range []string{`"Known"`, `"Files"`, `"Insertions"`, `"Deletions"`, `"Reason"`} {
				if strings.Contains(got, pascal) {
					t.Errorf("manifest JSON = %s, want no bare Go field name %s", got, pascal)
				}
			}

			readBack, err := Read(path)
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			if !reflect.DeepEqual(readBack, entries) {
				t.Errorf("Read: got %+v, want %+v", readBack, entries)
			}
		})
	}
}

// A corrupt manifest is a real error the caller logs, unlike the
// missing-file case above, which returns (nil, nil).
func TestReadMalformedJSONReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, err := Read(path)
	if err == nil {
		t.Fatal("Read: got nil err, want non-nil for malformed JSON")
	}
	if entries != nil {
		t.Errorf("Read: got %+v, want nil entries alongside the error", entries)
	}
}

// Read bounds how much of the Box-authored manifest it buffers (issue #2983
// DoS finding): a runaway or prompt-injected Box can write any size it likes
// to the 0o777 outbox mount, and the host calls Read on every console
// refresh tick. Past maxManifestBytes Read returns (nil, err), never the
// reserved (nil, nil) that means "no manifest was ever written".
func TestReadOversizedFileReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	oversized := make([]byte, maxManifestBytes+100)
	for i := range oversized {
		oversized[i] = 'a'
	}
	if err := os.WriteFile(path, oversized, 0o644); err != nil {
		t.Fatal(err)
	}

	entries, err := Read(path)
	if err == nil {
		t.Fatal("Read: got nil err, want non-nil for oversized file")
	}
	if entries != nil {
		t.Errorf("Read: got %+v, want nil entries alongside the error", entries)
	}
}

// A Box with shell access can mkfifo the manifest path (issue #2983 review
// finding), and opening a writerless FIFO read-only blocks forever, freezing
// the operator console that calls Read on every refresh tick. Read opens
// with O_NONBLOCK and stats the descriptor. The 2s deadline keeps a
// regression from hanging the whole test binary.
func TestReadFIFODoesNotHang(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	if err := syscall.Mkfifo(path, 0o644); err != nil {
		t.Fatalf("Mkfifo: %v", err)
	}

	type result struct {
		entries []Entry
		err     error
	}
	done := make(chan result, 1)
	go func() {
		entries, err := Read(path)
		done <- result{entries, err}
	}()

	select {
	case r := <-done:
		if r.err == nil {
			t.Fatal("Read: got nil err, want non-nil for a FIFO manifest path")
		}
		if r.entries != nil {
			t.Errorf("Read: got %+v, want nil entries alongside the error", r.entries)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Read: did not return within 2s -- blocked opening the FIFO")
	}
}

// Reproduces the TOCTOU race in the old Lstat-then-Open pattern (issue #2983
// review finding): a Box can rename a FIFO over the regular file between
// Read's Lstat and its os.Open, and Open then blocks forever on a path Lstat
// had already approved as regular. The swap goroutine and the repeated Read
// loop exist to hit that window.
func TestReadConcurrentFIFOSwapDoesNotHang(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	regular := []byte("[]")

	stop := make(chan struct{})
	swapErrs := make(chan error, 1)
	go func() {
		for {
			select {
			case <-stop:
				swapErrs <- nil
				return
			default:
			}
			// Both swaps go through os.Rename rather than opening path
			// directly: an O_WRONLY open would block on whatever FIFO is
			// there, and racing Read's own O_NONBLOCK open of it would
			// report a broken pipe unrelated to the race under test.
			regularPath := path + ".reg"
			if err := os.WriteFile(regularPath, regular, 0o644); err != nil {
				swapErrs <- err
				return
			}
			if err := os.Rename(regularPath, path); err != nil {
				swapErrs <- err
				return
			}
			fifoPath := path + ".fifo"
			if err := syscall.Mkfifo(fifoPath, 0o644); err != nil {
				swapErrs <- err
				return
			}
			if err := os.Rename(fifoPath, path); err != nil {
				swapErrs <- err
				return
			}
		}
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		type result struct {
			entries []Entry
			err     error
		}
		done := make(chan result, 1)
		go func() {
			entries, err := Read(path)
			done <- result{entries, err}
		}()

		select {
		case <-done:
			// Either outcome is fine; only prompt return is under test.
		// The regression blocks forever, so this bound only separates
		// "returned" from "never returns". At 500ms it tripped on loaded
		// aarch64-darwin CI builders, where the swap goroutine starves this
		// Read of a scheduler slot.
		case <-time.After(5 * time.Second):
			close(stop)
			t.Fatal("Read: did not return within 5s during a concurrent FIFO swap -- blocked opening a stale regular-file verdict")
		}
	}
	close(stop)
	if err := <-swapErrs; err != nil {
		t.Fatalf("swap goroutine: %v", err)
	}
}

// Reproduces the torn-read finding (issue #2983 review): Write used to
// truncate path in place via os.WriteFile, so a Read landing in the
// truncate-then-write window saw a partial file and failed to parse it,
// against pick.go:200's convention that a Read failure leaves prior state
// stale rather than raising a spurious error. A JSON error here is the bug.
func TestWriteIsAtomicUnderConcurrentRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		i := 0
		for {
			select {
			case <-stop:
				return
			default:
			}
			// Cycling the entry count varies the marshaled byte size every
			// iteration. A fixed-shape manifest could get lucky and never
			// expose an overwrite that shrinks the file mid-read.
			n := i%20 + 1
			entries := make([]Entry, n)
			for j := range entries {
				entries[j] = Entry{Pass: j + 1, Kind: "implement", Usage: usage.Usage{InputTokens: j}}
			}
			Write(path, entries)
			i++
		}
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, err := Read(path)
		if err == nil {
			continue
		}
		var syntaxErr *json.SyntaxError
		if errors.As(err, &syntaxErr) || errors.Is(err, io.ErrUnexpectedEOF) {
			close(stop)
			t.Fatalf("Read: got a JSON parse error during concurrent Write: %v (torn read of a non-atomic write)", err)
		}
		t.Fatalf("Read: got unexpected non-JSON error during concurrent Write: %v", err)
	}
	close(stop)
	<-done
}

// A Box with shell access to the 0o777 outbox mount can symlink the manifest
// path at an arbitrary host file (issue #2983 review finding), redirecting
// the host's read. Read passes O_NOFOLLOW, which fails the open outright on
// a symlink at the final path component.
func TestReadSymlinkReturnsError(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	if err := os.WriteFile(target, []byte(`[{"pass":1,"kind":"implement","outcome_found":false,"usage":{}}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "manifest.json")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}

	entries, err := Read(path)
	if err == nil {
		t.Fatal("Read: got nil err, want non-nil for a symlinked manifest path")
	}
	if entries != nil {
		t.Errorf("Read: got %+v, want nil entries alongside the error (not the symlink target's contents)", entries)
	}
}

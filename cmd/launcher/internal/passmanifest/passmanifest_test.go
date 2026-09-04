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

// TestReadMissingFileReturnsNilNil verifies Read's degrade-not-error
// contract for the ordinary case (issue #2983): no manifest was ever
// written (no outbox mounted, or a non-orchestrator box), so the path
// simply doesn't exist. This must return (nil, nil), never an error, so a
// caller can treat "no manifest" identically to "empty manifest" without
// special-casing os.IsNotExist itself.
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

// TestReadEmptyArrayReturnsEmptySlice verifies a present-but-empty manifest
// (a valid JSON `[]`) parses cleanly to a zero-length, non-error result.
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

// TestWriteThenReadRoundTrips verifies Write and Read agree on the wire
// format: a manifest with 2+ entries written via Write must come back from
// Read byte-for-byte equivalent in structure.
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

// TestWriteLandEntryUsesSnakeCaseDeltaKeys pins the on-disk JSON shape of a
// land entry's LandDelta field (issue #3244 review finding): every other
// field in manifest.json follows the snake_case convention
// (outcome_found, usage, land_delta), so landdelta.Delta's own fields must
// carry the same convention rather than serializing as bare Go field names
// (e.g. "Known" instead of "known"). Covers all three cases Delta can take
// on a land entry -- known-counted, known-zero, and unknown-with-reason --
// asserting the actual on-disk key names, not just that the value
// round-trips.
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

			// And it must still round-trip back to an equal Delta.
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

// TestReadMalformedJSONReturnsError verifies a present-but-corrupt manifest
// file is a real error for the caller to log, distinct from the
// missing-file case above.
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

// TestReadOversizedFileReturnsError verifies Read bounds how much of the
// Box-authored manifest file it will buffer into memory (issue #2983 DoS
// finding): manifest.json is written from inside a sandboxed, potentially
// prompt-injected or runaway Box with shell access to a 0o777 outbox mount,
// and Read is called on every console refresh tick as well as once per
// dispatch result on the host. A file past maxManifestBytes must be treated
// as corrupt/suspicious evidence -- a real (nil, err), never the reserved
// (nil, nil) "no manifest ever written" contract -- so an oversized file
// can't silently masquerade as "no manifest" nor get fully buffered.
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

// TestReadFIFODoesNotHang verifies Read never blocks on a non-regular file
// at the manifest path (issue #2983 review finding): manifest.json is
// Box-authored, and a Box with shell access could do `mkfifo
// /outbox/manifest.json` instead of writing a regular file. Opening a FIFO
// read-only with no writer blocks forever, and Read runs synchronously on
// every console refresh tick as well as once per dispatch result on the
// host -- an unbounded block here freezes the whole operator console or
// wedges settle. Read must reject the FIFO via O_NONBLOCK on the open call
// itself (checked by stat-ing the resulting descriptor) and return promptly
// with a non-nil error, never hang. The test itself is
// bounded by a short deadline so a regression fails fast instead of hanging
// the whole test binary/CI run.
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

// TestReadConcurrentFIFOSwapDoesNotHang reproduces the TOCTOU race in the
// Lstat-then-Open pattern (issue #2983 review finding): a Box with shell
// access to the 0o777 outbox mount can rename a FIFO over the regular file
// at the manifest path *between* Read's Lstat check and its subsequent
// os.Open call. Lstat approves the regular file that existed at check time,
// but by the time Open runs, a FIFO may be sitting at that path instead --
// and Open, unlike the Lstat check, blocks forever with no writer on the
// other end. A goroutine continuously swaps a FIFO and a regular file at
// the manifest path while a concurrent loop calls Read repeatedly; every
// individual Read call must return within a short deadline -- Read must
// never observe a stale "it was regular" verdict from a check that no
// longer describes the object it's about to open.
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
			// Swap in a regular file, then race a FIFO over it. Both
			// swap into place via os.Rename rather than opening path
			// directly -- opening path with O_WRONLY while a FIFO
			// happens to be there would itself block waiting for a
			// reader (and racing Read's own brief O_NONBLOCK open/close
			// of that FIFO would surface as a spurious broken-pipe
			// write error here, unrelated to the race under test).
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
			// Either outcome (nil entries + error for a FIFO, or a
			// parsed manifest for a regular file) is acceptable --
			// the only thing under test is that Read returns promptly.
		// The regression this guards blocks *forever* -- an open of a
		// writerless FIFO never returns -- so the bound only has to
		// separate "returned" from "never returns". A tighter one buys
		// no detection power and costs flakiness: at 500ms this tripped
		// on loaded aarch64-darwin CI builders, where the swap
		// goroutine's own syscall storm can starve this Read's
		// goroutine of a scheduler slot for longer than that.
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

// TestWriteIsAtomicUnderConcurrentRead reproduces the torn-read finding
// (issue #2983 review): Write used to truncate path in place via
// os.WriteFile, so a Read landing inside the truncate-then-write window
// could observe a partial file and fail with a JSON parse error even though
// nothing was actually wrong with the manifest -- just unlucky timing. This
// contradicts pick.go:200's documented convention that a Read failure here
// should leave prior state stale rather than surface a spurious error.
//
// One goroutine calls Write in a tight loop with a varying entry count (so
// the manifest's own on-disk byte size changes every iteration, widening
// whatever truncate-then-write window a fixed-size overwrite might not
// expose), while a concurrent goroutine calls Read in a tight loop for a
// bounded duration. Every Read must either succeed or -- before the first
// Write has ever run -- see the legitimate "no manifest yet" (nil, nil)
// case; it must never surface a JSON syntax/unexpected-EOF error, which
// would mean it observed a torn write. Mirrors
// TestReadConcurrentFIFOSwapDoesNotHang's goroutine+bounded-loop shape.
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
			// Cycling the entry count from 1 to 20 varies the marshaled
			// byte size every iteration, unlike a fixed-shape manifest,
			// which could get lucky and never expose an overwrite that
			// shrinks the file mid-read.
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

// TestReadSymlinkReturnsError verifies Read refuses to follow a symlink at
// the manifest path (issue #2983 review finding): a Box with shell access
// to the 0o777 outbox mount could do `ln -s /etc/shadow
// /outbox/manifest.json` to redirect the host's read to an arbitrary host
// path. Read must reject the symlink itself -- via O_NOFOLLOW on the open
// call, which fails the open outright on a symlink at the final path
// component -- rather than opening through it and returning the target
// file's contents.
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

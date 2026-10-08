package dispatchrecord

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/hostpaths"
)

// concurrentLog writes issue-<n>.log, a stamped log for its own Dispatch.
func concurrentLog(t *testing.T, root string, n int) {
	t.Helper()
	if err := writeConcurrentLog(root, n); err != nil {
		t.Fatal(err)
	}
}

// writeConcurrentLog is concurrentLog for goroutines, which must not call t.Fatal.
func writeConcurrentLog(root string, n int) error {
	key := fmt.Sprint(100 + n)
	claim := stampClaim.Add(time.Duration(n) * time.Minute)
	stamp := opLine(claude.SpindriftOp{Op: "dispatch_start", Start: &claude.DispatchStart{
		RecordID:    RecordID("work", key, claim),
		Kind:        "work",
		DispatchKey: key,
		ClaimTime:   claim,
		Started:     claim,
		Revision:    "abc123",
		Driver:      "claude",
	}})
	lines := append([]string{stamp}, workLog("2026-05-01T09:00:00Z", float64(n+1))...)
	dir := hostpaths.LogDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, fmt.Sprintf("issue-%s.log", key)), []byte(strings.Join(lines, "")), 0o644)
}

// runConcurrently releases n goroutines at once and returns their errors.
func runConcurrently(n int, fn func(i int) error) []error {
	var ready, done sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, n)
	ready.Add(n)
	done.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer done.Done()
			ready.Done()
			<-start
			errs[i] = fn(i)
		}()
	}
	ready.Wait()
	close(start)
	done.Wait()
	return errs
}

func wantRecordIDs(t *testing.T, root string, n int) {
	t.Helper()
	var want []string
	for i := 0; i < n; i++ {
		want = append(want, RecordID("work", fmt.Sprint(100+i), stampClaim.Add(time.Duration(i)*time.Minute)))
	}
	sort.Strings(want)
	got := ids(records(t, openStore(t, root)))
	sort.Strings(got)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("records = %v, want %v", got, want)
	}
}

// Several Daemon slots each hold their own Store on one root (issue #4788).
func TestStoreConcurrentOpenAndIngestFromSeparateHandles(t *testing.T) {
	const n = 8
	root := t.TempDir()
	for i := 0; i < n; i++ {
		concurrentLog(t, root, i)
	}
	errs := runConcurrently(n, func(int) error {
		s, err := Open(root)
		if err != nil {
			return err
		}
		defer s.Close()
		_, err = s.Ingest()
		return err
	})
	for i, err := range errs {
		if err != nil {
			t.Errorf("slot %d: %v", i, err)
		}
	}
	wantRecordIDs(t, root, n)
}

func TestStoreConcurrentSettlesEachIngestOwnLog(t *testing.T) {
	const n = 8
	root := t.TempDir()
	errs := runConcurrently(n, func(i int) error {
		s, err := Open(root)
		if err != nil {
			return err
		}
		defer s.Close()
		if err := writeConcurrentLog(root, i); err != nil {
			return err
		}
		_, err = s.Ingest()
		return err
	})
	for i, err := range errs {
		if err != nil {
			t.Errorf("slot %d: %v", i, err)
		}
	}
	wantRecordIDs(t, root, n)
}

// Records reads records, passes and prompt_hashes from one snapshot, so an
// Ingest committing between them never shows a passes row without its record
// (issue #4808).
func TestStoreRecordsConsistentUnderConcurrentIngest(t *testing.T) {
	const logs = 300
	root := t.TempDir()
	reader, writer := openStore(t, root), openStore(t, root)

	var writeErr error
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for i := 0; i < logs; i++ {
			if writeErr = writeConcurrentLog(root, i); writeErr != nil {
				return
			}
			if _, writeErr = writer.Ingest(); writeErr != nil {
				return
			}
		}
	}()
	reads, failed := 0, 0
	var firstErr error
	for done := false; !done; {
		select {
		case <-finished:
			done = true
		default:
		}
		reads++
		if _, err := reader.Records(); err != nil {
			failed++
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	if failed > 0 {
		t.Errorf("Records failed %d of %d calls, first: %v", failed, reads, firstErr)
	}
	wantRecordIDs(t, root, logs)
}

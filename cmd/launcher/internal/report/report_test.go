package report

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

func getenvFor(vals map[string]string) func(string) string {
	return func(k string) string { return vals[k] }
}

func TestFromEnv_RoundTrip(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer r.Close()
	t.Cleanup(func() { w.Close() })

	fd := int(w.Fd())
	var stderr bytes.Buffer
	rep := FromEnv(getenvFor(map[string]string{"SPINDRIFT_REPORT_FD": fmt.Sprint(fd)}), &stderr)
	if rep == nil {
		t.Fatalf("FromEnv returned nil, stderr: %s", stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr on success: %s", stderr.String())
	}

	rep.Box("3627", "initial")
	rep.Settled("3627", "merged", "landed clean")
	w.Close()

	scanner := bufio.NewScanner(r)
	var recs []Record
	for scanner.Scan() {
		var rec Record
		if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
			t.Fatalf("unmarshal %q: %v", scanner.Text(), err)
		}
		recs = append(recs, rec)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("got %d records, want 2: %+v", len(recs), recs)
	}
	wantBox := Record{Event: "box", Issue: "3627", Phase: "initial"}
	if recs[0] != wantBox {
		t.Errorf("box record = %+v, want %+v", recs[0], wantBox)
	}
	wantSettled := Record{Event: "settled", Issue: "3627", State: "merged", Note: "landed clean"}
	if recs[1] != wantSettled {
		t.Errorf("settled record = %+v, want %+v", recs[1], wantSettled)
	}
}

func TestFromEnv_Unset(t *testing.T) {
	var stderr bytes.Buffer
	rep := FromEnv(getenvFor(nil), &stderr)
	if rep != nil {
		t.Fatalf("FromEnv(unset) = %v, want nil", rep)
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %s", stderr.String())
	}
	// Nil-receiver methods must be no-ops, not panics.
	rep.Box("1", "initial")
	rep.Settled("1", "merged", "")
}

func TestFromEnv_RegularFileRefused(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "report-fd")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	defer f.Close()

	fd := int(f.Fd())
	var stderr bytes.Buffer
	rep := FromEnv(getenvFor(map[string]string{"SPINDRIFT_REPORT_FD": fmt.Sprint(fd)}), &stderr)
	if rep != nil {
		t.Fatalf("FromEnv(regular file) = %v, want nil", rep)
	}
	if !strings.Contains(stderr.String(), "not a pipe") {
		t.Errorf("stderr = %q, want mention of 'not a pipe'", stderr.String())
	}

	rep.Box("1", "initial") // no-op on nil, must not touch the file

	got, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("file was written: %q", got)
	}
}

func TestFromEnv_Malformed(t *testing.T) {
	cases := []struct {
		name string
		val  string
	}{
		{"non-numeric", "abc"},
		{"negative", "-1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			rep := FromEnv(getenvFor(map[string]string{"SPINDRIFT_REPORT_FD": tc.val}), &stderr)
			if rep != nil {
				t.Fatalf("FromEnv(%q) = %v, want nil", tc.val, rep)
			}
			if stderr.Len() == 0 {
				t.Errorf("FromEnv(%q): expected a stderr refusal line", tc.val)
			}
		})
	}
}

// clearCloseOnExec clears FD_CLOEXEC on fd, raw, on both linux and darwin.
// os.Pipe already returns close-on-exec descriptors, so a test that spawns a
// subprocess against a bare os.Pipe fd would pass with or without
// FromEnv's own syscall.CloseOnExec call — it is exercising the Go runtime's
// default, not this package's guarantee. The production fd this package
// guards is instead handed down via exec.Cmd.ExtraFiles (runner.go), and
// os/exec explicitly clears FD_CLOEXEC on every ExtraFiles descriptor before
// exec so the child can use it — so this clear reproduces that starting
// state, making a deleted syscall.CloseOnExec(fd) in FromEnv observable as a
// test failure (issue #3627).
func clearCloseOnExec(t *testing.T, fd int) {
	t.Helper()
	if _, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), uintptr(syscall.F_SETFD), 0); errno != 0 {
		t.Fatalf("fcntl F_SETFD clear on fd %d: %v", fd, errno)
	}
}

// TestFromEnv_CloseOnExec proves FromEnv itself re-establishes close-on-exec
// on a descriptor that starts without it — the ExtraFiles case above — so a
// subprocess spawned afterward cannot write to the same descriptor number.
func TestFromEnv_CloseOnExec(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer r.Close()
	defer w.Close()

	fd := int(w.Fd())
	clearCloseOnExec(t, fd)

	var stderr bytes.Buffer
	rep := FromEnv(getenvFor(map[string]string{"SPINDRIFT_REPORT_FD": fmt.Sprint(fd)}), &stderr)
	if rep == nil {
		t.Fatalf("FromEnv returned nil, stderr: %s", stderr.String())
	}

	script := fmt.Sprintf("echo hi >&%d", fd)
	cmd := exec.Command("/bin/sh", "-c", script)
	out, runErr := cmd.CombinedOutput()
	if runErr == nil {
		t.Fatalf("subprocess write to fd %d succeeded, want failure (fd should be closed); output: %s", fd, out)
	}
}

// TestFromEnv_CloseOnExec_ControlFdStaysOpenWithoutFromEnv is the mutation
// control for the test above: with FD_CLOEXEC cleared the same way but
// FromEnv never called, the same spawn must succeed. Without this, a broken
// clearCloseOnExec (e.g. a no-op) would make TestFromEnv_CloseOnExec pass
// for the wrong reason — the write failing regardless of FromEnv — and
// mutation-deleting syscall.CloseOnExec from FromEnv would go unnoticed.
func TestFromEnv_CloseOnExec_ControlFdStaysOpenWithoutFromEnv(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer r.Close()
	defer w.Close()

	fd := int(w.Fd())
	clearCloseOnExec(t, fd)

	script := fmt.Sprintf("echo hi >&%d", fd)
	cmd := exec.Command("/bin/sh", "-c", script)
	out, runErr := cmd.CombinedOutput()
	if runErr != nil {
		t.Fatalf("subprocess write to fd %d failed even without FromEnv, want success (control is broken): %v, output: %s", fd, runErr, out)
	}
}

// TestFromEnv_DoesNotOwnDescriptor pins the fix for the bug this package's
// comment on the fd field describes: a Reporter going out of scope must
// never close its descriptor, because it never owned it — the parent that
// passed it down did. Before the fix, FromEnv wrapped fd in an *os.File,
// which Go's finalizer closes once unreachable; dropping the Reporter and
// forcing a GC+finalizer pass here reproduced exactly that close.
func TestFromEnv_DoesNotOwnDescriptor(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer r.Close()
	defer w.Close()

	fd := int(w.Fd())
	var stderr bytes.Buffer
	rep := FromEnv(getenvFor(map[string]string{"SPINDRIFT_REPORT_FD": fmt.Sprint(fd)}), &stderr)
	if rep == nil {
		t.Fatalf("FromEnv returned nil, stderr: %s", stderr.String())
	}

	// Drop the only reference to rep and force finalizers to run. If
	// anything in this package still attaches ownership/a finalizer to fd,
	// this closes it out from under us.
	rep = nil
	runtime.GC()
	runtime.GC()

	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		t.Fatalf("fd %d was closed out from under us: Fstat: %v", fd, err)
	}

	if _, err := syscall.Write(fd, []byte("still writable\n")); err != nil {
		t.Fatalf("fd %d not writable after Reporter went out of scope: %v", fd, err)
	}
}

// TestSettled_LongNoteIsClippedToFitMaxLine drives Settled with a ~5000-byte
// note over a real pipe: the daemon reader's line cap is MaxLine bytes
// including the trailing newline (issue #3627's review finding), so emit
// must shorten Note until the encoded line fits, rather than let a long
// human-written reason discard the whole record.
func TestSettled_LongNoteIsClippedToFitMaxLine(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer r.Close()
	t.Cleanup(func() { w.Close() })

	rep := &Reporter{fd: int(w.Fd())}
	note := strings.Repeat("a", 5000)
	rep.Settled("123", "blocked", note)
	w.Close()

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, MaxLine), MaxLine)
	if !scanner.Scan() {
		t.Fatalf("scan: no line produced, err: %v", scanner.Err())
	}
	line := scanner.Bytes()
	// +1 for the newline the reader also counts against MaxLine.
	if got := len(line) + 1; got > MaxLine {
		t.Fatalf("line length %d (incl. newline) exceeds MaxLine %d", got, MaxLine)
	}
	var rec Record
	if err := json.Unmarshal(line, &rec); err != nil {
		t.Fatalf("unmarshal %q: %v", line, err)
	}
	if rec.Event != "settled" || rec.Issue != "123" || rec.State != "blocked" {
		t.Errorf("rec = %+v, want event=settled issue=123 state=blocked", rec)
	}
	if !strings.HasSuffix(rec.Note, "…") {
		t.Errorf("note = %q, want a trailing clip mark", rec.Note)
	}
	trimmed := strings.TrimSuffix(rec.Note, "…")
	if !strings.HasPrefix(note, trimmed) {
		t.Errorf("note %q is not a prefix of the original", trimmed)
	}
	if len(trimmed) >= len(note) {
		t.Errorf("note was not actually shortened: len(trimmed)=%d, len(original)=%d", len(trimmed), len(note))
	}
	if scanner.Scan() {
		t.Errorf("unexpected second line: %q", scanner.Bytes())
	}
}

// assertMaximalClip asserts the whole clipping contract for a Record whose
// Note is too long to fit: the line fits MaxLine, the note survives carrying
// the clip mark, and the clip is *maximal* — one more rune of the original
// would have pushed the encoded line over. Each long-note case below only has
// to state its input and reuse this.
func assertMaximalClip(t *testing.T, rec Record) {
	t.Helper()

	orig := []rune(rec.Note)
	line, ok := clippedLine(rec)
	if !ok {
		t.Fatalf("clippedLine reported ok=false for a %d-rune note; want a clipped record", len(orig))
	}
	if len(line) > MaxLine {
		t.Fatalf("line length %d (incl. newline) exceeds MaxLine %d", len(line), MaxLine)
	}
	var got Record
	if err := json.Unmarshal(bytes.TrimSuffix(line, []byte("\n")), &got); err != nil {
		t.Fatalf("unmarshal %q: %v", line, err)
	}
	if got.Event != rec.Event || got.Issue != rec.Issue || got.State != rec.State {
		t.Errorf("rec = %+v, want event/issue/state of %+v", got, rec)
	}
	if got.Note == "" {
		t.Fatalf("note field absent from a %d-byte line; a dropped reason is indistinguishable from no reason", len(line))
	}
	if !strings.HasSuffix(got.Note, clipMark) {
		t.Fatalf("note (%d runes) has no trailing clip mark %q", len([]rune(got.Note)), clipMark)
	}
	kept := []rune(strings.TrimSuffix(got.Note, clipMark))
	if len(kept) >= len(orig) {
		t.Fatalf("note was not shortened: kept %d runes of %d", len(kept), len(orig))
	}
	if string(orig[:len(kept)]) != string(kept) {
		t.Fatalf("surviving note is not a prefix of the original")
	}
	next := rec
	next.Note = string(orig[:len(kept)+1]) + clipMark
	longer, err := json.Marshal(next)
	if err != nil {
		t.Fatalf("marshal one-rune-longer candidate: %v", err)
	}
	if len(longer)+1 <= MaxLine {
		t.Fatalf("clip is not maximal: kept %d of %d runes for a %d-byte line, but %d runes encode to %d bytes, still within MaxLine %d",
			len(kept), len(orig), len(line), len(kept)+1, len(longer)+1, MaxLine)
	}
}

// TestSettled_NoteAllEscapedCharsStillFits covers the case a raw-byte bound
// gets wrong: a note made entirely of characters JSON escapes (each raw byte
// costs several bytes on the wire) must still yield a line within MaxLine —
// and must still carry as much of the note as fits, rather than clipping by a
// byte delta until nothing is left.
func TestSettled_NoteAllEscapedCharsStillFits(t *testing.T) {
	assertMaximalClip(t, Record{
		Event: EventSettled,
		Issue: "123",
		State: "blocked",
		Note:  strings.Repeat(`"`, 5000),
	})
}

// TestSettled_LongNoteWithEscapedPunctuationKeepsMostOfIt is the realistic
// version of the case above: prose quoting a diff or HTML fragment, so only
// some characters escape. A byte-delta clip degrades monotonically here — the
// longer and more detailed the reason, the less of it survives — so this
// pins maximality on mixed input, not just on an all-escaped extreme.
func TestSettled_LongNoteWithEscapedPunctuationKeepsMostOfIt(t *testing.T) {
	assertMaximalClip(t, Record{
		Event: EventSettled,
		Issue: "123",
		State: "blocked",
		Note:  strings.Repeat("checks failed: expected <div> & got <span>; retry aborted. ", 135),
	})
}

// TestEmit_ShortRecordUnchanged proves a record that already fits is
// written byte-identically to before this change — no mark, no re-encode
// difference.
func TestEmit_ShortRecordUnchanged(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer r.Close()
	t.Cleanup(func() { w.Close() })

	rep := &Reporter{fd: int(w.Fd())}
	rep.Settled("123", "merged", "landed clean")
	w.Close()

	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	want := `{"event":"settled","issue":"123","state":"merged","note":"landed clean"}` + "\n"
	if string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestClip_LastRuneOverflowKeepsAllButOne probes the top of the search range,
// the one input where the maximal prefix is every rune but the last: the note
// ends in a `<`, which escapes to six bytes, so dropping just that rune buys
// more room than the three-byte clip mark costs. An upper bound one rune too
// low silently clips further than needed on exactly this shape.
func TestClip_LastRuneOverflowKeepsAllButOne(t *testing.T) {
	rec := Record{Event: EventSettled, Issue: "123", State: "blocked", Note: "<"}
	for {
		l, err := json.Marshal(rec)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if len(l)+1 > MaxLine {
			break
		}
		rec.Note = "a" + rec.Note
	}
	assertMaximalClip(t, rec)
	kept := len([]rune(rec.Note)) - 1
	line, _ := clippedLine(rec)
	var got Record
	if err := json.Unmarshal(bytes.TrimSuffix(line, []byte("\n")), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if n := len([]rune(strings.TrimSuffix(got.Note, clipMark))); n != kept {
		t.Errorf("kept %d runes, want %d (all but the final escaped one)", n, kept)
	}
}

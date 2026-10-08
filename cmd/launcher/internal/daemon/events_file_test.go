package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dispatchkey"
)

func eventsLoopRun(t *testing.T, em *Emitter) {
	t.Helper()
	r := &scriptedRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 0}, {Exit: 0}, {Exit: 0}, {Exit: 5}}}
	Loop(context.Background(), testConfig(1), r, em, &testClock{})
}

func readEventsFileT(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func TestEventsFileMirrorsStdoutLineForLine(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	em := newTestEmitterErr(&stdout, &stderr)
	em.TeeEvents(NewEventsFile(dir))

	eventsLoopRun(t, em)

	if stdout.Len() == 0 {
		t.Fatal("loop emitted nothing to stdout")
	}
	if got := readEventsFileT(t, filepath.Join(dir, eventsFileName)); got != stdout.String() {
		t.Fatalf("events file != stdout\nfile:   %q\nstdout: %q", got, stdout.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dir, eventsFileName+rotatedSuffix)); !os.IsNotExist(err) {
		t.Errorf("rotated generation exists below the cap (stat err = %v)", err)
	}
}

func TestEventsFileRotatesKeepingOneGeneration(t *testing.T) {
	dir := t.TempDir()
	f := NewEventsFile(dir)
	f.max = 10
	cur := filepath.Join(dir, eventsFileName)
	old := cur + rotatedSuffix

	for _, line := range []string{"aaaa\n", "bbbb\n"} {
		if err := f.append([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	// 10 bytes exactly fit: no rotation yet.
	if got := readEventsFileT(t, cur); got != "aaaa\nbbbb\n" {
		t.Fatalf("current = %q", got)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("rotated early (stat err = %v)", err)
	}

	if err := f.append([]byte("cccc\n")); err != nil {
		t.Fatal(err)
	}
	if got := readEventsFileT(t, old); got != "aaaa\nbbbb\n" {
		t.Errorf(".1 = %q, want the earlier lines", got)
	}
	if got := readEventsFileT(t, cur); got != "cccc\n" {
		t.Errorf("current = %q, want the later line", got)
	}

	// Second rotation replaces the earlier .1.
	for _, line := range []string{"dddd\n", "eeee\n"} {
		if err := f.append([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	if got := readEventsFileT(t, old); got != "cccc\ndddd\n" {
		t.Errorf(".1 after second rotation = %q", got)
	}
	if got := readEventsFileT(t, cur); got != "eeee\n" {
		t.Errorf("current after second rotation = %q", got)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Errorf("dir holds %d files, want exactly 2", len(entries))
	}
}

func TestEventsFileRotatesAtLoopSeam(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	em := newTestEmitterErr(&stdout, &stderr)
	f := NewEventsFile(dir)
	f.max = 200
	em.TeeEvents(f)

	eventsLoopRun(t, em)

	cur := readEventsFileT(t, filepath.Join(dir, eventsFileName))
	old := readEventsFileT(t, filepath.Join(dir, eventsFileName+rotatedSuffix))
	if int64(len(cur)) > f.max || int64(len(old)) > f.max {
		t.Errorf("a generation exceeds the cap: cur=%d old=%d", len(cur), len(old))
	}
	if !strings.HasSuffix(stdout.String(), cur) {
		t.Errorf("current generation is not the tail of stdout")
	}
	if !strings.HasSuffix(strings.TrimSuffix(stdout.String(), cur), old) {
		t.Errorf("rotated generation does not directly precede the current one")
	}
}

func TestEventsFileWriteFailureLeavesStdoutIntact(t *testing.T) {
	dir := t.TempDir()
	// A directory squatting on the Events path makes every append fail.
	if err := os.Mkdir(filepath.Join(dir, eventsFileName), 0o755); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr, want bytes.Buffer
	em := newTestEmitterErr(&stdout, &stderr)
	em.TeeEvents(NewEventsFile(dir))
	eventsLoopRun(t, em)

	ref := newTestEmitter(&want)
	eventsLoopRun(t, ref)

	if stdout.String() != want.String() || stdout.Len() == 0 {
		t.Fatalf("stdout lost lines on events-file failure:\n got %q\nwant %q", stdout.String(), want.String())
	}
	if !strings.Contains(stderr.String(), "events file write failed") {
		t.Errorf("stderr = %q, want it to name the events file failure", stderr.String())
	}
}

// A restarted daemon builds a fresh EventsFile over the same path; it must
// extend the earlier run's record, not truncate it.
func TestEventsFileAppendsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	cur := filepath.Join(dir, eventsFileName)

	if err := NewEventsFile(dir).append([]byte("first\n")); err != nil {
		t.Fatal(err)
	}
	if err := NewEventsFile(dir).append([]byte("second\n")); err != nil {
		t.Fatal(err)
	}

	if got := readEventsFileT(t, cur); got != "first\nsecond\n" {
		t.Errorf("events file = %q, want both runs' lines", got)
	}
}

func TestReadEvents(t *testing.T) {
	dir := t.TempDir()
	line := func(rev string) string {
		b, err := json.Marshal(Event{V: eventVersion, Time: "2026-10-07T12:00:00Z", Event: "box", Revision: rev, Key: dispatchkey.Issue("42")})
		if err != nil {
			t.Fatal(err)
		}
		return string(b) + "\n"
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if evs, err := ReadEvents(dir); err != nil || len(evs) != 0 {
		t.Fatalf("ReadEvents on a missing file = %v, %v; want none, nil", evs, err)
	}

	write(eventsFileName+rotatedSuffix, line("old")+"not json\n")
	write(eventsFileName, line("mid")+"\n"+`{"issue":"1","chore":"x"}`+"\n"+line("new"))

	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, ev := range evs {
		got = append(got, ev.Revision)
	}
	if want := []string{"old", "mid", "new"}; !slices.Equal(got, want) {
		t.Errorf("revisions = %v, want %v (oldest first, undecodable lines skipped)", got, want)
	}
	if evs[0].Key != dispatchkey.Issue("42") {
		t.Errorf("Key = %v, want issue 42", evs[0].Key)
	}
}

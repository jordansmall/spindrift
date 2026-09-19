package console

import (
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/passmanifest"
)

// Issue #2983: with no outbox mounted, a non-orchestrator box, or a box that
// has not written its first pass yet, RunningPassState degrades silently to "",
// the same "no heartbeat yet" contract RunningHeartbeat's callers rely on.
func TestRunningPassState_NoManifestFile_ReturnsEmpty(t *testing.T) {
	dir := t.TempDir()

	got := RunningPassState(dir, "42")

	if got != "" {
		t.Errorf("RunningPassState() = %q, want \"\"", got)
	}
}

func TestRunningPassState_SingleEntryNoVerdict_FormatsPassAndKind(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "42", []passmanifest.Entry{
		{Pass: 1, Kind: "implement"},
	})

	got := RunningPassState(dir, "42")

	if want := "pass 1 (implement)"; got != want {
		t.Errorf("RunningPassState() = %q, want %q", got, want)
	}
}

// The fixture carries two entries so the test pins that RunningPassState reads
// the last entry, not the first.
func TestRunningPassState_LastEntryHasVerdict_FormatsWithVerdict(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "42", []passmanifest.Entry{
		{Pass: 1, Kind: "implement"},
		{Pass: 2, Kind: "review", Verdict: "BLOCK"},
	})

	got := RunningPassState(dir, "42")

	if want := "pass 2 (review: BLOCK)"; got != want {
		t.Errorf("RunningPassState() = %q, want %q", got, want)
	}
}

// A present but corrupt manifest degrades to "" rather than pushing a parse
// error to the console, the same contract as a missing file.
func TestRunningPassState_MalformedManifest_ReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	outbox := dispatch.OutboxDirFor(dir, "42")
	if err := os.MkdirAll(outbox, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outbox, "manifest.json"), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := RunningPassState(dir, "42")

	if got != "" {
		t.Errorf("RunningPassState() = %q, want \"\"", got)
	}
}

// manifest.json is Box-authored, untrusted input, so a partial entry (a stale
// file from an older orchestrator binary, say) must format as "pass <N>" with
// no parenthetical, not the malformed "pass <N> ()".
func TestRunningPassState_EmptyKindNoVerdict_FormatsPassOnly(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "42", []passmanifest.Entry{
		{Pass: 3, Kind: "", Verdict: ""},
	})

	got := RunningPassState(dir, "42")

	if want := "pass 3"; got != want {
		t.Errorf("RunningPassState() = %q, want %q", got, want)
	}
}

// With an empty Kind but a recorded Verdict, RunningPassState prints just the
// verdict, not the malformed "pass <N> (: <verdict>)".
func TestRunningPassState_EmptyKindWithVerdict_FormatsPassAndVerdict(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "42", []passmanifest.Entry{
		{Pass: 3, Kind: "", Verdict: "BLOCK"},
	})

	got := RunningPassState(dir, "42")

	if want := "pass 3 (BLOCK)"; got != want {
		t.Errorf("RunningPassState() = %q, want %q", got, want)
	}
}

func writeManifest(t *testing.T, pwd, number string, entries []passmanifest.Entry) {
	t.Helper()
	outbox := dispatch.OutboxDirFor(pwd, number)
	if err := os.MkdirAll(outbox, 0o755); err != nil {
		t.Fatal(err)
	}
	passmanifest.Write(filepath.Join(outbox, "manifest.json"), entries)
}

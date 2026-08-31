package console

import (
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/passmanifest"
)

// TestRunningPassState_NoManifestFile_ReturnsEmpty verifies the common case
// (issue #2983): no outbox mounted, a legacy/non-orchestrator box, or one
// that simply hasn't written its first pass yet — RunningPassState degrades
// silently to "", the same "no heartbeat yet" contract RunningHeartbeat's
// own callers already rely on.
func TestRunningPassState_NoManifestFile_ReturnsEmpty(t *testing.T) {
	dir := t.TempDir()

	got := RunningPassState(dir, "42")

	if got != "" {
		t.Errorf("RunningPassState() = %q, want \"\"", got)
	}
}

// TestRunningPassState_SingleEntryNoVerdict_FormatsPassAndKind verifies a
// manifest with one entry that hasn't recorded a verdict yet formats as
// "pass <N> (<kind>)".
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

// TestRunningPassState_LastEntryHasVerdict_FormatsWithVerdict verifies
// RunningPassState reads the LAST entry of a multi-entry manifest, not the
// first, and includes its verdict when one was recorded.
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

// TestRunningPassState_MalformedManifest_ReturnsEmpty verifies a
// present-but-corrupt manifest file degrades to "" rather than surfacing a
// parse error to the console — the same degrade-silently contract as a
// missing file.
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

// TestRunningPassState_EmptyKindNoVerdict_FormatsPassOnly verifies a manifest
// entry with an empty Kind and no Verdict (e.g. a stale manifest.json from an
// older orchestrator binary, or any other malformed/partial entry —
// manifest.json is Box-authored, untrusted input) formats as "pass <N>" with
// no parenthetical, rather than the malformed "pass <N> ()".
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

// TestRunningPassState_EmptyKindWithVerdict_FormatsPassAndVerdict verifies a
// manifest entry with an empty Kind but a recorded Verdict formats as
// "pass <N> (<verdict>)" using just the verdict, rather than the malformed
// "pass <N> (: <verdict>)".
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

// writeManifest writes entries to number's pass-manifest path under pwd's
// outbox, creating the directory as needed — the same path RunningPassState
// itself reads.
func writeManifest(t *testing.T, pwd, number string, entries []passmanifest.Entry) {
	t.Helper()
	outbox := dispatch.OutboxDirFor(pwd, number)
	if err := os.MkdirAll(outbox, 0o755); err != nil {
		t.Fatal(err)
	}
	passmanifest.Write(filepath.Join(outbox, "manifest.json"), entries)
}

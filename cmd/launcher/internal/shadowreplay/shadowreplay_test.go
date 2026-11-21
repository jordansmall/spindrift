package shadowreplay

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/driver/shadow"
	"spindrift.dev/launcher/internal/driver/shadow/shadowtest"
	"spindrift.dev/launcher/internal/hostop"
)

const fixturesEnv = "SPINDRIFT_CONNECTOR_FIXTURES_DIR"

func fixture(t *testing.T) []byte {
	t.Helper()
	dir := os.Getenv(fixturesEnv)
	if dir == "" {
		t.Skipf("%s is unset; it names the dir holding the fixture Connector .wasm files", fixturesEnv)
	}
	b, err := os.ReadFile(filepath.Join(dir, "driver.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func native() driverkit.Strategy { return shadowtest.Native() }

var corpus = []string{
	`{"type":"system","subtype":"init","session_id":"s1","model":"claude-opus-5-5"}`,
	`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"working"}]}}`,
	"",
	`{"type":"result","subtype":"success","is_error":false,"result":"done","session_id":"s1"}`,
}

func enc(op hostop.SpindriftOp) string { return hostop.EncodeSpindriftOp(op) }

func write(t *testing.T, path string, parts ...string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(parts, "")), 0o600); err != nil {
		t.Fatal(err)
	}
}

func stamp(driver string) string {
	return enc(hostop.SpindriftOp{Op: hostop.OpDispatchStart, Start: &hostop.DispatchStart{RecordID: "r1", Driver: driver}})
}

// live runs the shadow as driver-exec does, on one Pass's driver stream.
func live(t *testing.T, wasm []byte) []hostop.SpindriftOp {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pass.jsonl")
	write(t, path, strings.Join(corpus, "\n"), "\n")
	var ops []hostop.SpindriftOp
	shadow.Run(context.Background(), native(), connectorhost.Load, wasm, path, 0, func(op hostop.SpindriftOp) { ops = append(ops, op) })
	return ops
}

func pass(n int, liveOps []hostop.SpindriftOp) string {
	var b strings.Builder
	b.WriteString(enc(hostop.SpindriftOp{Op: hostop.OpPassStart, Pass: n, Role: "implement"}))
	b.WriteString(enc(hostop.SpindriftOp{Op: hostop.OpPassModel, Model: "m", Effort: "high"}))
	b.WriteString(strings.Join(corpus, "\n") + "\n")
	for _, op := range liveOps {
		b.WriteString(enc(op))
	}
	return b.String()
}

func usage(n int) string {
	return enc(hostop.SpindriftOp{Op: hostop.OpPassUsage, Pass: n, Usage: &hostop.PassUsage{APICalls: 1}})
}

func settled() string {
	return enc(hostop.SpindriftOp{Op: hostop.OpDispatchSettled, Settled: &hostop.DispatchSettled{RecordID: "r1", State: "complete"}})
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func readOps(t *testing.T, path string) []hostop.SpindriftOp {
	t.Helper()
	var ops []hostop.SpindriftOp
	for _, l := range strings.Split(strings.TrimSpace(readFile(t, path)), "\n") {
		var ev hostop.Event
		if err := json.Unmarshal([]byte(l), &ev); err != nil || ev.Type != hostop.TypeSpindriftOp || ev.SpindriftOp == nil {
			t.Fatalf("sidecar line is not a spindrift_op: %q", l)
		}
		ops = append(ops, *ev.SpindriftOp)
	}
	return ops
}

// normalised drops what a rerun cannot reproduce: the timings.
func normalised(op hostop.SpindriftOp) hostop.SpindriftOp {
	if op.ShadowSummary != nil {
		s := *op.ShadowSummary
		s.NativeNsPerLine, s.ShadowNsPerLine = 0, 0
		op.ShadowSummary = &s
	}
	return op
}

func replay(t *testing.T, wasm []byte, archive string) Result {
	t.Helper()
	res, err := Replay(context.Background(), "native", native(), connectorhost.Load, wasm, archive)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestReplayMatchesLiveShadowPerPass(t *testing.T) {
	wasm := fixture(t)
	liveOps := live(t, wasm)
	archive := filepath.Join(t.TempDir(), "issue-12-fix-1.log")
	// The live ops sit in the archive between the driver's lines and pass_usage,
	// as the Box wrote them; the replay must not feed them to the comparator.
	write(t, archive, stamp("native"), pass(1, liveOps), usage(1), pass(2, liveOps), usage(2), settled())

	res := replay(t, wasm, archive)

	want := filepath.Join(filepath.Dir(archive), "issue-12-fix-1.shadow-replay.jsonl")
	if res.Sidecar != want || res.Passes != 2 || res.Skipped != "" {
		t.Fatalf("result = %+v, want sidecar %s and 2 passes", res, want)
	}
	stampLine := strings.TrimSuffix(stamp("native"), "\n")
	if first, _, _ := strings.Cut(readFile(t, want), "\n"); first != stampLine {
		t.Errorf("sidecar opens with %q, want the archive's dispatch_start %q", first, stampLine)
	}
	got := readOps(t, want)
	if len(got) == 0 {
		t.Fatal("sidecar is empty")
	}
	got = got[1:] // past the dispatch_start
	var wantOps []hostop.SpindriftOp
	for range 2 {
		for _, op := range liveOps {
			op = normalised(op)
			if op.ShadowSummary != nil {
				op.ShadowSummary.Replayed = true
			}
			wantOps = append(wantOps, op)
		}
	}
	if len(got) != len(wantOps) || len(got) < 4 {
		t.Fatalf("sidecar has %d ops, want %d (two passes of divergences and a summary)", len(got), len(wantOps))
	}
	for i := range got {
		g, w := normalised(got[i]), wantOps[i]
		gb, _ := json.Marshal(g)
		wb, _ := json.Marshal(w)
		if !bytes.Equal(gb, wb) {
			t.Errorf("op %d:\n got %s\nwant %s", i, gb, wb)
		}
	}
	if s := got[len(got)-1].ShadowSummary; s == nil || !s.Replayed || !s.Loaded || s.Diverged == 0 {
		t.Errorf("last op = %+v, want a replayed summary of a loaded, diverging Connector", got[len(got)-1])
	}
}

// The archive merges the Box's stderr into the stream live shadow read only the
// stdout of, so stderr text inside a Pass must not reach the comparator.
func TestReplayDropsStderrTextInsideAPass(t *testing.T) {
	wasm := fixture(t)
	liveOps := live(t, wasm)
	noisy := []string{
		"==> nix develop failed to launch Driver (rc=1, empty stream) — relaunching in baked env",
		corpus[0],
		"warning: opencode CLI is out of date",
		corpus[1], corpus[2], corpus[3],
		"driver-exec: something on stderr",
	}
	pass1 := enc(hostop.SpindriftOp{Op: hostop.OpPassStart, Pass: 1, Role: "implement"}) +
		strings.Join(noisy, "\n") + "\n"
	archive := filepath.Join(t.TempDir(), "issue-12.log")
	write(t, archive, stamp("native"), pass1, usage(1), settled())

	res := replay(t, wasm, archive)

	got := readOps(t, res.Sidecar)
	if len(got) == 0 {
		t.Fatal("sidecar is empty")
	}
	got = got[1:] // past the dispatch_start
	if len(got) != len(liveOps) {
		t.Fatalf("sidecar has %d ops, want the %d live shadow emitted", len(got), len(liveOps))
	}
	for i := range got {
		want := normalised(liveOps[i])
		if want.ShadowSummary != nil {
			want.ShadowSummary.Replayed = true
		}
		gb, _ := json.Marshal(normalised(got[i]))
		wb, _ := json.Marshal(want)
		if !bytes.Equal(gb, wb) {
			t.Errorf("op %d:\n got %s\nwant %s", i, gb, wb)
		}
	}
}

func TestReplayLeavesTheArchiveUntouched(t *testing.T) {
	wasm := fixture(t)
	archive := filepath.Join(t.TempDir(), "issue-12.log")
	write(t, archive, stamp("native"), pass(1, live(t, wasm)), usage(1), settled())
	before, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	infoBefore, _ := os.Stat(archive)

	replay(t, wasm, archive)
	replay(t, wasm, archive) // a second replay replaces the first sidecar

	after, _ := os.ReadFile(archive)
	infoAfter, _ := os.Stat(archive)
	if !bytes.Equal(before, after) || !infoBefore.ModTime().Equal(infoAfter.ModTime()) {
		t.Error("the archive changed")
	}
	if n := len(readOps(t, strings.TrimSuffix(archive, ".log")+".shadow-replay.jsonl")); n == 0 {
		t.Error("sidecar is empty")
	}
	ents, _ := os.ReadDir(filepath.Dir(archive))
	if len(ents) != 2 {
		t.Errorf("dir holds %d files, want the archive and its sidecar only", len(ents))
	}
}

func TestReplaySkipsAnotherDriversLog(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "issue-12.log")
	write(t, archive, stamp("claude"), pass(1, nil), usage(1))

	res := replay(t, nil, archive)

	if res.Skipped == "" || res.Sidecar != "" {
		t.Errorf("result = %+v, want a skip", res)
	}
	if ents, _ := os.ReadDir(filepath.Dir(archive)); len(ents) != 1 {
		t.Errorf("dir holds %d files, want the archive alone", len(ents))
	}
}

func TestReplaySkipsAnUnstampedLog(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "issue-12.log")
	write(t, archive, pass(1, nil), usage(1))
	if res := replay(t, nil, archive); res.Skipped != "no dispatch_start stamp" || res.Sidecar != "" {
		t.Errorf("result = %+v, want a skip for the missing stamp", res)
	}
}

func TestReplayGivesAnUnendedPassNoSummary(t *testing.T) {
	wasm := fixture(t)
	archive := filepath.Join(t.TempDir(), "issue-12.log")
	write(t, archive, stamp("native"), pass(1, nil), usage(1), pass(2, nil))

	res := replay(t, wasm, archive)

	if res.Passes != 1 {
		t.Fatalf("passes = %d, want only the ended one", res.Passes)
	}
	var sums int
	for _, op := range readOps(t, res.Sidecar) {
		if op.Op == hostop.OpShadowSummary {
			sums++
		}
	}
	if sums != 1 {
		t.Errorf("%d summaries, want 1", sums)
	}
}

func TestReplayWithNoEndedPassWritesNothing(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "issue-12.log")
	write(t, archive, stamp("native"), pass(1, nil))
	res := replay(t, nil, archive)
	if res.Skipped == "" || res.Sidecar != "" {
		t.Errorf("result = %+v, want a skip", res)
	}
	if ents, _ := os.ReadDir(filepath.Dir(archive)); len(ents) != 1 {
		t.Errorf("dir holds %d files, want the archive alone", len(ents))
	}
}

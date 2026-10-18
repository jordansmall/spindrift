package seamtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/driver/driverkit/conformance"
	"spindrift.dev/launcher/internal/hostop"
)

func TestStrategyCorpus(t *testing.T) {
	conformance.Run(t, Strategy, filepath.Join("testdata", "corpus"))
}

func TestStrategyDecodesOneEventPerKind(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "corpus", "canonical", "pass.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	kinds := []driverkit.Kind{
		driverkit.KindStart, driverkit.KindText, driverkit.KindReasoning,
		driverkit.KindToolCall, driverkit.KindToolResult, driverkit.KindUsage,
		driverkit.KindModel, driverkit.KindEffort, driverkit.KindError,
		driverkit.KindUnparsed, driverkit.KindHostOp, driverkit.KindResult,
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != len(kinds) {
		t.Fatalf("fixture has %d lines, want one per kind (%d)", len(lines), len(kinds))
	}
	decode := Strategy.NewDecoder()
	for i, line := range lines {
		evs := decode(line)
		if len(evs) != 1 || evs[0].Kind != kinds[i] {
			t.Errorf("line %d decoded to %+v, want one %s event", i, evs, kinds[i])
		}
	}
	if evs := decode("not json"); len(evs) != 1 || evs[0].Kind != driverkit.KindUnparsed || evs[0].Text != "not json" {
		t.Errorf("malformed line decoded to %+v, want one unparsed event", evs)
	}
	if evs := decode("  "); len(evs) != 0 {
		t.Errorf("blank line decoded to %+v, want none", evs)
	}
}

func TestStrategyResultEventRoundTrips(t *testing.T) {
	d := driverkit.New(Strategy)
	ev, err := d.ResultEvent("STATUS: ok")
	if err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "pass.jsonl")
	if err := os.WriteFile(log, ev, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := d.ResultText(log); err != nil || got != "STATUS: ok\n" {
		t.Errorf("ResultText = %q, %v; want one line STATUS: ok", got, err)
	}
	if got := d.SessionFlags("initial", "o/r", "1", ""); got != "" {
		t.Errorf("SessionFlags = %q, want empty", got)
	}
	if got, err := d.ResolveExit(log, 7); err != nil || got != 7 {
		t.Errorf("ResolveExit = %d, %v; want 7", got, err)
	}
}

// This package links no Driver bundle, so the helper must install the
// Strategy itself.
func TestRegisterDriverInstallsStrategyWithoutBundle(t *testing.T) {
	RegisterDriver(t)
	RegisterDriver(t)
	d, err := driver.New(fakeDriverName)
	if err != nil {
		t.Fatal(err)
	}
	ev, err := d.ResultEvent("seam")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(ev), `{"Kind":"result"`) {
		t.Errorf("claude Driver encoded %q, want the seamtest canonical grammar", ev)
	}
}

func TestStrategyDecodesHostOpLine(t *testing.T) {
	line := hostop.EncodeSpindriftOp(hostop.SpindriftOp{Op: hostop.OpPassStart, Pass: 2, Role: "review"})
	evs := Strategy.NewDecoder()(line)
	if len(evs) != 1 || evs[0].Kind != driverkit.KindHostOp || evs[0].HostOp == nil {
		t.Fatalf("host op line decoded to %+v, want one host_op event", evs)
	}
	if got := evs[0].HostOp; got.Op != hostop.OpPassStart || got.Pass != 2 || got.Role != "review" {
		t.Errorf("host op payload = %+v, want pass_start pass 2 role review", got)
	}
}

func TestLineRoundTripsThroughTheDecoder(t *testing.T) {
	want := driverkit.Event{Kind: driverkit.KindUsage, MessageID: "m1", Model: "m", Timestamp: 1700000000000}
	line := Line(want)
	if !strings.HasSuffix(line, "\n") {
		t.Fatalf("Line %q has no trailing newline", line)
	}
	got := Strategy.NewDecoder()(line)
	if len(got) != 1 || got[0].Kind != want.Kind || got[0].MessageID != want.MessageID || got[0].Model != want.Model || got[0].Timestamp != want.Timestamp {
		t.Errorf("decoded %+v, want %+v", got, want)
	}
}

func TestRegisterStrategyKeepsAnExistingName(t *testing.T) {
	first := Strategy
	first.Name = "seam-register-strategy"
	RegisterStrategy(first)
	second := first
	second.ResultEvent = func(string) ([]byte, error) { return []byte("other"), nil }
	RegisterStrategy(second)
	d, err := driver.New(first.Name)
	if err != nil {
		t.Fatal(err)
	}
	if ev, err := d.ResultEvent("x"); err != nil || string(ev) == "other" {
		t.Errorf("ResultEvent = %q, %v; want the first registration kept", ev, err)
	}
}

func TestMillisParsesRFC3339(t *testing.T) {
	if got := Millis("2026-10-07T12:34:56.789Z"); got != 1791376496789 {
		t.Errorf("Millis = %d, want 1791376496789", got)
	}
}

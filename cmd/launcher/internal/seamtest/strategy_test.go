package seamtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/driver/driverkit/conformance"
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

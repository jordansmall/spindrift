package daemon

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/golden"
	"spindrift.dev/launcher/internal/report"
)

// contractFixtureDir is the Daemon/Dashboard contract fixture (ADR 0060): the
// Dashboard is a separate module that imports nothing from here, so its tests
// read these committed files instead. `nix run .#regen-goldens` rewrites them.
const contractFixtureDir = "../../../../dashboard/testdata/contract"

const (
	contractRev        = "0a1b2c3d4e5f"
	contractHaltDetail = "daemon build /nix/store/aaaa-spindrift-daemon changed at tip " + contractRev

	// contractOlderEvents is how many leading events land in the older
	// generation of the Events file.
	contractOlderEvents = 4
)

// TestContractFixture renders the Events file and every status-file shape the
// Dashboard reads through the real writers and holds the result equal to the
// committed fixture, file set included.
func TestContractFixture(t *testing.T) {
	produced := renderContractFixture(t)
	if golden.Update() {
		if err := os.RemoveAll(contractFixtureDir); err != nil {
			t.Fatal(err)
		}
		for rel, data := range produced {
			path := filepath.Join(contractFixtureDir, rel)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := golden.CompareOrUpdateText(path, data, true); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	var committed []string
	err := filepath.WalkDir(contractFixtureDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(contractFixtureDir, path)
		if err != nil {
			return err
		}
		committed = append(committed, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v (UPDATE_GOLDENS=1 regenerates)", contractFixtureDir, err)
	}
	var want []string
	for rel := range produced {
		want = append(want, rel)
	}
	sort.Strings(want)
	sort.Strings(committed)
	if !slices.Equal(want, committed) {
		t.Fatalf("fixture file set differs (UPDATE_GOLDENS=1 regenerates):\n produced  %v\n committed %v", want, committed)
	}
	for _, rel := range want {
		if err := golden.CompareOrUpdateText(filepath.Join(contractFixtureDir, rel), produced[rel], false); err != nil {
			t.Error(err)
		}
	}
}

// renderContractFixture returns the fixture as slash-separated relative path
// to bytes.
func renderContractFixture(t *testing.T) map[string][]byte {
	t.Helper()
	dir := t.TempDir()
	base := time.Date(2026, 3, 14, 9, 26, 53, 0, time.UTC)
	at := func(sec int) time.Time { return base.Add(time.Duration(sec) * time.Second) }
	stamp := func(sec int) string { return at(sec).Format(time.RFC3339) }

	writeContractEvents(t, dir, at, stamp)

	write := func(sub string, s Status) {
		d := filepath.Join(dir, sub)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		sw := &StatusWriter{dir: d, pid: 4242, host: "daemon-host", started: stamp(0), now: func() time.Time { return at(200) }}
		if err := sw.Write(s); err != nil {
			t.Fatal(err)
		}
	}
	write(".", contractStatus(stamp))
	for _, phase := range []SelfUpdatePhase{SelfUpdateBuilding, SelfUpdateWaiting, SelfUpdateRestarting, SelfUpdateBlocked} {
		su := &SelfUpdate{Phase: phase, Revision: "9f8e7d6c5b4a", Since: stamp(150)}
		if phase == SelfUpdateBlocked {
			su.Reason = "smoke test failed: daemon build exited 1"
		}
		write(filepath.Join("self-update", string(phase)), Status{
			Kinds: []Kind{workKind}, State: StateWorking, SelfUpdate: su,
			Slots: []SlotStatus{{Slot: 0, Phase: PhaseIdle, Since: stamp(90)}}, Checks: []KindCheck{},
		})
	}

	out := map[string][]byte{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)], err = os.ReadFile(path)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// writeContractEvents emits one event per report.Events() name through the
// Emitter and Events file, rolling the file once so both generations exist.
func writeContractEvents(t *testing.T, dir string, at func(int) time.Time, stamp func(int) string) {
	t.Helper()
	tick := 100
	em := NewEmitter(io.Discard, nil, func() time.Time { tick++; return at(tick) })
	ef := NewEventsFile(dir)
	em.TeeEvents(ef)

	const rev = contractRev
	issue, chore := dispatchkey.Issue("1234"), dispatchkey.Chore("docs-drift")
	butler := KindOf(dispatchkind.Butler)
	research := KindOf(dispatchkind.Research)
	slot0, slot1 := intPtr(0), intPtr(1)
	zero, three, ready := intPtr(0), intPtr(3), intPtr(5)
	exit0 := 0
	outcome, _, _ := Interpret(exit0, false)
	halt := Halt{Class: HaltSelfChanged, Kind: workKind, Revision: rev, Detail: contractHaltDetail}

	// The first contractOlderEvents events land in the older generation.
	events := []Event{
		{Event: report.EventChildStart, Time: stamp(100), Kind: workKind, Revision: rev, Slot: slot0, ChildLog: ".git/spindrift/child-0.log"},
		{Event: report.EventBox, Kind: workKind, Revision: rev, Key: issue, Phase: "fix-pass-1", PassLog: ".git/spindrift/pass-1234.log", RecordID: "rec-1234-1", Slot: slot0},
		{Event: report.EventModel, Kind: workKind, Revision: rev, Key: issue, Model: "claude-opus-4-5", ModelRole: "implement", Slot: slot0},
		{Event: report.EventImageBuild, Kind: workKind, Revision: rev, State: string(report.ImageBuildStarted), Slot: slot0},
		{Event: report.EventImageBuild, Kind: workKind, Revision: rev, State: string(report.ImageBuildFinished), Slot: slot0},
		{Event: report.EventSettled, Kind: workKind, Revision: rev, Key: issue, State: "complete", Note: "landed green", PRURL: "https://github.com/acme/widgets/pull/77", RecordID: "rec-1234-2", Slot: slot0},
		{Event: report.EventChildFinish, Kind: workKind, Key: issue, Revision: rev, Exit: &exit0, Outcome: outcome, Slot: slot0},
		{Event: report.EventChildStart, Time: stamp(106), Kind: butler, Revision: rev, Slot: slot1, ChildLog: ".git/spindrift/child-1.log"},
		{Event: report.EventBox, Kind: butler, Revision: rev, Key: chore, Phase: "initial", PassLog: ".git/spindrift/pass-docs-drift.log", RecordID: "rec-chore-1", Slot: slot1},
		{Event: report.EventChildInherited, Kind: butler, Key: chore, Revision: rev, Slot: slot1, ChildLog: ".git/spindrift/child-1.log"},
		{Event: report.EventAwakeClose, Slot: slot0, Wait: "6h0m0s", Reason: "the Awake window closed"},
		{Event: report.EventAwakeOpen, Slot: slot0, Reason: "the Awake window reopened"},
		{Event: report.EventBackoff, Kind: workKind, Revision: rev, Slot: slot0, Wait: "5m0s", Reason: "child exited 1"},
		{Event: report.EventBatonHold, Slot: slot1, Reason: batonHoldReason},
		{Event: report.EventBatonPass, Slot: slot0, Reason: string(batonPassClaimed)},
		{Event: report.EventBreakerTrip, Kind: workKind, Slot: slot0, Failures: three, Wait: "1h0m0s"},
		{Event: report.EventDemandAppeared, Kind: research, Slot: slot0, Ready: ready},
		{Event: report.EventDemandDrained, Kind: research, Slot: slot0, Ready: zero},
		{Event: report.EventDemandRose, Kind: research, Slot: slot0, Ready: three},
		{Event: report.EventDemandSourceMissing, Kind: research, Reason: "no demand source configured"},
		{Event: report.EventIdle, Kind: workKind, Wait: "15m0s", Slot: slot0},
		{Event: report.EventJam, Kind: workKind, Revision: rev, Slot: slot0, Wait: "30m0s", Reason: "no work is dispatchable and no sibling slot is doing anything that could unblock it"},
		{Event: report.EventPreflight, Revision: rev, Outcome: "doctor-ok", Exit: &exit0},
		{Event: report.EventProbeFailed, Kind: research, Slot: slot0, Reason: "demand: probe exited 1"},
		{Event: report.EventProbeRateLimited, Kind: research, Slot: slot0, Tracker: "github", Until: stamp(900), Reason: "demand: rate limited"},
		{Event: report.EventProbeResumed, Kind: research, Slot: slot0, Tracker: "github"},
		SelfUpdate{Phase: SelfUpdateBuilt, Revision: rev}.Event(),
		{Event: report.EventSelfUpdate, Phase: string(SelfUpdateHandoff), Revision: rev, FromPath: "/nix/store/aaaa-spindrift-daemon", ToPath: "/nix/store/bbbb-spindrift-daemon", Slots: []int{0, 1}},
		{Event: report.EventTipMoved, Slot: slot0, Revision: rev, Reason: "a merge can unblock a jammed queue or end a reported kind's on_tip_move wait", Kinds: []Kind{workKind, butler}},
		{Event: report.EventShutdown, Reason: ShutdownDrain},
		halt.Event(),
	}

	// Compared as sets: a name with several fixture events counts once.
	var names []string
	for _, ev := range events {
		if !slices.Contains(names, ev.Event) {
			names = append(names, ev.Event)
		}
	}
	want := report.Events()
	sort.Strings(names)
	sort.Strings(want)
	if !slices.Equal(names, want) {
		t.Fatalf("fixture events cover %v, report.Events() is %v", names, want)
	}

	for i, ev := range events {
		em.Emit(ev)
		if i == contractOlderEvents-1 {
			// A cap below any line forces the very next append to rotate.
			ef.max = 1
		} else if i == contractOlderEvents {
			ef.max = eventsFileCap
		}
	}
	if _, err := os.Stat(filepath.Join(dir, eventsFileName+rotatedSuffix)); err != nil {
		t.Fatalf("rotation did not produce the older generation: %v", err)
	}
}

// contractStatus is the main status file: a halted self-change with a busy
// slot, both KindCheck shapes, a paused tracker and a built self-update.
func contractStatus(stamp func(int) string) Status {
	const rev = contractRev
	two, five := 2, 5
	butler := KindOf(dispatchkind.Butler)
	halt := Halt{Class: HaltSelfChanged, Detail: contractHaltDetail}
	return Status{
		Kinds:  []Kind{workKind, butler},
		State:  StateHalted,
		Reason: halt.String(),
		Slots: []SlotStatus{
			{
				Slot: 0, Phase: PhaseRunning, Busy: true, Since: stamp(101), Kind: workKind, Revision: rev,
				Issues: []string{"1234", "1240"}, Pass: "fix-pass-1", Model: "claude-opus-4-5", ModelRole: "implement",
				CIWait: true, PRURL: "https://github.com/acme/widgets/pull/77", CIRunURL: "https://github.com/acme/widgets/actions/runs/9001",
				ImageBuildSince: stamp(103), ChildStart: stamp(100), ChildStartN: 1,
			},
			{Slot: 1, Phase: PhaseRunning, Busy: true, Since: stamp(107), Kind: butler, Revision: rev, Chore: "docs-drift", Pass: "initial", ChildStart: stamp(106)},
			{Slot: 2, Phase: PhaseIdle, Since: stamp(90)},
		},
		Checks: []KindCheck{
			{
				Kind: workKind, NextCheck: stamp(400), Jammed: true, Ready: &five, ProbedAt: stamp(150), NextProbe: stamp(450),
				JamUntil: stamp(700), ReadyAtJam: &two,
			},
			{Kind: butler, NextDue: report.NextDueOnTipMove},
			{Kind: KindOf(dispatchkind.Research), NextDue: stamp(7200), NextDueOnTipMove: true},
		},
		Trackers:   []TrackerCheck{{Tracker: "github", RateLimitedUntil: stamp(900)}},
		RepoURL:    "https://github.com/acme/widgets",
		SelfUpdate: &SelfUpdate{Phase: SelfUpdateBuilt, Revision: rev, Since: stamp(150)},
	}
}

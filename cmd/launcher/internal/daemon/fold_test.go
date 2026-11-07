package daemon

import (
	"reflect"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/report"
)

func TestSlotFlightFold(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	later := now.Add(time.Hour)
	kind := KindOf(dispatchkind.Work)
	facts := func(running bool) slotFacts {
		return slotFacts{slot: 2, kind: kind, revision: "rev1", now: now, running: running}
	}
	slot := intPtr(2)
	issue7, issue8 := dispatchkey.Issue("7"), dispatchkey.Issue("8")
	chore := dispatchkey.Chore("bugs")
	transient := slotFlight{ciWait: ciWait{pr: "pr", runURL: "run"}, imageBuildSince: now.Add(-time.Minute)}

	tests := []struct {
		name    string
		flight  slotFlight
		rec     Record
		running bool
		want    slotFlight
		events  []Event
		sched   schedEvent
	}{
		{
			name:    "box new key claims, records pass, clears model and transient",
			flight:  slotFlight{key: issue7, issues: []string{"7"}, model: "m", modelRole: "r", ciWait: transient.ciWait, imageBuildSince: transient.imageBuildSince},
			rec:     Record{Event: report.EventBox, Key: issue8, Phase: "fix", PassLog: "log", RecordID: "id"},
			running: true,
			want:    slotFlight{key: issue8, pass: "fix", issues: []string{"7", "8"}},
			events:  []Event{{Event: report.EventBox, Kind: kind, Revision: "rev1", Key: issue8, Phase: "fix", PassLog: "log", RecordID: "id", Slot: slot}},
			sched:   claimed{Kind: kind},
		},
		{
			name:    "box same key moves no count and dedups the issue",
			flight:  slotFlight{key: issue7, issues: []string{"7"}, pass: "a"},
			rec:     Record{Event: report.EventBox, Key: issue7, Phase: "b"},
			running: true,
			want:    slotFlight{key: issue7, issues: []string{"7"}, pass: "b"},
			events:  []Event{{Event: report.EventBox, Kind: kind, Revision: "rev1", Key: issue7, Phase: "b", Slot: slot}},
		},
		{
			name:    "box chore key claims but stays out of issues",
			flight:  slotFlight{},
			rec:     Record{Event: report.EventBox, Key: chore},
			running: true,
			want:    slotFlight{key: chore},
			events:  []Event{{Event: report.EventBox, Kind: kind, Revision: "rev1", Key: chore, Slot: slot}},
			sched:   claimed{Kind: kind},
		},
		{
			name:   "box after the slot cleared is dropped",
			flight: slotFlight{key: issue7},
			rec:    Record{Event: report.EventBox, Key: issue8},
			want:   slotFlight{key: issue7},
		},
		{
			name:    "model sets model and role",
			flight:  slotFlight{key: issue7},
			rec:     Record{Event: report.EventModel, Key: issue7, Model: "opus", ModelRole: "lead"},
			running: true,
			want:    slotFlight{key: issue7, model: "opus", modelRole: "lead"},
			events:  []Event{{Event: report.EventModel, Kind: kind, Revision: "rev1", Key: issue7, Model: "opus", ModelRole: "lead", Slot: slot}},
		},
		{
			name:   "model after the slot cleared is dropped",
			flight: slotFlight{},
			rec:    Record{Event: report.EventModel, Key: issue7, Model: "opus"},
			want:   slotFlight{},
		},
		{
			name:    "ci_wait sets the wait and emits nothing",
			flight:  slotFlight{key: issue7},
			rec:     Record{Event: report.EventCIWait, Key: issue7, PRURL: "pr", RunURL: "run"},
			running: true,
			want:    slotFlight{key: issue7, ciWait: ciWait{pr: "pr", runURL: "run"}},
		},
		{
			name:    "ci_wait without a run url resets the run url",
			flight:  slotFlight{key: issue7, ciWait: ciWait{pr: "pr", runURL: "run"}},
			rec:     Record{Event: report.EventCIWait, Key: issue7, PRURL: "pr"},
			running: true,
			want:    slotFlight{key: issue7, ciWait: ciWait{pr: "pr"}},
		},
		{
			name:   "ci_wait after the slot cleared is dropped",
			flight: slotFlight{},
			rec:    Record{Event: report.EventCIWait, Key: issue7, PRURL: "pr"},
			want:   slotFlight{},
		},
		{
			name:    "image_build started stamps now, keyless",
			flight:  slotFlight{key: issue7},
			rec:     Record{Event: report.EventImageBuild, State: string(report.ImageBuildStarted)},
			running: true,
			want:    slotFlight{key: issue7, imageBuildSince: now},
			events:  []Event{{Event: report.EventImageBuild, Kind: kind, Revision: "rev1", State: string(report.ImageBuildStarted), Slot: slot}},
		},
		{
			name:    "image_build finished zeroes the stamp",
			flight:  slotFlight{key: issue7, imageBuildSince: now.Add(-time.Minute)},
			rec:     Record{Event: report.EventImageBuild, State: string(report.ImageBuildFinished)},
			running: true,
			want:    slotFlight{key: issue7},
			events:  []Event{{Event: report.EventImageBuild, Kind: kind, Revision: "rev1", State: string(report.ImageBuildFinished), Slot: slot}},
		},
		{
			name:   "image_build after the slot cleared is dropped",
			flight: slotFlight{},
			rec:    Record{Event: report.EventImageBuild, State: string(report.ImageBuildStarted)},
			want:   slotFlight{},
		},
		{
			name:    "not_due merges and never moves key",
			flight:  slotFlight{key: issue7, nextDue: report.NextDue{At: later}},
			rec:     Record{Event: report.EventNotDue, Key: chore, NextDue: report.NextDue{At: now, OnTipMove: true}},
			running: true,
			want:    slotFlight{key: issue7, nextDue: report.NextDue{At: now, OnTipMove: true}},
		},
		{
			name:   "not_due after the slot cleared is dropped",
			flight: slotFlight{},
			rec:    Record{Event: report.EventNotDue, Key: chore, NextDue: report.NextDue{At: now}},
			want:   slotFlight{},
		},
		{
			name:    "settled clears transient and emits",
			flight:  slotFlight{key: issue7, ciWait: transient.ciWait, imageBuildSince: transient.imageBuildSince},
			rec:     Record{Event: report.EventSettled, Key: issue7, State: "complete", Note: "n", PRURL: "pr", RecordID: "id"},
			running: true,
			want:    slotFlight{key: issue7},
			events:  []Event{{Event: report.EventSettled, Kind: kind, Revision: "rev1", Key: issue7, State: "complete", Note: "n", PRURL: "pr", RecordID: "id", Slot: slot}},
		},
		{
			name:   "settled after the slot cleared still emits and leaves the flight alone",
			flight: slotFlight{ciWait: transient.ciWait},
			rec:    Record{Event: report.EventSettled, Key: issue7, State: "complete"},
			want:   slotFlight{ciWait: transient.ciWait},
			events: []Event{{Event: report.EventSettled, Kind: kind, Revision: "rev1", Key: issue7, State: "complete", Slot: slot}},
		},
		{
			name:    "unknown event is a no-op",
			flight:  slotFlight{key: issue7},
			rec:     Record{Event: "mystery", Key: issue8},
			running: true,
			want:    slotFlight{key: issue7},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, events, sched := tc.flight.fold(tc.rec, facts(tc.running))
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("flight = %+v, want %+v", got, tc.want)
			}
			if !reflect.DeepEqual(events, tc.events) {
				t.Errorf("events = %+v, want %+v", events, tc.events)
			}
			if !reflect.DeepEqual(sched, tc.sched) {
				t.Errorf("sched = %+v, want %+v", sched, tc.sched)
			}
		})
	}
}

// The fold is pure: boxing a new issue must not write through the input
// flight's backing array, which a concurrent snapshot may still be reading.
func TestSlotFlightFoldDoesNotMutateIssues(t *testing.T) {
	backing := make([]string, 1, 4)
	backing[0] = "7"
	f := slotFlight{issues: backing}
	rec := Record{Event: report.EventBox, Key: dispatchkey.Issue("8")}
	got, _, _ := f.fold(rec, slotFacts{running: true})
	if !reflect.DeepEqual(got.issues, []string{"7", "8"}) {
		t.Fatalf("issues = %v, want [7 8]", got.issues)
	}
	if len(f.issues) != 1 || backing[:2][1] != "" {
		t.Errorf("input flight mutated: len=%d backing=%v", len(f.issues), backing[:2])
	}
}

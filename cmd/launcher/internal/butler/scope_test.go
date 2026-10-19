package butler

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/chore"
	"spindrift.dev/launcher/internal/ledger"
)

func TestTreeSourceDue(t *testing.T) {
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	cfg := chore.DueConfig{Every: time.Hour, ClaimTimeout: time.Hour}
	src := &treeSource{tree: fakeTree{}, head: "head1"}

	moved := ledger.Tip{State: ledger.State{LastSwept: "old", Phase: ledger.Done}}
	if got := src.Due(moved, nil, now, chore.Room{}, cfg); got != chore.Due {
		t.Errorf("head moved since last sweep: got %v, want Due", got)
	}

	rotated := ledger.Tip{State: ledger.State{LastSwept: "head1", Phase: ledger.Done}}
	if got := src.Due(rotated, nil, now, chore.Room{}, cfg); got != chore.NothingToScan {
		t.Errorf("head unchanged and cursor rotated: got %v, want NothingToScan", got)
	}

	midWalk := ledger.Tip{State: ledger.State{LastSwept: "head1", Cursor: "a/b", Phase: ledger.Done}}
	if got := src.Due(midWalk, nil, now, chore.Room{}, cfg); got != chore.Due {
		t.Errorf("head unchanged but cursor mid-walk: got %v, want Due", got)
	}
}

func TestTreeSourceNextDue(t *testing.T) {
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	cfg := chore.DueConfig{Every: time.Hour, ClaimTimeout: time.Hour}
	src := &treeSource{tree: fakeTree{}, head: "head1"}

	rotated := ledger.Tip{State: ledger.State{LastSwept: "head1", Phase: ledger.Done}}
	if at, onTipMove := src.NextDue(rotated, nil, now, chore.Room{}, cfg); !at.IsZero() || !onTipMove {
		t.Errorf("NothingToScan: got (%v, %v), want (zero, true)", at, onTipMove)
	}

	moved := ledger.Tip{State: ledger.State{LastSwept: "old", Phase: ledger.Done}}
	if at, onTipMove := src.NextDue(moved, nil, now, chore.Room{}, cfg); !at.Equal(now) || onTipMove {
		t.Errorf("Due: got (%v, %v), want (now, false)", at, onTipMove)
	}
}

func TestTreeSourcePrepare(t *testing.T) {
	claim := ledger.Tip{State: ledger.State{LastSwept: "old", Phase: ledger.Claimed}}
	src := &treeSource{tree: fakeTree{files: []string{"b.go", "a.go"}}, head: "head1"}

	if err := src.Stage(); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	got, err := src.Prepare(claim)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	want := chore.Scope{Head: "head1", DiffRange: "old..head1", Slice: []string{"a.go", "b.go"}}
	if !reflect.DeepEqual(got.scope, want) {
		t.Errorf("scope = %+v, want %+v", got.scope, want)
	}
	if got.input != "" || !got.claimTime.IsZero() || got.arm != nil {
		t.Errorf("tree source must hand no input, claim time or settle hook: %+v", got)
	}
}

func TestTreeSourceStageSurfacesTrackedFilesError(t *testing.T) {
	boom := errors.New("ls-tree failed")
	src := &treeSource{tree: fakeTree{filesErr: boom}, head: "head1"}
	if err := src.Stage(); !errors.Is(err, boom) {
		t.Errorf("err = %v, want %v", err, boom)
	}
}

package console

import (
	"testing"
	"time"

	"spindrift.dev/launcher/internal/forge"
)

// The operator's launch decision lands on the session queue (#646).
func TestUpdate_PickQueuedMsg_AppendsPick(t *testing.T) {
	m := NewModel()
	m = Update(m, PickQueuedMsg{Number: "42", Title: "fix the thing", Kind: KindWork})

	if len(m.Picks) != 1 {
		t.Fatalf("Picks = %+v, want one queued pick", m.Picks)
	}
	got := m.Picks[0]
	if got.Number != "42" || got.Title != "fix the thing" || got.Kind != KindWork || got.State != PickQueued {
		t.Errorf("Picks[0] = %+v, want {42 fix the thing work queued}", got)
	}
}

// The operator changes their mind before launch, and unpicking touches no
// tracker (#646).
func TestUpdate_UnpickMsg_RemovesQueuedPick(t *testing.T) {
	m := NewModel()
	m = Update(m, PickQueuedMsg{Number: "42", Title: "fix the thing", Kind: KindWork})

	m = Update(m, UnpickMsg{Number: "42"})

	if len(m.Picks) != 0 {
		t.Errorf("Picks = %+v, want empty after unpick", m.Picks)
	}
}

// A failed promotion lands already dissolved with its reason attached rather
// than vanishing, so the operator sees why the pick never queued (#646).
func TestUpdate_PickDissolvedMsg_AddsDissolvedRow(t *testing.T) {
	m := NewModel()
	m = Update(m, PickDissolvedMsg{Number: "42", Title: "fix the thing", Reason: "issue is closed"})

	if len(m.Picks) != 1 {
		t.Fatalf("Picks = %+v, want one dissolved pick", m.Picks)
	}
	got := m.Picks[0]
	if got.Number != "42" || got.State != PickDissolved || got.Reason != "issue is closed" {
		t.Errorf("Picks[0] = %+v, want dissolved #42 with reason", got)
	}
}

// Update installs the launcher's snapshot verbatim, so a render picks up
// claim, run, settle and dissolve transitions that happened entirely on the
// background Queue (#646).
func TestUpdate_QueueSnapshotMsg_ReplacesPicks(t *testing.T) {
	m := NewModel()
	m = Update(m, PickQueuedMsg{Number: "42", Title: "fix the thing", Kind: KindWork})

	m = Update(m, QueueSnapshotMsg{Picks: []Pick{{Number: "42", Title: "fix the thing", State: PickRunning}}})

	if len(m.Picks) != 1 || m.Picks[0].State != PickRunning {
		t.Errorf("Picks = %+v, want [{42 ... running}]", m.Picks)
	}
}

// Unpick removes only a pick still at PickQueued. A claiming, running or
// settled pick must not be unpicked out from under its Dispatch.
func TestUpdate_UnpickMsg_LeavesNonQueuedPickAlone(t *testing.T) {
	m := NewModel()
	m.Picks = []Pick{{Number: "42", State: PickRunning}}

	m = Update(m, UnpickMsg{Number: "42"})

	if len(m.Picks) != 1 {
		t.Errorf("Picks = %+v, want the running pick left in place", m.Picks)
	}
}

// Every PickState maps onto exactly one of the four work Sections (ADR 0030).
// Anything that ended without settling, whether it never launched, the
// operator ended it, or the Box exited non-zero, lands in SectionFailed.
func TestPickSection_PartitionsEveryPickStateIntoOneOfFourWorkSections(t *testing.T) {
	cases := []struct {
		state PickState
		want  Section
	}{
		{PickQueued, SectionRunning},
		{PickClaiming, SectionRunning},
		{PickRunning, SectionRunning},
		{PickHeld, SectionHeld},
		{PickSettled, SectionSettled},
		{PickDissolved, SectionFailed},
		{PickTerminated, SectionFailed},
		{PickFailed, SectionFailed},
	}
	for _, c := range cases {
		if got := pickSection(c.state); got != c.want {
			t.Errorf("pickSection(%s) = %v, want %v", c.state, got, c.want)
		}
	}
}

// H and L step through the five Sections in their fixed order and wrap at
// either end, and a direct jump lands on its Section from anywhere (ADR 0030).
func TestUpdate_SectionNav_NextPrevWrapAndJumpDirect(t *testing.T) {
	m := NewModel()
	if m.ActiveSection != SectionBacklog {
		t.Fatalf("ActiveSection = %v, want SectionBacklog as the zero value", m.ActiveSection)
	}

	m = Update(m, SectionPrevMsg{})
	if m.ActiveSection != SectionFailed {
		t.Errorf("ActiveSection after prev from Backlog = %v, want SectionFailed (wraps backward)", m.ActiveSection)
	}

	m = Update(m, SectionNextMsg{})
	if m.ActiveSection != SectionBacklog {
		t.Errorf("ActiveSection after next from Failed = %v, want SectionBacklog (wraps forward)", m.ActiveSection)
	}

	m = Update(m, SectionJumpMsg{Section: SectionHeld})
	if m.ActiveSection != SectionHeld {
		t.Errorf("ActiveSection after jump = %v, want SectionHeld", m.ActiveSection)
	}
}

// The cursor clamps against the active Section, not the backlog. Switching to
// a work Section with fewer rows must not leave Cursor past its end (ADR 0030).
func TestUpdate_CursorMoveMsg_ClampsAgainstActiveSectionTotal(t *testing.T) {
	m := NewModel()
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{{Number: "1"}, {Number: "2"}, {Number: "3"}}})
	m = Update(m, QueueSnapshotMsg{Picks: []Pick{{Number: "9", State: PickHeld}}})
	m = Update(m, SectionJumpMsg{Section: SectionHeld})

	m = Update(m, CursorMoveMsg{Delta: 5})

	if m.Cursor != 0 {
		t.Errorf("Cursor = %d, want clamped at 0 (one held pick, index 0 is the only valid row)", m.Cursor)
	}
}

// Switching Sections resets Cursor and Offset, but jumping to the Section
// that is already active leaves them where the operator left them.
func TestUpdate_SectionJumpMsg_ResetsCursorAndOffsetOnChange(t *testing.T) {
	m := NewModel()
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{{Number: "1"}, {Number: "2"}, {Number: "3"}}})
	m = Update(m, CursorMoveMsg{Delta: 2})
	if m.Cursor != 2 {
		t.Fatalf("Cursor = %d, want 2 before switching Sections", m.Cursor)
	}

	m = Update(m, SectionJumpMsg{Section: SectionBacklog})
	if m.Cursor != 2 {
		t.Errorf("Cursor = %d, want unchanged (jumped to the already-active Section)", m.Cursor)
	}

	m = Update(m, SectionJumpMsg{Section: SectionRunning})
	if m.Cursor != 0 || m.Offset != 0 {
		t.Errorf("Cursor/Offset = %d/%d, want reset to 0/0 after switching to a different Section", m.Cursor, m.Offset)
	}
}

// formatAge picks the coarsest unit that still reads precisely, so the age
// column stays narrow at every scale instead of always showing hh:mm:ss.
func TestFormatAge_RendersHumanScaleDurations(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "<1m"},
		{3 * time.Minute, "3m"},
		{59 * time.Minute, "59m"},
		{90 * time.Minute, "1h30m"},
		{23*time.Hour + 59*time.Minute, "23h59m"},
		{25 * time.Hour, "1d"},
		{72 * time.Hour, "3d"},
	}
	for _, c := range cases {
		if got := formatAge(c.d); got != c.want {
			t.Errorf("formatAge(%s) = %q, want %q", c.d, got, c.want)
		}
	}
}

package console

import (
	"testing"
	"time"

	"spindrift.dev/launcher/internal/forge"
)

// TestUpdate_PickQueuedMsg_AppendsPick verifies Update adds a new queued Pick
// to Model.Picks, defaulted to KindWork — the operator's launch decision
// landing on the session queue (#646).
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

// TestUpdate_UnpickMsg_RemovesQueuedPick verifies UnpickMsg drops a queued
// pick from Model.Picks — the operator changing their mind before launch,
// with no tracker interaction of any kind (#646).
func TestUpdate_UnpickMsg_RemovesQueuedPick(t *testing.T) {
	m := NewModel()
	m = Update(m, PickQueuedMsg{Number: "42", Title: "fix the thing", Kind: KindWork})

	m = Update(m, UnpickMsg{Number: "42"})

	if len(m.Picks) != 0 {
		t.Errorf("Picks = %+v, want empty after unpick", m.Picks)
	}
}

// TestUpdate_PickDissolvedMsg_AddsDissolvedRow verifies a failed promotion
// lands on Model.Picks already dissolved, reason attached, rather than
// vanishing silently — the operator still sees why their pick never queued
// (#646).
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

// TestUpdate_QueueSnapshotMsg_ReplacesPicks verifies Update installs the
// launcher's live queue snapshot verbatim, so a render after the snapshot
// reflects claim/run/settle/dissolve transitions that happened entirely on
// the background Queue, not just the initial pick (#646).
func TestUpdate_QueueSnapshotMsg_ReplacesPicks(t *testing.T) {
	m := NewModel()
	m = Update(m, PickQueuedMsg{Number: "42", Title: "fix the thing", Kind: KindWork})

	m = Update(m, QueueSnapshotMsg{Picks: []Pick{{Number: "42", Title: "fix the thing", State: PickRunning}}})

	if len(m.Picks) != 1 || m.Picks[0].State != PickRunning {
		t.Errorf("Picks = %+v, want [{42 ... running}]", m.Picks)
	}
}

// TestUpdate_UnpickMsg_LeavesNonQueuedPickAlone verifies Unpick only ever
// removes a pick still holding at PickQueued — a pick already claiming,
// running, or settled cannot be unpicked out from under its Dispatch.
func TestUpdate_UnpickMsg_LeavesNonQueuedPickAlone(t *testing.T) {
	m := NewModel()
	m.Picks = []Pick{{Number: "42", State: PickRunning}}

	m = Update(m, UnpickMsg{Number: "42"})

	if len(m.Picks) != 1 {
		t.Errorf("Picks = %+v, want the running pick left in place", m.Picks)
	}
}

// TestPickSection_PartitionsEveryPickStateIntoOneOfFourWorkSections verifies
// pickSection maps every PickState onto exactly one of the four work
// Sections (ADR 0030): Queued/Claiming/Running are still active in the
// pipeline (SectionRunning); Held stays SectionHeld; a clean completion is
// SectionSettled; anything that ended without settling — Dissolved (never
// launched), Terminated (operator ended it), Failed (Box exited non-zero) —
// lands in SectionFailed.
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

// TestUpdate_SectionNav_NextPrevWrapAndJumpDirect verifies H/L step through
// the five Sections in their fixed order and wrap at either end, and a
// direct 1-5 jump lands on the matching Section regardless of where the
// cursor started (ADR 0030).
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

// TestUpdate_CursorMoveMsg_ClampsAgainstActiveSectionTotal verifies the
// cursor clamps against whichever Section is active, not the backlog —
// switching to a work Section with fewer rows than the backlog must not
// leave Cursor pointing past its end (ADR 0030).
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

// TestUpdate_SectionJumpMsg_ResetsCursorAndOffsetOnChange verifies switching
// to a different Section resets Cursor and Offset to 0, but jumping to the
// Section that's already active leaves them where the operator left them.
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

// TestFormatAge_RendersHumanScaleDurations verifies formatAge picks the
// coarsest unit that still reads precisely at each scale — minutes under an
// hour, hours+minutes under a day, whole days beyond that — so an aligned
// age column stays narrow at every scale rather than always showing
// hh:mm:ss.
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

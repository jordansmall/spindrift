package console

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"spindrift.dev/launcher/internal/forge"
)

// Issue #1830 AC1: the toast names the pick's number and title.
func TestPickTransitionToast_QueuedToRunning_ReturnsStartedToast(t *testing.T) {
	old := []Pick{{Number: "1818", Title: "fix the thing", State: PickQueued}}
	updated := []Pick{{Number: "1818", Title: "fix the thing", State: PickRunning}}

	got := pickTransitionToast(old, updated)
	want := "#1818 started: fix the thing"
	if got != want {
		t.Errorf("pickTransitionToast() = %q, want %q", got, want)
	}
}

// An issue title is untrusted input, the same threat model #721 hardened the
// backlog and queue title rows against, so the toast text runs through
// SanitizeControlSequences like every other title-render call site (issue
// #1830 review finding).
func TestPickTransitionToast_TitleWithControlSequence_Sanitized(t *testing.T) {
	old := []Pick{{Number: "1818", Title: "fix \x1b]0;pwned\x07it", State: PickQueued}}
	updated := []Pick{{Number: "1818", Title: "fix \x1b]0;pwned\x07it", State: PickRunning}}

	got := pickTransitionToast(old, updated)
	want := "#1818 started: fix it"
	if got != want {
		t.Errorf("pickTransitionToast() = %q, want %q", got, want)
	}
}

// Issue #1830 AC1.
func TestPickTransitionToast_RunningToSettled_ReturnsSettledToast(t *testing.T) {
	old := []Pick{{Number: "1818", Title: "fix the thing", State: PickRunning}}
	updated := []Pick{{Number: "1818", Title: "fix the thing", State: PickSettled}}

	got := pickTransitionToast(old, updated)
	want := "#1818 settled: fix the thing"
	if got != want {
		t.Errorf("pickTransitionToast() = %q, want %q", got, want)
	}
}

// Issue #1830 AC1.
func TestPickTransitionToast_RunningToFailed_ReturnsFailedToast(t *testing.T) {
	old := []Pick{{Number: "1818", Title: "fix the thing", State: PickRunning}}
	updated := []Pick{{Number: "1818", Title: "fix the thing", State: PickFailed}}

	got := pickTransitionToast(old, updated)
	want := "#1818 failed: fix the thing"
	if got != want {
		t.Errorf("pickTransitionToast() = %q, want %q", got, want)
	}
}

// Issue #1830 AC1.
func TestPickTransitionToast_QueuedToHeld_ReturnsHeldToast(t *testing.T) {
	old := []Pick{{Number: "1818", Title: "fix the thing", State: PickQueued}}
	updated := []Pick{{Number: "1818", Title: "fix the thing", State: PickHeld}}

	got := pickTransitionToast(old, updated)
	want := "#1818 held: fix the thing"
	if got != want {
		t.Errorf("pickTransitionToast() = %q, want %q", got, want)
	}
}

// A pick with no prior snapshot (the startup bootstrap snapshot from
// initialQueueSyncCmd, or a freshly queued pick) has an unknown starting
// state, not an observed transition, so it fires no toast (issue #1830,
// "detected from the console's own state").
func TestPickTransitionToast_PickAbsentFromOld_NoToast(t *testing.T) {
	var old []Pick
	updated := []Pick{{Number: "1818", Title: "fix the thing", State: PickRunning}}

	got := pickTransitionToast(old, updated)
	if got != "" {
		t.Errorf("pickTransitionToast() = %q, want \"\" for a pick absent from old", got)
	}
}

func TestPickTransitionToast_NoStateChange_NoToast(t *testing.T) {
	old := []Pick{{Number: "1818", Title: "fix the thing", State: PickRunning}}
	updated := []Pick{{Number: "1818", Title: "fix the thing", State: PickRunning}}

	got := pickTransitionToast(old, updated)
	if got != "" {
		t.Errorf("pickTransitionToast() = %q, want \"\" for no state change", got)
	}
}

// A re-picked issue leaves its terminated row in the queue ahead of the new
// one (issue #5392), so an unchanged snapshot must not toast against the stale
// row.
func TestPickTransitionToast_RepickedUnchanged_NoToast(t *testing.T) {
	rows := []Pick{
		{Number: "5", Title: "t", State: PickFailed},
		{Number: "5", Title: "t", State: PickRunning},
	}

	got := pickTransitionToast(rows, rows)
	if got != "" {
		t.Errorf("pickTransitionToast() = %q, want \"\" for an unchanged re-picked number", got)
	}
}

func TestPickTransitionToast_RepickedQueuedToClaiming_NoStaleFailedToast(t *testing.T) {
	old := []Pick{
		{Number: "5", Title: "t", State: PickFailed},
		{Number: "5", Title: "t", State: PickQueued},
	}
	updated := []Pick{
		{Number: "5", Title: "t", State: PickFailed},
		{Number: "5", Title: "t", State: PickClaiming},
	}

	got := pickTransitionToast(old, updated)
	if got != "" {
		t.Errorf("pickTransitionToast() = %q, want \"\" (queued->claiming raises no toast)", got)
	}
}

func TestPickTransitionToast_RepickedNewestRowTransitions_ToastsNewestRow(t *testing.T) {
	old := []Pick{
		{Number: "5", Title: "t", State: PickFailed},
		{Number: "5", Title: "t", State: PickRunning},
	}
	updated := []Pick{
		{Number: "5", Title: "t", State: PickFailed},
		{Number: "5", Title: "t", State: PickFailed},
	}

	got := pickTransitionToast(old, updated)
	want := "#5 failed: t"
	if got != want {
		t.Errorf("pickTransitionToast() = %q, want %q", got, want)
	}
}

// Work and research picks of one issue number are distinct rows (keyed by
// Number and kind); a nil Kind is work.
func TestPickTransitionToast_SharedNumberAcrossKinds_UnchangedNoToast(t *testing.T) {
	rows := []Pick{
		{Number: "5", Title: "t", State: PickSettled},
		{Number: "5", Title: "t", State: PickRunning, Kind: KindResearch},
	}

	got := pickTransitionToast(rows, rows)
	if got != "" {
		t.Errorf("pickTransitionToast() = %q, want \"\" for unchanged work+research rows", got)
	}
}

func TestPickTransitionToast_SharedNumberAcrossKinds_OnlyTransitioningRowToasts(t *testing.T) {
	old := []Pick{
		{Number: "5", Title: "t", State: PickSettled, Kind: KindWork},
		{Number: "5", Title: "t", State: PickQueued, Kind: KindResearch},
	}
	// The nil Kind here against KindWork in old is deliberate: it exercises
	// the nil -> KindWork normalisation in effectiveKind().
	updated := []Pick{
		{Number: "5", Title: "t", State: PickSettled},
		{Number: "5", Title: "t", State: PickRunning, Kind: KindResearch},
	}

	got := pickTransitionToast(old, updated)
	want := "#5 started: t"
	if got != want {
		t.Errorf("pickTransitionToast() = %q, want %q", got, want)
	}
}

// Unpicking an unclaimed re-pick splices its row out (Queue.Remove), leaving
// the older terminal row as the newest: a different logical row, not a
// transition.
func TestPickTransitionToast_UnpickedRepick_NoToast(t *testing.T) {
	tests := []struct {
		name      string
		old, next []Pick
	}{
		{
			name: "queued after failed",
			old: []Pick{
				{Number: "5", Title: "t", State: PickFailed},
				{Number: "5", Title: "t", State: PickQueued},
			},
			next: []Pick{{Number: "5", Title: "t", State: PickFailed}},
		},
		{
			name: "held after settled",
			old: []Pick{
				{Number: "5", Title: "t", State: PickSettled},
				{Number: "5", Title: "t", State: PickHeld},
			},
			next: []Pick{{Number: "5", Title: "t", State: PickSettled}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := pickTransitionToast(tc.old, tc.next); got != "" {
				t.Errorf("pickTransitionToast() = %q, want \"\" for an unpicked re-pick", got)
			}
		})
	}
}

// A re-pick appending straight into Held toasts nothing, like a first-time
// pick into Held.
func TestPickTransitionToast_RepickAppendedAsHeld_NoToast(t *testing.T) {
	old := []Pick{{Number: "5", Title: "t", State: PickFailed}}
	next := []Pick{{Number: "5", Title: "t", State: PickFailed}, {Number: "5", Title: "t", State: PickHeld}}

	if got := pickTransitionToast(old, next); got != "" {
		t.Errorf("pickTransitionToast() = %q, want \"\"", got)
	}
}

// Re-applying an unchanged re-picked snapshot after the toast is dismissed
// must not resurrect a toast from the stale terminal row.
func TestTea_RepickedSnapshot_ReappliedAfterDismiss_NoToast(t *testing.T) {
	rows := []Pick{{Number: "5", Title: "t", State: PickFailed}, {Number: "5", Title: "t", State: PickRunning}}
	tm := teaModel{m: NewModel()}
	next, _ := tm.Update(QueueSnapshotMsg{Picks: []Pick{rows[0], {Number: "5", Title: "t", State: PickQueued}}})
	tm = next.(teaModel)
	next, _ = tm.Update(QueueSnapshotMsg{Picks: rows})
	tm = next.(teaModel)
	if tm.m.Toast == "" {
		t.Fatal("Toast = \"\", want it set after the re-pick started")
	}
	next, _ = tm.Update(toastDismissTickMsg{gen: tm.toastGen})
	tm = next.(teaModel)

	next, _ = tm.Update(QueueSnapshotMsg{Picks: rows})
	tm = next.(teaModel)

	if tm.m.Toast != "" {
		t.Errorf("Toast = %q, want \"\" after re-applying an unchanged snapshot", tm.m.Toast)
	}
}

// Issue #1830 AC1.
func TestUpdate_QueueSnapshotMsg_SetsToastOnTransition(t *testing.T) {
	m := NewModel()
	m = Update(m, QueueSnapshotMsg{Picks: []Pick{{Number: "1818", Title: "fix the thing", State: PickQueued}}})

	m = Update(m, QueueSnapshotMsg{Picks: []Pick{{Number: "1818", Title: "fix the thing", State: PickRunning}}})

	want := "#1818 started: fix the thing"
	if m.Toast != want {
		t.Errorf("Toast = %q, want %q", m.Toast, want)
	}
}

// The tea layer fires ToastDismissedMsg on the operator's next keypress or the
// auto-dismiss timer, whichever comes first (issue #1830 AC2).
func TestUpdate_ToastDismissedMsg_ClearsToast(t *testing.T) {
	m := NewModel()
	m.Toast = "#1818 started: fix the thing"

	m = Update(m, ToastDismissedMsg{})

	if m.Toast != "" {
		t.Errorf("Toast = %q, want \"\" after ToastDismissedMsg", m.Toast)
	}
}

// A toast clears on any key in ModeList, the same precedent QueueEnterNotice
// already uses (issue #1830 AC2).
func TestTea_Toast_ClearsOnNextKey(t *testing.T) {
	m := NewModel()
	m.Toast = "#1818 started: fix the thing"
	tm := teaModel{m: m}

	next, _ := tm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	tm = next.(teaModel)

	if tm.m.Toast != "" {
		t.Errorf("Toast = %q, want \"\" after next keypress", tm.m.Toast)
	}
}

// The dismiss tick clears the toast only when it carries the generation the
// transition armed it with. This is the auto-dismiss half of issue #1830 AC2.
func TestTea_QueueSnapshotMsg_ToastDismissTick_ClearsToast(t *testing.T) {
	tm := teaModel{m: NewModel()}
	next, _ := tm.Update(QueueSnapshotMsg{Picks: []Pick{{Number: "1818", Title: "fix the thing", State: PickQueued}}})
	tm = next.(teaModel)
	next, _ = tm.Update(QueueSnapshotMsg{Picks: []Pick{{Number: "1818", Title: "fix the thing", State: PickRunning}}})
	tm = next.(teaModel)
	if tm.m.Toast == "" {
		t.Fatal("Toast = \"\", want it set after a queued->running transition")
	}

	next, _ = tm.Update(toastDismissTickMsg{gen: tm.toastGen})
	tm = next.(teaModel)

	if tm.m.Toast != "" {
		t.Errorf("Toast = %q, want \"\" after its dismiss tick fires", tm.m.Toast)
	}
}

// A straggler tick from a replaced toast must not clear the newer toast (issue
// #1830 AC2), the same stale-generation guard sidebarActivityTickMsg uses.
func TestTea_ToastDismissTick_DropsStaleGeneration(t *testing.T) {
	tm := teaModel{m: NewModel()}
	next, _ := tm.Update(QueueSnapshotMsg{Picks: []Pick{
		{Number: "1818", Title: "fix the thing", State: PickQueued},
		{Number: "1819", Title: "other thing", State: PickQueued},
	}})
	tm = next.(teaModel)

	next, _ = tm.Update(QueueSnapshotMsg{Picks: []Pick{
		{Number: "1818", Title: "fix the thing", State: PickRunning},
		{Number: "1819", Title: "other thing", State: PickQueued},
	}})
	tm = next.(teaModel)
	staleGen := tm.toastGen

	next, _ = tm.Update(QueueSnapshotMsg{Picks: []Pick{
		{Number: "1818", Title: "fix the thing", State: PickRunning},
		{Number: "1819", Title: "other thing", State: PickRunning},
	}})
	tm = next.(teaModel)
	newerToast := tm.m.Toast
	if tm.toastGen == staleGen {
		t.Fatalf("toastGen = %d, want it to have advanced past the replaced toast's generation %d", tm.toastGen, staleGen)
	}

	next, cmd := tm.Update(toastDismissTickMsg{gen: staleGen})
	tm = next.(teaModel)

	if cmd != nil {
		t.Error("cmd != nil, want the stale-generation tick dropped with no further action")
	}
	if tm.m.Toast != newerToast {
		t.Errorf("Toast = %q after stale tick fired, want the newer toast %q left untouched", tm.m.Toast, newerToast)
	}
}

// Issue #1830 AC1: the toast gets its own line.
func TestView_Toast_Renders(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	m.Toast = "#1818 started: fix the thing"

	out := View(m)
	if !strings.Contains(out, "#1818 started: fix the thing") {
		t.Errorf("View() = %q, want it to contain the toast line", out)
	}
}

// The body's row budget must absorb the toast's line, so the frame stays
// within Height and the list shrinks by exactly one row (issue #1830 AC3).
func TestView_Toast_NeverOverflowsHeight(t *testing.T) {
	const height = 10
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: height})
	issues := make([]forge.Issue, 20)
	for i := range issues {
		issues[i] = forge.Issue{Number: fmt.Sprintf("%d", i), Title: "an issue"}
	}
	m = Update(m, IssuesLoadedMsg{Issues: issues})
	m.Toast = "#1818 started: fix the thing"

	out := View(m)
	if got := strings.Count(out, "\n") + 1; got > height {
		t.Errorf("View() rendered %d lines, want at most Height (%d) with a toast showing: %q", got, height, out)
	}
}

// bodyBudget feeds Update's scroll and cursor clamps, so it must count a
// visible toast the way View does or the two diverge (issue #1830 AC3,
// mirroring the QueueEnterNotice precedent already in bodyBudget).
func TestBodyBudget_Toast_ShrinksByOneRow(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	without := bodyBudget(m)

	m.Toast = "#1818 started: fix the thing"
	with := bodyBudget(m)

	if with != without-1 {
		t.Errorf("bodyBudget() with toast = %d, want %d (one less than %d without)", with, without-1, without)
	}
}

// An unclipped toast line wraps onto a second terminal row that the one-row
// reserved-lines budget never accounts for, overflowing the frame (issue #1830
// AC3 review finding).
func TestView_Toast_LongTitleClipsToWidth(t *testing.T) {
	const width, height = 40, 10
	m := Update(NewModel(), SizeChangedMsg{Width: width, Height: height})
	m.Toast = "#1818 started: " + strings.Repeat("a very long issue title ", 10)

	out := View(m)
	// lipgloss.Width, not runewidth.StringWidth: on a color-capable ambient TERM
	// the panel border carries ANSI codes (ADR 0031) that runewidth counts as
	// display width and lipgloss does not. Mirrors view_test.go's panel check.
	for i, line := range strings.Split(out, "\n") {
		if got := lipgloss.Width(line); got > width {
			t.Errorf("View() line %d is %d columns wide, want at most the terminal's %d: %q", i, got, width, line)
		}
	}
	if got := strings.Count(out, "\n") + 1; got > height {
		t.Errorf("View() rendered %d lines, want at most Height (%d) with a long-title toast showing: %q", got, height, out)
	}
}

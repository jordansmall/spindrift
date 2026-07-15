package console

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// TestView_ListsVisibleIssuesWithNumberTitleLabels verifies View renders
// each visible issue's number, title, and labels — the backlog line the
// operator reads to decide what to pick in a later issue.
func TestView_ListsVisibleIssuesWithNumberTitleLabels(t *testing.T) {
	m := NewModel()
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{
		{Number: "12", Title: "Fix the thing", Labels: []string{"ready-for-agent", "bug"}},
	}})

	out := View(m)
	for _, want := range []string{"12", "Fix the thing", "ready-for-agent", "bug"} {
		if !strings.Contains(out, want) {
			t.Errorf("View() = %q, want it to contain %q", out, want)
		}
	}
}

// TestView_DogfoodNotice_ShownWhenLiveSilentOtherwise verifies the
// informational dogfood-competition notice renders only when a live
// pid-file was found at startup — absence renders nothing extra.
func TestView_DogfoodNotice_ShownWhenLiveSilentOtherwise(t *testing.T) {
	absent := View(NewModel())
	if strings.Contains(absent, "dogfood") {
		t.Errorf("View() with no dogfood notice = %q, want no mention of dogfood", absent)
	}

	live := Update(NewModel(), DogfoodNoticeMsg{Live: true})
	if out := View(live); !strings.Contains(out, "dogfood") {
		t.Errorf("View() with live dogfood pid-file = %q, want a dogfood notice", out)
	}
}

// TestView_CapAndLive_Shown verifies View renders the session's live
// parallelism cap and current live count (issue #653) in the header status
// line — visible without a separate command, the same way the queue rows
// already are.
func TestView_CapAndLive_Shown(t *testing.T) {
	m := NewModel()
	m = Update(m, CapMsg{Cap: 3, Live: 1})

	out := View(m)
	if !strings.Contains(out, "running 1/3") {
		t.Errorf("View() = %q, want a \"running 1/3\" line (live/cap)", out)
	}
}

// TestView_Header_StatusLine_ShowsRunningWaitingHeldSettled verifies the
// header's status line reports running/cap, waiting, held, and settled
// counts derived from Cap/Live and the Picks slice's PickState tags — no new
// stored counters (issue #843, ADR 0025).
func TestView_Header_StatusLine_ShowsRunningWaitingHeldSettled(t *testing.T) {
	m := NewModel()
	m = Update(m, CapMsg{Cap: 3, Live: 1})
	m.Picks = []Pick{
		{Number: "1", State: PickQueued},
		{Number: "2", State: PickHeld},
		{Number: "3", State: PickSettled},
		{Number: "4", State: PickSettled},
	}

	out := View(m)
	want := "running 1/3 · waiting 1 · held 1 · settled 2"
	if !strings.Contains(out, want) {
		t.Errorf("View() = %q, want it to contain status line %q", out, want)
	}
}

// TestView_Header_Banner_ShownWhenTallCollapsedWhenShort verifies the fixed
// "spindrift" banner renders above the status line when the terminal has
// room for it, and collapses to the status line alone on a short terminal
// rather than pushing the backlog/queue off-screen (issue #843, ADR 0025).
func TestView_Header_Banner_ShownWhenTallCollapsedWhenShort(t *testing.T) {
	tall := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	if out := View(tall); !strings.Contains(out, "spindrift") {
		t.Errorf("View() on a tall terminal = %q, want the spindrift banner", out)
	}

	short := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 3})
	out := View(short)
	if strings.Contains(out, "spindrift") {
		t.Errorf("View() on a short terminal = %q, want the banner collapsed", out)
	}
	if !strings.Contains(out, "running 0/0") {
		t.Errorf("View() on a short terminal = %q, want the status line to remain", out)
	}
}

// TestView_Header_AlertsRenderBeforeEphemeralPrompts verifies the
// stale-image and competing-dogfood alert lines render as part of the
// header — grouped with the status line, ahead of ephemeral operator
// prompts like an in-progress filter edit — rather than interleaved with
// them (issue #843, ADR 0025).
func TestView_Header_AlertsRenderBeforeEphemeralPrompts(t *testing.T) {
	m := NewModel()
	m = Update(m, StaleStatusMsg{Stale: true, Message: "rebuild needed"})
	m = Update(m, DogfoodNoticeMsg{Live: true})
	m = Update(m, FilterEditStartMsg{})
	m = Update(m, FilterChangedMsg{Filter: "bug"})

	out := View(m)
	statusIdx := strings.Index(out, "running 0/0")
	staleIdx := strings.Index(out, "stale")
	dogfoodIdx := strings.Index(out, "dogfood")
	filterIdx := strings.Index(out, "/bug")

	if statusIdx == -1 || staleIdx == -1 || dogfoodIdx == -1 || filterIdx == -1 {
		t.Fatalf("View() = %q, want status/stale/dogfood/filter all present", out)
	}
	if !(statusIdx < staleIdx && staleIdx < dogfoodIdx && dogfoodIdx < filterIdx) {
		t.Errorf("View() = %q, want status < stale < dogfood < filter prompt ordering", out)
	}
}

// TestView_Header_LaunchLessSession_RendersCleanly verifies a launch-less
// session (no CapMsg, no picks, no size event ever delivered) renders a
// clean header — zero/zero counts, no stale/dogfood alert text, no panic —
// rather than requiring a Launcher round-trip before the header is usable
// (issue #843 AC5).
func TestView_Header_LaunchLessSession_RendersCleanly(t *testing.T) {
	out := View(NewModel())
	if !strings.Contains(out, "running 0/0 · waiting 0 · held 0 · settled 0") {
		t.Errorf("View() = %q, want a clean zero/zero status line", out)
	}
	for _, unwanted := range []string{"stale", "dogfood", "!!"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("View() = %q, want no stray %q in a launch-less header", out, unwanted)
		}
	}
}

// TestView_ListsPicksWithNumberTitleState verifies View renders each queue
// row's number, title, and state — a dissolved row also carries its reason
// — so the operator can see the queue without a separate command (#646).
func TestView_ListsPicksWithNumberTitleState(t *testing.T) {
	m := NewModel()
	m.Picks = []Pick{
		{Number: "42", Title: "fix the thing", State: PickQueued},
		{Number: "7", Title: "raced pick", State: PickDissolved, Reason: "issue is closed"},
	}

	out := View(m)
	for _, want := range []string{"42", "fix the thing", "queued", "7", "raced pick", "dissolved", "issue is closed"} {
		if !strings.Contains(out, want) {
			t.Errorf("View() = %q, want it to contain %q", out, want)
		}
	}
}

// TestView_RunningPick_ShowsHeartbeat verifies a running row renders its
// latest heartbeat line alongside number/title/state, so the overview is
// scannable without drilling in (#647 AC2).
func TestView_RunningPick_ShowsHeartbeat(t *testing.T) {
	m := NewModel()
	m.Picks = []Pick{
		{Number: "42", Title: "fix the thing", State: PickRunning, Heartbeat: "#42 [edit] \xc2\xb7 7 turns"},
	}

	out := View(m)
	if !strings.Contains(out, "#42 [edit] \xc2\xb7 7 turns") {
		t.Errorf("View() = %q, want the running row's heartbeat line", out)
	}
}

// TestView_StaleBanner_ShownWhenStaleSilentOtherwise verifies the stale
// banner (with the probe's message and the rebuild-key hint) renders only
// while Stale is true — a fresh session shows no mention of it (issue
// #652).
func TestView_StaleBanner_ShownWhenStaleSilentOtherwise(t *testing.T) {
	fresh := View(NewModel())
	if strings.Contains(fresh, "stale") {
		t.Errorf("View() with no stale status = %q, want no mention of stale", fresh)
	}

	m := Update(NewModel(), StaleStatusMsg{Stale: true, Message: "rebuild needed (main tip abc123 produces spindrift:def, loaded image is spindrift:abc)"})
	out := View(m)
	if !strings.Contains(out, "stale") {
		t.Errorf("View() = %q, want a stale banner", out)
	}
	if !strings.Contains(out, "rebuild needed (main tip abc123 produces spindrift:def, loaded image is spindrift:abc)") {
		t.Errorf("View() = %q, want the probe's message", out)
	}
	if !strings.Contains(out, "[b]") {
		t.Errorf("View() = %q, want the rebuild-key hint", out)
	}
}

// TestView_Rebuilding_ShowsProgress verifies an in-flight rebuild renders a
// progress line so the operator sees the confirm key took effect.
func TestView_Rebuilding_ShowsProgress(t *testing.T) {
	m := Update(NewModel(), StaleStatusMsg{Stale: true, Rebuilding: true})
	out := View(m)
	if !strings.Contains(out, "rebuild") {
		t.Errorf("View() = %q, want a rebuilding-in-progress line", out)
	}
}

// TestView_RebuildErr_Surfaced verifies a failed rebuild's error text
// appears, and launches stay noted as held (Stale remains true).
func TestView_RebuildErr_Surfaced(t *testing.T) {
	m := Update(NewModel(), StaleStatusMsg{Stale: true, RebuildErr: "nix build failed"})
	out := View(m)
	if !strings.Contains(out, "nix build failed") {
		t.Errorf("View() = %q, want the rebuild failure surfaced", out)
	}
}

// TestView_RefreshError_Surfaced verifies a failed refresh's error text
// appears in View so the operator sees why the list went stale.
func TestView_RefreshError_Surfaced(t *testing.T) {
	m := Update(NewModel(), IssuesLoadedMsg{Err: errBoom})

	out := View(m)
	if !strings.Contains(out, errBoom.Error()) {
		t.Errorf("View() = %q, want it to contain %q", out, errBoom.Error())
	}
}

// TestView_Cursor_MarksHighlightedRow verifies the row at m.Cursor is
// visually marked so the operator can see which issue j/down or the up
// arrow will act on (issue #784).
func TestView_Cursor_MarksHighlightedRow(t *testing.T) {
	m := NewModel()
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{{Number: "1"}, {Number: "2"}}})
	m = Update(m, CursorMoveMsg{Delta: 1})

	out := View(m)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	var marked, unmarked string
	for _, l := range lines {
		if strings.Contains(l, "#1") {
			unmarked = l
		}
		if strings.Contains(l, "#2") {
			marked = l
		}
	}
	if !strings.HasPrefix(marked, ">") {
		t.Errorf("cursor row = %q, want a leading marker", marked)
	}
	if strings.HasPrefix(unmarked, ">") {
		t.Errorf("non-cursor row = %q, want no leading marker", unmarked)
	}
}

// TestView_FilterEditing_ShowsInputLine verifies an in-progress filter edit
// renders a visible input line with the text typed so far (issue #784).
func TestView_FilterEditing_ShowsInputLine(t *testing.T) {
	m := NewModel()
	m = Update(m, FilterEditStartMsg{})
	m = Update(m, FilterChangedMsg{Filter: "bug"})

	out := View(m)
	if !strings.Contains(out, "/bug") && !strings.Contains(out, "/ bug") {
		t.Errorf("View() = %q, want the in-progress filter text shown", out)
	}
}

// TestView_ShowHelp_ListsBoundKeys verifies the help overlay lists every key
// the tea layer binds, replacing the normal backlog rendering (issue #784).
func TestView_ShowHelp_ListsBoundKeys(t *testing.T) {
	m := NewModel()
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{{Number: "1", Title: "should not show"}}})
	m = Update(m, HelpToggleMsg{})

	out := View(m)
	if strings.Contains(out, "should not show") {
		t.Errorf("View() = %q, want the backlog hidden while help is open", out)
	}
	for _, want := range []string{"j", "k", "/", "enter", "esc", "r", "q", "?", "d", "t", "x", "pgup", "pgdown"} {
		if !strings.Contains(strings.ToLower(out), want) {
			t.Errorf("View() = %q, want it to mention key %q", out, want)
		}
	}
}

// TestView_ShowHelp_ListsNewKeybindings verifies the help overlay lists the
// picks/queue-driving keys wired in issue #785, and no longer claims "k"
// moves the cursor (it Terminates instead).
func TestView_ShowHelp_ListsNewKeybindings(t *testing.T) {
	m := Update(NewModel(), HelpToggleMsg{})

	out := View(m)
	for _, want := range []string{"p ", "u ", "pa ", "k ", "+", "-", "b "} {
		if !strings.Contains(out, want) {
			t.Errorf("View() = %q, want it to mention key %q", out, want)
		}
	}
	if strings.Contains(out, "k / up") || strings.Contains(out, "k/up") {
		t.Errorf("View() = %q, want no mention of \"k\" moving the cursor (it Terminates)", out)
	}
}

// TestView_ShowHelp_ListsPaneModeKey verifies the help overlay lists "m",
// the pane-mode cycle key added by issue #846 (ADR 0025), naming all three
// modes so the operator can discover the cycle without reading the source.
func TestView_ShowHelp_ListsPaneModeKey(t *testing.T) {
	m := Update(NewModel(), HelpToggleMsg{})

	out := View(m)
	for _, want := range []string{"docked", "floating", "fullscreen"} {
		if !strings.Contains(out, want) {
			t.Errorf("View() = %q, want it to mention pane mode %q", out, want)
		}
	}
}

// TestView_DrillInOpen_RendersTranscriptInsteadOfBacklog verifies an open
// drill-in replaces the backlog/queue rendering with the transcript, the
// rendered form by default, plus a hint for the toggle/close keystrokes —
// the operator's view of the work, not just liveness (#648).
func TestView_DrillInOpen_RendersTranscriptInsteadOfBacklog(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Height: 24})
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{{Number: "1", Title: "should not show"}}})
	m = Update(m, DrillInMsg{Number: "42", Rendered: "[implementor] hi", Raw: `{"type":"assistant"}`})

	out := View(m)
	if strings.Contains(out, "should not show") {
		t.Errorf("View() = %q, want the backlog hidden while drilled in", out)
	}
	if !strings.Contains(out, "42") {
		t.Errorf("View() = %q, want the drilled-in issue number", out)
	}
	if !strings.Contains(out, "[implementor] hi") {
		t.Errorf("View() = %q, want the rendered transcript", out)
	}
	if strings.Contains(out, `{"type":"assistant"}`) {
		t.Errorf("View() = %q, want the raw form hidden by default", out)
	}
}

// TestView_DrillInShowRaw_RendersRawInsteadOfRendered verifies toggling
// ShowRaw swaps which form View shows.
func TestView_DrillInShowRaw_RendersRawInsteadOfRendered(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Height: 24})
	m = Update(m, DrillInMsg{Number: "42", Rendered: "[implementor] hi", Raw: `{"type":"assistant"}`})
	m = Update(m, DrillInToggleMsg{})

	out := View(m)
	if strings.Contains(out, "[implementor] hi") {
		t.Errorf("View() = %q, want the rendered form hidden while ShowRaw", out)
	}
	if !strings.Contains(out, `{"type":"assistant"}`) {
		t.Errorf("View() = %q, want the raw form shown while ShowRaw", out)
	}
}

// TestView_DrillInOffset_HidesLinesBeforeOffset verifies scrolling (a
// non-zero Offset) drops the leading lines from the rendered pane instead of
// always showing the transcript's start (issue #786).
func TestView_DrillInOffset_HidesLinesBeforeOffset(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Height: 24})
	m = Update(m, DrillInMsg{Number: "42", Rendered: "l0\nl1\nl2\nl3"})
	m = Update(m, DrillInScrollMsg{Delta: 2})

	out := View(m)
	if strings.Contains(out, "l0") || strings.Contains(out, "l1") {
		t.Errorf("View() = %q, want lines before the offset hidden", out)
	}
	if !strings.Contains(out, "l2") || !strings.Contains(out, "l3") {
		t.Errorf("View() = %q, want lines from the offset onward", out)
	}
}

// TestView_DrillInErr_Surfaced verifies a failed drill-in's error text
// appears instead of blank content.
func TestView_DrillInErr_Surfaced(t *testing.T) {
	m := Update(NewModel(), DrillInMsg{Number: "42", Err: errBoom})

	out := View(m)
	if !strings.Contains(out, errBoom.Error()) {
		t.Errorf("View() = %q, want it to contain %q", out, errBoom.Error())
	}
}

// TestView_DrillInFullscreen_WindowsToViewportHeight verifies the fullscreen
// transcript pane joins only as many lines as the viewport can show, instead
// of the whole tail from Offset to the end of a (potentially multi-MB)
// transcript, so scrolling near the top of a huge transcript doesn't
// re-serialize content nowhere near the screen (issue #722).
func TestView_DrillInFullscreen_WindowsToViewportHeight(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Height: 5})
	content := "HEAD-MARKER\n" + strings.Repeat("x\n", 100) + "TAIL-MARKER"
	m = Update(m, DrillInMsg{Number: "42", Rendered: content})

	out := View(m)
	if !strings.Contains(out, "HEAD-MARKER") {
		t.Errorf("View() = %q, want the first visible line present", out)
	}
	if strings.Contains(out, "TAIL-MARKER") {
		t.Errorf("View() = %q, want content past the viewport height hidden", out)
	}
}

// TestView_TwoColumn_BacklogColumn_HasLabel verifies the backlog renders
// under its own "backlog" column label, so the two-column body (issue #844,
// ADR 0025) reads as two named regions instead of a bare list.
func TestView_TwoColumn_BacklogColumn_HasLabel(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{{Number: "1", Title: "fix the thing"}}})

	out := View(m)
	if !strings.Contains(out, "backlog") {
		t.Errorf("View() = %q, want a labeled backlog column", out)
	}
}

// TestView_TwoColumn_WorkQueueColumn_HasLabelEvenWhenEmpty verifies the
// work-queue column renders its label even with no picks yet — a labeled
// empty column, not one that appears only once something is queued (issue
// #844, ADR 0025).
func TestView_TwoColumn_WorkQueueColumn_HasLabelEvenWhenEmpty(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})

	out := View(m)
	if !strings.Contains(out, "picks:") {
		t.Errorf("View() = %q, want the work-queue column label even with no picks", out)
	}
}

// TestView_TwoColumn_Body_RendersSideBySide verifies the backlog and
// work-queue columns render side by side on a terminal wide enough for both,
// so their rows share output lines instead of stacking one above the other
// (issue #844, ADR 0025).
func TestView_TwoColumn_Body_RendersSideBySide(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{{Number: "1", Title: "backlog issue"}}})
	m.Picks = []Pick{{Number: "42", Title: "queued pick", State: PickQueued}}

	out := View(m)
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "backlog issue") && strings.Contains(l, "queued pick") {
			return
		}
	}
	t.Errorf("View() = %q, want a line carrying both a backlog row and a work-queue row", out)
}

// TestView_TwoColumn_Body_StacksOnNarrowTerminal verifies the body stacks
// the backlog above the work queue, rather than splitting them side by
// side, on a terminal too narrow for two readable columns — degrading
// gracefully instead of wrapping into an unreadable mess (issue #844, ADR
// 0025 AC6).
func TestView_TwoColumn_Body_StacksOnNarrowTerminal(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 30, Height: 24})
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{{Number: "1", Title: "backlog issue"}}})
	m.Picks = []Pick{{Number: "42", Title: "queued pick", State: PickQueued}}

	out := View(m)
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "backlog issue") && strings.Contains(l, "queued pick") {
			t.Errorf("View() on a narrow terminal = %q, want stacked columns, not a shared line", out)
		}
	}
}

// TestView_TwoColumn_Queue_RowsTaggedWithBracketedState verifies each
// work-queue row carries its PickState as a bracketed tag — running, held,
// queued distinguishable at a glance — with a held row also naming its
// blocker and a running row also carrying its heartbeat (issue #844 AC3/
// AC4, ADR 0025).
func TestView_TwoColumn_Queue_RowsTaggedWithBracketedState(t *testing.T) {
	m := NewModel()
	m.Picks = []Pick{
		{Number: "1", Title: "queued one", State: PickQueued},
		{Number: "2", Title: "blocked one", State: PickHeld, BlockedBy: "#41 (native)"},
		{Number: "3", Title: "running one", State: PickRunning, Heartbeat: "7 turns"},
	}

	out := View(m)
	for _, want := range []string{"[queued]", "[held]", "[running]"} {
		if !strings.Contains(out, want) {
			t.Errorf("View() = %q, want bracketed state tag %q", out, want)
		}
	}
	if !strings.Contains(out, "held by #41 (native)") {
		t.Errorf("View() = %q, want the held row's blocker", out)
	}
	if !strings.Contains(out, "7 turns") {
		t.Errorf("View() = %q, want the running row's heartbeat", out)
	}
}

// TestView_TwoColumn_Body_LinesNeverExceedTerminalWidth verifies a joined
// row never exceeds m.Width — a long backlog title/labels paired with a
// verbose held badge must truncate rather than push the line past the
// terminal's edge and wrap into an unreadable mess (issue #844 AC6).
func TestView_TwoColumn_Body_LinesNeverExceedTerminalWidth(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{
		{Number: "1", Title: "a very long backlog title that would otherwise blow out the left column", Labels: []string{"ready-for-agent", "needs-review"}},
	}})
	m.Picks = []Pick{{Number: "42", Title: "a pick with a fairly long title too", State: PickHeld, BlockedBy: "#41 (native), #43 (body)"}}

	out := View(m)
	for _, l := range strings.Split(out, "\n") {
		if n := len([]rune(l)); n > m.Width {
			t.Errorf("View() line %q has %d runes, want it clamped to Width (%d)", l, n, m.Width)
		}
	}
}

// TestView_Focus_MarksFocusedColumnVisually verifies the focused column's
// header carries a visible marker the other column's header doesn't, and
// that Tab moves the marker — the operator's only cue for which column
// cursor keys and Enter currently act on (issue #845).
func TestView_Focus_MarksFocusedColumnVisually(t *testing.T) {
	m := NewModel()

	backlogFocused := View(m)
	if !strings.Contains(backlogFocused, "backlog [focus]") {
		t.Errorf("View() with FocusBacklog = %q, want the backlog header marked focused", backlogFocused)
	}
	if strings.Contains(backlogFocused, "picks [focus]") {
		t.Errorf("View() with FocusBacklog = %q, want the queue header unmarked", backlogFocused)
	}

	m = Update(m, FocusToggleMsg{})
	queueFocused := View(m)
	if !strings.Contains(queueFocused, "picks [focus]") {
		t.Errorf("View() with FocusQueue = %q, want the queue header marked focused", queueFocused)
	}
	if strings.Contains(queueFocused, "backlog [focus]") {
		t.Errorf("View() with FocusQueue = %q, want the backlog header unmarked", queueFocused)
	}
}

// TestView_QueueCursor_MarksHighlightedRow verifies the work-queue column
// marks the row under QueueCursor the same way the backlog column already
// marks Cursor's row (issue #845).
func TestView_QueueCursor_MarksHighlightedRow(t *testing.T) {
	m := Update(NewModel(), FocusToggleMsg{})
	m.Picks = []Pick{{Number: "1", State: PickQueued}, {Number: "2", State: PickQueued}}
	m.QueueCursor = 1

	out := View(m)
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "#2") && strings.HasPrefix(strings.TrimLeft(l, " "), ">") {
			return
		}
	}
	t.Errorf("View() = %q, want row #2 marked with the cursor", out)
}

// TestView_DrillInDocked_KeepsBacklogAndQueueVisible verifies the default
// PaneDocked mode renders the Transcript as a third column while the
// backlog and work-queue columns stay visible on a terminal wide enough for
// three columns (issue #846, ADR 0025).
func TestView_DrillInDocked_KeepsBacklogAndQueueVisible(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 120, Height: 24})
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{{Number: "1", Title: "backlog issue"}}})
	m.Picks = []Pick{{Number: "42", Title: "queued pick", State: PickQueued}}
	m = Update(m, DrillInMsg{Number: "42", Rendered: "[implementor] hi"})

	out := View(m)
	if !strings.Contains(out, "backlog issue") {
		t.Errorf("View() = %q, want the backlog still visible while docked", out)
	}
	if !strings.Contains(out, "queued pick") {
		t.Errorf("View() = %q, want the work queue still visible while docked", out)
	}
	if !strings.Contains(out, "[implementor] hi") {
		t.Errorf("View() = %q, want the transcript content visible while docked", out)
	}
}

// TestView_DrillInNarrowTerminal_FallsBackToFullscreen verifies a terminal
// too narrow for three columns renders the Transcript fullscreen regardless
// of the operator's selected PaneMode — never leaving unreadable, wrapped
// columns (issue #846, ADR 0025 AC4).
func TestView_DrillInNarrowTerminal_FallsBackToFullscreen(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 60, Height: 24})
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{{Number: "1", Title: "backlog issue"}}})
	m.Picks = []Pick{{Number: "42", Title: "queued pick", State: PickQueued}}
	m = Update(m, DrillInMsg{Number: "42", Rendered: "[implementor] hi"})

	out := View(m)
	if strings.Contains(out, "backlog issue") {
		t.Errorf("View() = %q, want the backlog hidden — too narrow for three columns", out)
	}
	if strings.Contains(out, "queued pick") {
		t.Errorf("View() = %q, want the work queue hidden — too narrow for three columns", out)
	}
	if !strings.Contains(out, "[implementor] hi") {
		t.Errorf("View() = %q, want the transcript content visible fullscreen", out)
	}
}

// TestView_DrillInFloating_OverlaysTranscriptOnTwoColumnBody verifies
// PaneFloating renders the Transcript as an overlay atop the two-column
// body — the backlog and queue labels stay present (dimly, on the left) and
// the transcript content becomes visible, distinct from the docked mode's
// permanent three-way column split (issue #846, ADR 0025).
func TestView_DrillInFloating_OverlaysTranscriptOnTwoColumnBody(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 120, Height: 24})
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{{Number: "1", Title: "backlog issue"}}})
	m.Picks = []Pick{{Number: "42", Title: "queued pick", State: PickQueued}}
	m = Update(m, DrillInMsg{Number: "42", Rendered: "[implementor] hi"})
	m = Update(m, PaneModeCycleMsg{})
	if m.PaneMode != PaneFloating {
		t.Fatalf("PaneMode = %v, want PaneFloating after one cycle", m.PaneMode)
	}

	out := View(m)
	if !strings.Contains(out, "backlog") {
		t.Errorf("View() = %q, want the backlog column label still present under the floating pane", out)
	}
	if !strings.Contains(out, "[implementor] hi") {
		t.Errorf("View() = %q, want the transcript content visible in the floating overlay", out)
	}
}

// TestView_DrillInFullscreen_TakesWholeBodyEvenWhenWide verifies an
// explicitly-selected PaneFullscreen hides the backlog/queue even on a
// terminal wide enough for three columns — fullscreen is a selectable mode,
// not just the narrow-terminal fallback (issue #846, ADR 0025).
func TestView_DrillInFullscreen_TakesWholeBodyEvenWhenWide(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 120, Height: 24})
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{{Number: "1", Title: "backlog issue"}}})
	m.Picks = []Pick{{Number: "42", Title: "queued pick", State: PickQueued}}
	m = Update(m, DrillInMsg{Number: "42", Rendered: "[implementor] hi"})
	m = Update(m, PaneModeCycleMsg{}) // docked -> floating
	m = Update(m, PaneModeCycleMsg{}) // floating -> fullscreen
	if m.PaneMode != PaneFullscreen {
		t.Fatalf("PaneMode = %v, want PaneFullscreen after two cycles", m.PaneMode)
	}

	out := View(m)
	if strings.Contains(out, "backlog issue") {
		t.Errorf("View() = %q, want the backlog hidden in fullscreen mode", out)
	}
	if strings.Contains(out, "queued pick") {
		t.Errorf("View() = %q, want the work queue hidden in fullscreen mode", out)
	}
	if !strings.Contains(out, "[implementor] hi") {
		t.Errorf("View() = %q, want the transcript content visible fullscreen", out)
	}
}

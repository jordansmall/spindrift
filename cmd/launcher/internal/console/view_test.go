package console

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"

	"spindrift.dev/launcher/internal/forge"
)

// TestView_ListsVisibleIssuesWithNumberTitleLabels verifies View renders
// each visible issue's number, title, and labels — the backlog line the
// operator reads to decide what to pick in a later issue.
func TestView_ListsVisibleIssuesWithNumberTitleLabels(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 120, Height: 24})
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

// TestView_Header_StatusLine_ShowsRunningWaitingHeldSettledFailed verifies
// the header's status line reports running/cap, waiting, held, settled, and
// failed counts derived from Cap/Live and the Picks slice's PickState tags —
// no new stored counters (issue #843, ADR 0025).
func TestView_Header_StatusLine_ShowsRunningWaitingHeldSettledFailed(t *testing.T) {
	m := NewModel()
	m = Update(m, CapMsg{Cap: 3, Live: 1})
	m.Picks = []Pick{
		{Number: "1", State: PickQueued},
		{Number: "2", State: PickHeld},
		{Number: "3", State: PickSettled},
		{Number: "4", State: PickSettled},
		{Number: "5", State: PickFailed},
	}

	out := View(m)
	want := "running 1/3 · waiting 1 · held 1 · settled 2 · failed 1"
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

// TestBannerHeight_MatchesRenderedRowCount pins bannerHeight to the banner's
// actual rendered row count (the two "===" border rows plus the "spindrift"
// name row) rather than a raw newline count that also includes the leading
// blank line TrimPrefix strips before rendering (issue #852).
func TestBannerHeight_MatchesRenderedRowCount(t *testing.T) {
	const wantRows = 3
	if bannerHeight != wantRows {
		t.Errorf("bannerHeight = %d, want %d (the banner's rendered row count)", bannerHeight, wantRows)
	}
}

// TestView_Header_AlertsRenderBeforeEphemeralPrompts verifies the
// stale-image and competing-dogfood alert lines render as part of the
// header — grouped with the status line, ahead of ephemeral operator
// prompts like an in-progress filter edit — rather than interleaved with
// them (issue #843, ADR 0025). Rebuilding/RebuildErr are the other two
// header alerts and share this same grouping; they're covered separately by
// TestView_Rebuilding_ShowsProgress and TestView_RebuildErr_Surfaced below.
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
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
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
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
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
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
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
	for _, want := range []string{"j", "k", "/", "enter", "esc", "r", "q", "?", "tab", "t", "x", "pgup", "pgdown"} {
		if !strings.Contains(strings.ToLower(out), want) {
			t.Errorf("View() = %q, want it to mention key %q", out, want)
		}
	}
	if strings.Contains(strings.ToLower(out), "d / enter") || strings.Contains(strings.ToLower(out), "d/enter") {
		t.Errorf("View() = %q, want no mention of the retired \"d\" drill-in binding", out)
	}
}

// TestView_ShowHelp_ListsTabKey verifies the help overlay explicitly
// describes the "tab" focus-switch binding, not just a substring match
// that any word containing "t" would vacuously satisfy (issue #995).
func TestView_ShowHelp_ListsTabKey(t *testing.T) {
	m := Update(NewModel(), HelpToggleMsg{})

	out := View(m)
	if !strings.Contains(out, "\n  tab ") {
		t.Errorf("View() = %q, want a \"tab\" key entry", out)
	}
	if !strings.Contains(out, "switch focus between the backlog and work-queue columns") {
		t.Errorf("View() = %q, want it to describe the \"tab\" focus-switch binding", out)
	}
}

// TestView_ShowHelp_DescribesContextSensitiveEnter verifies the help
// overlay's "enter" entry documents both context-sensitive behaviors —
// picking the highlighted backlog row and drilling into a queued pick's
// transcript — not just the bare word "enter" (issue #995).
func TestView_ShowHelp_DescribesContextSensitiveEnter(t *testing.T) {
	m := Update(NewModel(), HelpToggleMsg{})

	out := View(m)
	for _, want := range []string{
		"otherwise: pick the",
		"highlighted backlog row",
		"drill into the",
		"highlighted pick's transcript",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("View() = %q, want it to describe context-sensitive enter behavior %q", out, want)
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

// TestView_ShowHelp_ListsBodyScrollKeys verifies the help overlay lists
// pgup/pgdown as the backlog/queue viewport's own line-scroll keys,
// distinct from the drill-in transcript's identically-named scroll keys
// (issue #1036 AC — help overlay documents the new scroll keys).
func TestView_ShowHelp_ListsBodyScrollKeys(t *testing.T) {
	m := Update(NewModel(), HelpToggleMsg{})

	out := View(m)
	if !strings.Contains(out, "pgup/pgdown  scroll the backlog/queue") {
		t.Errorf("View() = %q, want it to mention the backlog/queue scroll keys", out)
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
// always showing the transcript's start (issue #786). Height is small enough
// that the transcript outruns the viewport budget, or the viewport clamp
// (issue #829) would pin Offset at 0 since the whole thing already fits.
func TestView_DrillInOffset_HidesLinesBeforeOffset(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Height: 4})
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

// TestView_NarrowTerminal_LongBacklog_HeaderStaysPinned verifies the
// header's status line stays visible on a narrow (stacked-layout) terminal
// too — the stacked backlog and picks columns must split the body's row
// budget between them, not each claim it in full, or their combined height
// still pushes the header off-screen (issue #1035 AC1/AC2 review finding).
func TestView_NarrowTerminal_LongBacklog_HeaderStaysPinned(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 40, Height: 10})
	issues := make([]forge.Issue, 20)
	for i := range issues {
		issues[i] = forge.Issue{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("issue %d", i)}
	}
	m = Update(m, IssuesLoadedMsg{Issues: issues})
	picks := make([]Pick, 20)
	for i := range picks {
		picks[i] = Pick{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("pick %d", i), State: PickQueued}
	}
	m.Picks = picks

	out := View(m)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) > m.Height {
		t.Errorf("View() rendered %d lines, want at most Height (%d) — the header must stay pinned", len(lines), m.Height)
	}
}

// TestView_TwoColumn_Queue_RowsTaggedWithBracketedState verifies each
// work-queue row carries its PickState as a bracketed tag — running, held,
// queued distinguishable at a glance — with a held row also naming its
// blocker and a running row also carrying its heartbeat (issue #844 AC3/
// AC4, ADR 0025).
func TestView_TwoColumn_Queue_RowsTaggedWithBracketedState(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
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

// TestView_TwoColumn_Queue_HeldRowSuppressesRedundantFailedBlockerReason
// verifies a held pick whose Reason merely restates the blocker BlockedBy
// already names renders only the "held by" badge, not both — a held pick
// with a failed blocker previously named the same blocker twice on one row
// (issue #755).
func TestView_TwoColumn_Queue_HeldRowSuppressesRedundantFailedBlockerReason(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 300, Height: 24})
	m.Picks = []Pick{
		{Number: "42", Title: "held one", State: PickHeld, BlockedBy: "#41 (native)", Reason: "blocker #41 (native) failed"},
	}

	out := View(m)
	if !strings.Contains(out, "held by #41 (native)") {
		t.Errorf("View() = %q, want the held row's blocker badge", out)
	}
	if strings.Contains(out, "(blocker #41 (native) failed)") {
		t.Errorf("View() = %q, want the redundant failed-blocker reason suppressed", out)
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
		if w := runewidth.StringWidth(l); w > m.Width {
			t.Errorf("View() line %q has display width %d, want it clamped to Width (%d)", l, w, m.Width)
		}
	}
}

// TestView_TwoColumn_Body_BacklogOnlyRowsHaveNoTrailingWhitespace verifies a
// backlog row with no corresponding work-queue row renders without trailing
// spaces — joinColumns previously padded the left column out to leftWidth
// even when the right column had nothing on that row, leaking padding onto
// the end of the line (issue #861).
func TestView_TwoColumn_Body_BacklogOnlyRowsHaveNoTrailingWhitespace(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{
		{Number: "1", Title: "short"},
		{Number: "2", Title: "a mid length backlog title"},
		{Number: "3", Title: "another backlog issue"},
	}})
	m.Picks = []Pick{{Number: "42", Title: "queued pick", State: PickQueued}}

	out := View(m)
	for _, l := range strings.Split(out, "\n") {
		if strings.HasSuffix(l, " ") {
			t.Errorf("View() line %q has trailing whitespace, want none", l)
		}
	}
}

// TestView_TwoColumn_Queue_BlockerVisibleDespiteLongTitle verifies a held
// row's blocker badge survives clipping even when paired with a long title —
// the queue row previously put BlockedBy/Reason/Heartbeat after Title, so
// clip()'s tail truncation dropped the operator-relevant blocker text first
// on realistic 80-column terminals (issue #858).
func TestView_TwoColumn_Queue_BlockerVisibleDespiteLongTitle(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{
		{Number: "123", Title: "fix(console): reassess leftColumnFraction for queue layout", Labels: []string{"ready-for-agent", "needs-review"}},
		{Number: "124", Title: "fix(console): measure display width in clip for wide runes", Labels: []string{"ready-for-agent"}},
	}})
	m.Picks = []Pick{
		{Number: "42", Title: "fix the launcher retry backoff for the dispatch workflow", State: PickHeld, BlockedBy: "#41 (native)", Reason: "issue is closed"},
	}

	out := View(m)
	if !strings.Contains(out, "held by #41 (native)") {
		t.Errorf("View() = %q, want the held row's blocker badge visible despite a long title", out)
	}
}

// TestView_NarrowTerminal_Body_LinesNeverExceedTerminalWidth verifies the
// stacked (narrow-terminal) body clips each line to Width the same way the
// two-column body does — a long backlog or picks row must not blow out
// past the terminal on the narrow path either (issue #860, issue #844 AC6).
// Exercises renderBody directly (as the docked/floating drill-in panes do,
// view.go:407) rather than the full View(), since the header and banner are
// fixed-width and out of this issue's scope.
func TestView_NarrowTerminal_Body_LinesNeverExceedTerminalWidth(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 30, Height: 24})
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{
		{Number: "1", Title: "a very long backlog title that would otherwise blow out a narrow terminal", Labels: []string{"ready-for-agent", "needs-review"}},
	}})
	m.Picks = []Pick{{Number: "42", Title: "a pick with a fairly long title too", State: PickHeld, BlockedBy: "#41 (native), #43 (body)"}}

	out := renderBody(m, unboundedBudget)
	for _, l := range strings.Split(out, "\n") {
		if n := len([]rune(l)); n > m.Width {
			t.Errorf("renderBody() line %q has %d runes, want it clamped to Width (%d)", l, n, m.Width)
		}
	}
}

// TestView_Focus_MarksFocusedColumnVisually verifies the focused column's
// header carries a visible marker the other column's header doesn't, and
// that Tab moves the marker — the operator's only cue for which column
// cursor keys and Enter currently act on (issue #845).
func TestView_Focus_MarksFocusedColumnVisually(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})

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
	m = Update(m, SizeChangedMsg{Width: 80, Height: 24})
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
	if !strings.Contains(out, "[t] toggle raw · [x] close") {
		t.Errorf("View() = %q, want the keystroke hint visible while docked", out)
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
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{
		{Number: "1", Title: "backlog issue"},
		{Number: "2", Title: "second backlog issue"},
	}})
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
	if !strings.Contains(out, "[t] toggle raw · [x] close") {
		t.Errorf("View() = %q, want the keystroke hint visible in the floating overlay", out)
	}
}

// TestView_DrillInDocked_ToggleRaw_ShowsRawHeader verifies the rendered/raw
// toggle works through renderTranscriptColumn — the docked pane mode's
// render path, distinct from renderDrillIn's fullscreen path (issue #1004).
func TestView_DrillInDocked_ToggleRaw_ShowsRawHeader(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 120, Height: 24})
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

// TestView_DrillInFloating_ToggleRaw_ShowsRawHeader verifies the
// rendered/raw toggle also works in the floating pane mode, through the
// same renderTranscriptColumn path as docked (issue #1004).
func TestView_DrillInFloating_ToggleRaw_ShowsRawHeader(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 120, Height: 24})
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{{Number: "1", Title: "backlog issue"}}})
	m.Picks = []Pick{{Number: "42", Title: "queued pick", State: PickQueued}}
	m = Update(m, DrillInMsg{Number: "42", Rendered: "[implementor] hi", Raw: `{"type":"assistant"}`})
	m = Update(m, PaneModeCycleMsg{})
	if m.PaneMode != PaneFloating {
		t.Fatalf("PaneMode = %v, want PaneFloating after one cycle", m.PaneMode)
	}
	m = Update(m, DrillInToggleMsg{})

	out := View(m)
	if strings.Contains(out, "[implementor] hi") {
		t.Errorf("View() = %q, want the rendered form hidden while ShowRaw", out)
	}
	if !strings.Contains(out, `{"type":"assistant"}`) {
		t.Errorf("View() = %q, want the raw form shown while ShowRaw", out)
	}
}

// TestView_DrillInDocked_Scroll_HidesLinesBeforeOffset verifies scrolling
// (a non-zero Offset) drops the leading lines through renderTranscriptColumn
// — the docked pane mode's render path (issue #1004).
func TestView_DrillInDocked_Scroll_HidesLinesBeforeOffset(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 120, Height: 4})
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

// TestView_DrillInFloating_Scroll_HidesLinesBeforeOffset verifies scrolling
// also works in the floating pane mode, through the same
// renderTranscriptColumn path as docked (issue #1004).
func TestView_DrillInFloating_Scroll_HidesLinesBeforeOffset(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 120, Height: 4})
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{
		{Number: "1", Title: "backlog issue 1"},
		{Number: "2", Title: "backlog issue 2"},
		{Number: "3", Title: "backlog issue 3"},
	}})
	m.Picks = []Pick{{Number: "42", Title: "queued pick", State: PickQueued}}
	m = Update(m, DrillInMsg{Number: "42", Rendered: "l0\nl1\nl2\nl3"})
	m = Update(m, PaneModeCycleMsg{})
	if m.PaneMode != PaneFloating {
		t.Fatalf("PaneMode = %v, want PaneFloating after one cycle", m.PaneMode)
	}
	m = Update(m, DrillInScrollMsg{Delta: 2})

	out := View(m)
	if strings.Contains(out, "l0") || strings.Contains(out, "l1") {
		t.Errorf("View() = %q, want lines before the offset hidden", out)
	}
	if !strings.Contains(out, "l2") || !strings.Contains(out, "l3") {
		t.Errorf("View() = %q, want lines from the offset onward", out)
	}
}

// TestView_DrillInDocked_Err_Surfaced verifies a failed drill-in's error
// text appears through renderTranscriptColumn — the docked pane mode's
// render path (issue #1004).
func TestView_DrillInDocked_Err_Surfaced(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 120, Height: 24})
	m = Update(m, DrillInMsg{Number: "42", Err: errBoom})

	out := View(m)
	if !strings.Contains(out, errBoom.Error()) {
		t.Errorf("View() = %q, want it to contain %q", out, errBoom.Error())
	}
}

// TestView_DrillInFloating_Err_Surfaced verifies a failed drill-in's error
// text also appears in the floating pane mode, through the same
// renderTranscriptColumn path as docked (issue #1004).
func TestView_DrillInFloating_Err_Surfaced(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 120, Height: 24})
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{{Number: "1", Title: "backlog issue"}}})
	m.Picks = []Pick{{Number: "42", Title: "queued pick", State: PickQueued}}
	m = Update(m, DrillInMsg{Number: "42", Err: errBoom})
	m = Update(m, PaneModeCycleMsg{})
	if m.PaneMode != PaneFloating {
		t.Fatalf("PaneMode = %v, want PaneFloating after one cycle", m.PaneMode)
	}

	out := View(m)
	if !strings.Contains(out, errBoom.Error()) {
		t.Errorf("View() = %q, want it to contain %q", out, errBoom.Error())
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

// TestView_LongBacklog_HeaderStaysPinned verifies the header's status line
// stays visible, and the backlog column stops short of the last loaded
// issue, when the backlog has more rows than the terminal has height for —
// the header must never scroll off the top (issue #1035 AC1/AC2).
func TestView_LongBacklog_HeaderStaysPinned(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 10})
	issues := make([]forge.Issue, 50)
	for i := range issues {
		issues[i] = forge.Issue{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("issue %d", i)}
	}
	m = Update(m, IssuesLoadedMsg{Issues: issues})

	out := View(m)
	if !strings.Contains(out, "running 0/0") {
		t.Errorf("View() = %q, want the header status line present even with a long backlog", out)
	}
	if strings.Contains(out, "issue 49") {
		t.Errorf("View() = %q, want the last issue clipped past the viewport height", out)
	}
}

// TestView_LongBacklog_ShowsMoreBelowAffordance verifies a truncated backlog
// column ends with an "N more below" line naming how many rows were clipped,
// so the operator knows the list is incomplete rather than reading a short
// backlog as the whole one (issue #1035 AC4).
func TestView_LongBacklog_ShowsMoreBelowAffordance(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 10})
	issues := make([]forge.Issue, 50)
	for i := range issues {
		issues[i] = forge.Issue{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("issue %d", i)}
	}
	m = Update(m, IssuesLoadedMsg{Issues: issues})

	out := View(m)
	if !strings.Contains(out, "more below") {
		t.Errorf("View() = %q, want a \"more below\" affordance line", out)
	}
}

// TestView_LongPicksQueue_HeaderStaysPinnedAndShowsMoreBelow verifies the
// picks column is height-budgeted the same way the backlog column is — a
// long work queue can't push the header off-screen either, and it gets its
// own truncation affordance (issue #1035 AC1/AC2/AC4).
func TestView_LongPicksQueue_HeaderStaysPinnedAndShowsMoreBelow(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 10})
	picks := make([]Pick, 50)
	for i := range picks {
		picks[i] = Pick{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("pick %d", i), State: PickQueued}
	}
	m.Picks = picks

	out := View(m)
	if !strings.Contains(out, "running 0/0") {
		t.Errorf("View() = %q, want the header status line present even with a long picks queue", out)
	}
	if strings.Contains(out, "pick 49") {
		t.Errorf("View() = %q, want the last pick clipped past the viewport height", out)
	}
	if !strings.Contains(out, "more below") {
		t.Errorf("View() = %q, want a \"more below\" affordance line", out)
	}
}

// TestView_ScrolledBacklog_ReachesLastRow verifies scrolling the backlog
// column all the way (BacklogOffset clamped to its maximum) surfaces the
// last loaded issue — every row in the (filtered) backlog is reachable by
// scrolling, not just the leading window (issue #1036 AC3).
func TestView_ScrolledBacklog_ReachesLastRow(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 10})
	issues := make([]forge.Issue, 50)
	for i := range issues {
		issues[i] = forge.Issue{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("issue %d", i)}
	}
	m = Update(m, IssuesLoadedMsg{Issues: issues})

	m = Update(m, ScrollMsg{Delta: 1000})

	out := View(m)
	if !strings.Contains(out, "issue 49") {
		t.Errorf("View() = %q, want the last issue reachable once scrolled all the way down", out)
	}
}

// TestView_ScrolledQueue_ReachesLastRow verifies the same reachability for
// the picks column once it has focus and is scrolled all the way — issue
// #1036 AC3 covers both columns, since Tab already toggles focus between
// them.
func TestView_ScrolledQueue_ReachesLastRow(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 10})
	m = Update(m, FocusToggleMsg{})
	picks := make([]Pick, 50)
	for i := range picks {
		picks[i] = Pick{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("pick %d", i), State: PickQueued}
	}
	m.Picks = picks

	m = Update(m, ScrollMsg{Delta: 1000})

	out := View(m)
	if !strings.Contains(out, "pick 49") {
		t.Errorf("View() = %q, want the last pick reachable once scrolled all the way down", out)
	}
}

// TestView_BacklogColumn_ShowsPositionIndicator verifies the backlog column
// label carries a compact "X-Y of N" position indicator reflecting the
// visible row range and total, so the operator can see where they are in a
// long backlog without counting rows (issue #1037 AC3).
func TestView_BacklogColumn_ShowsPositionIndicator(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 10})
	issues := make([]forge.Issue, 50)
	for i := range issues {
		issues[i] = forge.Issue{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("issue %d", i)}
	}
	m = Update(m, IssuesLoadedMsg{Issues: issues})

	out := View(m)
	if !strings.Contains(out, "backlog [focus] (1-4 of 50):") {
		t.Errorf("View() = %q, want the backlog label to show \"(1-4 of 50)\"", out)
	}

	m = Update(m, ScrollMsg{Delta: 5})
	out = View(m)
	if !strings.Contains(out, "backlog [focus] (6-9 of 50):") {
		t.Errorf("View() = %q, want the backlog label to show \"(6-9 of 50)\" after scrolling", out)
	}
}

// TestView_QueueColumn_ShowsPositionIndicator verifies the picks column
// label carries the same position indicator as the backlog column, and that
// it is absent when the queue is empty rather than reading "(1-0 of 0)"
// (issue #1037 AC3/AC4).
func TestView_QueueColumn_ShowsPositionIndicator(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 10})
	m = Update(m, FocusToggleMsg{})
	picks := make([]Pick, 50)
	for i := range picks {
		picks[i] = Pick{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("pick %d", i), State: PickQueued}
	}
	m.Picks = picks

	out := View(m)
	if !strings.Contains(out, "picks [focus] (1-4 of 50):") {
		t.Errorf("View() = %q, want the picks label to show \"(1-4 of 50)\"", out)
	}

	empty := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 10})
	out = View(empty)
	if !strings.Contains(out, "picks:") || strings.Contains(out, "picks (") {
		t.Errorf("View() = %q, want the picks label with no position indicator when empty", out)
	}
}

// TestView_LongBacklog_WithRefreshError_HeaderStaysPinned verifies the
// trailing "refresh failed" line is budgeted the same way prompt lines are —
// a long backlog plus a refresh error must not together push the header off
// the top, or push the body's own budget past what actually renders (issue
// #1035 AC1/AC2 review finding).
func TestView_LongBacklog_WithRefreshError_HeaderStaysPinned(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 10})
	issues := make([]forge.Issue, 20)
	for i := range issues {
		issues[i] = forge.Issue{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("issue %d", i)}
	}
	m = Update(m, IssuesLoadedMsg{Issues: issues})
	m = Update(m, IssuesLoadedMsg{Err: errors.New("boom")})

	out := View(m)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) > m.Height {
		t.Errorf("View() rendered %d lines, want at most Height (%d) — a refresh error must not push the header off", len(lines), m.Height)
	}
	if !strings.Contains(out, "running 0/0") {
		t.Errorf("View() = %q, want the header status line present", out)
	}
}

// TestView_ExtremelyShortTerminal_NeverExceedsHeight verifies a terminal too
// short even for a labeled empty column never renders more lines than
// Height, in both the wide (side-by-side) and narrow (stacked) layouts — a
// column's label line is part of the body budget too, not an unconditional
// floor on top of it (issue #1035 AC1/AC2 review finding).
func TestView_ExtremelyShortTerminal_NeverExceedsHeight(t *testing.T) {
	issues := make([]forge.Issue, 20)
	for i := range issues {
		issues[i] = forge.Issue{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("issue %d", i)}
	}
	picks := make([]Pick, 20)
	for i := range picks {
		picks[i] = Pick{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("pick %d", i), State: PickQueued}
	}

	for _, tc := range []struct {
		width, height int
	}{
		{width: 80, height: 1},
		{width: 80, height: 2},
		{width: 40, height: 1},
		{width: 40, height: 2},
		{width: 40, height: 3},
	} {
		m := Update(NewModel(), SizeChangedMsg{Width: tc.width, Height: tc.height})
		m = Update(m, IssuesLoadedMsg{Issues: issues})
		m.Picks = picks

		out := View(m)
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		if len(lines) > tc.height {
			t.Errorf("width=%d height=%d: View() rendered %d lines, want at most %d", tc.width, tc.height, len(lines), tc.height)
		}
	}
}

// TestView_HeaderHeight_AdaptsToAlertLines verifies the body's row budget
// shrinks as alert lines (stale/rebuilding/dogfood) are added to the header,
// so a longer header always leaves proportionally less room for the body
// instead of a stale, hardcoded header-height assumption (issue #1035 AC3).
func TestView_HeaderHeight_AdaptsToAlertLines(t *testing.T) {
	issues := make([]forge.Issue, 20)
	for i := range issues {
		issues[i] = forge.Issue{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("issue %d", i)}
	}

	plain := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 12})
	plain = Update(plain, IssuesLoadedMsg{Issues: issues})
	plainOut := View(plain)
	plainRows := strings.Count(plainOut, "issue ")

	withAlert := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 12})
	withAlert = Update(withAlert, IssuesLoadedMsg{Issues: issues})
	withAlert = Update(withAlert, StaleStatusMsg{Stale: true, Message: "rebuild needed"})
	withAlertOut := View(withAlert)
	withAlertRows := strings.Count(withAlertOut, "issue ")

	if withAlertRows >= plainRows {
		t.Errorf("visible issue rows with a stale alert = %d, want fewer than without one (%d) — the extra header line should shrink the body budget", withAlertRows, plainRows)
	}
}

// TestView_HeaderHeight_BannerCollapse_StillBudgetsBody verifies the body
// windowing still leaves the header (status line) visible and clips the
// backlog on a terminal too short for the banner — the collapsed-banner
// header height, not the tall one, must drive the budget (issue #1035 AC3).
func TestView_HeaderHeight_BannerCollapse_StillBudgetsBody(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 3})
	issues := make([]forge.Issue, 20)
	for i := range issues {
		issues[i] = forge.Issue{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("issue %d", i)}
	}
	m = Update(m, IssuesLoadedMsg{Issues: issues})

	out := View(m)
	if strings.Contains(out, "spindrift") {
		t.Errorf("View() = %q, want the banner collapsed on a short terminal", out)
	}
	if !strings.Contains(out, "running 0/0") {
		t.Errorf("View() = %q, want the status line present", out)
	}
	if strings.Contains(out, "issue 19") {
		t.Errorf("View() = %q, want the backlog clipped to the short viewport", out)
	}
}

// TestClip_WideCharacters_MeasuresDisplayWidthNotRuneCount verifies clip
// measures visual display width, not rune count, when deciding whether to
// truncate or pad — a CJK string can be well under a rune-count budget while
// its display width (2 columns per wide rune) already overflows the
// terminal (issue #859).
func TestClip_WideCharacters_MeasuresDisplayWidthNotRuneCount(t *testing.T) {
	s := "中文标题超长测试文字" // 10 runes, 20 display columns
	got := clip(s, 10, false)
	if got == s {
		t.Errorf("clip(%q, 10, false) = %q, want truncated — 10 runes is 20 display columns, over the width-10 budget", s, got)
	}
	if w := runewidth.StringWidth(got); w > 10 {
		t.Errorf("clip(%q, 10, false) = %q with display width %d, want at most 10", s, got, w)
	}
}

// TestView_Backlog_SanitizesTitleAndLabelControlSequences verifies a backlog title
// carrying CSI/OSC escape sequences renders with the escapes stripped and
// the surrounding text intact — a tracker title is untrusted input, and
// Bubble Tea does not filter arbitrary control sequences before writing to
// the operator's terminal (issue #862).
func TestView_Backlog_SanitizesTitleAndLabelControlSequences(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{
		{Number: "1", Title: "evil\x1b[2Jtitle\x1b]0;pwned\x07here", Labels: []string{"evil\x1b[2Jlabel"}},
	}})

	out := View(m)
	if strings.Contains(out, "\x1b") {
		t.Errorf("View() = %q, want no raw escape bytes in backlog title", out)
	}
	if !strings.Contains(out, "eviltitlehere") {
		t.Errorf("View() = %q, want the surrounding title text intact after stripping escapes", out)
	}
	if !strings.Contains(out, "evillabel") {
		t.Errorf("View() = %q, want the surrounding label text intact after stripping escapes", out)
	}
}

// TestView_Queue_SanitizesTitleAndReasonControlSequences verifies a pick's
// Title and Reason — both tracker/dispatch-derived free text — render with
// CSI/OSC escape sequences stripped and surrounding text intact (issue #862).
func TestView_Queue_SanitizesTitleAndReasonControlSequences(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 300, Height: 24})
	m.Picks = []Pick{
		{Number: "42", Title: "evil\x1b[2Jtitle", State: PickHeld, Reason: "bad\x1b]0;pwned\x07reason"},
	}

	out := View(m)
	if strings.Contains(out, "\x1b") {
		t.Errorf("View() = %q, want no raw escape bytes in queue row", out)
	}
	if !strings.Contains(out, "eviltitle") {
		t.Errorf("View() = %q, want the surrounding title text intact after stripping escapes", out)
	}
	if !strings.Contains(out, "badreason") {
		t.Errorf("View() = %q, want the surrounding reason text intact after stripping escapes", out)
	}
}

// TestSplitLeftWidth_ClampsToLeftColumnFraction verifies splitLeftWidth caps
// its result at leftColumnFraction of width when the backlog's own longest
// line runs past that share (issue #1001).
func TestSplitLeftWidth_ClampsToLeftColumnFraction(t *testing.T) {
	backlog := "a very long backlog line that would blow past the fraction cap"
	width := 40

	got := splitLeftWidth(backlog, width)
	want := int(float64(width) * leftColumnFraction)
	if got != want {
		t.Errorf("splitLeftWidth(%q, %d) = %d, want %d (clamped to leftColumnFraction)", backlog, width, got, want)
	}
}

// TestView_BodyAndDockedBody_AgreeOnLeftColumnWidth verifies renderBody and
// renderDockedBody place the work-queue column at the same offset for the
// same effective body width and backlog content — i.e. the two layouts
// actually stay in sync, not just the extracted helper in isolation.
// renderDockedBody carves its body width down from m.Width by the
// Transcript column's share first (bodyWidth = m.Width - transcriptWidth),
// so the docked model here uses a wider m.Width chosen so its bodyWidth
// equals the plain body's m.Width exactly. The backlog title is long enough
// to force the leftColumnFraction clamp in both, so the test would catch a
// future edit that re-derives the clamp in only one of the two callers
// (issue #1001 AC3/AC4). Fixture text is ASCII-only, so the byte offset
// strings.Index returns equals both the rune count and the display-column
// position — this isn't a general wide-rune column-offset proof, just a
// same-input comparison between the two callers.
func TestView_BodyAndDockedBody_AgreeOnLeftColumnWidth(t *testing.T) {
	const bodyWidth = 60    // leftColumnFraction clamp: int(60 * 2/5) = 24
	const dockedWidth = 100 // transcriptWidth = int(100*2/5) = 40, bodyWidth = 60

	issues := []forge.Issue{{Number: "1", Title: "a very long backlog title that blows past the fraction cap"}}
	picks := []Pick{{Number: "42", Title: "QUEUEMARK", State: PickQueued}}

	mBody := Update(NewModel(), SizeChangedMsg{Width: bodyWidth, Height: 24})
	mBody = Update(mBody, IssuesLoadedMsg{Issues: issues})
	mBody.Picks = picks

	mDocked := Update(NewModel(), SizeChangedMsg{Width: dockedWidth, Height: 24})
	mDocked = Update(mDocked, IssuesLoadedMsg{Issues: issues})
	mDocked.Picks = picks
	mDocked = Update(mDocked, DrillInMsg{Number: "42", Rendered: "[implementor] hi"})

	bodyOut := renderBody(mBody, unboundedBudget)
	dockedOut := renderDockedBody(mDocked)

	bodyLine := lineContaining(t, bodyOut, "QUEUEMARK")
	dockedLine := lineContaining(t, dockedOut, "QUEUEMARK")

	bodyOffset := strings.Index(bodyLine, "QUEUEMARK")
	dockedOffset := strings.Index(dockedLine, "QUEUEMARK")
	if bodyOffset != dockedOffset {
		t.Errorf("queue column starts at byte %d in renderBody but %d in renderDockedBody, want equal (same leftWidth)", bodyOffset, dockedOffset)
	}
}

// lineContaining returns the first line of s containing substr, failing the
// test if no line matches.
func lineContaining(t *testing.T, s, substr string) string {
	t.Helper()
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, substr) {
			return l
		}
	}
	t.Fatalf("no line in %q contains %q", s, substr)
	return ""
}

// TestSplitLeftWidth_UsesBacklogWidthUnderFractionCap verifies splitLeftWidth
// returns the backlog's own longest-line width, not the fraction cap, when
// the backlog is narrower than its fraction share — the fraction is a
// ceiling, not a fixed column width (issue #1001).
func TestSplitLeftWidth_UsesBacklogWidthUnderFractionCap(t *testing.T) {
	backlog := "short"
	width := 80

	got := splitLeftWidth(backlog, width)
	want := maxLineWidth(backlog)
	if got != want {
		t.Errorf("splitLeftWidth(%q, %d) = %d, want %d (backlog's own width)", backlog, width, got, want)
	}
}

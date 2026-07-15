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
// parallelism cap and current live count (issue #653) — visible without a
// separate command, the same way the queue rows already are.
func TestView_CapAndLive_Shown(t *testing.T) {
	m := NewModel()
	m = Update(m, CapMsg{Cap: 3, Live: 1})

	out := View(m)
	if !strings.Contains(out, "cap: 1/3") {
		t.Errorf("View() = %q, want a \"cap: 1/3\" line (live/cap)", out)
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
// visually marked so the operator can see which issue j/k/arrows will act
// on (issue #784).
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

// TestView_DrillInOpen_RendersTranscriptInsteadOfBacklog verifies an open
// drill-in replaces the backlog/queue rendering with the transcript, the
// rendered form by default, plus a hint for the toggle/close keystrokes —
// the operator's view of the work, not just liveness (#648).
func TestView_DrillInOpen_RendersTranscriptInsteadOfBacklog(t *testing.T) {
	m := NewModel()
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
	m := NewModel()
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
	m := NewModel()
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

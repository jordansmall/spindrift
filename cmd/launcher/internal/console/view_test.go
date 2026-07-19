package console

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
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
// no new stored counters (issue #843, ADR 0025). Each count segment is
// asserted individually rather than as one contiguous line: per-role styling
// (ADR 0031) wraps each segment in its own ANSI escape codes, so content
// survives styling as separate substrings, not as one unbroken string
// (issue #1499 AC).
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
	for _, want := range []string{"running 1/3", "waiting 1", "held 1", "settled 2", "failed 1"} {
		if !strings.Contains(out, want) {
			t.Errorf("View() = %q, want it to contain status segment %q", out, want)
		}
	}
}

// TestView_Header_StatusLine_StyledByRole verifies the status line renders
// with ANSI color codes on a color-capable terminal — colour applied by
// semantic role, per ADR 0031 — rather than as bare text.
func TestView_Header_StatusLine_StyledByRole(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	m := NewModel()
	m = Update(m, CapMsg{Cap: 3, Live: 1})
	m.Picks = []Pick{{Number: "1", State: PickFailed}}

	out := View(m)
	if !strings.Contains(out, "\x1b[") {
		t.Errorf("View() = %q, want the status line styled with an ANSI escape sequence", out)
	}
}

// TestView_Header_StaleAlert_StyledWithGlyph verifies the stale-image alert
// carries the plain-Unicode warning glyph and renders styled by role
// (ADR 0031), while keeping its existing content.
func TestView_Header_StaleAlert_StyledWithGlyph(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	m := Update(NewModel(), StaleStatusMsg{RebuildStatus: RebuildStatus{Stale: true, Message: "rebuild needed"}})
	out := View(m)

	if !strings.Contains(out, "⚠") {
		t.Errorf("View() = %q, want the stale alert to carry the warning glyph", out)
	}
	if !strings.Contains(out, "image stale: rebuild needed") {
		t.Errorf("View() = %q, want the stale alert content preserved", out)
	}
	if !strings.Contains(out, "\x1b[") {
		t.Errorf("View() = %q, want the stale alert styled with an ANSI escape sequence", out)
	}
}

// TestView_Header_RebuildingAlert_StyledWithGlyph verifies the
// rebuilding-in-progress alert carries the plain-Unicode rebuilding glyph
// and renders styled by role (ADR 0031), while keeping its existing
// content.
func TestView_Header_RebuildingAlert_StyledWithGlyph(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	m := Update(NewModel(), StaleStatusMsg{RebuildStatus: RebuildStatus{Rebuilding: true}})
	out := View(m)

	if !strings.Contains(out, "↻") {
		t.Errorf("View() = %q, want the rebuilding alert to carry the rebuilding glyph", out)
	}
	if !strings.Contains(out, "rebuilding image...") {
		t.Errorf("View() = %q, want the rebuilding alert content preserved", out)
	}
	if !strings.Contains(out, "\x1b[") {
		t.Errorf("View() = %q, want the rebuilding alert styled with an ANSI escape sequence", out)
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
//
// wantRows stays a literal rather than a strings.Count(...)-derived value:
// deriving it with bannerHeight's own formula would make this test
// tautological (formula(banner) == formula(banner), always true even if the
// formula itself were wrong) — precisely what pinning it here guards against
// (issue #1242).
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
	m = Update(m, StaleStatusMsg{RebuildStatus: RebuildStatus{Stale: true, Message: "rebuild needed"}})
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
	// Per-segment, not one contiguous line: per-role styling (ADR 0031)
	// wraps each segment in its own ANSI escape codes.
	for _, want := range []string{"running 0/0", "waiting 0", "held 0", "settled 0"} {
		if !strings.Contains(out, want) {
			t.Errorf("View() = %q, want a clean status segment %q", out, want)
		}
	}
	for _, unwanted := range []string{"stale", "dogfood", "!!", "⚠", "↻", "ℹ"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("View() = %q, want no stray %q in a launch-less header", out, unwanted)
		}
	}
}

// TestView_ListsPicksWithNumberTitleState verifies View renders a work
// Section's rows with number, title, and state — the queue overview the
// operator reads without a separate command (#646, ADR 0030 moved this from
// the two-column queue to whichever Section the pick's state maps into).
func TestView_ListsPicksWithNumberTitleState(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	m = Update(m, QueueSnapshotMsg{Picks: []Pick{
		{Number: "42", Title: "fix the thing", State: PickQueued},
	}})
	m = Update(m, SectionJumpMsg{Section: SectionRunning})

	out := View(m)
	for _, want := range []string{"42", "fix the thing", "queued"} {
		if !strings.Contains(out, want) {
			t.Errorf("View() = %q, want it to contain %q", out, want)
		}
	}
}

// TestView_DissolvedPick_ShowsReason verifies a dissolved row in the Failed
// Section also carries its reason, so the operator sees why a pick never
// launched (#646, ADR 0030's fold of PickDissolved into SectionFailed).
func TestView_DissolvedPick_ShowsReason(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	m = Update(m, QueueSnapshotMsg{Picks: []Pick{
		{Number: "7", Title: "raced pick", State: PickDissolved, Reason: "issue is closed"},
	}})
	m = Update(m, SectionJumpMsg{Section: SectionFailed})

	out := View(m)
	for _, want := range []string{"7", "raced pick", "dissolved", "issue is closed"} {
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
	m = Update(m, QueueSnapshotMsg{Picks: []Pick{
		{Number: "42", Title: "fix the thing", State: PickRunning, Heartbeat: "#42 [edit] \xc2\xb7 7 turns"},
	}})
	m = Update(m, SectionJumpMsg{Section: SectionRunning})

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

	m := Update(NewModel(), StaleStatusMsg{RebuildStatus: RebuildStatus{Stale: true, Message: "rebuild needed (main tip abc123 produces spindrift:def, loaded image is spindrift:abc)"}})
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
	m := Update(NewModel(), StaleStatusMsg{RebuildStatus: RebuildStatus{Stale: true, Rebuilding: true}})
	out := View(m)
	if !strings.Contains(out, "rebuild") {
		t.Errorf("View() = %q, want a rebuilding-in-progress line", out)
	}
}

// TestView_RebuildErr_Surfaced verifies a failed rebuild's error text
// appears, and launches stay noted as held (Stale remains true).
func TestView_RebuildErr_Surfaced(t *testing.T) {
	m := Update(NewModel(), StaleStatusMsg{RebuildStatus: RebuildStatus{Stale: true, Err: "nix build failed"}})
	out := View(m)
	if !strings.Contains(out, "nix build failed") {
		t.Errorf("View() = %q, want the rebuild failure surfaced", out)
	}
}

// TestView_Header_RebuildFailedAlert_StyledWithGlyph verifies the
// rebuild-failed alert carries the plain-Unicode warning glyph and renders
// styled by role (ADR 0031), while keeping its existing content.
func TestView_Header_RebuildFailedAlert_StyledWithGlyph(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	m := Update(NewModel(), StaleStatusMsg{RebuildStatus: RebuildStatus{Err: "nix build failed"}})
	out := View(m)

	var bannerLine string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "rebuild failed") {
			bannerLine = line
			break
		}
	}
	if bannerLine == "" {
		t.Fatalf("View() = %q, want a rebuild-failed banner line", out)
	}
	if !strings.Contains(bannerLine, "⚠") {
		t.Errorf("rebuild-failed banner line = %q, want the warning glyph", bannerLine)
	}
	if !strings.Contains(bannerLine, "nix build failed") {
		t.Errorf("rebuild-failed banner line = %q, want the error content preserved", bannerLine)
	}
	if !strings.Contains(bannerLine, "\x1b[") {
		t.Errorf("rebuild-failed banner line = %q, want it styled with an ANSI escape sequence", bannerLine)
	}
}

// TestView_RebuildErr_Truncated verifies a long, multi-line RebuildErr (the
// merged nix stdout+stderr RunNixBuild wraps into one error, issue #1131)
// renders as a single bounded banner line instead of blowing out the header.
func TestView_RebuildErr_Truncated(t *testing.T) {
	// Pinned rather than inherited: the rebuild-failed banner is styled
	// (ADR 0031), and the width bound below must hold whether or not this
	// process's ambient TERM happens to be color-capable.
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	long := strings.Repeat("line of nix build output that is quite long\n", 20)
	m := Update(NewModel(), StaleStatusMsg{RebuildStatus: RebuildStatus{Stale: true, Err: long}})
	out := View(m)

	var bannerLine string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "rebuild failed") {
			bannerLine = line
			break
		}
	}
	if bannerLine == "" {
		t.Fatalf("View() = %q, want a rebuild-failed banner line", out)
	}
	if !strings.HasSuffix(bannerLine, "…") {
		t.Errorf("banner line = %q, want it truncated with an ellipsis", bannerLine)
	}
	// lipgloss.Width, not runewidth.StringWidth: the banner line's prefix
	// now carries ANSI color codes (ADR 0031), which runewidth counts as
	// display width and lipgloss's ANSI-aware measurement does not.
	prefixWidth := lipgloss.Width(glyphWarning + " rebuild failed: ")
	if n := lipgloss.Width(bannerLine); n > bannerErrWidth+prefixWidth {
		t.Errorf("banner line width = %d, want <= %d", n, bannerErrWidth+prefixWidth)
	}
	if m.RebuildStatus.Err != long {
		t.Errorf("m.RebuildStatus.Err = %q, want the full untruncated text preserved", m.RebuildStatus.Err)
	}
}

// TestView_OrphanRecoveryErr_Surfaced verifies a startup orphan recovery
// failure's error text appears in the rendered header (issue #1218).
func TestView_OrphanRecoveryErr_Surfaced(t *testing.T) {
	m := Update(NewModel(), OrphanRecoveryMsg{Err: "failed to adopt orphan #42: boom"})
	out := View(m)
	if !strings.Contains(out, "failed to adopt orphan #42: boom") {
		t.Errorf("View() = %q, want the orphan recovery failure surfaced", out)
	}
}

// TestView_Header_OrphanRecoveryAlert_StyledWithGlyph verifies the
// orphan-adopt-failed alert carries the plain-Unicode warning glyph and
// renders styled by role (ADR 0031), while keeping its existing content.
func TestView_Header_OrphanRecoveryAlert_StyledWithGlyph(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	m := Update(NewModel(), OrphanRecoveryMsg{Err: "boom"})
	out := View(m)

	var bannerLine string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "orphan adopt failed") {
			bannerLine = line
			break
		}
	}
	if bannerLine == "" {
		t.Fatalf("View() = %q, want an orphan-adopt-failed banner line", out)
	}
	if !strings.Contains(bannerLine, "⚠") {
		t.Errorf("orphan-recovery banner line = %q, want the warning glyph", bannerLine)
	}
	if !strings.Contains(bannerLine, "\x1b[") {
		t.Errorf("orphan-recovery banner line = %q, want it styled with an ANSI escape sequence", bannerLine)
	}
}

// TestView_BranchSwitchNotice_Surfaced verifies a rebuild's branch-switch
// notice appears in the rendered header — the silent-switch gap issue #1141
// closes: an operator whose pwd got moved off a branch during rebuild needs
// to see it, not discover it cold.
func TestView_BranchSwitchNotice_Surfaced(t *testing.T) {
	fresh := View(NewModel())
	if strings.Contains(fresh, "switched") {
		t.Errorf("View() with no branch-switch notice = %q, want no mention of a switch", fresh)
	}

	m := Update(NewModel(), StaleStatusMsg{RebuildStatus: RebuildStatus{BranchSwitchNotice: "switched off-branch tree from feature to main"}})
	out := View(m)
	if !strings.Contains(out, "switched off-branch tree from feature to main") {
		t.Errorf("View() = %q, want the branch-switch notice surfaced", out)
	}
}

// TestView_Header_BranchSwitchAndDogfoodNotices_StyledWithGlyph verifies the
// branch-switch and competing-dogfood notice lines carry the plain-Unicode
// notice glyph and render styled by role (ADR 0031), while keeping their
// existing content.
func TestView_Header_BranchSwitchAndDogfoodNotices_StyledWithGlyph(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	m := Update(NewModel(), StaleStatusMsg{RebuildStatus: RebuildStatus{BranchSwitchNotice: "switched off-branch tree from feature to main"}})
	m = Update(m, DogfoodNoticeMsg{Live: true})
	out := View(m)

	var branchLine, dogfoodLine string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "switched off-branch") {
			branchLine = line
		}
		if strings.Contains(line, "dogfood") {
			dogfoodLine = line
		}
	}
	if branchLine == "" {
		t.Fatalf("View() = %q, want a branch-switch notice line", out)
	}
	if dogfoodLine == "" {
		t.Fatalf("View() = %q, want a dogfood notice line", out)
	}
	for _, line := range []string{branchLine, dogfoodLine} {
		if !strings.Contains(line, "ℹ") {
			t.Errorf("notice line = %q, want the notice glyph", line)
		}
		if !strings.Contains(line, "\x1b[") {
			t.Errorf("notice line = %q, want it styled with an ANSI escape sequence", line)
		}
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

// TestView_PendingPick_EmptyBacklog_HidesIndicator verifies the "p_" pending
// pick indicator does not render when the backlog is empty — with no
// highlighted row, the chord's resolution is a no-op, so showing the
// indicator would promise a pick that can't happen (issue #1237).
func TestView_PendingPick_EmptyBacklog_HidesIndicator(t *testing.T) {
	m := Update(NewModel(), PickPendingMsg{})

	out := View(m)
	if strings.Contains(out, "p_") {
		t.Errorf("View() with empty backlog = %q, want no \"p_\" indicator", out)
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
	for _, want := range []string{"j", "k", "H", "L", "/", "enter", "esc", "r", "q", "?", "t", "x", "pgup", "pgdown"} {
		if !strings.Contains(strings.ToLower(out), strings.ToLower(want)) {
			t.Errorf("View() = %q, want it to mention key %q", out, want)
		}
	}
	if strings.Contains(strings.ToLower(out), "d / enter") || strings.Contains(strings.ToLower(out), "d/enter") {
		t.Errorf("View() = %q, want no mention of the retired \"d\" drill-in binding", out)
	}
}

// TestView_ShowHelp_ListsSectionKeys verifies the help overlay describes
// H/L (previous/next Section) and 1-5 (direct jump) — the section-switched
// list's navigation, replacing the retired "tab" focus-switch binding (ADR
// 0030, issue #1500).
func TestView_ShowHelp_ListsSectionKeys(t *testing.T) {
	m := Update(NewModel(), HelpToggleMsg{})

	out := View(m)
	if !strings.Contains(out, "\n  H / L") {
		t.Errorf("View() = %q, want an \"H / L\" key entry", out)
	}
	if !strings.Contains(out, "previous / next Section") {
		t.Errorf("View() = %q, want it to describe the H/L Section-switch binding", out)
	}
	if !strings.Contains(out, "\n  1-5") {
		t.Errorf("View() = %q, want a \"1-5\" key entry", out)
	}
	if strings.Contains(out, "switch focus between the backlog and work-queue columns") {
		t.Errorf("View() = %q, want no mention of the retired tab focus-switch binding", out)
	}
}

// TestView_ShowHelp_DescribesContextSensitiveEnter verifies the help
// overlay's "enter" entry documents both context-sensitive behaviors —
// picking the highlighted Backlog row and opening a work Section pick's
// live-tail sidebar — not just the bare word "enter" (issue #995, reworded
// for ADR 0030's Section-switched body by issue #1500, then for the sidebar
// by #1501).
func TestView_ShowHelp_DescribesContextSensitiveEnter(t *testing.T) {
	m := Update(NewModel(), HelpToggleMsg{})

	out := View(m)
	for _, want := range []string{
		"otherwise: pick",
		"the highlighted row (Backlog Section)",
		"highlighted pick's live-tail sidebar",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("View() = %q, want it to describe context-sensitive enter behavior %q", out, want)
		}
	}
}

// TestView_ShowHelp_ListsNewKeybindings verifies the help overlay lists the
// picks/queue-driving keys wired in issue #785, and documents "X" as the
// Terminate key now that "k" reverted to vim's cursor-up (issue #1500).
func TestView_ShowHelp_ListsNewKeybindings(t *testing.T) {
	m := Update(NewModel(), HelpToggleMsg{})

	out := View(m)
	for _, want := range []string{"p ", "u ", "pa ", "X ", "+", "-", "b "} {
		if !strings.Contains(out, want) {
			t.Errorf("View() = %q, want it to mention key %q", out, want)
		}
	}
	if !strings.Contains(out, "terminate the highlighted live Dispatch") {
		t.Errorf("View() = %q, want \"X\" documented as Terminate", out)
	}
}

// TestView_ShowHelp_ListsAdoptOrphanKey verifies the help overlay documents
// "A", the explicit adopt gesture on an orphan-flagged Backlog row (issue
// #1619) — startup only ever detects an orphan now, so the operator needs a
// discoverable way to learn how to adopt one.
func TestView_ShowHelp_ListsAdoptOrphanKey(t *testing.T) {
	m := Update(NewModel(), HelpToggleMsg{})

	out := View(m)
	if !strings.Contains(out, "\n  A ") {
		t.Errorf("View() = %q, want an \"A\" key entry", out)
	}
	if !strings.Contains(out, "adopt") || !strings.Contains(out, "orphan") {
		t.Errorf("View() = %q, want it to describe the \"A\" adopt-orphan gesture", out)
	}
}

// TestView_ShowHelp_ListsRebuildOutputKey verifies the help overlay lists
// "o", the rebuild-output pane's open key added by issue #1128.
func TestView_ShowHelp_ListsRebuildOutputKey(t *testing.T) {
	m := Update(NewModel(), HelpToggleMsg{})

	out := View(m)
	if !strings.Contains(out, "\n  o ") {
		t.Errorf("View() = %q, want an \"o\" key entry", out)
	}
	if !strings.Contains(out, "rebuild output") {
		t.Errorf("View() = %q, want it to describe the rebuild-output pane", out)
	}
}

// TestView_ShowHelp_ListsBodyScrollKeys verifies the help overlay lists
// pgup/pgdown as the backlog/queue viewport's own line-scroll keys,
// distinct from the sidebar's identically-named scroll keys
// (issue #1036 AC — help overlay documents the new scroll keys).
func TestView_ShowHelp_ListsBodyScrollKeys(t *testing.T) {
	m := Update(NewModel(), HelpToggleMsg{})

	out := View(m)
	if !strings.Contains(out, "pgup/pgdown  jump a full page of the active Section's live") {
		t.Errorf("View() = %q, want it to mention the dynamic Section page jump", out)
	}
}

// TestView_ShowHelp_ListsJumpKeys verifies the help overlay documents "G"
// and "gg" — the list body's jump-to-bottom/top motions — alongside the
// existing j/k and pgup/pgdown entries (issue #1628 AC7).
func TestView_ShowHelp_ListsJumpKeys(t *testing.T) {
	m := Update(NewModel(), HelpToggleMsg{})

	out := View(m)
	if !strings.Contains(out, "\n  G ") {
		t.Errorf("View() = %q, want a \"G\" key entry", out)
	}
	if !strings.Contains(out, "\n  gg ") {
		t.Errorf("View() = %q, want a \"gg\" key entry", out)
	}
	if !strings.Contains(out, "last row") {
		t.Errorf("View() = %q, want the \"G\" entry to describe jumping to the last row", out)
	}
	if !strings.Contains(out, "first row") {
		t.Errorf("View() = %q, want the \"gg\" entry to describe jumping to the first row", out)
	}
}

// TestView_ShowHelp_ContrastsSidebarFixedPage verifies the help overlay's
// sidebar pgup/pgdown line calls out that its page jump is a fixed size,
// unlike the backlog/queue's live-viewport-derived one — the two keys share
// a name but not a page size (issue #1059).
func TestView_ShowHelp_ContrastsSidebarFixedPage(t *testing.T) {
	m := Update(NewModel(), HelpToggleMsg{})

	out := View(m)
	want := fmt.Sprintf("fixed at %d lines", fixedPaneScrollDelta)
	if !strings.Contains(out, want) {
		t.Errorf("View() = %q, want it to contain %q", out, want)
	}
}

// TestView_RebuildOutputOpen_RendersOutputInsteadOfBacklog verifies an open
// rebuild-output pane replaces the backlog/queue rendering with the captured
// nix output, plus a close-key hint — RebuildOutput's only consumer (issue
// #1128).
func TestView_RebuildOutputOpen_RendersOutputInsteadOfBacklog(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Height: 24})
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{{Number: "1", Title: "should not show"}}})
	m = Update(m, StaleStatusMsg{RebuildStatus: RebuildStatus{Output: "building derivation...\ndone"}})
	m = Update(m, RebuildOutputOpenMsg{})

	out := View(m)
	if strings.Contains(out, "should not show") {
		t.Errorf("View() = %q, want the backlog hidden while the rebuild-output pane is open", out)
	}
	if !strings.Contains(out, "building derivation...") || !strings.Contains(out, "done") {
		t.Errorf("View() = %q, want the captured rebuild output shown", out)
	}
	if !strings.Contains(out, "close") {
		t.Errorf("View() = %q, want a close-key hint", out)
	}
}

// TestView_RebuildOutputOpen_ScrollOffsetWindowsContent verifies scrolling
// the rebuild-output pane slides the visible window instead of always
// showing from the top — an off-by-one in the offset/end clamp would either
// repeat or skip a line at the boundary.
func TestView_RebuildOutputOpen_ScrollOffsetWindowsContent(t *testing.T) {
	// Height 4 leaves a 2-line content budget (headerFooterLines), too small
	// to show all 5 lines at once — so a scroll actually slides the window
	// instead of clamping straight back to the top like a short transcript
	// that already fits (mirrored from DrillIn's own clamp, model.go).
	m := Update(NewModel(), SizeChangedMsg{Height: 4})
	m = Update(m, StaleStatusMsg{RebuildStatus: RebuildStatus{Output: "l0\nl1\nl2\nl3\nl4"}})
	m = Update(m, RebuildOutputOpenMsg{})
	m = Update(m, RebuildOutputScrollMsg{Delta: 2})

	out := View(m)
	if strings.Contains(out, "l0") || strings.Contains(out, "l1") {
		t.Errorf("View() = %q, want l0/l1 scrolled above the window", out)
	}
	if !strings.Contains(out, "l2") || !strings.Contains(out, "l3") {
		t.Errorf("View() = %q, want l2/l3 visible after scrolling past l0/l1", out)
	}
}

// TestView_SidebarOpen_RendersActivityInsteadOfBacklog verifies an open
// sidebar, on a terminal too narrow to dock it, replaces the backlog/queue
// rendering with the sidebar's Activity feed — the default view, not the
// Transcript — the operator's view of the work, not just liveness (#648,
// #1501).
func TestView_SidebarOpen_RendersActivityInsteadOfBacklog(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Height: 24})
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{{Number: "1", Title: "should not show"}}})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "#42 · hi"}}, Rendered: "[implementor] hi", Raw: `{"type":"assistant"}`})

	out := View(m)
	if strings.Contains(out, "should not show") {
		t.Errorf("View() = %q, want the backlog hidden while the sidebar is open", out)
	}
	if !strings.Contains(out, "42") {
		t.Errorf("View() = %q, want the sidebar's issue number", out)
	}
	if !strings.Contains(out, "hi") {
		t.Errorf("View() = %q, want the Activity feed", out)
	}
	if strings.Contains(out, "[implementor] hi") || strings.Contains(out, `{"type":"assistant"}`) {
		t.Errorf("View() = %q, want the Transcript forms hidden while showing the Activity feed by default", out)
	}
}

// TestView_SidebarToggle_RendersTranscriptThenRaw verifies advancing the
// toggle swaps the sidebar from the Activity feed to the rendered Transcript,
// then to the raw byte-exact form.
func TestView_SidebarToggle_RendersTranscriptThenRaw(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Height: 24})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "#42 · hi"}}, Rendered: "[implementor] hi", Raw: `{"type":"assistant"}`})
	m = Update(m, SidebarToggleMsg{})

	out := View(m)
	if !strings.Contains(out, "[implementor] hi") {
		t.Errorf("View() = %q, want the rendered Transcript shown after one toggle", out)
	}
	if strings.Contains(out, `{"type":"assistant"}`) {
		t.Errorf("View() = %q, want the raw form still hidden after one toggle", out)
	}

	m = Update(m, SidebarToggleMsg{})
	out = View(m)
	if !strings.Contains(out, `{"type":"assistant"}`) {
		t.Errorf("View() = %q, want the raw form shown after two toggles", out)
	}
}

// TestView_SidebarOffset_HidesLinesBeforeOffset verifies scrolling (a
// non-zero Offset) drops the leading lines from the sidebar instead of
// always showing its start (issue #786, inherited). Height is small enough
// that the content outruns the viewport budget, or the viewport clamp (issue
// #829) would pin Offset at 0 since the whole thing already fits. The
// Transcript (rendered) view is toggled on so the content matches the plain
// "l0".."l3" lines the old drill-in test exercised.
func TestView_SidebarOffset_HidesLinesBeforeOffset(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Height: 4})
	m = Update(m, SidebarLoadedMsg{Number: "42", Rendered: "l0\nl1\nl2\nl3"})
	m = Update(m, SidebarToggleMsg{})
	m = Update(m, SidebarScrollMsg{Delta: 2})

	out := View(m)
	if strings.Contains(out, "l0") || strings.Contains(out, "l1") {
		t.Errorf("View() = %q, want lines before the offset hidden", out)
	}
	if !strings.Contains(out, "l2") || !strings.Contains(out, "l3") {
		t.Errorf("View() = %q, want lines from the offset onward", out)
	}
}

// TestView_SidebarErr_Surfaced verifies a sidebar that failed to load
// surfaces its error text instead of blank content.
func TestView_SidebarErr_Surfaced(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Height: 24})
	m = Update(m, SidebarLoadedMsg{Number: "42", Err: errBoom})

	out := View(m)
	if !strings.Contains(out, errBoom.Error()) {
		t.Errorf("View() = %q, want it to contain %q", out, errBoom.Error())
	}
}

// TestView_SidebarTranscriptErr_HiddenBehindActivity verifies a Transcript-
// only load failure (DrillIn's error, surfaced as TranscriptErr) never blanks
// out an independently-loaded, otherwise-good Activity feed — the error only
// shows once the operator actually toggles to the Transcript (#1501 review
// finding).
func TestView_SidebarTranscriptErr_HiddenBehindActivity(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Height: 24})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "#42 · hi"}}, TranscriptErr: errBoom})

	out := View(m)
	if strings.Contains(out, errBoom.Error()) {
		t.Errorf("View() = %q, want the Transcript error hidden while showing the Activity feed", out)
	}
	if !strings.Contains(out, "hi") {
		t.Errorf("View() = %q, want the Activity feed shown", out)
	}

	m = Update(m, SidebarToggleMsg{})
	out = View(m)
	if !strings.Contains(out, errBoom.Error()) {
		t.Errorf("View() = %q, want the Transcript error shown once toggled to the Transcript", out)
	}
}

// TestView_SidebarFullscreen_WindowsToViewportHeight verifies the fullscreen
// sidebar joins only as many lines as the viewport can show, instead of the
// whole tail from Offset to the end of a (potentially multi-MB) transcript,
// so scrolling near the top of a huge transcript doesn't re-serialize
// content nowhere near the screen (issue #722, inherited). The Transcript
// (rendered) view is toggled on so the content matches the head/tail markers
// the old drill-in test exercised.
func TestView_SidebarFullscreen_WindowsToViewportHeight(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Height: 5})
	content := "HEAD-MARKER\n" + strings.Repeat("x\n", 100) + "TAIL-MARKER"
	m = Update(m, SidebarLoadedMsg{Number: "42", Rendered: content})
	m = Update(m, SidebarToggleMsg{})

	out := View(m)
	if !strings.Contains(out, "HEAD-MARKER") {
		t.Errorf("View() = %q, want the first visible line present", out)
	}
	if strings.Contains(out, "TAIL-MARKER") {
		t.Errorf("View() = %q, want content past the viewport height hidden", out)
	}
}

// TestView_SidebarOpen_WideTerminal_DocksBesideList verifies a terminal wide
// enough for sidebarFits shows the sidebar docked beside the still-visible
// list, rather than the list disappearing behind a fullscreen takeover — the
// core of ADR 0030's docked layout (#1501).
func TestView_SidebarOpen_WideTerminal_DocksBesideList(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: sidebarMinListWidth + sidebarWidth + 1, Height: 24})
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{{Number: "1", Title: "still visible"}}})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "#42 · hi"}}})

	out := View(m)
	if !strings.Contains(out, "still visible") {
		t.Errorf("View() = %q, want the list still visible docked beside the sidebar", out)
	}
	if !strings.Contains(out, "activity #42") {
		t.Errorf("View() = %q, want the docked sidebar's label", out)
	}
}

// TestView_SidebarOpen_ShortContent_DividerDoesNotFillWholeBudget verifies
// the divider between the docked list and sidebar spans only as many rows
// as the taller of the two actually rendered, not the whole body budget —
// one Backlog issue and a one-line Activity feed must not force blank
// divider rows down to the bottom of a tall terminal (#1501 review finding).
func TestView_SidebarOpen_ShortContent_DividerDoesNotFillWholeBudget(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: sidebarMinListWidth + sidebarWidth + 1, Height: 24})
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{{Number: "1", Title: "only issue"}}})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "one line"}}})

	out := View(m)
	got := strings.Count(out, "\n")
	// header (banner + status line) + tabs + a couple of content rows —
	// nowhere near the full Height: 24 budget the pre-fix divider always
	// forced the joined body up to.
	if got > 15 {
		t.Errorf("View() rendered %d lines, want well under Height (24) — the divider must not pad the body out to the full budget for short content", got)
	}
}

// TestView_SidebarOpen_NarrowTerminal_FallsBackFullscreen verifies a
// terminal one column short of sidebarFits' threshold falls back to the
// fullscreen takeover — the list disappears entirely rather than squeezing
// both columns illegibly (ADR 0030's narrow-terminal degradation, #1501).
func TestView_SidebarOpen_NarrowTerminal_FallsBackFullscreen(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: sidebarMinListWidth + sidebarWidth, Height: 24})
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{{Number: "1", Title: "should not show"}}})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "#42 · hi"}}})

	out := View(m)
	if strings.Contains(out, "should not show") {
		t.Errorf("View() = %q, want the list hidden behind the fullscreen fallback one column short of sidebarFits", out)
	}
	if !strings.Contains(out, "activity #42") {
		t.Errorf("View() = %q, want the fullscreen sidebar's label", out)
	}
}

// TestView_SidebarLabel_ShowsFollowIndicator verifies the sidebar's label
// names whether the Activity feed is following the newest line or paused
// after a scroll-up — the operator's only render-level signal for Follow
// state (issue #1502, ADR 0030).
func TestView_SidebarLabel_ShowsFollowIndicator(t *testing.T) {
	m := Update(NewModel(), SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "hi"}}})
	if !strings.Contains(View(m), "[follow]") {
		t.Errorf("View() = %q, want the follow indicator while Follow is true", View(m))
	}

	m = Update(m, SidebarScrollMsg{Delta: -1})
	if !strings.Contains(View(m), "[paused]") {
		t.Errorf("View() = %q, want the paused indicator after a scroll-up detaches Follow", View(m))
	}
}

// TestView_SidebarFooter_ShowsZoomHint verifies both the docked and
// fullscreen sidebar footers advertise the "z" zoom key (issue #1502).
func TestView_SidebarFooter_ShowsZoomHint(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: sidebarMinListWidth + sidebarWidth + 1, Height: 24})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "hi"}}})
	if !strings.Contains(View(m), "[z] zoom") {
		t.Errorf("docked View() = %q, want the zoom key hint", View(m))
	}

	m = Update(m, SidebarZoomToggleMsg{})
	if !strings.Contains(View(m), "[z] zoom") {
		t.Errorf("fullscreen View() = %q, want the zoom key hint", View(m))
	}
}

// TestView_SidebarZoom_WideTerminal_ForcesFullscreen verifies SidebarZoom
// forces the fullscreen takeover even on a terminal wide enough to dock —
// the "deep reading" zoom is an operator choice independent of sidebarFits'
// own narrow-terminal fallback (issue #1502, ADR 0030).
func TestView_SidebarZoom_WideTerminal_ForcesFullscreen(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: sidebarMinListWidth + sidebarWidth + 1, Height: 24})
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{{Number: "1", Title: "should not show while zoomed"}}})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "#42 · hi"}}})
	m = Update(m, SidebarZoomToggleMsg{})

	out := View(m)
	if strings.Contains(out, "should not show while zoomed") {
		t.Errorf("View() = %q, want the list hidden behind the zoomed fullscreen sidebar", out)
	}
	if !strings.Contains(out, "activity #42") {
		t.Errorf("View() = %q, want the fullscreen sidebar's label", out)
	}
}

// TestView_BacklogSection_HasColumnHeader verifies the Backlog Section
// renders under its own column-header row (issue #844, moved from the
// two-column body's "backlog" label to ADR 0030's single-Section table by
// issue #1500).
func TestView_BacklogSection_HasColumnHeader(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{{Number: "1", Title: "fix the thing"}}})

	out := View(m)
	if !strings.Contains(out, "title") {
		t.Errorf("View() = %q, want the Backlog Section's column-header row", out)
	}
}

// TestView_BacklogSection_FlagsOrphanRow verifies a Backlog row for an issue
// startup detection flagged an orphan renders distinguishable from an
// ordinary row — the operator's only signal that a running sandbox exists
// with no Dispatch this session launched to account for it (issue #1619).
func TestView_BacklogSection_FlagsOrphanRow(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{
		{Number: "1", Title: "fix the widget"},
		{Number: "2", Title: "fix the gadget"},
	}})
	m = Update(m, OrphanDetectedMsg{Numbers: []string{"1"}})

	out := View(m)
	lines := strings.Split(out, "\n")
	var orphanLine, ordinaryLine string
	for _, l := range lines {
		if strings.Contains(l, "fix the widget") {
			orphanLine = l
		}
		if strings.Contains(l, "fix the gadget") {
			ordinaryLine = l
		}
	}
	if !strings.Contains(orphanLine, "orphan") {
		t.Errorf("orphan row = %q, want it flagged with \"orphan\"", orphanLine)
	}
	if strings.Contains(ordinaryLine, "orphan") {
		t.Errorf("ordinary row = %q, want no orphan flag", ordinaryLine)
	}
}

// TestView_WorkSection_RendersEvenWhenEmpty verifies a work Section renders
// its column-header row even with no picks in it yet — a labeled empty
// table, not one that appears only once something lands there (issue #844,
// adapted to ADR 0030's single active Section by issue #1500).
func TestView_WorkSection_RendersEvenWhenEmpty(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	m = Update(m, SectionJumpMsg{Section: SectionRunning})

	out := View(m)
	if !strings.Contains(out, "state") || !strings.Contains(out, "age") {
		t.Errorf("View() = %q, want the work Section's column-header row even with no picks", out)
	}
}

// TestView_Section_RowsTaggedWithState verifies each work Section row
// carries its PickState as its state cell — running, queued distinguishable
// at a glance in the Running Section, held naming its blocker in the Held
// Section (issue #844 AC3/AC4, moved from the two-column queue to
// Section-partitioned tables by ADR 0030/issue #1500).
func TestView_Section_RowsTaggedWithState(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	m = Update(m, QueueSnapshotMsg{Picks: []Pick{
		{Number: "1", Title: "queued one", State: PickQueued},
		{Number: "3", Title: "running one", State: PickRunning, Heartbeat: "7 turns"},
	}})
	m = Update(m, SectionJumpMsg{Section: SectionRunning})

	out := View(m)
	for _, want := range []string{"queued", "running", "7 turns"} {
		if !strings.Contains(out, want) {
			t.Errorf("View() (Running Section) = %q, want %q", out, want)
		}
	}
}

// TestView_Section_RowsShowAge verifies a work-Section row renders its
// precomputed Age (syncQueue's formatAge output, same as Heartbeat) in the
// age column — the column is otherwise only proven present via the header
// word, not an actual value (issue #1500 review).
func TestView_Section_RowsShowAge(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	m = Update(m, QueueSnapshotMsg{Picks: []Pick{
		{Number: "1", Title: "queued one", State: PickQueued, Age: "3m"},
	}})
	m = Update(m, SectionJumpMsg{Section: SectionRunning})

	out := View(m)
	if !strings.Contains(out, "3m") {
		t.Errorf("View() (Running Section) = %q, want the row's Age (3m) rendered", out)
	}
}

// TestView_HeldSection_ShowsBlocker verifies a held row in the Held Section
// carries its state and blocker badge.
func TestView_HeldSection_ShowsBlocker(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	m = Update(m, QueueSnapshotMsg{Picks: []Pick{
		{Number: "2", Title: "blocked one", State: PickHeld, BlockedBy: "#41 (native)"},
	}})
	m = Update(m, SectionJumpMsg{Section: SectionHeld})

	out := View(m)
	if !strings.Contains(out, "held") {
		t.Errorf("View() = %q, want the held state cell", out)
	}
	if !strings.Contains(out, "held by #41 (native)") {
		t.Errorf("View() = %q, want the held row's blocker", out)
	}
}

// TestView_HeldSection_SuppressesRedundantFailedBlockerReason verifies a
// held pick whose Reason merely restates the blocker BlockedBy already names
// renders only the "held by" badge, not both — a held pick with a failed
// blocker previously named the same blocker twice on one row (issue #755).
func TestView_HeldSection_SuppressesRedundantFailedBlockerReason(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 300, Height: 24})
	m = Update(m, QueueSnapshotMsg{Picks: []Pick{
		{Number: "42", Title: "held one", State: PickHeld, BlockedBy: "#41 (native)", Reason: blockerFailedPrefix + "#41 (native) failed"},
	}})
	m = Update(m, SectionJumpMsg{Section: SectionHeld})

	out := View(m)
	if !strings.Contains(out, "held by #41 (native)") {
		t.Errorf("View() = %q, want the held row's blocker badge", out)
	}
	if strings.Contains(out, "("+blockerFailedPrefix+"#41 (native) failed)") {
		t.Errorf("View() = %q, want the redundant failed-blocker reason suppressed", out)
	}
}

// TestView_HeldSection_BlockerVisibleDespiteLongTitle verifies a held row's
// blocker badge survives even when paired with a long title — the row's
// fixed number/title/state/age columns clip the title in place, so the
// trailing blocker annotation (issue #858) is never pushed off by a long
// title the way an unbounded natural-order row once could be.
func TestView_HeldSection_BlockerVisibleDespiteLongTitle(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	m = Update(m, QueueSnapshotMsg{Picks: []Pick{
		{Number: "42", Title: "fix the launcher retry backoff for the dispatch workflow", State: PickHeld, BlockedBy: "#41 (native)", Reason: "issue is closed"},
	}})
	m = Update(m, SectionJumpMsg{Section: SectionHeld})

	out := View(m)
	if !strings.Contains(out, "held by #41 (native)") {
		t.Errorf("View() = %q, want the held row's blocker badge visible despite a long title", out)
	}
}

// TestRenderBacklogSection_BudgetExceedsRowCount_NeverTruncates verifies a
// budget comfortably larger than the row count renders every Backlog row
// with no "more below" affordance (issue #1540 — the render pipeline's
// budget is always a real, finite figure; Viewport's own height==0 covers
// "unbounded" for callers who actually want that, exercised directly in
// viewport_test.go).
func TestRenderBacklogSection_BudgetExceedsRowCount_NeverTruncates(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	issues := make([]forge.Issue, 500)
	for i := range issues {
		issues[i] = forge.Issue{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("issue %d", i)}
	}
	m = Update(m, IssuesLoadedMsg{Issues: issues})

	out := renderBacklogSection(m, len(issues)+1)
	if !strings.Contains(out, "issue 499") {
		t.Errorf("renderBacklogSection(m, 501) = %q, want the last of 500 rows present, unwindowed", out)
	}
	if strings.Contains(out, "more below") {
		t.Errorf("renderBacklogSection(m, 501) = %q, want no truncation affordance", out)
	}
}

// TestRenderWorkSection_BudgetExceedsRowCount_NeverTruncates is
// TestRenderBacklogSection_BudgetExceedsRowCount_NeverTruncates mirrored for
// a work Section.
func TestRenderWorkSection_BudgetExceedsRowCount_NeverTruncates(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	picks := make([]Pick, 500)
	for i := range picks {
		picks[i] = Pick{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("pick %d", i), State: PickQueued}
	}
	m = Update(m, QueueSnapshotMsg{Picks: picks})
	m = Update(m, SectionJumpMsg{Section: SectionRunning})

	out := renderWorkSection(m, len(picks)+1)
	if !strings.Contains(out, "pick 499") {
		t.Errorf("renderWorkSection(m, 501) = %q, want the last of 500 rows present, unwindowed", out)
	}
	if strings.Contains(out, "more below") {
		t.Errorf("renderWorkSection(m, 501) = %q, want no truncation affordance", out)
	}
}

// TestView_SectionTabs_HighlightsActiveSection verifies the Section tabs
// line renders differently depending on which Section is active — the
// operator's cue for which Section H/L/1-5 currently target (ADR 0030/0031,
// issue #1500).
func TestView_SectionTabs_HighlightsActiveSection(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	backlogActive := renderSectionTabs(m)

	m = Update(m, SectionJumpMsg{Section: SectionRunning})
	runningActive := renderSectionTabs(m)

	if backlogActive == runningActive {
		t.Errorf("renderSectionTabs(m) = %q both before and after switching Sections, want the active tab's styling to differ", backlogActive)
	}
}

// TestView_Cursor_MarksHighlightedRowInWorkSection verifies Cursor marks the
// highlighted row within a work Section the same way it already does in the
// Backlog Section (issue #845, generalized from FocusedColumn to
// ActiveSection by issue #1500).
func TestView_Cursor_MarksHighlightedRowInWorkSection(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	m = Update(m, QueueSnapshotMsg{Picks: []Pick{{Number: "1", State: PickQueued}, {Number: "2", State: PickQueued}}})
	m = Update(m, SectionJumpMsg{Section: SectionRunning})
	m = Update(m, CursorMoveMsg{Delta: 1})

	out := View(m)
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "#2") && strings.HasPrefix(l, ">") {
			return
		}
	}
	t.Errorf("View() = %q, want row #2 marked with the cursor", out)
}

// TestView_SidebarFullscreen_RespectsTinyBudget verifies the fullscreen
// sidebar's total output never exceeds m.Height at the smallest possible
// budget — renderDrillIn (its predecessor) wrote the header line and then
// always appended the footer with no height check at all, so at Height: 1 it
// overflowed to 2 lines. Mirrors the docked/floating tiny-budget fix from
// #1380, applied to renderSidebarFullscreen (issue #1534, inherited).
func TestView_SidebarFullscreen_RespectsTinyBudget(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Height: 1})
	lines := make([]string, 100)
	for i := range lines {
		lines[i] = fmt.Sprintf("transcript line %d", i)
	}
	m = Update(m, SidebarLoadedMsg{Number: "42", Rendered: strings.Join(lines, "\n")})
	m = Update(m, SidebarToggleMsg{})

	out := View(m)
	got := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(got) > m.Height {
		t.Errorf("View() rendered %d lines, want at most Height (%d) — renderSidebarFullscreen's own header+footer chrome must be budgeted against height, not written unconditionally", len(got), m.Height)
	}
}

// TestView_SidebarFullscreen_RetainsFooterAtBoundary verifies the label and
// footer both render, and stay within budget, at Height: 2 — the boundary
// where label+footer exactly fills the budget, one above the Height: 1 case
// that drops the footer (issue #1534, inherited).
func TestView_SidebarFullscreen_RetainsFooterAtBoundary(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Height: 2})
	lines := make([]string, 100)
	for i := range lines {
		lines[i] = fmt.Sprintf("transcript line %d", i)
	}
	m = Update(m, SidebarLoadedMsg{Number: "42", Rendered: strings.Join(lines, "\n")})
	m = Update(m, SidebarToggleMsg{})

	out := View(m)
	if !strings.Contains(out, "[t] cycle activity/transcript · [x] close") {
		t.Errorf("View() = %q, want the footer retained at the Height: 2 boundary", out)
	}
	got := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(got) > m.Height {
		t.Errorf("View() rendered %d lines, want at most Height (%d)", len(got), m.Height)
	}
}

// TestView_SidebarFullscreen_ErrRespectsTinyBudget verifies a sidebar that
// failed to load also respects the tiny-budget guard — the label+error combo
// is two lines, same as label+footer, so it must be dropped at Height: 1
// same as the footer is (issue #1534, inherited).
func TestView_SidebarFullscreen_ErrRespectsTinyBudget(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Height: 1})
	m = Update(m, SidebarLoadedMsg{Number: "42", Err: errBoom})

	out := View(m)
	got := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(got) > m.Height {
		t.Errorf("View() rendered %d lines, want at most Height (%d) — the error line must be budgeted against height same as the footer", len(got), m.Height)
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
	m = Update(m, QueueSnapshotMsg{Picks: picks})
	m = Update(m, SectionJumpMsg{Section: SectionRunning})

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
// a work Section once it's active and scrolled all the way — issue #1036
// AC3 covers every Section, generalized from Tab-toggled focus to
// ActiveSection by issue #1500.
func TestView_ScrolledQueue_ReachesLastRow(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 10})
	picks := make([]Pick, 50)
	for i := range picks {
		picks[i] = Pick{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("pick %d", i), State: PickQueued}
	}
	m = Update(m, QueueSnapshotMsg{Picks: picks})
	m = Update(m, SectionJumpMsg{Section: SectionRunning})

	m = Update(m, ScrollMsg{Delta: 1000})

	out := View(m)
	if !strings.Contains(out, "pick 49") {
		t.Errorf("View() = %q, want the last pick reachable once scrolled all the way down", out)
	}
}

// TestView_BacklogSection_ShowsPositionIndicator verifies the Backlog
// Section's column header carries a compact "X-Y of N" position indicator
// reflecting the visible row range and total, so the operator can see where
// they are in a long backlog without counting rows (issue #1037 AC3).
func TestView_BacklogSection_ShowsPositionIndicator(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 10})
	issues := make([]forge.Issue, 50)
	for i := range issues {
		issues[i] = forge.Issue{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("issue %d", i)}
	}
	m = Update(m, IssuesLoadedMsg{Issues: issues})

	out := View(m)
	if !strings.Contains(out, "(1-3 of 50)") {
		t.Errorf("View() = %q, want the Backlog header to show \"(1-3 of 50)\"", out)
	}

	m = Update(m, ScrollMsg{Delta: 5})
	out = View(m)
	if !strings.Contains(out, "(6-8 of 50)") {
		t.Errorf("View() = %q, want the Backlog header to show \"(6-8 of 50)\" after scrolling", out)
	}
}

// TestView_WorkSection_ShowsPositionIndicator verifies a work Section's
// column header carries the same position indicator as the Backlog Section,
// and that it is absent when the Section is empty rather than reading
// "(1-0 of 0)" (issue #1037 AC3/AC4).
func TestView_WorkSection_ShowsPositionIndicator(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 10})
	picks := make([]Pick, 50)
	for i := range picks {
		picks[i] = Pick{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("pick %d", i), State: PickQueued}
	}
	m = Update(m, QueueSnapshotMsg{Picks: picks})
	m = Update(m, SectionJumpMsg{Section: SectionRunning})

	out := View(m)
	if !strings.Contains(out, "(1-3 of 50)") {
		t.Errorf("View() = %q, want the Running header to show \"(1-3 of 50)\"", out)
	}

	empty := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 10})
	empty = Update(empty, SectionJumpMsg{Section: SectionRunning})
	out = View(empty)
	if strings.Contains(out, " of 0)") {
		t.Errorf("View() = %q, want no position indicator for an empty Section", out)
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
	withAlert = Update(withAlert, StaleStatusMsg{RebuildStatus: RebuildStatus{Stale: true, Message: "rebuild needed"}})
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
	// The header carries legitimate styling escapes of its own (ADR 0031),
	// so the check below scopes "no raw ESC byte" to the row rendering the
	// untrusted title/label rather than the whole output — anything past
	// the sanitizer trust boundary in that row is still caught, styled
	// header lines elsewhere are not a false positive.
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "eviltitlehere") || strings.Contains(line, "evillabel") {
			if strings.Contains(line, "\x1b") {
				t.Errorf("backlog row = %q, want no raw escape bytes surviving sanitization", line)
			}
		}
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
	// A work-Section row's state cell carries its own legitimate role
	// styling (ADR 0031) on the same line as the sanitized title/reason —
	// unlike the Backlog row, which has no per-row styling at all. NO_COLOR
	// keeps that legitimate styling from ever emitting an escape byte, so
	// the check below stays an unambiguous test of sanitization alone.
	t.Setenv("NO_COLOR", "1")

	m := Update(NewModel(), SizeChangedMsg{Width: 300, Height: 24})
	m = Update(m, QueueSnapshotMsg{Picks: []Pick{
		{Number: "42", Title: "evil\x1b[2Jtitle", State: PickHeld, Reason: "bad\x1b]0;pwned\x07reason"},
	}})
	m = Update(m, SectionJumpMsg{Section: SectionHeld})

	out := View(m)
	// The header carries legitimate styling escapes of its own (ADR 0031)
	// on a color-capable terminal, so the check below scopes "no raw ESC
	// byte" to the row rendering the untrusted title/reason rather than the
	// whole output — anything past the sanitizer trust boundary in that row
	// is still caught.
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "eviltitle") || strings.Contains(line, "badreason") {
			if strings.Contains(line, "\x1b") {
				t.Errorf("queue row = %q, want no raw escape bytes surviving sanitization", line)
			}
		}
	}
	if !strings.Contains(out, "eviltitle") {
		t.Errorf("View() = %q, want the surrounding title text intact after stripping escapes", out)
	}
	if !strings.Contains(out, "badreason") {
		t.Errorf("View() = %q, want the surrounding reason text intact after stripping escapes", out)
	}
}

// TestRenderTable_NonPositiveItemBudgetRendersHeaderOnly verifies renderTable,
// the helper renderBacklogSection and renderWorkSection share, writes the
// header and nothing else when itemBudget leaves no room for any row — the
// guard lives in the shared helper (issue #1040, ADR 0030), and Viewport is
// never asked to represent a non-positive item budget itself (issue #1540:
// SetHeight(0) means unbounded, not zero rows).
func TestRenderTable_NonPositiveItemBudgetRendersHeaderOnly(t *testing.T) {
	got := renderTable("header\n", []string{"row1\n"}, Viewport{}, 1, 0)
	if want := "header\n"; got != want {
		t.Errorf("renderTable() with itemBudget 0 = %q, want %q", got, want)
	}
}

// TestRenderTable_RendersHeaderAndWindowedRows verifies renderTable writes
// the header line followed by every row when they all fit within itemBudget,
// matching the convention renderBacklogSection/renderWorkSection applied
// inline before extraction.
func TestRenderTable_RendersHeaderAndWindowedRows(t *testing.T) {
	got := renderTable("header\n", []string{"row1\n", "row2\n"}, Viewport{}, 2, 2)
	want := "header\nrow1\nrow2\n"
	if got != want {
		t.Errorf("renderTable() = %q, want %q", got, want)
	}
}

// TestRenderTable_PassesOffsetThroughToWindow verifies renderTable windows
// through vp's own offset rather than always starting at row 0 — the
// Section's own scroll position (m.Offset) both callers pass through.
func TestRenderTable_PassesOffsetThroughToWindow(t *testing.T) {
	vp := Viewport{offset: 1}
	got := renderTable("header\n", []string{"row1\n", "row2\n", "row3\n"}, vp, 3, 2)
	want := "header\nrow2\nrow3\n"
	if got != want {
		t.Errorf("renderTable() with offset 1 = %q, want %q", got, want)
	}
}

// TestRenderTable_TruncatedWindow_HoldsBackOneRowForMoreBelow verifies an
// itemBudget too small for every row holds one row back so the "… N more
// below" affordance itself fits within itemBudget rather than overflowing it
// by one line (issue #1061, inherited): itemBudget 4 against 50 rows shows
// 3 rows plus the affordance, not 4 rows with no room left to name it.
func TestRenderTable_TruncatedWindow_HoldsBackOneRowForMoreBelow(t *testing.T) {
	rows := make([]string, 50)
	for i := range rows {
		rows[i] = fmt.Sprintf("row%d\n", i)
	}
	got := renderTable("header\n", rows, Viewport{}, 50, 4)
	want := "header\nrow0\nrow1\nrow2\n… 47 more below\n"
	if got != want {
		t.Errorf("renderTable() = %q, want %q", got, want)
	}
}

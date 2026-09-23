package console

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"

	"spindrift.dev/launcher/internal/forge"
)

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

// viewWithLayout(m, resolveLayout(m)) must render exactly what View(m) does:
// issue #3018 slice 3 split View, and the split has to be behaviour-preserving
// across every layout branch View itself reads.
func TestViewWithLayout_MatchesView(t *testing.T) {
	docked := Update(NewModel(), SizeChangedMsg{Width: sidebarMinListWidth + sidebarWidth + dockedBorderCols, Height: 24})
	docked = Update(docked, SidebarLoadedMsg{Number: "1", Activity: []ActivityLine{{Text: "hi"}}})

	floatingDetail := Update(NewModel(), SizeChangedMsg{Width: 120, Height: 40})
	floatingDetail = Update(floatingDetail, DetailModalOpenMsg{Number: "1", Title: "fix the thing"})
	floatingDetail = Update(floatingDetail, DetailModalLoadedMsg{Number: "1", Body: "the body"})

	fullscreenFallback := Update(NewModel(), SizeChangedMsg{Width: 30, Height: 10})
	fullscreenFallback = Update(fullscreenFallback, DetailModalOpenMsg{Number: "1", Title: "fix the thing"})
	fullscreenFallback = Update(fullscreenFallback, DetailModalLoadedMsg{Number: "1", Body: "the body"})

	cases := []struct {
		name string
		m    Model
	}{
		{"plain list", Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})},
		{"docked sidebar", docked},
		{"floating detail modal", floatingDetail},
		{"fullscreen fallback", fullscreenFallback},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := viewWithLayout(c.m, resolveLayout(c.m))
			if want := View(c.m); got != want {
				t.Errorf("viewWithLayout(m, resolveLayout(m)) and View(m) disagree for %s", c.name)
			}
		})
	}
}

// The main list view was the one Console view issue #1792 called out as lacking
// a pinned keystroke footer; issue #1791 gave it the shared renderer.
func TestView_ModeList_ShowsPinnedFooter(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{{Number: "1", Title: "one"}}})

	out := View(m)
	for _, want := range []string{"[/] filter", "[p] pick", "[P] pick all", "[r] research", "[R] refresh"} {
		if !strings.Contains(out, want) {
			t.Errorf("View() = %q, want it to contain pinned footer hint %q", out, want)
		}
	}
}

// Issue #1792 AC3: on a narrow terminal the footer clips with an ellipsis,
// never wrapping or overflowing the terminal width.
func TestView_ModeList_NarrowWidth_FooterClipsWithoutOverflow(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	m := Update(NewModel(), SizeChangedMsg{Width: 20, Height: 24})
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{{Number: "1", Title: "one"}}})

	out := View(m)
	footerLine := ""
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "[/] filter") {
			footerLine = line
		}
	}
	if footerLine == "" {
		t.Fatalf("View() = %q, want a footer line starting with the filter hint", out)
	}
	if w := runewidth.StringWidth(footerLine); w > 20 {
		t.Errorf("footer line %q is %d columns wide, want it clipped to the 20-column terminal", footerLine, w)
	}
	if strings.Contains(footerLine, "[R] refresh") {
		t.Errorf("footer line %q, want it actually clipped at width 20 rather than fitting unclipped", footerLine)
	}
	if !strings.Contains(footerLine, "…") {
		t.Errorf("footer line %q, want the clipped footer's trailing ellipsis", footerLine)
	}
}

// Issue #1792 AC2: the footer has to survive a scroll to the clamped last page,
// not just a short unscrolled list.
func TestView_ModeList_FooterSurvivesScrollClamp(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 10})
	issues := make([]forge.Issue, 50)
	for i := range issues {
		issues[i] = forge.Issue{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("issue %d", i)}
	}
	m = Update(m, IssuesLoadedMsg{Issues: issues})

	m = Update(m, ScrollMsg{Delta: 1000})

	out := View(m)
	if !strings.Contains(out, "[/] filter") {
		t.Errorf("View() = %q, want the pinned footer to survive a scroll to the clamped last page", out)
	}
	if lines := strings.Count(out, "\n"); lines > 10 {
		t.Errorf("View() rendered %d lines, want it to fit within Height (10) with the footer pinned", lines)
	}
}

// Issue #1631: the label cell counts the labels that do not fit as "+N" instead
// of the ellipsis clip the title and other cells use.
func TestView_Backlog_TruncatedLabelsShowPlusN(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{
		{Number: "1", Title: "x", Labels: []string{"alpha", "beta", "gamma", "delta", "epsilon"}},
	}})

	out := View(m)
	if !strings.Contains(out, "alpha, beta, gamma, delta, +1") {
		t.Errorf("View() = %q, want the fitted labels followed by \"+1\" for the one label that didn't fit", out)
	}
	if strings.Contains(out, "epsilon") {
		t.Errorf("View() = %q, want \"epsilon\" omitted (it's the one label counted by +1), not spelled out", out)
	}
}

// Issue #1631: labels that all fit render as a plain joined list, no "+N" suffix.
func TestView_Backlog_FittingLabelsShowNoPlusN(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{
		{Number: "1", Title: "x", Labels: []string{"ready-for-agent", "bug"}},
	}})

	// The exact "[ready-for-agent, bug]" match already proves clipLabels did
	// not truncate; a truncated cell would read "[ready-for-agent, +1]". A bare
	// strings.Contains(out, "+") check would trip on the header panel's own
	// ASCII "+" corners (issue #1756) under this test's unset TERM.
	out := View(m)
	if !strings.Contains(out, "[ready-for-agent, bug]") {
		t.Errorf("View() = %q, want the label cell rendered as \"[ready-for-agent, bug]\" with no +N suffix", out)
	}
}

// Issue #1631: the "+N" suffix counts against the label column's width budget, so
// the cell clips rather than overflowing even where no label plus its count fits.
func TestView_Backlog_LabelCellNeverOverflowsWidth(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 30, Height: 24})
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{
		{Number: "1", Title: "x", Labels: []string{"ready-for-agent", "agent-in-progress", "agent-complete"}},
	}})

	out := View(m)
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, ">") {
			open := strings.LastIndex(line, "[")
			end := strings.LastIndex(line, "]")
			if open == -1 || end == -1 || end < open {
				t.Fatalf("backlog row = %q, want a bracketed label cell", line)
			}
			if w := runewidth.StringWidth(line[open+1 : end]); w > 16 {
				t.Errorf("backlog row = %q, label cell width %d, want at most the 16-column budget this terminal width leaves", line, w)
			}
		}
	}
}

// The synthetic orphan label (issue #1619) is just another entry for truncation:
// it counts toward both the shown labels and the "+N" remainder (issue #1631).
func TestView_Backlog_OrphanLabelCountsTowardPlusN(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 30, Height: 24})
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{
		{Number: "1", Title: "x", Labels: []string{"ready-for-agent", "bug"}},
	}})
	m = Update(m, OrphanDetectedMsg{Numbers: []string{"1"}})

	out := View(m)
	if !strings.Contains(out, "[orphan, +2]") {
		t.Errorf("View() = %q, want the orphan label shown and the other two labels counted by +2", out)
	}
}

// Issue #1631 acceptance criterion 4: "+N" is scoped to the label cell, so an
// over-width title still clips with a trailing ellipsis.
func TestView_Backlog_TitleStillEllipsisClippedNotPlusN(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{
		{Number: "1", Title: strings.Repeat("a very long title ", 5), Labels: []string{"bug"}},
	}})

	out := View(m)
	var row string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, ">") {
			row = line
		}
	}
	if !strings.Contains(row, "…") {
		t.Errorf("backlog row = %q, want the over-width title clipped with a trailing ellipsis", row)
	}
	if strings.Contains(row, "+") {
		t.Errorf("backlog row = %q, want no \"+N\" suffix — only the title overflowed, not the labels", row)
	}
}

// Issue #653: the header status line shows the session's parallelism cap and live
// count without the operator running a separate command.
func TestView_CapAndLive_Shown(t *testing.T) {
	m := NewModel()
	m = Update(m, CapMsg{Cap: 3, Live: 1})

	out := View(m)
	if !strings.Contains(out, "running 1/3") {
		t.Errorf("View() = %q, want a \"running 1/3\" line (live/cap)", out)
	}
}

// Issue #843 and ADR 0025: the counts derive from Cap/Live and the Picks' PickState
// tags, with no new stored counters. Each segment is asserted on its own because
// per-role styling (ADR 0031) wraps each one in its own ANSI escape codes, so the
// line is not one unbroken string (issue #1499).
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

// Issue #2255, ADR 0039 slice S4: RecoverableCount is pre-existing terminal state
// from a prior run, its own segment distinct from the Picks-derived failed count.
func TestView_Header_StatusLine_ShowsRecoverable(t *testing.T) {
	m := NewModel()
	m = Update(m, IssuesLoadedMsg{RecoverableCount: 2})

	out := View(m)
	if !strings.Contains(out, "recoverable 2") {
		t.Errorf("View() = %q, want it to contain status segment %q", out, "recoverable 2")
	}
}

// Held and recoverable are distinct session axes (issue #2255) and must be visually
// distinguishable (issue #2314): cyan RoleRecoverable, not RoleHeld's yellow.
func TestView_Header_StatusLine_RecoverableStyledDistinctFromHeld(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	m := NewModel()
	m = Update(m, IssuesLoadedMsg{RecoverableCount: 2})
	m.Picks = []Pick{{Number: "1", State: PickHeld}}

	out := View(m)
	if !strings.Contains(out, "\x1b[36mrecoverable 2\x1b[0m") {
		t.Errorf("View() = %q, want the recoverable segment styled with RoleRecoverable's cyan (\\x1b[36m), not RoleHeld's yellow", out)
	}
	if !strings.Contains(out, "\x1b[33mheld 1\x1b[0m") {
		t.Errorf("View() = %q, want the held segment styled with RoleHeld's yellow (\\x1b[33m)", out)
	}
}

// ADR 0031: the status line colors by semantic role on a color-capable terminal
// rather than rendering as bare text.
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

// ADR 0031: the stale-image alert carries the plain-Unicode warning glyph and
// styles by role, keeping its existing content.
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

// ADR 0031: the rebuilding alert carries the plain-Unicode rebuilding glyph and
// styles by role, keeping its existing content.
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

// Issue #1798 folded the "spindrift" wordmark into the header panel's top border,
// so the literal "====" rule is gone. The title disappears only once the terminal
// is too short to afford the bordered header at all, the same unboxed fallback
// renderBoxedHeader already applies (issue #1035 AC1/AC2), not a banner-specific rule.
func TestView_Header_Title_FoldedInBorder_CollapsesWhenTooShortToBox(t *testing.T) {
	tall := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	out := View(tall)
	if !strings.Contains(out, "spindrift") {
		t.Errorf("View() on a tall terminal = %q, want the spindrift title", out)
	}
	if strings.Contains(out, "====") {
		t.Errorf("View() on a tall terminal = %q, want no literal ==== banner rule", out)
	}

	// The boxed header's minimum render is 3 rows (top border, status line,
	// bottom border), so a height of 2 falls back to the unboxed header,
	// dropping the border and the title folded into it.
	short := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 2})
	out = View(short)
	if strings.Contains(out, "spindrift") {
		t.Errorf("View() on a too-short terminal = %q, want the titled header collapsed unboxed", out)
	}
	if !strings.Contains(out, "running 0/0") {
		t.Errorf("View() on a too-short terminal = %q, want the status line to remain", out)
	}
}

// Issue #1756: the header renders as its own bordered panel rather than running
// straight into the Section tabs below it.
func TestView_Header_RendersBordered(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	out := View(m)
	if got := strings.Count(out, "╭"); got != 1 {
		t.Errorf("View() has %d rounded top-left corners, want 1 (the header's own bordered panel): %q", got, out)
	}
}

// Issue #843 and ADR 0025: alert lines group with the header status line, ahead of
// ephemeral operator prompts like an in-progress filter edit. Rebuilding and
// RebuildErr share the grouping and are covered by TestView_Rebuilding_ShowsProgress
// and TestView_RebuildErr_Surfaced.
func TestView_Header_AlertsRenderBeforeEphemeralPrompts(t *testing.T) {
	m := NewModel()
	m = Update(m, StaleStatusMsg{RebuildStatus: RebuildStatus{Stale: true, Message: "rebuild needed"}})
	m = Update(m, FilterEditStartMsg{})
	m = Update(m, FilterChangedMsg{Filter: "bug"})

	out := View(m)
	statusIdx := strings.Index(out, "running 0/0")
	staleIdx := strings.Index(out, "stale")
	filterIdx := strings.Index(out, "/bug")

	if statusIdx == -1 || staleIdx == -1 || filterIdx == -1 {
		t.Fatalf("View() = %q, want status/stale/filter all present", out)
	}
	if !(statusIdx < staleIdx && staleIdx < filterIdx) {
		t.Errorf("View() = %q, want status < stale < filter prompt ordering", out)
	}
}

// Issue #843 AC5: a session with no CapMsg, no picks and no size event must render a
// clean header rather than needing a Launcher round-trip first.
func TestView_Header_LaunchLessSession_RendersCleanly(t *testing.T) {
	out := View(NewModel())
	// Per-segment, not one contiguous line: per-role styling (ADR 0031)
	// wraps each segment in its own ANSI escape codes.
	for _, want := range []string{"running 0/0", "waiting 0", "held 0", "settled 0"} {
		if !strings.Contains(out, want) {
			t.Errorf("View() = %q, want a clean status segment %q", out, want)
		}
	}
	for _, unwanted := range []string{"stale", "!!", "⚠", "↻", "ℹ"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("View() = %q, want no stray %q in a launch-less header", out, unwanted)
		}
	}
}

// Issue #646; ADR 0030 moved this from the two-column queue to whichever Section the
// pick's state maps into.
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

// Issue #646: a dissolved row carries its reason so the operator sees why a pick never
// launched. ADR 0030 folds PickDissolved into SectionFailed.
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

// Issue #647 AC2: a running row shows its latest heartbeat, so the overview is
// scannable without drilling in.
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

// Issue #2983: a running row shows its latest pass-manifest summary, mirroring
// TestView_RunningPick_ShowsHeartbeat.
func TestView_RunningPick_ShowsPassState(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	m = Update(m, QueueSnapshotMsg{Picks: []Pick{
		{Number: "42", Title: "fix the thing", State: PickRunning, PassState: "pass 2 (review: BLOCK)"},
	}})
	m = Update(m, SectionJumpMsg{Section: SectionRunning})

	out := View(m)
	if !strings.Contains(out, "pass 2 (review: BLOCK)") {
		t.Errorf("View() = %q, want the running row's pass-state line", out)
	}
}

// Heartbeat is box-log-derived, untrusted content (issue #1639), so it is stripped of
// control sequences the same way Title and Reason already are.
func TestView_RunningPick_SanitizesHeartbeatControlSequences(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	m := Update(NewModel(), SizeChangedMsg{Width: 300, Height: 24})
	m = Update(m, QueueSnapshotMsg{Picks: []Pick{
		{Number: "42", Title: "fix the thing", State: PickRunning, Heartbeat: "evil\x1b[2Jbeat"},
	}})
	m = Update(m, SectionJumpMsg{Section: SectionRunning})

	out := View(m)
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "evilbeat") && strings.Contains(line, "\x1b") {
			t.Errorf("running row = %q, want no raw escape bytes surviving sanitization", line)
		}
	}
	if !strings.Contains(out, "evilbeat") {
		t.Errorf("View() = %q, want the surrounding heartbeat text intact after stripping escapes", out)
	}
}

// Issue #652: the stale banner carries the probe's message and the rebuild-key hint,
// and renders only while Stale is true.
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

// An in-flight rebuild renders a progress line so the operator sees the confirm key
// took effect.
func TestView_Rebuilding_ShowsProgress(t *testing.T) {
	m := Update(NewModel(), StaleStatusMsg{RebuildStatus: RebuildStatus{Stale: true, Rebuilding: true}})
	out := View(m)
	if !strings.Contains(out, "rebuild") {
		t.Errorf("View() = %q, want a rebuilding-in-progress line", out)
	}
}

// A failed rebuild surfaces its error text, and launches stay held (Stale stays true).
func TestView_RebuildErr_Surfaced(t *testing.T) {
	m := Update(NewModel(), StaleStatusMsg{RebuildStatus: RebuildStatus{Stale: true, Err: "nix build failed"}})
	out := View(m)
	if !strings.Contains(out, "nix build failed") {
		t.Errorf("View() = %q, want the rebuild failure surfaced", out)
	}
}

// ADR 0031: the rebuild-failed alert carries the plain-Unicode warning glyph and
// styles by role, keeping its existing content.
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

// Issue #1131: RunNixBuild wraps merged nix stdout and stderr into one long
// multi-line error, which has to render as a single bounded banner line instead of
// blowing out the header.
func TestView_RebuildErr_Truncated(t *testing.T) {
	// Pinned rather than inherited: the rebuild-failed banner is styled
	// (ADR 0031), and the width bound below has to hold whether or not this
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

// Issue #1218: a startup orphan recovery failure's error text reaches the header.
func TestView_OrphanRecoveryErr_Surfaced(t *testing.T) {
	m := Update(NewModel(), OrphanRecoveryMsg{Err: "failed to adopt orphan #42: boom"})
	out := View(m)
	if !strings.Contains(out, "failed to adopt orphan #42: boom") {
		t.Errorf("View() = %q, want the orphan recovery failure surfaced", out)
	}
}

// ADR 0031: the orphan-adopt-failed alert carries the plain-Unicode warning glyph
// and styles by role, keeping its existing content.
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

// Issue #1141 closed the silent-switch gap: an operator whose pwd got moved off a
// branch during a rebuild has to see it, not discover it cold.
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

// Issue #2678: a Console session running under tea.WithAltScreen() never renders
// RunContinuous's stdout stale-drain report, so the summary needs a header banner. A
// zero-value RebuildStatus (every pre-#2678 Model) must still render byte-identical
// to before the change: no new blank line, no stray banner.
func TestView_StaleDrainSummary_Surfaced(t *testing.T) {
	before := View(NewModel())
	fresh := View(NewModel())
	if fresh != before {
		t.Fatalf("View(NewModel()) is non-deterministic across calls; can't use it as a regression baseline")
	}
	if strings.Contains(fresh, "stale-drain:") {
		t.Errorf("View() with no drain summary = %q, want no mention of a drain report", fresh)
	}

	m := Update(NewModel(), StaleStatusMsg{RebuildStatus: RebuildStatus{StaleDrainSummary: "==> stale-drain: 1.2s idle, 3.4 free-slot-s, 2 issue(s) held back"}})
	out := View(m)
	if strings.Contains(out, "==>") {
		t.Errorf("View() = %q, want the stdout-style \"==>\" arrow stripped from the TUI banner", out)
	}
	if !strings.Contains(out, "notice: stale-drain: 1.2s idle, 3.4 free-slot-s, 2 issue(s) held back") {
		t.Errorf("View() = %q, want the drain summary surfaced with the sibling lines' \"notice: \" prefix", out)
	}

	unchanged := View(Update(NewModel(), StaleStatusMsg{RebuildStatus: RebuildStatus{}}))
	if unchanged != before {
		t.Errorf("View() with empty StaleDrainSummary regressed the pre-#2678 rendering:\ngot:  %q\nwant: %q", unchanged, before)
	}
}

// ADR 0031: the branch-switch notice line carries the plain-Unicode notice
// glyph and styles by role, keeping its existing content.
func TestView_Header_BranchSwitchNotice_StyledWithGlyph(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	m := Update(NewModel(), StaleStatusMsg{RebuildStatus: RebuildStatus{BranchSwitchNotice: "switched off-branch tree from feature to main"}})
	out := View(m)

	var branchLine string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "switched off-branch") {
			branchLine = line
		}
	}
	if branchLine == "" {
		t.Fatalf("View() = %q, want a branch-switch notice line", out)
	}
	if !strings.Contains(branchLine, "ℹ") {
		t.Errorf("notice line = %q, want the notice glyph", branchLine)
	}
	if !strings.Contains(branchLine, "\x1b[") {
		t.Errorf("notice line = %q, want it styled with an ANSI escape sequence", branchLine)
	}
}

// A failed refresh surfaces its error text so the operator sees why the list went stale.
func TestView_RefreshError_Surfaced(t *testing.T) {
	m := Update(NewModel(), IssuesLoadedMsg{Err: errBoom})

	out := View(m)
	if !strings.Contains(out, errBoom.Error()) {
		t.Errorf("View() = %q, want it to contain %q", out, errBoom.Error())
	}
}

// Issue #784: the row at m.Cursor is marked so the operator can see which issue the
// cursor keys will act on.
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

// Issue #1818: the prompt's "/filter" prefix counts toward the clip width, so the
// footer stays inside bodyBudget's single reserved row on a narrow terminal.
func TestView_ModeFilterEdit_NarrowWidth_FooterFitsWidth(t *testing.T) {
	const width, height = 20, 24
	m := Update(NewModel(), SizeChangedMsg{Width: width, Height: height})
	m = Update(m, FilterEditStartMsg{})
	m = Update(m, FilterChangedMsg{Filter: "bug"})

	out := View(m)
	for i, line := range strings.Split(out, "\n") {
		if got := lipgloss.Width(line); got > width {
			t.Errorf("View() line %d is %d columns wide, want at most the terminal's %d: %q", i, got, width, line)
		}
	}
	if got := strings.Count(out, "\n") + 1; got > height {
		t.Errorf("View() rendered %d lines, want at most Height (%d) — a clipped footer must still fit bodyBudget's single reserved row for it", got, height)
	}
}

// Issue #1818: the "terminate #N? " prefix counts toward the clip width, so the
// footer stays inside bodyBudget's single reserved row on a narrow terminal.
func TestView_ModeTerminateConfirm_NarrowWidth_FooterFitsWidth(t *testing.T) {
	const width, height = 20, 24
	m := Update(NewModel(), SizeChangedMsg{Width: width, Height: height})
	m = Update(m, TerminateRequestedMsg{Number: "42"})

	out := View(m)
	for i, line := range strings.Split(out, "\n") {
		if got := lipgloss.Width(line); got > width {
			t.Errorf("View() line %d is %d columns wide, want at most the terminal's %d: %q", i, got, width, line)
		}
	}
	if got := strings.Count(out, "\n") + 1; got > height {
		t.Errorf("View() rendered %d lines, want at most Height (%d) — a clipped footer must still fit bodyBudget's single reserved row for it", got, height)
	}
}

// Issue #1818: the quit-confirm hint wraps past one row when rendered unclipped, so
// it clips to the terminal's own width like every other footer in this file.
func TestView_ModeQuitConfirm_NarrowWidth_FooterFitsWidth(t *testing.T) {
	const width, height = 20, 24
	m := Update(NewModel(), SizeChangedMsg{Width: width, Height: height})
	m = Update(m, QuitRequestedMsg{})

	out := View(m)
	for i, line := range strings.Split(out, "\n") {
		if got := lipgloss.Width(line); got > width {
			t.Errorf("View() line %d is %d columns wide, want at most the terminal's %d: %q", i, got, width, line)
		}
	}
	if got := strings.Count(out, "\n") + 1; got > height {
		t.Errorf("View() rendered %d lines, want at most Height (%d) — a clipped footer must still fit bodyBudget's single reserved row for it", got, height)
	}
}

// Issue #784: an in-progress filter edit shows the text typed so far.
func TestView_ModeFilterEdit_ShowsInputLine(t *testing.T) {
	m := NewModel()
	m = Update(m, FilterEditStartMsg{})
	m = Update(m, FilterChangedMsg{Filter: "bug"})

	out := View(m)
	if !strings.Contains(out, "/bug") && !strings.Contains(out, "/ bug") {
		t.Errorf("View() = %q, want the in-progress filter text shown", out)
	}
}

// Issue #1793: the filter-edit hints render dim (RoleDim) through the shared footer
// renderer, the same treatment the other migrated footers got.
func TestView_ModeFilterEdit_FooterStyledDim(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	m = Update(m, FilterEditStartMsg{})
	m = Update(m, FilterChangedMsg{Filter: "bug"})

	out := View(m)
	if !strings.Contains(out, "\x1b[90m[enter] apply · [esc] cancel\x1b[0m") {
		t.Errorf("View() = %q, want the filter-edit hint dim-styled with its text intact", out)
	}
}

// Issue #784: the help overlay lists every key the tea layer binds, and replaces the
// normal backlog rendering while it is open.
func TestView_ModeHelp_ListsBoundKeys(t *testing.T) {
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

// ADR 0030, issue #1500: H/L and 1-5 are the section-switched list's navigation,
// replacing the retired tab focus-switch binding.
func TestView_ModeHelp_ListsSectionKeys(t *testing.T) {
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

// Issue #995, reworded by #1500 for ADR 0030's Section body, then by #1501 for the
// sidebar and #1632 for the detail modal: enter no longer picks (picking moved to
// "p"), so its help entry has to document both context-sensitive behaviours.
func TestView_ModeHelp_DescribesContextSensitiveEnter(t *testing.T) {
	m := Update(NewModel(), HelpToggleMsg{})

	out := View(m)
	for _, want := range []string{
		"otherwise: open",
		"the highlighted row's ticket detail (Backlog Section)",
		"highlighted pick's live-tail sidebar",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("View() = %q, want it to describe context-sensitive enter behavior %q", out, want)
		}
	}
}

// Issue #785's picks/queue keys, plus "X" as Terminate now that "k" reverted to
// vim's cursor-up (issue #1500).
func TestView_ModeHelp_ListsNewKeybindings(t *testing.T) {
	m := Update(NewModel(), HelpToggleMsg{})

	out := View(m)
	for _, want := range []string{"p ", "u ", "P ", "X ", "+", "-", "b "} {
		if !strings.Contains(out, want) {
			t.Errorf("View() = %q, want it to mention key %q", out, want)
		}
	}
	if !strings.Contains(out, "terminate the highlighted live Dispatch") {
		t.Errorf("View() = %q, want \"X\" documented as Terminate", out)
	}
}

// Issue #1619: startup only detects an orphan now, so the operator needs a
// discoverable "A" adopt gesture in the help overlay.
func TestView_ModeHelp_ListsAdoptOrphanKey(t *testing.T) {
	m := Update(NewModel(), HelpToggleMsg{})

	out := View(m)
	if !strings.Contains(out, "\n  A ") {
		t.Errorf("View() = %q, want an \"A\" key entry", out)
	}
	if !strings.Contains(out, "adopt") || !strings.Contains(out, "orphan") {
		t.Errorf("View() = %q, want it to describe the \"A\" adopt-orphan gesture", out)
	}
}

// Issue #1128 added the rebuild-output pane's "o" open key.
func TestView_ModeHelp_ListsRebuildOutputKey(t *testing.T) {
	m := Update(NewModel(), HelpToggleMsg{})

	out := View(m)
	if !strings.Contains(out, "\n  o ") {
		t.Errorf("View() = %q, want an \"o\" key entry", out)
	}
	if !strings.Contains(out, "rebuild output") {
		t.Errorf("View() = %q, want it to describe the rebuild-output pane", out)
	}
}

// Issue #1036 AC: pgup/pgdown are the backlog and queue viewport's own scroll keys,
// distinct from the sidebar's identically named ones.
func TestView_ModeHelp_ListsBodyScrollKeys(t *testing.T) {
	m := Update(NewModel(), HelpToggleMsg{})

	out := View(m)
	if !strings.Contains(out, "pgup/pgdown  jump a full page of the active Section's live") {
		t.Errorf("View() = %q, want it to mention the dynamic Section page jump", out)
	}
}

// Issue #1628 AC7: "G" and "gg" are the list body's jump-to-bottom and
// jump-to-top motions.
func TestView_ModeHelp_ListsJumpKeys(t *testing.T) {
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

// Issue #1629 AC4: the sidebar has its own "gg", detach follow and jump to its top,
// alongside the existing "G / end" jump to the bottom.
func TestView_ModeHelp_ListsSidebarJumpToTop(t *testing.T) {
	m := Update(NewModel(), HelpToggleMsg{})

	out := View(m)
	if !strings.Contains(out, "G / end     re-attach follow and jump to the sidebar's bottom") {
		t.Errorf("View() = %q, want the sidebar's \"G / end\" entry unchanged", out)
	}
	if !strings.Contains(out, "gg          detach follow and jump to the sidebar's top") {
		t.Errorf("View() = %q, want a sidebar \"gg\" entry", out)
	}
}

// Issue #1059: the sidebar's page jump is a fixed size, unlike the backlog and
// queue's viewport-derived one. The two keys share a name but not a page size.
func TestView_ModeHelp_ContrastsSidebarFixedPage(t *testing.T) {
	m := Update(NewModel(), HelpToggleMsg{})

	out := View(m)
	want := fmt.Sprintf("fixed at %d lines", fixedPaneScrollDelta)
	if !strings.Contains(out, want) {
		t.Errorf("View() = %q, want it to contain %q", out, want)
	}
}

// Issue #1647 AC: the vim page chords sit next to each pane's pgup/pgdown entry.
func TestView_ModeHelp_ListsCtrlFCtrlBAlongsidePageKeys(t *testing.T) {
	m := Update(NewModel(), HelpToggleMsg{})

	out := View(m)
	if !strings.Contains(out, "ctrl+f/ctrl+b, pgup/pgdown  jump a full page of the active Section's live") {
		t.Errorf("View() = %q, want the list body entry to list ctrl+f/ctrl+b alongside pgup/pgdown", out)
	}
	if !strings.Contains(out, "j/k, ctrl+f/ctrl+b, pgup/pgdown  scroll the sidebar") {
		t.Errorf("View() = %q, want the sidebar entry to list ctrl+f/ctrl+b alongside pgup/pgdown", out)
	}
	if !strings.Contains(out, "j/k, ctrl+f/ctrl+b, pgup/pgdown scroll it") {
		t.Errorf("View() = %q, want the rebuild-output entry to list ctrl+f/ctrl+b alongside pgup/pgdown", out)
	}
}

// Issue #1648 AC: the vim half-page chords are listed for each pane.
func TestView_ModeHelp_ListsCtrlDCtrlUAlongsideHalfPageKeys(t *testing.T) {
	m := Update(NewModel(), HelpToggleMsg{})

	out := View(m)
	if !strings.Contains(out, "ctrl+d/ctrl+u  jump a half page of the active Section's live") {
		t.Errorf("View() = %q, want the list body entry to list ctrl+d/ctrl+u", out)
	}
	if !strings.Contains(out, "ctrl+d/ctrl+u  scroll the sidebar a half page") {
		t.Errorf("View() = %q, want the sidebar entry to list ctrl+d/ctrl+u", out)
	}
	if !strings.Contains(out, "ctrl+d/ctrl+u scroll it a half page") {
		t.Errorf("View() = %q, want the rebuild-output entry to list ctrl+d/ctrl+u", out)
	}
}

// Issue #1630 AC: "G" and "gg" are documented for the rebuild-output pane.
func TestView_ModeHelp_ListsRebuildOutputJumpKeys(t *testing.T) {
	m := Update(NewModel(), HelpToggleMsg{})

	out := View(m)
	if !strings.Contains(out, "G jumps to its last page") {
		t.Errorf("View() = %q, want the rebuild-output pane's \"G\" jump documented", out)
	}
	if !strings.Contains(out, "gg to its first") {
		t.Errorf("View() = %q, want the rebuild-output pane's \"gg\" jump documented", out)
	}
}

// Issue #1128: the rebuild-output pane is RebuildOutput's only consumer. Open, it
// replaces the backlog rendering and carries a close-key hint.
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

// An off-by-one in the offset/end clamp would either repeat or skip a line at the
// window boundary.
func TestView_RebuildOutputOpen_ScrollOffsetWindowsContent(t *testing.T) {
	// Height 5 leaves a 2-line content budget (headerFooterLines plus the
	// trailing-"\n" reservation issue #1827 added), too small to show all 5
	// lines at once, so a scroll actually slides the window instead of clamping
	// back to the top like a short transcript that already fits.
	m := Update(NewModel(), SizeChangedMsg{Height: 5})
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

// Issue #1827, the same trailing-"\n" off-by-one #1825 fixed for the list view:
// View()'s output always ends in exactly one "\n", and that costs the terminal a
// physical row, so count with Split rather than TrimRight-then-count.
func TestView_RebuildOutputExactFit_FitsHeightWithFooterPinned(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Height: 10})
	lines := make([]string, 20)
	for i := range lines {
		lines[i] = fmt.Sprintf("l%d", i)
	}
	m = Update(m, StaleStatusMsg{RebuildStatus: RebuildStatus{Output: strings.Join(lines, "\n")}})
	m = Update(m, RebuildOutputOpenMsg{})

	out := View(m)
	if got := len(strings.Split(out, "\n")); got > m.Height {
		t.Errorf("View() rendered %d physical lines, want <= m.Height (%d): %q", got, m.Height, out)
	}
	if !strings.Contains(out, "rebuild output:") {
		t.Errorf("View() = %q, want the pane's header line still present", out)
	}
	if !strings.Contains(out, "close") {
		t.Errorf("View() = %q, want the close-key hint footer present", out)
	}
}

// Issue #1791: the close-key hint renders dim (RoleDim) through the shared footer
// renderer, like the other migrated footers.
func TestView_RebuildOutputOpen_FooterStyledDim(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	m := Update(NewModel(), SizeChangedMsg{Height: 24})
	m = Update(m, StaleStatusMsg{RebuildStatus: RebuildStatus{Output: "building derivation...\ndone"}})
	m = Update(m, RebuildOutputOpenMsg{})

	out := View(m)
	if !strings.Contains(out, "\x1b[90m[x] close\x1b[0m") {
		t.Errorf("View() = %q, want the close-key hint dim-styled", out)
	}
}

// Issue #1758: the detail modal is a box floating over the still-rendered list, not a
// fullscreen takeover, so the header banner above it stays visible.
func TestView_DetailModal_FloatsOverList_BannerStillVisible(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 100, Height: 40})
	m = Update(m, DetailModalOpenMsg{Number: "42", Title: "fix the thing"})

	out := View(m)
	if !strings.Contains(out, "spindrift") {
		t.Errorf("View() = %q, want the banner still visible above the floating detail modal instead of a fullscreen takeover", out)
	}
}

// Issue #1759 AC: below detailModalFits' threshold the modal renders through the
// fullscreen renderer instead of a cramped floating box, mirroring the sidebar's own
// sidebarFits degradation.
func TestView_DetailModal_TinyTerminal_FallsBackToFullscreen(t *testing.T) {
	cases := []struct {
		name          string
		width, height int
	}{
		{"width short", detailModalBoxMinWidth - 1, 24},
		{"height short", 80, detailModalBoxMinHeight - 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := Update(NewModel(), SizeChangedMsg{Width: c.width, Height: c.height})
			m = Update(m, DetailModalOpenMsg{Number: "42", Title: "fix the thing"})

			out := View(m)
			if strings.Contains(out, "spindrift") {
				t.Errorf("View() = %q, want the fullscreen fallback to replace the banner entirely, not float over it", out)
			}
			if strings.Contains(out, "╭") || strings.Contains(out, "╰") {
				t.Errorf("View() = %q, want no floating box border on a too-small terminal", out)
			}
			if !strings.Contains(out, "#42 fix the thing") {
				t.Errorf("View() = %q, want the fullscreen renderer's own number/title line", out)
			}
		})
	}
}

// Issue #1791: the tiny-terminal fallback's footer renders dim (RoleDim) like the
// fullscreen and docked sidebar footers.
func TestView_DetailModal_FullscreenFallback_FooterStyledDim(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	m := Update(NewModel(), SizeChangedMsg{Width: detailModalBoxMinWidth - 1, Height: 24})
	m = Update(m, DetailModalOpenMsg{Number: "42", Title: "fix the thing"})

	out := View(m)
	if !strings.Contains(out, "\x1b[90m[esc] close · [p] pick") {
		t.Errorf("View() = %q, want the fullscreen fallback footer dim-styled with its hint text intact", out)
	}
}

// Issue #1832: the fallback's pinned label row gets the same bracketed, dim-styled
// treatment as the floating box, so the two renderings stay in parity.
func TestView_DetailModal_FullscreenFallback_LabelsStyledDim(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	m := Update(NewModel(), SizeChangedMsg{Width: detailModalBoxMinWidth - 1, Height: 24})
	m = Update(m, DetailModalOpenMsg{Number: "42", Title: "fix the thing", Labels: []string{"bug", "console"}})

	out := View(m)
	if !strings.Contains(out, "\x1b[90m[bug, console]\x1b[0m") {
		t.Errorf("View() = %q, want the fullscreen fallback's label row bracketed and dim-styled", out)
	}
}

// Issue #1818. The title is kept short so the width picked here, narrower than
// "[esc] close" itself, exercises the footer's own clip path rather than tripping on
// an unrelated longer line first.
func TestView_DetailModal_FullscreenFallback_NarrowWidth_FooterFitsWidth(t *testing.T) {
	const width, height = 10, 24
	m := Update(NewModel(), SizeChangedMsg{Width: width, Height: height})
	m = Update(m, DetailModalOpenMsg{Number: "42", Title: "x"})

	out := View(m)
	for i, line := range strings.Split(out, "\n") {
		if got := lipgloss.Width(line); got > width {
			t.Errorf("View() line %d is %d columns wide, want at most the terminal's %d: %q", i, got, width, line)
		}
	}
	if got := strings.Count(out, "\n") + 1; got > height {
		t.Errorf("View() rendered %d lines, want at most Height (%d) — a clipped footer must still fit its reserved row", got, height)
	}
}

// detailModalBoxTopBorderLine returns out's floating detail modal box's top border
// row. It keys on "╭" plus the title rather than a bare "╭" search: once the header
// and sidebar panels grew their own rounded border (issue #1756), a bare search finds
// whichever box comes first, which depends on whether termenv degrades to ASCII.
// detailModalBoxTopBorder renders "#number title" into its border line and nowhere else.
func detailModalBoxTopBorderLine(t *testing.T, out, title string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "╭") && strings.Contains(line, title) {
			return line
		}
	}
	t.Fatalf("View() = %q, want a floating box top border line containing %q", out, title)
	return ""
}

// detailModalBoxBorderWidth returns the display width of the box itself, the "╭...╮"
// span, not the whole composited row: compositeOverlay splices the box into a
// still-visible base row, so the row is always terminal-width. Measuring the render
// keeps the check independent of detailModalBoxSize's own math.
func detailModalBoxBorderWidth(t *testing.T, out, title string) int {
	t.Helper()
	line := detailModalBoxTopBorderLine(t, out, title)
	start := strings.Index(line, "╭")
	end := strings.Index(line[start:], "╮")
	if end < 0 {
		t.Fatalf("View() = %q, want a matching closing corner on the top border line", out)
	}
	return ansi.StringWidth(line[start : start+end+len("╮")])
}

// detailModalBoxOriginX returns the display column the box's top-left corner lands at,
// measured with ansi.StringWidth so styled base content sharing the row (a colored
// Section tab, say) does not skew the count the way a byte or rune index would.
func detailModalBoxOriginX(t *testing.T, out, title string) int {
	t.Helper()
	line := detailModalBoxTopBorderLine(t, out, title)
	return ansi.StringWidth(line[:strings.Index(line, "╭")])
}

// Issue #1759 AC: a resize while the modal is open re-sizes and re-centers the box
// instead of leaving it pinned at whatever size it opened at.
func TestView_DetailModal_Resize_RecentersAndResizesBox(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	m := Update(NewModel(), SizeChangedMsg{Width: 60, Height: 30})
	m = Update(m, DetailModalOpenMsg{Number: "42", Title: "fix the thing"})
	outSmall := View(m)
	smallWidth := detailModalBoxBorderWidth(t, outSmall, "#42 fix the thing")
	smallX := detailModalBoxOriginX(t, outSmall, "#42 fix the thing")

	m = Update(m, SizeChangedMsg{Width: 120, Height: 30})
	outGrown := View(m)
	grownWidth := detailModalBoxBorderWidth(t, outGrown, "#42 fix the thing")
	grownX := detailModalBoxOriginX(t, outGrown, "#42 fix the thing")

	wantSmall, _ := detailModalBoxSize(60, 30)
	wantGrown, _ := detailModalBoxSize(120, 30)
	wantSmallX, _ := detailModalBoxOrigin(60, 30, wantSmall, 0)
	wantGrownX, _ := detailModalBoxOrigin(120, 30, wantGrown, 0)
	if smallX != wantSmallX {
		t.Errorf("box origin x at 60-column terminal = %d, want %d (centered)", smallX, wantSmallX)
	}
	if grownX != wantGrownX {
		t.Errorf("box origin x at 120-column terminal = %d, want %d (re-centered)", grownX, wantGrownX)
	}
	if smallWidth != wantSmall {
		t.Errorf("box border width at 60-column terminal = %d, want %d", smallWidth, wantSmall)
	}
	if grownWidth != wantGrown {
		t.Errorf("box border width at 120-column terminal = %d, want %d", grownWidth, wantGrown)
	}
	if grownWidth <= smallWidth {
		t.Errorf("box border width after widening = %d, want wider than the original %d", grownWidth, smallWidth)
	}
}

// Issue #1758 AC: the ticket's "#number title" is set in the box's top border line,
// not on an interior content row.
func TestView_DetailModal_BorderShowsNumberAndTitle(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	m := Update(NewModel(), SizeChangedMsg{Width: 100, Height: 40})
	m = Update(m, DetailModalOpenMsg{Number: "42", Title: "fix the thing"})

	out := View(m)
	found := false
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "╭") && strings.Contains(line, "#42 fix the thing") {
			found = true
		}
	}
	if !found {
		t.Errorf("View() = %q, want the #number title set in the box's top border line", out)
	}
	if !strings.Contains(out, "╰") {
		t.Errorf("View() = %q, want a visible bottom border on the floating box", out)
	}
}

// Issue #1797 AC: the modal's old hand-rolled Unicode-only border never degraded under
// NO_COLOR, unlike every other panel in the package (issue #1755). Moving it onto the
// shared titled-border helper closed that gap.
func TestView_DetailModal_NoColor_BorderDegradesToAscii(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "xterm-256color")

	m := Update(NewModel(), SizeChangedMsg{Width: 100, Height: 40})
	m = Update(m, DetailModalOpenMsg{Number: "42", Title: "fix the thing"})

	out := View(m)
	if strings.Contains(out, "╭") {
		t.Errorf("View() = %q, want no rounded border glyphs under NO_COLOR", out)
	}
	found := false
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "+") && strings.Contains(line, "#42 fix the thing") {
			found = true
		}
	}
	if !found {
		t.Errorf("View() = %q, want the #number title set in an ASCII top border line", out)
	}
	if strings.Contains(out, "\x1b[") {
		t.Errorf("View() = %q, want no escape sequences at all under NO_COLOR", out)
	}
}

// Issue #1791: the floating box is the path View actually renders once detailModalFits
// (issue #1758), so its footer must be dim too, not just the tiny-terminal fallback's.
func TestView_DetailModal_FloatingBox_FooterStyledDim(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	m := Update(NewModel(), SizeChangedMsg{Width: 100, Height: 40})
	m = Update(m, DetailModalOpenMsg{Number: "42", Title: "fix the thing"})

	out := View(m)
	if !strings.Contains(out, "\x1b[90m[esc] close · [p] pick · [r] research · [u] unpick\x1b[0m") {
		t.Errorf("View() = %q, want the floating box's footer dim-styled with its hint text intact", out)
	}
}

// Issue #1632 AC: both sections render, each entry as number, source, open or closed
// state, and title.
func TestView_DetailModal_ShowsBlockedByAndBlocksSections(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	m = Update(m, DetailModalOpenMsg{Number: "42", Title: "fix the thing"})
	m = Update(m, DetailModalLoadedMsg{
		Number: "42",
		Body:   "the body",
		BlockedBy: []BlockerRef{
			{Number: "1540", Source: forge.DepSourceNative, State: forge.IssueOpen, Title: "Waves core"},
		},
		Blocks: []BlockerRef{
			{Number: "99", Source: forge.DepSourceBody, State: forge.IssueClosed, Title: "downstream thing"},
		},
	})

	out := View(m)
	if !strings.Contains(out, "Blocked by") {
		t.Errorf("View() = %q, want a \"Blocked by\" section header", out)
	}
	if !strings.Contains(out, `✗ #1540 (native) open "Waves core"`) {
		t.Errorf("View() = %q, want the Blocked-by entry formatted number+source+state+title", out)
	}
	if !strings.Contains(out, "Blocks") {
		t.Errorf("View() = %q, want a \"Blocks\" section header", out)
	}
	if !strings.Contains(out, `✓ #99 (body) closed "downstream thing"`) {
		t.Errorf("View() = %q, want the Blocks entry formatted number+source+state+title", out)
	}
}

// Issue #1632 review finding: when resolveBlockerRef finds the ref in neither the
// backlog nor an Issue fetch, it renders "unknown" rather than a bare double space
// and an empty quoted string.
func TestFormatBlockerRef_UnresolvedTitleAndState_RendersUnknown(t *testing.T) {
	got := formatBlockerRef(BlockerRef{Number: "123", Source: forge.DepSourceNative})
	want := `✗ #123 (native) unknown "unknown"`
	if got != want {
		t.Errorf("formatBlockerRef(...) = %q, want %q", got, want)
	}
}

// Issue #1632 review finding: the error render path had no coverage, and a failed body
// fetch has to surface the error instead of a blank or stuck-loading modal.
func TestView_DetailModal_Err_ShowsFailedToLoad(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	m = Update(m, DetailModalOpenMsg{Number: "42", Title: "fix the thing"})
	m = Update(m, DetailModalLoadedMsg{Number: "42", Err: errBoom})

	out := View(m)
	if !strings.Contains(out, errBoom.Error()) {
		t.Errorf("View() = %q, want it to contain %q", out, errBoom.Error())
	}
	if strings.Contains(out, "loading...") {
		t.Errorf("View() = %q, want the loading placeholder replaced by the error, not both shown", out)
	}
}

// Issue #1632 review finding: a tracker error message is untrusted text and crosses
// the same sanitization boundary as everything else the modal renders.
func TestView_DetailModal_SanitizesErr(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	m = Update(m, DetailModalOpenMsg{Number: "42", Title: "fix the thing"})
	m = Update(m, DetailModalLoadedMsg{Number: "42", Err: errors.New("evil\x1b[2Jerrtext\x1b]0;pwned\x07here")})

	out := View(m)
	// Checked as specific injected byte sequences rather than "no \x1b
	// anywhere on this line": the floating box (issue #1758) shares a physical
	// row with styled base UI, whose legitimate SGR escapes must not trip a
	// check meant to catch the untrusted error text's own sequences.
	for _, escape := range []string{"\x1b[2J", "\x1b]0;pwned\x07"} {
		if strings.Contains(out, escape) {
			t.Errorf("View() = %q, want the injected escape sequence %q stripped by sanitization", out, escape)
		}
	}
	if !strings.Contains(out, "evilerrtexthere") {
		t.Errorf("View() = %q, want the surrounding error text intact after stripping escapes", out)
	}
}

// Issue #1760's scrim: while the modal is open the base layer renders in RoleDim,
// replacing a running Pick's own RoleRunning escape, and closing it restores that.
func TestView_DetailModal_DimsListBehind(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	m := Update(NewModel(), SizeChangedMsg{Width: 100, Height: 40})
	m = Update(m, QueueSnapshotMsg{Picks: []Pick{
		{Number: "1", Title: "running one", State: PickRunning, Heartbeat: "7 turns"},
	}})
	m = Update(m, SectionJumpMsg{Section: SectionRunning})

	runningEscape := "\x1b[34m"
	dimEscape := "\x1b[90m"

	before := View(m)
	if !strings.Contains(before, runningEscape) {
		t.Fatalf("View() before opening modal = %q, want the running row styled with %q", before, runningEscape)
	}

	opened := Update(m, DetailModalOpenMsg{Number: "42", Title: "fix the thing"})
	duringModal := View(opened)
	if !strings.Contains(duringModal, dimEscape) {
		t.Errorf("View() with modal open = %q, want the base layer dimmed with %q", duringModal, dimEscape)
	}
	if strings.Contains(duringModal, runningEscape) {
		t.Errorf("View() with modal open = %q, want the running row's own %q replaced by the dim style", duringModal, runningEscape)
	}

	closed := Update(opened, DetailModalCloseMsg{})
	after := View(closed)
	if !strings.Contains(after, runningEscape) {
		t.Errorf("View() after closing modal = %q, want the running row's normal %q styling restored", after, runningEscape)
	}
}

// Issue #1632: a ticket with nothing declared in either direction must not grow empty
// "Blocked by" and "Blocks" headers with nothing under them.
func TestView_DetailModal_NoBlockersOrBlocks_ShowsNoSectionClutter(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	m = Update(m, DetailModalOpenMsg{Number: "42", Title: "fix the thing"})
	m = Update(m, DetailModalLoadedMsg{Number: "42", Body: "the body"})

	out := View(m)
	if strings.Contains(out, "Blocked by") {
		t.Errorf("View() = %q, want no \"Blocked by\" header with nothing to list", out)
	}
	if strings.Contains(out, "Blocks") {
		t.Errorf("View() = %q, want no \"Blocks\" header with nothing to list", out)
	}
}

// Issue #1632 AC, the body scrolls with j/k and the arrow keys. Height is pinned to
// detailModalBoxMinHeight, the smallest terminal still on the floating path (issue
// #1759), because its interior budget (2 border rows, 1 label line, 1 footer line,
// issue #1772) falls short of the 8-line body, so the scroll actually clips something.
func TestView_DetailModal_ScrollOffset_HidesLinesBeforeOffset(t *testing.T) {
	const labelLines = 1 // no Labels set below, so the bracketed "[]" is 1 line
	const bodyBudget = detailModalBoxMinHeight - 2 - labelLines - detailModalFooterLines
	lines := make([]string, 8)
	for i := range lines {
		lines[i] = fmt.Sprintf("l%d", i)
	}
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: detailModalBoxMinHeight})
	m = Update(m, DetailModalOpenMsg{Number: "42", Title: "fix the thing"})
	m = Update(m, DetailModalLoadedMsg{Number: "42", Body: strings.Join(lines, "\n")})
	m = Update(m, DetailModalScrollMsg{Delta: 2})

	out := View(m)
	if strings.Contains(out, "l0") || strings.Contains(out, "l1") {
		t.Errorf("View() = %q, want lines before the offset hidden", out)
	}
	if !strings.Contains(out, "l2") || !strings.Contains(out, fmt.Sprintf("l%d", 1+bodyBudget)) {
		t.Errorf("View() = %q, want lines from the offset onward", out)
	}
}

// Issue #1845: sidebarModalFits is the log modal's floating-versus-fullscreen gate,
// mirroring detailModalFits, and rejects a terminal below either legibility floor.
func TestSidebarModalFits_BelowMinDimension_ReturnsFalse(t *testing.T) {
	cases := []struct {
		name          string
		width, height int
		want          bool
	}{
		{"both at floor", sidebarModalBoxMinWidth, sidebarModalBoxMinHeight, true},
		{"width one short", sidebarModalBoxMinWidth - 1, sidebarModalBoxMinHeight, false},
		{"height one short", sidebarModalBoxMinWidth, sidebarModalBoxMinHeight - 1, false},
		{"plenty of room", sidebarModalBoxMinWidth * 2, sidebarModalBoxMinHeight * 2, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := Model{Width: c.width, Height: c.height}
			if got := sidebarModalFits(m); got != c.want {
				t.Errorf("sidebarModalFits(Width:%d, Height:%d) = %v, want %v", c.width, c.height, got, c.want)
			}
		})
	}
}

// Issue #1845 AC1: opening the sidebar on a terminal too narrow to dock floats the log
// modal over the queue instead of replacing the whole screen.
func TestView_SidebarModal_FloatsOverList_BannerStillVisible(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 100, Height: 40})
	m = Update(m, SidebarLoadedMsg{Number: "42", Title: "fix the thing", Activity: []ActivityLine{{Text: "hi"}}})

	out := View(m)
	if !strings.Contains(out, "spindrift") {
		t.Errorf("View() = %q, want the banner still visible above the floating log modal instead of a fullscreen takeover", out)
	}
}

// Issue #1845 AC2: the floating log modal dims the queue behind it exactly like the
// detail modal does, and restores the queue's own styling once it closes.
func TestView_SidebarModal_DimsListBehind(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	m := Update(NewModel(), SizeChangedMsg{Width: 100, Height: 40})
	m = Update(m, QueueSnapshotMsg{Picks: []Pick{
		{Number: "1", Title: "running one", State: PickRunning, Heartbeat: "7 turns"},
	}})
	m = Update(m, SectionJumpMsg{Section: SectionRunning})

	runningEscape := "\x1b[34m"
	dimEscape := "\x1b[90m"

	before := View(m)
	if !strings.Contains(before, runningEscape) {
		t.Fatalf("View() before opening sidebar = %q, want the running row styled with %q", before, runningEscape)
	}

	opened := Update(m, SidebarLoadedMsg{Number: "42", Title: "fix the thing", Activity: []ActivityLine{{Text: "hi"}}})
	duringModal := View(opened)
	if !strings.Contains(duringModal, dimEscape) {
		t.Errorf("View() with sidebar modal open = %q, want the base layer dimmed with %q", duringModal, dimEscape)
	}
	if strings.Contains(duringModal, runningEscape) {
		t.Errorf("View() with sidebar modal open = %q, want the running row's own %q replaced by the dim style", duringModal, runningEscape)
	}

	closed := Update(opened, SidebarCloseMsg{})
	after := View(closed)
	if !strings.Contains(after, runningEscape) {
		t.Errorf("View() after closing sidebar modal = %q, want the running row's normal %q styling restored", after, runningEscape)
	}
}

// Issue #1845 AC3: the log modal's border carries the row's "#<num> <title>", matching
// the detail modal's own aesthetic.
func TestView_SidebarModal_BorderShowsNumberAndTitle(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	m := Update(NewModel(), SizeChangedMsg{Width: 100, Height: 40})
	m = Update(m, SidebarLoadedMsg{Number: "42", Title: "fix the thing", Activity: []ActivityLine{{Text: "hi"}}})

	out := View(m)
	found := false
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "╭") && strings.Contains(line, "#42 fix the thing") {
			found = true
		}
	}
	if !found {
		t.Errorf("View() = %q, want the #number title set in the modal box's top border line", out)
	}
	if !strings.Contains(out, "╰") {
		t.Errorf("View() = %q, want a visible bottom border on the floating log modal", out)
	}
}

// Issue #1759 AC: the floating box is capped at detailModalBoxMax{Width,Height} rather
// than scaling without bound, and issue #1796 AC1/AC2 raised the width cap to 100
// columns from the old 84.
func TestDetailModalBoxSize_WideTerminal_ClampsToMax(t *testing.T) {
	width, height := detailModalBoxSize(300, 100)
	if width != 100 {
		t.Errorf("detailModalBoxSize(300, 100) width = %d, want 100 (new max-width cap)", width)
	}
	if height != detailModalBoxMaxHeight {
		t.Errorf("detailModalBoxSize(300, 100) height = %d, want %d (clamped to max)", height, detailModalBoxMaxHeight)
	}
}

// Issue #1759 AC: the box scales with the terminal rather than shrinking by a fixed
// margin, so well under the max clamp its size is a fraction of the terminal's own.
func TestDetailModalBoxSize_MidTerminal_SizedToFraction(t *testing.T) {
	width, height := detailModalBoxSize(60, 30)
	wantWidth := 60 * detailModalBoxWidthPercent / 100
	wantHeight := 30 * detailModalBoxHeightPercent / 100
	if width != wantWidth {
		t.Errorf("detailModalBoxSize(60, 30) width = %d, want %d (%d%% of terminal width)", width, wantWidth, detailModalBoxWidthPercent)
	}
	if height != wantHeight {
		t.Errorf("detailModalBoxSize(60, 30) height = %d, want %d (%d%% of terminal height)", height, wantHeight, detailModalBoxHeightPercent)
	}
}

// Issue #1759 AC, "clamped to a minimum": just above detailModalFits' threshold the
// width and height fraction would fall short of the floor, so the box clamps up.
func TestDetailModalBoxSize_NearFloorTerminal_ClampsToMin(t *testing.T) {
	width, height := detailModalBoxSize(detailModalBoxMinWidth, detailModalBoxMinHeight)
	if width != detailModalBoxMinWidth {
		t.Errorf("detailModalBoxSize(%d, %d) width = %d, want %d (clamped to min)", detailModalBoxMinWidth, detailModalBoxMinHeight, width, detailModalBoxMinWidth)
	}
	if height != detailModalBoxMinHeight {
		t.Errorf("detailModalBoxSize(%d, %d) height = %d, want %d (clamped to min)", detailModalBoxMinWidth, detailModalBoxMinHeight, height, detailModalBoxMinHeight)
	}
}

// Issue #1875 AC1: the log modal's own larger caps have to take effect once 80% of the
// terminal exceeds the detail modal's 100x30 cap, instead of clamping to that footprint.
func TestSidebarModalBoxSize_LargeTerminal_ExceedsDetailModal(t *testing.T) {
	sidebarWidth, sidebarHeight := sidebarModalBoxSize(165, 50)
	detailWidth, detailHeight := detailModalBoxSize(165, 50)
	if sidebarWidth <= detailWidth {
		t.Errorf("sidebarModalBoxSize(165, 50) width = %d, want > detailModalBoxSize width %d", sidebarWidth, detailWidth)
	}
	if sidebarHeight <= detailHeight {
		t.Errorf("sidebarModalBoxSize(165, 50) height = %d, want > detailModalBoxSize height %d", sidebarHeight, detailHeight)
	}
}

// Issue #1875 AC2: on a very large monitor the log modal pins to its own 180x54 cap
// rather than stretching corner to corner.
func TestSidebarModalBoxSize_VeryLargeTerminal_PinsToMax(t *testing.T) {
	width, height := sidebarModalBoxSize(300, 100)
	if width != sidebarModalBoxMaxWidth {
		t.Errorf("sidebarModalBoxSize(300, 100) width = %d, want %d (pinned to max)", width, sidebarModalBoxMaxWidth)
	}
	if height != sidebarModalBoxMaxHeight {
		t.Errorf("sidebarModalBoxSize(300, 100) height = %d, want %d (pinned to max)", height, sidebarModalBoxMaxHeight)
	}
}

// Issue #1759 AC: detailModalFits is the single predicate gating floating versus
// fullscreen, sidebarFits' analogue, and rejects a terminal below either floor.
func TestDetailModalFits_BelowMinDimension_ReturnsFalse(t *testing.T) {
	cases := []struct {
		name          string
		width, height int
		want          bool
	}{
		{"both at floor", detailModalBoxMinWidth, detailModalBoxMinHeight, true},
		{"width one short", detailModalBoxMinWidth - 1, detailModalBoxMinHeight, false},
		{"height one short", detailModalBoxMinWidth, detailModalBoxMinHeight - 1, false},
		{"plenty of room", detailModalBoxMinWidth * 2, detailModalBoxMinHeight * 2, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := Model{Width: c.width, Height: c.height}
			if got := detailModalFits(m); got != c.want {
				t.Errorf("detailModalFits(Width:%d, Height:%d) = %v, want %v", c.width, c.height, got, c.want)
			}
		})
	}
}

// Issue #1832 regression test: a single short label pinned atop the modal used to render
// as bare comma-joined text, indistinguishable at a glance from a stranded line of body
// text. It must read as labels, bracketed and dim-styled on a color terminal, degrading
// to plain bracketed text under NO_COLOR per ADR 0031.
func TestView_DetailModal_SingleLabel_VisuallyDistinctFromBody(t *testing.T) {
	t.Run("color terminal: bracketed and dim-styled, distinct from body", func(t *testing.T) {
		t.Setenv("NO_COLOR", "")
		t.Setenv("TERM", "xterm-256color")

		m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
		m = Update(m, DetailModalOpenMsg{Number: "42", Title: "fix the thing", Labels: []string{"bug"}})
		m = Update(m, DetailModalLoadedMsg{Number: "42", Body: "plain body text"})

		out := View(m)
		wantLabel := "\x1b[90m[bug]\x1b[0m"
		if !strings.Contains(out, wantLabel) {
			t.Errorf("View() = %q, want the pinned label row rendered %q", out, wantLabel)
		}
		if strings.Contains(out, "\x1b[90mplain body text\x1b[0m") {
			t.Errorf("View() = %q, want the body text left unstyled, not dimmed like the label row", out)
		}
	})

	t.Run("NO_COLOR: degrades to plain bracketed text", func(t *testing.T) {
		t.Setenv("NO_COLOR", "1")

		m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
		m = Update(m, DetailModalOpenMsg{Number: "42", Title: "fix the thing", Labels: []string{"bug"}})
		m = Update(m, DetailModalLoadedMsg{Number: "42", Body: "plain body text"})

		out := View(m)
		if !strings.Contains(out, "[bug]") {
			t.Errorf("View() = %q, want the pinned label row bracketed even under NO_COLOR", out)
		}
		if strings.Contains(out, "\x1b[") {
			t.Errorf("View() = %q, want no escape sequences at all under NO_COLOR", out)
		}
	})
}

// Issue #1632 AC: the modal exists so an operator can see what a clipped backlog row
// hides, so it shows every label in full, unlike clipLabels' "+N" truncation (issue #1631).
func TestView_DetailModal_LabelsUnclipped(t *testing.T) {
	labels := []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel"}
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	m = Update(m, DetailModalOpenMsg{Number: "42", Title: "fix the thing", Labels: labels})

	// Every label present unclipped already proves clipLabels-style "+N"
	// truncation did not happen. A bare strings.Contains(out, "+") check would
	// trip on the header panel's own ASCII "+" corners (issue #1756) under
	// this test's unset TERM.
	out := View(m)
	for _, label := range labels {
		if !strings.Contains(out, label) {
			t.Errorf("View() = %q, want every label present unclipped, missing %q", out, label)
		}
	}
}

// Issue #1772: a labels line overflowing the box interior wraps onto further rows
// instead of being truncated. TestView_DetailModal_LabelsUnclipped's 8 short labels
// never exceed even an 80-column interior, so they never reach padDisplay's truncate.
// The width here caps the box at detailModalBoxMaxWidth (AC2), a 98-column interior.
func TestView_DetailModal_LabelsWrapOnOverflow(t *testing.T) {
	labels := []string{
		"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf",
		"hotel", "india", "juliett", "kilo", "lima", "mike", "november",
		"oscar", "papa", "quebec", "romeo", "sierra", "tango", "uniform",
		"victor", "whiskey", "xray", "yankee", "zulu",
	}
	m := Update(NewModel(), SizeChangedMsg{Width: 200, Height: 24})
	m = Update(m, DetailModalOpenMsg{Number: "42", Title: "fix the thing", Labels: labels})

	out := View(m)
	for _, label := range labels {
		if !strings.Contains(out, label) {
			t.Errorf("View() = %q, want every label present after wrapping, missing %q", out, label)
		}
	}
}

// Issue #1832: the "+N more labels" indicator (issue #1778) folds into the same
// bracketed, dim-styled block as the retained labels, rather than trailing after the
// closing bracket as its own unbracketed, unstyled line.
func TestDetailModalLabelLinesCapped_OverflowIndicatorSharesBracket(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	got := detailModalLabelLinesCapped([]string{"alpha", "bravo", "charlie", "delta"}, 24, 1)
	want := []string{"\x1b[90m[alpha, +3 more labels]\x1b[0m"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("detailModalLabelLinesCapped(...) = %v, want %v", got, want)
	}
}

// The maxLines <= 0 case has no room for even one label inside a bracket, so it falls
// back to the bare indicator documented on detailModalLabelLinesCapped rather than an
// empty bracket or a styled line the zero-row budget cannot show.
func TestDetailModalLabelLinesCapped_ZeroBudget_FallsBackToBareIndicator(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	got := detailModalLabelLinesCapped([]string{"alpha", "bravo"}, 24, 0)
	want := []string{"+2 more labels"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("detailModalLabelLinesCapped(...) = %v, want %v", got, want)
	}
}

// Issue #1778, a gap left by #1772/#1780's wrap-instead-of-truncate fix: when wrapped
// label lines alone would consume the box's whole interior height,
// renderDetailModalContent caps them and appends an indicator instead of silently
// dropping trailing label lines or the footer. Each label here is wider than the
// interior, so wrapText puts one per line and the overflow point is deterministic.
func TestView_DetailModal_LabelOverflowShowsIndicator(t *testing.T) {
	labels := make([]string, 40)
	for i := range labels {
		labels[i] = fmt.Sprintf("label-%02d-%s", i, strings.Repeat("x", 90))
	}
	m := Update(NewModel(), SizeChangedMsg{Width: 200, Height: 60})
	m = Update(m, DetailModalOpenMsg{Number: "42", Title: "fix the thing", Labels: labels})

	out := View(m)
	if !strings.Contains(out, "more labels") {
		t.Errorf("View() = %q, want a \"+N more labels\" overflow indicator", out)
	}
	if !strings.Contains(out, "[esc] close") {
		t.Errorf("View() = %q, want the footer never dropped by label overflow", out)
	}
	// Each label is 99 columns, wider than the 98-column interior padDisplay
	// truncates every row to, so checking for the full label string would pass
	// whether or not it was capped. The short "label-NN-" prefix does fit a row
	// intact, so its absence distinguishes "dropped by the cap" from "rendered
	// and merely truncated".
	lastPrefix := fmt.Sprintf("label-%02d-", len(labels)-1)
	if strings.Contains(out, lastPrefix) {
		t.Errorf("View() = %q, want the last label (prefix %q) dropped behind the overflow indicator, not rendered", out, lastPrefix)
	}
}

// Issue #862, extended to the detail modal by an issue #1632 review finding: tracker
// title, labels, body and blocker titles are untrusted input, and Bubble Tea does not
// filter arbitrary control sequences before writing to the operator's terminal.
func TestView_DetailModal_SanitizesTitleLabelsBodyAndBlockerTitles(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	m = Update(m, DetailModalOpenMsg{
		Number: "42",
		Title:  "evil\x1b[2Jtitle\x1b]0;pwned\x07here",
		Labels: []string{"evil\x1b[2Jlabel"},
	})
	m = Update(m, DetailModalLoadedMsg{
		Number: "42",
		Body:   "evil\x1b[2Jbody\x1b]0;pwned\x07here",
		BlockedBy: []BlockerRef{
			{Number: "7", Source: forge.DepSourceNative, State: forge.IssueOpen, Title: "evil\x1b[2Jblocker\x1b]0;pwned\x07here"},
		},
	})

	out := View(m)
	// Checked as specific injected byte sequences rather than "no \x1b
	// anywhere on this line": the floating box (issue #1758) shares a physical
	// row with styled base UI, whose legitimate SGR escapes must not trip a
	// check meant to catch the untrusted text's own control sequences.
	for _, escape := range []string{"\x1b[2J", "\x1b]0;pwned\x07"} {
		if strings.Contains(out, escape) {
			t.Errorf("View() = %q, want the injected escape sequence %q stripped by sanitization", out, escape)
		}
	}
	for _, want := range []string{"eviltitlehere", "evillabel", "evilbodyhere", "evilblockerhere"} {
		if !strings.Contains(out, want) {
			t.Errorf("View() = %q, want the surrounding text %q intact after stripping escapes", out, want)
		}
	}
}

// Issues #648 and #1501: on a terminal too narrow to dock, the open sidebar replaces
// the backlog with the Activity feed, the default view rather than the Transcript.
func TestView_SidebarOpen_RendersActivityInsteadOfBacklog(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
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

func TestView_SidebarToggle_RendersTranscriptThenRaw(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
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

// Issue #786: Height is small enough that the content outruns the viewport budget, or
// the viewport clamp (issue #829) would pin Offset at 0. The Transcript view is toggled
// on so the content matches the plain lines the old drill-in test exercised.
func TestView_SidebarOffset_HidesLinesBeforeOffset(t *testing.T) {
	// Height 5, not 4: headerFooterLines(2) plus the trailing-newline
	// reservation (issue #1841) leave a 2-line content budget here, the same
	// 2-line window this test's offset math always assumed.
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 5})
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

func TestView_SidebarErr_Surfaced(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Height: 24})
	m = Update(m, SidebarLoadedMsg{Number: "42", Err: errBoom})

	out := View(m)
	if !strings.Contains(out, errBoom.Error()) {
		t.Errorf("View() = %q, want it to contain %q", out, errBoom.Error())
	}
}

// Issue #1501 review finding: a Transcript-only load failure must not blank an
// independently loaded, otherwise-good Activity feed. The error shows only once the
// operator toggles to the Transcript.
func TestView_SidebarTranscriptErr_HiddenBehindActivity(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
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

// Issue #722: the fullscreen sidebar joins only as many lines as the viewport can show,
// so scrolling near the top of a multi-MB transcript does not re-serialize content
// nowhere near the screen.
func TestView_SidebarFullscreen_WindowsToViewportHeight(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 5})
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

// Issue #1501, the core of ADR 0030's docked layout: a terminal wide enough for
// sidebarFits keeps the list visible beside the sidebar instead of hiding it.
func TestView_SidebarOpen_WideTerminal_DocksBesideList(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: sidebarMinListWidth + sidebarWidth + dockedBorderCols, Height: 24})
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

// Issue #1755 replaced the bare column divider with bordered panels, so the split reads
// as two boxes. With the header's own panel (issue #1756) that is 3 boxes.
func TestView_SidebarOpen_WideTerminal_PanelsRenderBordered(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	m := Update(NewModel(), SizeChangedMsg{Width: sidebarMinListWidth + sidebarWidth + dockedBorderCols, Height: 24})
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{{Number: "1", Title: "still visible"}}})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "#42 · hi"}}})

	out := View(m)
	if got := strings.Count(out, "╭"); got != 3 {
		t.Errorf("View() has %d rounded top-left corners, want 3 (the header, the docked list, and the sidebar each their own bordered panel): %q", got, out)
	}
}

// Issue #1755: the docked panels' border degrades to plain ASCII under NO_COLOR, the
// same degradation colorProfile() already applies to role coloring.
func TestView_SidebarOpen_NoColor_PanelsRenderAsciiBorder(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "xterm-256color")

	m := Update(NewModel(), SizeChangedMsg{Width: sidebarMinListWidth + sidebarWidth + dockedBorderCols, Height: 24})
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{{Number: "1", Title: "still visible"}}})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "#42 · hi"}}})

	out := View(m)
	if strings.Contains(out, "╭") {
		t.Errorf("View() = %q, want no rounded border glyphs under NO_COLOR", out)
	}
	if got := strings.Count(out, "+"); got != 12 {
		t.Errorf("View() has %d ASCII corner glyphs, want 12 (three panels — header, docked list, sidebar — four corners each): %q", got, out)
	}
	if strings.Contains(out, "\x1b[") {
		t.Errorf("View() = %q, want no escape sequences at all under NO_COLOR", out)
	}
}

// Issue #1755: the other half of colorProfile()'s degradation, a non-color terminal.
func TestView_SidebarOpen_DumbTerminal_PanelsRenderAsciiBorder(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "dumb")

	m := Update(NewModel(), SizeChangedMsg{Width: sidebarMinListWidth + sidebarWidth + dockedBorderCols, Height: 24})
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{{Number: "1", Title: "still visible"}}})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "#42 · hi"}}})

	out := View(m)
	if strings.Contains(out, "╭") {
		t.Errorf("View() = %q, want no rounded border glyphs on TERM=dumb", out)
	}
	if got := strings.Count(out, "+"); got != 12 {
		t.Errorf("View() has %d ASCII corner glyphs, want 12 (three panels — header, docked list, sidebar — four corners each): %q", got, out)
	}
	if strings.Contains(out, "\x1b[") {
		t.Errorf("View() = %q, want no escape sequences at all on TERM=dumb", out)
	}
}

// Issue #1755: bodyBudget has to reserve exactly the lines View itself reserves, or the
// bordered panels' row budget comes out too generous and the render spills past the
// terminal, breaking the #1035/#1500 never-overflow-Height invariant.
func TestView_SidebarOpen_QueueEnterNotice_DockedPanelsRespectHeight(t *testing.T) {
	const height = 10
	m := Update(NewModel(), SizeChangedMsg{Width: sidebarMinListWidth + sidebarWidth + dockedBorderCols, Height: height})
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{{Number: "1", Title: "still visible"}}})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "#42 · hi"}}})
	m = Update(m, QueueEnterNoticedMsg{})

	out := View(m)
	if got := strings.Count(out, "\n") + 1; got > height {
		t.Errorf("View() rendered %d lines, want at most Height (%d) with QueueEnterNotice showing beside a docked sidebar: %q", got, height, out)
	}
}

// Issue #1755: at the narrowest width sidebarFits allows docking, the two panels plus
// their border overhead must still fit the terminal, with nothing wrapping.
func TestView_SidebarOpen_MinimumFittingWidth_PanelsFitTerminalWidth(t *testing.T) {
	width := sidebarMinListWidth + sidebarWidth + dockedBorderCols
	m := Update(NewModel(), SizeChangedMsg{Width: width, Height: 24})
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{{Number: "1", Title: "still visible"}}})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "#42 · hi"}}})

	out := View(m)
	// lipgloss.Width, not runewidth.StringWidth: on a color-capable ambient
	// TERM the panel border carries ANSI codes (ADR 0031), which runewidth
	// counts as display width and lipgloss's ANSI-aware measurement does not.
	for i, line := range strings.Split(out, "\n") {
		if got := lipgloss.Width(line); got > width {
			t.Errorf("View() line %d is %d columns wide, want at most the terminal's %d: %q", i, got, width, line)
		}
	}
}

// Issue #1755: the shorter panel's content pads out to the taller one's height before
// the border wraps it, so both boxes close on the same row.
func TestView_SidebarOpen_UnevenContent_PanelBottomsAlign(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	m := Update(NewModel(), SizeChangedMsg{Width: sidebarMinListWidth + sidebarWidth + dockedBorderCols, Height: 24})
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{{Number: "1", Title: "only issue"}}})
	activity := make([]ActivityLine, 6)
	for i := range activity {
		activity[i] = ActivityLine{Text: fmt.Sprintf("line %d", i)}
	}
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: activity})

	out := View(m)
	lines := strings.Split(out, "\n")
	last := lines[len(lines)-1]
	if got := strings.Count(last, "╰"); got != 2 {
		t.Errorf("View()'s last line has %d bottom-left corners, want 2 (both panels closing on the same row): %q\nfull output:\n%s", got, last, out)
	}
}

// Issue #1501 review finding: the divider spans only as many rows as the taller panel
// actually rendered, so short content does not force blank divider rows down to the
// bottom of a tall terminal.
func TestView_SidebarOpen_ShortContent_DividerDoesNotFillWholeBudget(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: sidebarMinListWidth + sidebarWidth + dockedBorderCols, Height: 24})
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{{Number: "1", Title: "only issue"}}})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "one line"}}})

	out := View(m)
	got := strings.Count(out, "\n")
	// Header, tabs and a couple of content rows, nowhere near the Height 24
	// budget the pre-fix divider always forced the joined body up to.
	if got > 15 {
		t.Errorf("View() rendered %d lines, want well under Height (24) — the divider must not pad the body out to the full budget for short content", got)
	}
}

// ADR 0030's narrow-terminal degradation (issue #1501): one column short of sidebarFits
// the list disappears entirely rather than squeezing both columns illegibly.
func TestView_SidebarOpen_NarrowTerminal_FallsBackFullscreen(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: sidebarMinListWidth + sidebarWidth + dockedBorderCols - 1, Height: 24})
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

// Issue #1818: the fullscreen sidebar's footer wraps past one row when rendered
// unclipped, so it clips to the terminal's own width like every other footer here.
func TestView_SidebarFullscreen_NarrowWidth_FooterFitsWidth(t *testing.T) {
	// Wide enough that the sidebar's own label line (not this issue's
	// concern) already fits, narrow enough that the footer's 52-column
	// unclipped hint would still overflow it.
	const width, height = 25, 24
	m := Update(NewModel(), SizeChangedMsg{Width: width, Height: height})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "#42 · hi"}}})

	out := View(m)
	for i, line := range strings.Split(out, "\n") {
		if got := lipgloss.Width(line); got > width {
			t.Errorf("View() line %d is %d columns wide, want at most the terminal's %d: %q", i, got, width, line)
		}
	}
	if got := strings.Count(out, "\n") + 1; got > height {
		t.Errorf("View() rendered %d lines, want at most Height (%d) — a clipped footer must still fit its reserved row", got, height)
	}
}

// Issue #1841: an unclipped long line soft-wraps past windowSidebarLines' logical-line
// budget and pushes the modal's top border and footer off the viewport, so each line
// clips to one physical row the way renderSidebarDocked already does.
func TestView_SidebarFullscreen_LongTranscriptLine_ClipsToWidth(t *testing.T) {
	const width, height = 40, 24
	m := Update(NewModel(), SizeChangedMsg{Width: width, Height: height})
	m = Update(m, SidebarLoadedMsg{Number: "42", Rendered: strings.Repeat("x", width*3)})
	m = Update(m, SidebarToggleMsg{}) // -> Transcript (rendered)

	out := View(m)
	for i, line := range strings.Split(out, "\n") {
		if got := lipgloss.Width(line); got > width {
			t.Errorf("View() line %d is %d columns wide, want at most the terminal's %d: %q", i, got, width, line)
		}
	}
	if got := strings.Count(out, "\n") + 1; got > height {
		t.Errorf("View() rendered %d lines, want at most Height (%d) — an unclipped line's soft-wrap must not spill past the budget", got, height)
	}
}

// Issue #1841 AC3: the clip holds for the raw JSONL view too. windowSidebarLines and
// the clip loop it feeds do not care which of the three views populated Sidebar.Lines.
func TestView_SidebarFullscreen_RawViewLongLine_ClipsToWidth(t *testing.T) {
	const width, height = 40, 24
	m := Update(NewModel(), SizeChangedMsg{Width: width, Height: height})
	m = Update(m, SidebarLoadedMsg{Number: "42", Raw: strings.Repeat("y", width*3)})
	m = Update(m, SidebarToggleMsg{}) // -> Transcript (rendered)
	m = Update(m, SidebarToggleMsg{}) // -> Transcript (raw)

	out := View(m)
	for i, line := range strings.Split(out, "\n") {
		if got := lipgloss.Width(line); got > width {
			t.Errorf("View() line %d is %d columns wide, want at most the terminal's %d: %q", i, got, width, line)
		}
	}
}

// Issue #1841 AC3 on the [z] zoom trigger. At this width zoom renders the floating log
// modal (issue #1845) rather than the old fullscreen takeover, so this also guards that
// the modal's own clip never lets a composited line spill past the terminal width.
func TestView_SidebarFullscreen_ZoomedLongLine_ClipsToWidth(t *testing.T) {
	const width, height = sidebarMinListWidth + sidebarWidth + dockedBorderCols, 24
	m := Update(NewModel(), SizeChangedMsg{Width: width, Height: height})
	m = Update(m, SidebarLoadedMsg{Number: "42", Rendered: strings.Repeat("z", width*3)})
	m = Update(m, SidebarToggleMsg{}) // -> Transcript (rendered)
	m = Update(m, SidebarZoomToggleMsg{})

	out := View(m)
	for i, line := range strings.Split(out, "\n") {
		if got := lipgloss.Width(line); got > width {
			t.Errorf("View() line %d is %d columns wide, want at most the terminal's %d: %q", i, got, width, line)
		}
	}
}

// Issue #1841: View()'s guaranteed trailing "\n" costs the terminal a physical row, the
// off-by-one class #1825/#1827 fixed elsewhere, and renderSidebarFullscreen never
// reserved one, unlike renderSidebarDocked which inherits it through bodyBudget.
func TestView_SidebarFullscreen_ExactFitContent_FitsHeightWithFooterPinned(t *testing.T) {
	const width, height = 80, 10
	m := Update(NewModel(), SizeChangedMsg{Width: width, Height: height})
	// headerFooterLines(2) leaves an 8-line content budget at height 10;
	// 8 short lines exactly fill it without soft-wrapping, isolating the
	// trailing-newline reservation gap from the per-line clip fix above.
	lines := make([]string, height-headerFooterLines)
	for i := range lines {
		lines[i] = fmt.Sprintf("l%d", i)
	}
	m = Update(m, SidebarLoadedMsg{Number: "42", Rendered: strings.Join(lines, "\n")})
	m = Update(m, SidebarToggleMsg{}) // -> Transcript (rendered)

	out := View(m)
	// Split, not TrimRight-then-count, for the same reason
	// TestView_ExactFitBacklog_FitsHeightWithBannerAndFooterPinned uses it:
	// trimming the trailing "\n" first hides exactly the row this overflow
	// turns on.
	if got := len(strings.Split(out, "\n")); got > height {
		t.Errorf("View() rendered %d physical lines, want at most Height (%d): %q", got, height, out)
	}
	if !strings.Contains(out, "[t] cycle") {
		t.Errorf("View() = %q, want the footer hint line still pinned and visible", out)
	}
}

// assertSidebarFitsHeightBudget is issue #1842's shared guard, reused by the fullscreen
// and docked sidebar tests. Checking width and row count together matters: a renderer
// that clips every line but forgets a chrome row, or reserves the right row count but
// joins one unclipped wide line, still overflows the way #1841 did. The height check
// counts "\n"-split rows rather than re-wrapping, so it only holds beside the width check.
func assertSidebarFitsHeightBudget(t *testing.T, out string, width, height int) {
	t.Helper()
	rows := strings.Split(out, "\n")
	for i, row := range rows {
		if got := lipgloss.Width(row); got > width {
			t.Errorf("row %d is %d columns wide, want at most width (%d): %q", i, got, width, row)
		}
	}
	if got := len(rows); got > height {
		t.Errorf("rendered %d physical rows, want at most height (%d):\n%s", got, height, out)
	}
}

// sidebarWideLinesFixture is the shared setup half of issue #1842's guard, so the
// fullscreen and docked cases differ only in the width, height and Update sequence each
// renderer needs.
func sidebarWideLinesFixture(width, count int) (activity []ActivityLine, content string) {
	wide := strings.Repeat("w", width*3)
	lines := make([]string, count)
	activity = make([]ActivityLine, count)
	for i := range lines {
		lines[i] = wide
		activity[i] = ActivityLine{Text: wide}
	}
	return activity, strings.Join(lines, "\n")
}

// Issue #1842's fullscreen guard. The #1841 *_ClipsToWidth tests each use a single wide
// line, never enough to fill a renderer's whole logical-line budget and never shared
// with a docked-renderer test, so this is what covers "no sidebar view, in any
// renderer" per the AC.
func TestView_SidebarFullscreen_WideLines_FitHeightBudgetAcrossAllViews(t *testing.T) {
	const width, height = 40, 24
	activity, content := sidebarWideLinesFixture(width, height)

	m := Update(NewModel(), SizeChangedMsg{Width: width, Height: height})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: activity, Rendered: content, Raw: content})

	assertSidebarFitsHeightBudget(t, View(m), width, height) // activity

	m = Update(m, SidebarToggleMsg{})
	assertSidebarFitsHeightBudget(t, View(m), width, height) // transcript (rendered)

	m = Update(m, SidebarToggleMsg{})
	assertSidebarFitsHeightBudget(t, View(m), width, height) // transcript (raw)
}

// Issue #1842's docked counterpart, sharing the same fixture and helper.
// renderSidebarDocked already clips (issue #1799, predating #1841's fullscreen fix), so
// this is expected to pass unchanged; its value is guarding against a later regression.
func TestView_SidebarDocked_WideLines_FitHeightBudgetAcrossAllViews(t *testing.T) {
	const width, height = sidebarMinListWidth + sidebarWidth + dockedBorderCols, 24
	activity, content := sidebarWideLinesFixture(sidebarWidth, height)

	m := Update(NewModel(), SizeChangedMsg{Width: width, Height: height})
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{{Number: "1", Title: "still visible"}}})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: activity, Rendered: content, Raw: content})

	assertSidebarFitsHeightBudget(t, View(m), width, height) // activity

	m = Update(m, SidebarToggleMsg{})
	assertSidebarFitsHeightBudget(t, View(m), width, height) // transcript (rendered)

	m = Update(m, SidebarToggleMsg{})
	assertSidebarFitsHeightBudget(t, View(m), width, height) // transcript (raw)
}

// Issue #1752: a sidebar docked beside the list is the trigger for the compact/wrapped
// queue-row form.
func TestQueueNarrowed_SidebarDocked_ReportsTrue(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: sidebarMinListWidth + sidebarWidth + dockedBorderCols, Height: 24})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "hi"}}})

	if !queueNarrowed(m) {
		t.Errorf("queueNarrowed(m) = false, want true once the sidebar is docked beside the list")
	}
}

// Issue #1752 AC: with no sidebar open the list renders at full width, unchanged.
func TestQueueNarrowed_SidebarClosed_ReportsFalse(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: sidebarMinListWidth + sidebarWidth + dockedBorderCols, Height: 24})

	if queueNarrowed(m) {
		t.Errorf("queueNarrowed(m) = true, want false with no sidebar open")
	}
}

// Issue #1752: a fullscreen sidebar renders no list at all, so there is no queue column
// to narrow.
func TestQueueNarrowed_SidebarFullscreen_ReportsFalse(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: sidebarMinListWidth + sidebarWidth, Height: 24})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "hi"}}})

	if queueNarrowed(m) {
		t.Errorf("queueNarrowed(m) = true, want false while the sidebar is fullscreen, not docked")
	}
}

// Issue #1752: View forces the fullscreen takeover on zoom regardless of sidebarFits
// (issue #1502), hiding the list the same way the too-narrow case does, so queueNarrowed
// must never disagree.
func TestQueueNarrowed_SidebarZoomed_ReportsFalse(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: sidebarMinListWidth + sidebarWidth + dockedBorderCols, Height: 24})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "hi"}}})
	m = Update(m, SidebarZoomToggleMsg{})

	if queueNarrowed(m) {
		t.Errorf("queueNarrowed(m) = true, want false while the sidebar is zoomed to fullscreen")
	}
}

// Issue #1752: a column budget of 6 (1 header row plus 5 available) fits exactly 2
// entries, 2*compactRowLines plus 1 separator, with nothing left over.
func TestCompactColumnItemBudget_ExactFit_ReturnsWholeItems(t *testing.T) {
	if got := compactColumnItemBudget(6); got != 2 {
		t.Errorf("compactColumnItemBudget(6) = %d, want 2", got)
	}
}

// Issue #1752: a budget too small for one entry's header and title block returns zero,
// not a negative or fractional count.
func TestCompactColumnItemBudget_TooSmallForOneEntry_ReturnsZero(t *testing.T) {
	if got := compactColumnItemBudget(1); got != 0 {
		t.Errorf("compactColumnItemBudget(1) = %d, want 0", got)
	}
}

// Issue #1752: matches columnItemBudget's own guard for a terminal too short to show
// anything past the header.
func TestCompactColumnItemBudget_NonPositive_ReturnsZero(t *testing.T) {
	if got := compactColumnItemBudget(0); got != 0 {
		t.Errorf("compactColumnItemBudget(0) = %d, want 0", got)
	}
}

// Issue #1752: sectionPageSize has to pick up queueItemBudget's compact branch rather
// than reusing the classic one-line-per-item budget, so a page holds fewer entries once
// the sidebar docks and each entry spends more than one line.
func TestSectionPageSize_Compact_SmallerThanClassic(t *testing.T) {
	base := Update(NewModel(), SizeChangedMsg{Width: sidebarMinListWidth + sidebarWidth + dockedBorderCols, Height: 24})
	picks := make([]Pick, 20)
	for i := range picks {
		picks[i] = Pick{Number: fmt.Sprintf("%d", i+1), Title: "t", State: PickRunning, Age: "1m"}
	}
	base = Update(base, QueueSnapshotMsg{Picks: picks})
	base = Update(base, SectionJumpMsg{Section: SectionRunning})

	classic := sectionPageSize(base, resolveLayout(base))
	docked := Update(base, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "hi"}}})
	compact := sectionPageSize(docked, resolveLayout(docked))

	if compact >= classic {
		t.Errorf("sectionPageSize() docked = %d, classic = %d, want the docked/compact page size smaller — compact rows spend more than one line each", compact, classic)
	}
}

// Issue #1751: at the narrowest docking width there is no room to grow past the
// sidebarWidth floor without breaking the queue list's own sidebarMinListWidth floor.
func TestComputeSidebarWidth_MinimumFittingWidth_ReturnsFloor(t *testing.T) {
	got := computeSidebarWidth(sidebarMinListWidth + sidebarWidth + dockedBorderCols)
	if got != sidebarWidth {
		t.Errorf("computeSidebarWidth(%d) = %d, want the floor %d", sidebarMinListWidth+sidebarWidth+dockedBorderCols, got, sidebarWidth)
	}
}

// Issue #1751's worked example: at 160 columns the sidebar used to be pinned at 42
// while the queue absorbed the other ~117.
func TestComputeSidebarWidth_WideTerminal_TargetsFortyFivePercent(t *testing.T) {
	got := computeSidebarWidth(160)
	if want := 72; got != want {
		t.Errorf("computeSidebarWidth(160) = %d, want %d (45%% of 160)", got, want)
	}
}

// Issue #1751: the queue must never shrink below its floor even where there is room to
// dock at all, so a terminal too narrow for a full 45% share clamps the sidebar down.
func TestComputeSidebarWidth_ModeratelyWideTerminal_ClampsToQueueFloor(t *testing.T) {
	// 140 columns: 45% would be 63, but that only leaves the queue
	// 140-63-4 = 73, under its 80-column floor (the 4 is dockedBorderCols,
	// both panels' borders). The clamp caps the sidebar at 140-80-4 = 56 so
	// the queue holds exactly its floor.
	got := computeSidebarWidth(140)
	if want := 56; got != want {
		t.Errorf("computeSidebarWidth(140) = %d, want %d (clamped so the queue keeps its %d floor)", got, want, sidebarMinListWidth)
	}
}

// Issue #1751: the docked sidebar's content clips to computeSidebarWidth's wider column,
// not the old fixed 42-column floor. A long Activity line makes the clip boundary
// observable: it truncates to exactly the computed width.
func TestView_SidebarOpen_WideTerminal_SidebarGrowsPastFloor(t *testing.T) {
	const width = 160
	m := Update(NewModel(), SizeChangedMsg{Width: width, Height: 24})
	long := strings.Repeat("x", 300)
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: long}}})

	out := View(m)
	want := clip(long, computeSidebarWidth(width), false)
	if !strings.Contains(out, want) {
		t.Errorf("View() = %q, want the docked sidebar content clipped to computeSidebarWidth(%d) = %d, i.e. %q", out, width, computeSidebarWidth(width), want)
	}
}

// Issue #1752 AC, the activity stream retains the extra width granted by the rebalanced
// split: compact queue rows must not claw back what issue #1751 gave the docked sidebar.
func TestView_SidebarOpen_CompactQueueRows_SidebarKeepsComputedWidth(t *testing.T) {
	const width = 160
	m := Update(NewModel(), SizeChangedMsg{Width: width, Height: 24})
	m = Update(m, QueueSnapshotMsg{Picks: []Pick{
		{Number: "1", Title: "a compact row", State: PickRunning, Age: "1m"},
	}})
	m = Update(m, SectionJumpMsg{Section: SectionRunning})
	long := strings.Repeat("x", 300)
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: long}}})

	out := View(m)
	want := clip(long, computeSidebarWidth(width), false)
	if !strings.Contains(out, want) {
		t.Errorf("View() = %q, want the docked sidebar content still clipped to computeSidebarWidth(%d) = %d with compact queue rows present, i.e. %q", out, width, computeSidebarWidth(width), want)
	}
}

// Issue #1751: the docked queue's title column shrinks to make room for the wider
// sidebar rather than staying pinned at its old fixed-42-column width. A long Backlog
// title makes the clip boundary observable.
func TestView_SidebarOpen_WideTerminal_QueueNarrowsForWiderSidebar(t *testing.T) {
	const width = 160
	m := Update(NewModel(), SizeChangedMsg{Width: width, Height: 24})
	long := strings.Repeat("x", 300)
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{{Number: "1", Title: long}}})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "hi"}}})

	out := View(m)
	listWidth := width - computeSidebarWidth(width) - dockedBorderCols
	titleWidth := listWidth - backlogFixedWidth - extrasBudget
	want := clip(long, titleWidth, true)
	if !strings.Contains(out, want) {
		t.Errorf("View() = %q, want the docked queue's title column clipped to %d (listWidth %d narrowed for the wider sidebar), i.e. %q", out, titleWidth, listWidth, want)
	}
}

// Issue #1751: the rebalanced split must not leave a stale narrower list behind once the
// log closes.
func TestView_SidebarClose_WideTerminal_QueueRestoresFullWidth(t *testing.T) {
	const width = 160
	long := strings.Repeat("x", 300)
	m := Update(NewModel(), SizeChangedMsg{Width: width, Height: 24})
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{{Number: "1", Title: long}}})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "hi"}}})
	m = Update(m, SidebarCloseMsg{})

	out := View(m)
	fullTitleWidth := width - backlogFixedWidth - extrasBudget
	want := clip(long, fullTitleWidth, true)
	if !strings.Contains(out, want) {
		t.Errorf("View() = %q, want the queue's title column back at the full-width %d once the sidebar closes, i.e. %q", out, fullTitleWidth, want)
	}
}

// Issue #1751: the docked footer's hint text is hand-tuned to survive clipping at the
// 42-column floor, and must still render in full once computeSidebarWidth grows the
// sidebar past that floor.
func TestView_SidebarFooter_WideTerminal_RendersFullHintsUnclipped(t *testing.T) {
	const width = 160
	m := Update(NewModel(), SizeChangedMsg{Width: width, Height: 24})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "hi"}}})

	out := View(m)
	if !strings.Contains(out, "[t] cycle ·[h] list ·[x] close ·[z] ·H/L") {
		t.Errorf("View() = %q, want the docked footer's full, unclipped hint text at the wider computed sidebar width", out)
	}
}

// Issue #1791: dim (RoleDim, ANSI slot 8) through the shared footer renderer, without
// disturbing the tight "·" separators and footerHintCompact wording the 42-column
// docked budget needs.
func TestView_SidebarDocked_FooterStyledDim(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	m := Update(NewModel(), SizeChangedMsg{Width: sidebarMinListWidth + sidebarWidth + dockedBorderCols, Height: 24})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "hi"}}})

	out := View(m)
	if !strings.Contains(out, "\x1b[90m[t] cycle ·[h] list ·[x] close ·[z] ·H/L\x1b[0m") {
		t.Errorf("View() = %q, want the docked footer dim-styled with its compact wording/separators intact", out)
	}
}

// Issue #1846: H/L closes the log and switches Section, so it has to be discoverable in
// the docked footer too, even at the tight 42-column floor.
func TestView_SidebarDocked_FooterAdvertisesHL(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: sidebarMinListWidth + sidebarWidth + dockedBorderCols, Height: 24})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "hi"}}})

	out := View(m)
	if !strings.Contains(out, "[t] cycle ·[h] list ·[x] close ·[z] ·H/L") {
		t.Errorf("View() = %q, want the docked sidebar's footer to advertise H/L within the 42-column floor", out)
	}
}

// Issue #1791 review: no caller hands renderFooterHints a width narrower than its hints
// (the docked sidebar's 42-column floor fits its 40-column footer), so the clip branch
// needs exercising directly rather than being left uncovered.
func TestRenderFooterHints_NarrowWidth_ClipsWithEllipsis(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	got := renderFooterHints(ModeSidebar, []string{"t", "h", "x", "z"}, 10, true)
	if want := "\x1b[90m[t] cycle…\x1b[0m"; got != want {
		t.Errorf("renderFooterHints(...) = %q, want %q — clipped to 10 columns with a trailing ellipsis", got, want)
	}
}

// Issue #1502, ADR 0030: the label is the operator's only render-level signal for
// Follow state.
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

// Issue #1799: the docked sidebar's label rides in its panel's top border rule, the same
// move the header and detail modal make with their own titles.
func TestView_SidebarDocked_LabelFoldedIntoTopBorder(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := NewModel()
	m = Update(m, SizeChangedMsg{Width: sidebarMinListWidth + sidebarWidth + dockedBorderCols, Height: 12})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "hi"}}})

	out := View(m)
	if !strings.Contains(out, "+- activity #42 [follow]") {
		t.Errorf("View() = %q, want the sidebar label folded into its top border rule", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "|activity #42 [follow]") {
			t.Errorf("View() line %q, want the label gone from the interior content row", line)
		}
	}
}

// Issue #1799: the border fold covers every sidebarLabel mode, not just the Activity
// feed's default "[follow]" form.
func TestView_SidebarDocked_TranscriptRawLabelFoldedIntoTopBorder(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := NewModel()
	m = Update(m, SizeChangedMsg{Width: sidebarMinListWidth + sidebarWidth + dockedBorderCols, Height: 12})
	m = Update(m, SidebarLoadedMsg{Number: "42", Rendered: "hi"})
	m = Update(m, SidebarToggleMsg{}) // -> Transcript (rendered)
	m = Update(m, SidebarToggleMsg{}) // -> Transcript (raw)

	out := View(m)
	if !strings.Contains(out, "+- transcript #42 (raw)") {
		t.Errorf("View() = %q, want the transcript (raw) label folded into its top border rule", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "|transcript #42 (raw)") {
			t.Errorf("View() line %q, want the label gone from the interior content row", line)
		}
	}
}

// Issue #1799: the border title carries the focus signal the old interior label row did,
// accent when the sidebar is focused and dim otherwise.
func TestView_SidebarDocked_BorderTitleColoredByFocus(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	m := NewModel()
	m = Update(m, SizeChangedMsg{Width: sidebarMinListWidth + sidebarWidth + dockedBorderCols, Height: 12})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "hi"}}})

	wantAccent := roleStyle(RoleAccent).Render("activity #42 [follow]")
	if focused := View(m); !strings.Contains(focused, wantAccent) {
		t.Errorf("View() focused = %q, want the border title styled RoleAccent %q", focused, wantAccent)
	}

	m = Update(m, FocusListMsg{})
	wantDim := roleStyle(RoleDim).Render("activity #42 [follow]")
	if unfocused := View(m); !strings.Contains(unfocused, wantDim) {
		t.Errorf("View() unfocused = %q, want the border title styled RoleDim %q", unfocused, wantDim)
	}
}

// Issue #1502's "z" zoom key, shortened to "[z]" in the docked footer to make room for
// the "H/L" hint within the 42-column floor (issue #1846).
func TestView_SidebarFooter_ShowsZoomHint(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: sidebarMinListWidth + sidebarWidth + dockedBorderCols, Height: 24})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "hi"}}})
	if !strings.Contains(View(m), "[z]") {
		t.Errorf("docked View() = %q, want the zoom key hint", View(m))
	}

	m = Update(m, SidebarZoomToggleMsg{})
	if !strings.Contains(View(m), "[z] zoom") {
		t.Errorf("fullscreen View() = %q, want the zoom key hint", View(m))
	}
}

// Issue #1791: the fullscreen sidebar's footer gets the same RoleDim treatment through
// the shared footer renderer.
func TestView_SidebarFullscreen_FooterStyledDim(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	m := Update(NewModel(), SizeChangedMsg{Width: 100, Height: 24})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "hi"}}})
	m = Update(m, SidebarZoomToggleMsg{})

	out := View(m)
	if !strings.Contains(out, "[t] cycle activity/transcript") {
		t.Errorf("View() = %q, want the fullscreen footer hint text preserved", out)
	}
	footer := out[strings.LastIndex(out, "[t] cycle"):]
	if !strings.Contains(footer, "\x1b[") {
		t.Errorf("View() footer = %q, want it styled with an ANSI escape sequence", footer)
	}
}

// Issue #1502, ADR 0030: zoom is an operator choice independent of sidebarFits' own
// narrow-terminal fallback. Since issue #1845 zoomed no longer means a fullscreen
// takeover; at this size it renders the same floating log modal, dimming the list behind
// it rather than replacing it outright.
func TestView_SidebarZoom_WideTerminal_RendersModal(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	m := Update(NewModel(), SizeChangedMsg{Width: sidebarMinListWidth + sidebarWidth + dockedBorderCols, Height: 24})
	m = Update(m, SidebarLoadedMsg{Number: "42", Title: "fix the thing", Activity: []ActivityLine{{Text: "#42 · hi"}}})
	m = Update(m, SidebarZoomToggleMsg{})

	out := View(m)
	found := false
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "╭") && strings.Contains(line, "#42 fix the thing") {
			found = true
		}
	}
	if !found {
		t.Errorf("View() = %q, want the #number title set in the zoomed modal's top border line", out)
	}
	if !strings.Contains(out, "activity #42") {
		t.Errorf("View() = %q, want the modal's own activity label", out)
	}
}

// Issue #844, moved from the two-column body's "backlog" label to ADR 0030's
// single-Section table by issue #1500.
func TestView_BacklogSection_HasColumnHeader(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{{Number: "1", Title: "fix the thing"}}})

	out := View(m)
	var headerLine string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "title") && strings.Contains(l, "labels") {
			headerLine = l
			break
		}
	}
	if !strings.Contains(headerLine, "issue") {
		t.Errorf("header row = %q, want the number column labeled \"issue\"", headerLine)
	}
}

// Issue #1619: the flag is the operator's only signal that a running sandbox exists with
// no Dispatch this session launched to account for it.
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

// Issue #844, adapted to ADR 0030's single active Section by issue #1500: a labeled
// empty table, not one that appears only once something lands there.
func TestView_WorkSection_RendersEvenWhenEmpty(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	m = Update(m, SectionJumpMsg{Section: SectionRunning})

	out := View(m)
	var headerLine string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "state") && strings.Contains(l, "age") {
			headerLine = l
			break
		}
	}
	if !strings.Contains(headerLine, "issue") {
		t.Errorf("header row = %q, want the number column labeled \"issue\"", headerLine)
	}
	if !strings.Contains(out, "state") || !strings.Contains(out, "age") {
		t.Errorf("View() = %q, want the work Section's column-header row even with no picks", out)
	}
}

// Issue #844 AC3/AC4, moved from the two-column queue to Section-partitioned tables by
// ADR 0030 and issue #1500.
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

// Issue #1500 review: the age column was otherwise only proven present through its
// header word, never an actual value.
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

// Issue #1752: once a docked sidebar narrows the queue column, a work row renders as a
// "#num · state · age" header line plus an unclipped title line, instead of the classic
// single-line table's aggressive clip().
func TestView_WorkSection_Compact_ShowsTwoLineRowWithFullTitle(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: sidebarMinListWidth + sidebarWidth + dockedBorderCols, Height: 24})
	long := strings.Repeat("x", 60)
	m = Update(m, QueueSnapshotMsg{Picks: []Pick{
		{Number: "7", Title: long, State: PickRunning, Age: "3m"},
	}})
	m = Update(m, SectionJumpMsg{Section: SectionRunning})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "hi"}}})

	out := View(m)
	if !strings.Contains(out, long) {
		t.Errorf("View() = %q, want the full 60-char title unclipped in the compact form", out)
	}
	// Checked per line, not as one joined literal: the state cell styles by
	// Role (ADR 0031), so on a color-capable profile "running" sits between
	// ANSI escapes that would split a single "#7 · running · 3m" substring
	// check even though every piece renders on the same line.
	lines := strings.Split(out, "\n")
	headerIdx, titleIdx := -1, -1
	for i, l := range lines {
		if strings.Contains(l, "#7 ·") && strings.Contains(l, "running") && strings.Contains(l, "3m") {
			headerIdx = i
		}
		if strings.Contains(l, long) {
			titleIdx = i
		}
	}
	if headerIdx < 0 {
		t.Fatalf("View() = %q, want a line with \"#7 ·\", \"running\", and \"3m\"", out)
	}
	if titleIdx < 0 || titleIdx <= headerIdx {
		t.Fatalf("View() = %q, want the title on its own line after the header line", out)
	}
}

// Issue #1752: the compact header carries blocker, reason and heartbeat extras in raw
// Sprintf form, so it needs the same extrasBudget discipline the classic single-line row
// applies (issue #1500).
func TestRenderWorkSection_Compact_LongExtrasClippedToWidth(t *testing.T) {
	const listWidth = 80
	m := Update(NewModel(), SizeChangedMsg{Width: 300, Height: 24})
	m = Update(m, QueueSnapshotMsg{Picks: []Pick{
		{Number: "1", Title: "t", State: PickHeld, Age: "1m", BlockedBy: strings.Repeat("#99 (native), ", 20)},
	}})
	m = Update(m, SectionJumpMsg{Section: SectionHeld})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "hi"}}})
	m.Width = listWidth

	out := renderWorkSection(m, 10, true)
	for _, l := range strings.Split(out, "\n") {
		if w := lipgloss.Width(l); w > listWidth {
			t.Errorf("renderWorkSection() line %q has display width %d, want clamped to %d", l, w, listWidth)
		}
	}
}

// Issue #1752 review: the classic form clips Number and Age to their own column widths,
// so the compact header applies the same defensive cap rather than leaving them unbounded.
func TestRenderWorkSection_Compact_LongNumberAndAgeClippedToWidth(t *testing.T) {
	const listWidth = 80
	m := Update(NewModel(), SizeChangedMsg{Width: 300, Height: 24})
	m = Update(m, QueueSnapshotMsg{Picks: []Pick{
		{Number: strings.Repeat("9", 200), Title: "t", State: PickRunning, Age: strings.Repeat("m", 200)},
	}})
	m = Update(m, SectionJumpMsg{Section: SectionRunning})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "hi"}}})
	m.Width = listWidth

	out := renderWorkSection(m, 10, true)
	for _, l := range strings.Split(out, "\n") {
		if w := lipgloss.Width(l); w > listWidth {
			t.Errorf("renderWorkSection() line %q has display width %d, want clamped to %d", l, w, listWidth)
		}
	}
}

// Issue #1752 review: pins compact behaviour right at sidebarFits' minimum fitting width,
// the narrowest the queue column ever renders at while docked, with a realistic pick
// rather than a pathological one.
func TestView_SidebarOpen_AtMinimumFittingWidth_CompactRowRendersCleanly(t *testing.T) {
	const width = sidebarMinListWidth + sidebarWidth + dockedBorderCols
	m := Update(NewModel(), SizeChangedMsg{Width: width, Height: 24})
	m = Update(m, QueueSnapshotMsg{Picks: []Pick{
		{Number: "7", Title: "fix the thing", State: PickRunning, Age: "3m"},
	}})
	m = Update(m, SectionJumpMsg{Section: SectionRunning})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "hi"}}})

	out := View(m)
	for _, want := range []string{"#7", "running", "3m", "fix the thing"} {
		if !strings.Contains(out, want) {
			t.Errorf("View() = %q, want %q at the minimum fitting width", out, want)
		}
	}
	listWidth := width - computeSidebarWidth(width) - dockedBorderCols
	for _, l := range strings.Split(out, "\n") {
		if w := lipgloss.Width(l); w > width {
			t.Errorf("View() line %q has display width %d, want clamped to the terminal width %d (listWidth %d)", l, w, width, listWidth)
		}
	}
}

// Issue #1752: the width budget has to account for every literal character the
// "%s #%s [%s]\n" format spends outside the marker, number and labels.
func TestRenderBacklogSection_Compact_LongLabelsClippedToWidth(t *testing.T) {
	const listWidth = 80
	longLabels := make([]string, 20)
	for i := range longLabels {
		longLabels[i] = "a-fairly-long-label-name"
	}
	m := Update(NewModel(), SizeChangedMsg{Width: 300, Height: 24})
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{{Number: "1", Title: "t", Labels: longLabels}}})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "hi"}}})
	m.Width = listWidth

	out := renderBacklogSection(m, 10, true)
	for _, l := range strings.Split(out, "\n") {
		if w := lipgloss.Width(l); w > listWidth {
			t.Errorf("renderBacklogSection() line %q has display width %d, want clamped to %d", l, w, listWidth)
		}
	}
}

// Issue #1752 review: parity with compactWorkRow's own number clip and the classic row's
// numberColWidth clip.
func TestRenderBacklogSection_Compact_LongNumberClippedToWidth(t *testing.T) {
	const listWidth = 80
	m := Update(NewModel(), SizeChangedMsg{Width: 300, Height: 24})
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{{Number: strings.Repeat("9", 200), Title: "t"}}})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "hi"}}})
	m.Width = listWidth

	out := renderBacklogSection(m, 10, true)
	for _, l := range strings.Split(out, "\n") {
		if w := lipgloss.Width(l); w > listWidth {
			t.Errorf("renderBacklogSection() line %q has display width %d, want clamped to %d", l, w, listWidth)
		}
	}
}

// Issue #1752: a non-positive width must still render one glyph. An empty or negative
// strings.Repeat count panics.
func TestCompactQueueSeparator_ZeroWidth_ClampsToOne(t *testing.T) {
	got := strings.TrimSuffix(compactQueueSeparator(0), "\n")
	// Styled through the same roleStyle call, not a bare literal: on a
	// color-capable profile the glyph carries ANSI escapes a raw string
	// comparison would miss (issue #1752 review's ANSI-boundary lesson).
	if want := roleStyle(RoleDim).Render(compactQueueSeparatorGlyph); got != want {
		t.Errorf("compactQueueSeparator(0) = %q, want the single-glyph floor %q", got, want)
	}
}

// Issue #1752: compactWorkRow clamps its title column to at least one rather than
// panicking or emitting a pathological clip() call at a width too small for any column.
func TestCompactWorkRow_ZeroWidth_DoesNotPanic(t *testing.T) {
	got := compactWorkRow(0, ">", Pick{Number: "1", State: PickRunning, Age: "1m"}, "title", RoleRunning, "")
	if !strings.Contains(got, "\n") {
		t.Errorf("compactWorkRow(0, ...) = %q, want at least the header and title lines", got)
	}
}

// Issue #1752: the same clamp for compactBacklogRow.
func TestCompactBacklogRow_ZeroWidth_DoesNotPanic(t *testing.T) {
	got := compactBacklogRow(0, ">", "1", "title", nil)
	if !strings.Contains(got, "\n") {
		t.Errorf("compactBacklogRow(0, ...) = %q, want at least the header and title lines", got)
	}
}

// Issue #1752: the two-line stacked entries need exactly one faint rule between them so
// they do not run together.
func TestView_WorkSection_Compact_SeparatorBetweenAdjacentIssues(t *testing.T) {
	const width = sidebarMinListWidth + sidebarWidth + dockedBorderCols
	m := Update(NewModel(), SizeChangedMsg{Width: width, Height: 24})
	m = Update(m, QueueSnapshotMsg{Picks: []Pick{
		{Number: "1", Title: "first", State: PickRunning, Age: "1m"},
		{Number: "2", Title: "second", State: PickRunning, Age: "2m"},
	}})
	m = Update(m, SectionJumpMsg{Section: SectionRunning})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "hi"}}})

	out := View(m)
	listWidth := width - computeSidebarWidth(width) - dockedBorderCols
	// lipgloss.JoinHorizontal rejoins the docked sidebar onto the same line
	// as the separator, so its own trailing "\n" no longer directly follows
	// the rule in the joined output. Match the rule's content only.
	sep := strings.TrimSuffix(compactQueueSeparator(listWidth), "\n")
	if got := strings.Count(out, sep); got != 1 {
		t.Errorf("View() = %q, want exactly one separator %q between the two issues, got %d", out, sep, got)
	}
}

// Issue #1752 AC: selection and highlight keep working once the queue column narrows.
func TestView_WorkSection_Compact_CursorMarksHighlightedRow(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: sidebarMinListWidth + sidebarWidth + dockedBorderCols, Height: 24})
	m = Update(m, QueueSnapshotMsg{Picks: []Pick{
		{Number: "1", Title: "first", State: PickRunning, Age: "1m"},
		{Number: "2", Title: "second", State: PickRunning, Age: "2m"},
	}})
	m = Update(m, SectionJumpMsg{Section: SectionRunning})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "hi"}}})
	m = Update(m, CursorMoveMsg{Delta: 1})

	out := View(m)
	var marked, unmarked string
	for _, l := range strings.Split(out, "\n") {
		switch {
		case strings.Contains(l, "#1 ·"):
			unmarked = l
		case strings.Contains(l, "#2 ·"):
			marked = l
		}
	}
	if !strings.Contains(marked, ">") {
		t.Errorf("marked row = %q, want the cursor marker on #2's header line", marked)
	}
	if strings.Contains(unmarked, ">") {
		t.Errorf("unmarked row = %q, want no cursor marker on #1's header line", unmarked)
	}
}

// Issue #1752: the compact form's item budget has to account for its own multi-line,
// separator-bearing rows. Reusing the classic one-line-per-item assumption blows well
// past the column's row budget instead of windowing down to what fits.
func TestRenderWorkSection_Compact_ItemBudgetNeverOverflowsColumnBudget(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: sidebarMinListWidth + sidebarWidth + dockedBorderCols, Height: 24})
	picks := make([]Pick, 5)
	for i := range picks {
		picks[i] = Pick{Number: fmt.Sprintf("%d", i+1), Title: "t", State: PickRunning, Age: "1m"}
	}
	m = Update(m, QueueSnapshotMsg{Picks: picks})
	m = Update(m, SectionJumpMsg{Section: SectionRunning})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "hi"}}})

	const budget = 7
	out := renderWorkSection(m, budget, true)
	if lines := strings.Count(out, "\n"); lines > budget {
		t.Errorf("renderWorkSection(m, %d) = %q, rendered %d lines, want at most %d (no overflow of the column budget)", budget, out, lines, budget)
	}
}

// Issue #1752 AC, at full window width queue rows render unchanged from today: a work
// row must be the classic single-line clip()ped table, never the compact form.
func TestView_WorkSection_SidebarClosed_RendersClassicSingleLineForm(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: sidebarMinListWidth + sidebarWidth + dockedBorderCols, Height: 24})
	long := strings.Repeat("x", 300)
	m = Update(m, QueueSnapshotMsg{Picks: []Pick{
		{Number: "7", Title: long, State: PickRunning, Age: "3m"},
	}})
	m = Update(m, SectionJumpMsg{Section: SectionRunning})

	out := View(m)
	if strings.Contains(out, long) {
		t.Errorf("View() = %q, want the classic clip()ped title, not the full unclipped title the compact form would show", out)
	}
	if strings.Contains(out, "#7 · running · 3m") {
		t.Errorf("View() = %q, want no compact-form header line with no sidebar open", out)
	}
}

// Issue #1752 AC: the Backlog counterpart to
// TestView_WorkSection_SidebarClosed_RendersClassicSingleLineForm.
func TestView_BacklogSection_SidebarClosed_RendersClassicSingleLineForm(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: sidebarMinListWidth + sidebarWidth + dockedBorderCols, Height: 24})
	long := strings.Repeat("x", 300)
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{{Number: "1", Title: long}}})

	out := View(m)
	if strings.Contains(out, long) {
		t.Errorf("View() = %q, want the classic clip()ped title, not the full unclipped title the compact form would show", out)
	}
}

// Issue #1752: the Backlog row's compact form, a "#num" header line plus the title
// unclipped on its own line, once a docked sidebar narrows the queue column.
func TestView_BacklogSection_Compact_ShowsTwoLineRowWithFullTitle(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: sidebarMinListWidth + sidebarWidth + dockedBorderCols, Height: 24})
	long := strings.Repeat("x", 60)
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{{Number: "1", Title: long}}})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "hi"}}})

	out := View(m)
	if !strings.Contains(out, long) {
		t.Errorf("View() = %q, want the full 60-char title unclipped in the compact form", out)
	}
	idx := strings.Index(out, "#1")
	titleIdx := strings.Index(out, long)
	if idx < 0 || titleIdx < 0 || titleIdx <= idx {
		t.Fatalf("View() = %q, want the header line (with #1) before the title", out)
	}
	between := out[idx:titleIdx]
	if !strings.Contains(between, "\n") {
		t.Errorf("View() header-to-title span = %q, want the title on its own line, not joined to the header", between)
	}
}

// Issue #1752: the Backlog Section's compact form also separates adjacent issues with
// exactly one faint rule.
func TestView_BacklogSection_Compact_SeparatorBetweenAdjacentIssues(t *testing.T) {
	const width = sidebarMinListWidth + sidebarWidth + dockedBorderCols
	m := Update(NewModel(), SizeChangedMsg{Width: width, Height: 24})
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{
		{Number: "1", Title: "first"},
		{Number: "2", Title: "second"},
	}})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "hi"}}})

	out := View(m)
	listWidth := width - computeSidebarWidth(width) - dockedBorderCols
	sep := strings.TrimSuffix(compactQueueSeparator(listWidth), "\n")
	if got := strings.Count(out, sep); got != 1 {
		t.Errorf("View() = %q, want exactly one separator %q between the two issues, got %d", out, sep, got)
	}
}

// Issue #1710: the console needs a way to tell an operator at a glance which queued or
// in-flight picks are research-only rather than real work, driven off Pick.Kind.
func TestView_ResearchPick_ShowsMarker(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	m = Update(m, QueueSnapshotMsg{Picks: []Pick{
		{Number: "1", Title: "research one", Kind: KindResearch, State: PickQueued},
	}})
	m = Update(m, SectionJumpMsg{Section: SectionRunning})

	out := View(m)
	if !strings.Contains(out, researchMarker) {
		t.Errorf("View() = %q, want the research pick's row to carry %q", out, researchMarker)
	}
}

// Issue #1710: the marker tags research picks only, leaving a work pick's row unchanged.
func TestView_WorkPick_HasNoResearchMarker(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	m = Update(m, QueueSnapshotMsg{Picks: []Pick{
		{Number: "1", Title: "work one", Kind: KindWork, State: PickQueued},
	}})
	m = Update(m, SectionJumpMsg{Section: SectionRunning})

	out := View(m)
	if strings.Contains(out, researchMarker) {
		t.Errorf("View() = %q, want no research marker on a work pick's row", out)
	}
}

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

// Issue #755: a held pick whose Reason merely restates the blocker BlockedBy already
// names used to name the same blocker twice on one row.
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

// The row's fixed number, title, state and age columns clip the title in place, so the
// trailing blocker annotation (issue #858) is never pushed off by a long title the way
// an unbounded natural-order row once could be.
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

// Issue #1540: the render pipeline's budget is always a real, finite figure. Viewport's
// own height==0 covers "unbounded" for callers who want that, exercised directly in
// viewport_test.go.
func TestRenderBacklogSection_BudgetExceedsRowCount_NeverTruncates(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	issues := make([]forge.Issue, 500)
	for i := range issues {
		issues[i] = forge.Issue{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("issue %d", i)}
	}
	m = Update(m, IssuesLoadedMsg{Issues: issues})

	out := renderBacklogSection(m, len(issues)+1, false)
	if !strings.Contains(out, "issue 499") {
		t.Errorf("renderBacklogSection(m, 501) = %q, want the last of 500 rows present, unwindowed", out)
	}
	if strings.Contains(out, "more below") {
		t.Errorf("renderBacklogSection(m, 501) = %q, want no truncation affordance", out)
	}
}

// TestRenderBacklogSection_BudgetExceedsRowCount_NeverTruncates mirrored for a work
// Section (issue #1540).
func TestRenderWorkSection_BudgetExceedsRowCount_NeverTruncates(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	picks := make([]Pick, 500)
	for i := range picks {
		picks[i] = Pick{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("pick %d", i), State: PickQueued}
	}
	m = Update(m, QueueSnapshotMsg{Picks: picks})
	m = Update(m, SectionJumpMsg{Section: SectionRunning})

	out := renderWorkSection(m, len(picks)+1, false)
	if !strings.Contains(out, "pick 499") {
		t.Errorf("renderWorkSection(m, 501) = %q, want the last of 500 rows present, unwindowed", out)
	}
	if strings.Contains(out, "more below") {
		t.Errorf("renderWorkSection(m, 501) = %q, want no truncation affordance", out)
	}
}

// ADR 0030/0031, issue #1500: the tabs are the operator's cue for which Section H/L and
// 1-5 currently target.
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

// Issue #845, generalized from FocusedColumn to ActiveSection by issue #1500.
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

// Issue #1534: renderDrillIn, its predecessor, wrote the header line and then always
// appended the footer with no height check, so at Height 1 it overflowed to 2 lines.
// Mirrors the docked and floating tiny-budget fix from issue #1380.
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

// Issue #1534: Height 2 is the boundary where label plus footer exactly fills the budget,
// one above the Height 1 case that drops the footer.
func TestView_SidebarFullscreen_RetainsFooterAtBoundary(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 100, Height: 2})
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

// Issue #1846: H/L closing the log and switching Section was a silent no-op here, so the
// fullscreen footer advertises it the way the list's own hint already does.
func TestView_SidebarFullscreen_FooterAdvertisesHL(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 100, Height: 24})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "hi"}}})

	out := View(m)
	if !strings.Contains(out, "H/L") {
		t.Errorf("View() = %q, want the fullscreen sidebar's footer to advertise H/L", out)
	}
}

// Issue #1534: label plus error is two lines, same as label plus footer, so it has to be
// dropped at Height 1 the same way the footer is.
func TestView_SidebarFullscreen_ErrRespectsTinyBudget(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Height: 1})
	m = Update(m, SidebarLoadedMsg{Number: "42", Err: errBoom})

	out := View(m)
	got := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(got) > m.Height {
		t.Errorf("View() rendered %d lines, want at most Height (%d) — the error line must be budgeted against height same as the footer", len(got), m.Height)
	}
}

// Issue #1035 AC1/AC2: the header must never scroll off the top when the backlog has
// more rows than the terminal has height for.
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

// Issue #1035 AC4: the operator has to know the list is incomplete rather than reading a
// truncated backlog as the whole one.
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

// Issue #1035 AC1/AC2/AC4: the picks column is height-budgeted the same way the backlog
// column is, and gets its own truncation affordance.
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

// Issue #1794: the banner and footer both have to survive at full budget. The existing
// height tests trimmed the trailing newline before counting, which is exactly why this
// regression slipped through.
func TestView_LongBacklog_FitsHeightWithBannerAndFooterPinned(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 10})
	issues := make([]forge.Issue, 50)
	for i := range issues {
		issues[i] = forge.Issue{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("issue %d", i)}
	}
	m = Update(m, IssuesLoadedMsg{Issues: issues})

	out := View(m)
	if !strings.Contains(out, "more below") {
		t.Fatalf("View() = %q, want a \"more below\" affordance line", out)
	}
	// Split, not TrimRight-then-count: View() always ends in exactly one
	// trailing "\n", which costs the terminal a physical row of its own.
	// Printing it at the bottom of an already-full m.Height budget is what
	// scrolls the pinned top banner off-screen, and trimming first throws away
	// exactly the row this regression turns on.
	if got := len(strings.Split(out, "\n")); got > m.Height {
		t.Errorf("View() rendered %d physical lines, want <= m.Height (%d): %q", got, m.Height, out)
	}
	if !strings.Contains(out, "running 0/0") {
		t.Errorf("View() = %q, want the top banner status line present", out)
	}
	if !strings.Contains(out, "[/] filter") {
		t.Errorf("View() = %q, want the bottom footer hint line present", out)
	}
}

// Issue #1825: a Backlog whose total lands exactly on itemBudget was left unreserved by
// #1794's fix, whose condition (total > itemBudget) only fires once the total spills past
// the budget. Reserving the trailing-"\n" row turns that invisible exact-fit into a
// correctly labeled "… N more below" instead of a silent overrun of Height.
func TestView_ExactFitBacklog_FitsHeightWithBannerAndFooterPinned(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 10})
	issues := make([]forge.Issue, 4)
	for i := range issues {
		issues[i] = forge.Issue{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("issue %d", i)}
	}
	m = Update(m, IssuesLoadedMsg{Issues: issues})

	out := View(m)
	if got := len(strings.Split(out, "\n")); got > m.Height {
		t.Errorf("View() rendered %d physical lines, want <= m.Height (%d): %q", got, m.Height, out)
	}
	if !strings.Contains(out, "running 0/0") {
		t.Errorf("View() = %q, want the top banner status line present", out)
	}
	if !strings.Contains(out, "[/] filter") {
		t.Errorf("View() = %q, want the bottom footer hint line present", out)
	}
}

// Issue #1825: on a terminal too short to show any item row the top banner still
// survives, and the output never exceeds Height once View()'s own guaranteed trailing
// "\n" row is counted. The footer joins once Height leaves room for it.
func TestView_VeryShortTerminal_FitsHeightWithBannerPinned(t *testing.T) {
	issues := make([]forge.Issue, 20)
	for i := range issues {
		issues[i] = forge.Issue{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("issue %d", i)}
	}

	// wantFooter only turns true at height 6: below that the header, and the
	// Section tabs line once there is room, already consume the whole budget.
	// The footer's absence there is this range's collapse order, not a bug.
	for _, tc := range []struct {
		height     int
		wantFooter bool
	}{
		{height: 2, wantFooter: false},
		{height: 3, wantFooter: false},
		{height: 4, wantFooter: false},
		{height: 5, wantFooter: false},
		{height: 6, wantFooter: true},
	} {
		m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: tc.height})
		m = Update(m, IssuesLoadedMsg{Issues: issues})

		out := View(m)
		if got := len(strings.Split(out, "\n")); got > m.Height {
			t.Errorf("height=%d: View() rendered %d physical lines, want <= m.Height (%d): %q", tc.height, got, m.Height, out)
		}
		if !strings.Contains(out, "running 0/0") {
			t.Errorf("height=%d: View() = %q, want the top banner status line present", tc.height, out)
		}
		if got := strings.Contains(out, "[/] filter"); got != tc.wantFooter {
			t.Errorf("height=%d: View() footer present = %v, want %v: %q", tc.height, got, tc.wantFooter, out)
		}
	}
}

// Issue #1036 AC3: every row in the filtered backlog is reachable by scrolling, not just
// the leading window.
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

// Issue #1036 AC3 covers every Section, generalized from Tab-toggled focus to
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

// Issue #1037 AC3: the operator can see where they are in a long backlog without
// counting rows.
func TestView_BacklogSection_ShowsPositionIndicator(t *testing.T) {
	// Height 10 plus boxBorderRows pays for the header's own bordered panel
	// (issue #1756); the header itself costs only 3 rows, since the wordmark
	// folds into its top border rule (issue #1798). listFooterLines pays for
	// ModeList's pinned footer row (issue #1792), and viewBody holds one more
	// row back for View()'s trailing "\n" (issue #1825), so the ranges stay 1-5 and 6-10.
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 10 + boxBorderRows + listFooterLines})
	issues := make([]forge.Issue, 50)
	for i := range issues {
		issues[i] = forge.Issue{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("issue %d", i)}
	}
	m = Update(m, IssuesLoadedMsg{Issues: issues})

	out := View(m)
	if !strings.Contains(out, "(1-5 of 50)") {
		t.Errorf("View() = %q, want the Backlog header to show \"(1-5 of 50)\"", out)
	}

	m = Update(m, ScrollMsg{Delta: 5})
	out = View(m)
	if !strings.Contains(out, "(6-10 of 50)") {
		t.Errorf("View() = %q, want the Backlog header to show \"(6-10 of 50)\" after scrolling", out)
	}
}

// Issue #1037 AC3/AC4: the same indicator as the Backlog Section, and absent on an empty
// Section rather than reading "(1-0 of 0)".
func TestView_WorkSection_ShowsPositionIndicator(t *testing.T) {
	// Height 10 plus boxBorderRows pays for the header's own bordered panel
	// (issue #1756); the header itself costs only 3 rows, since the wordmark
	// folds into its top border rule (issue #1798). listFooterLines pays for
	// ModeList's pinned footer row (issue #1792), and viewBody holds one more
	// row back for View()'s trailing "\n" (issue #1825), so the range stays (1-5 of 50).
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 10 + boxBorderRows + listFooterLines})
	picks := make([]Pick, 50)
	for i := range picks {
		picks[i] = Pick{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("pick %d", i), State: PickQueued}
	}
	m = Update(m, QueueSnapshotMsg{Picks: picks})
	m = Update(m, SectionJumpMsg{Section: SectionRunning})

	out := View(m)
	if !strings.Contains(out, "(1-5 of 50)") {
		t.Errorf("View() = %q, want the Running header to show \"(1-5 of 50)\"", out)
	}

	empty := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 10})
	empty = Update(empty, SectionJumpMsg{Section: SectionRunning})
	out = View(empty)
	if strings.Contains(out, " of 0)") {
		t.Errorf("View() = %q, want no position indicator for an empty Section", out)
	}
}

// Issue #1035 AC1/AC2 review finding: the trailing "refresh failed" line is budgeted the
// same way prompt lines are, so a long backlog plus a refresh error cannot together push
// the header off the top.
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

// Issue #1035 AC1/AC2 review finding: a column's label line is part of the body budget
// too, not an unconditional floor on top of it.
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

// Issue #1035 AC3: the body's row budget shrinks as alert lines are added to the header,
// instead of a stale hardcoded header-height assumption.
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

// Issue #1035 AC3, extended to the titled border by issue #1798: the unboxed header's
// smaller height has to drive the budget. At Height 2 renderBoxedHeader falls back to
// unboxed (its minimum boxed render is 3 rows), and sectionTabsReserved collapses the
// tabs line too, since showing it would land right on Height with no row left for
// View()'s own guaranteed trailing "\n" (issue #1825).
func TestView_HeaderHeight_TooShortToBox_StillBudgetsBody(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 2})
	issues := make([]forge.Issue, 20)
	for i := range issues {
		issues[i] = forge.Issue{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("issue %d", i)}
	}
	m = Update(m, IssuesLoadedMsg{Issues: issues})

	out := View(m)
	if strings.Contains(out, "spindrift") {
		t.Errorf("View() = %q, want the titled header collapsed unboxed on a too-short terminal", out)
	}
	if !strings.Contains(out, "running 0/0") {
		t.Errorf("View() = %q, want the status line present", out)
	}
	if strings.Contains(out, "issue 0") {
		t.Errorf("View() = %q, want the backlog fully clipped — no room left after the header/tabs rows", out)
	}
	if strings.Contains(out, "[1] Backlog") {
		t.Errorf("View() = %q, want the Section tabs line also collapsed — no room left after the header", out)
	}
	if got := len(strings.Split(out, "\n")); got > m.Height {
		t.Errorf("View() rendered %d physical lines, want <= Height (%d)", got, m.Height)
	}
}

// Issue #859: a CJK string can sit well under a rune-count budget while its display
// width, 2 columns per wide rune, already overflows the terminal.
func TestClip_WideCharacters_MeasuresDisplayWidthNotRuneCount(t *testing.T) {
	s := "中文标题超长测试文字" // 10 runes, 20 display columns
	got := clip(s, 10, false)
	if got == s {
		t.Errorf("clip(%q, 10, false) = %q, want truncated — 10 runes is 20 display columns, over the width-10 budget", s, got)
	}
	if w := runewidth.StringWidth(got); w != 10 {
		t.Errorf("clip(%q, 10, false) = %q with display width %d, want exactly 10", s, got, w)
	}
}

// Issue #1785: clip with pad true is the shape the fixed-width table columns use, so it
// has to land on exactly the requested width even when a wide rune straddles the
// truncation boundary, or a column drifts by a space.
func TestClip_Pad_WideCharacterStraddlesBoundary_LandsExactlyOnWidth(t *testing.T) {
	got := clip("ab中文", 4, true)
	if w := runewidth.StringWidth(got); w != 4 {
		t.Errorf("clip(%q, 4, true) = %q with display width %d, want exactly 4", "ab中文", got, w)
	}
}

// Issue #1779: padDisplay marks a truncated overflow with a trailing ellipsis, mirroring
// clip, instead of silently dropping the cut content.
func TestPadDisplay_TruncatesWithEllipsis(t *testing.T) {
	got := padDisplay("supercalifragilisticexpialidocious", 10)
	if !strings.HasSuffix(got, "…") {
		t.Errorf("padDisplay(...) = %q, want a trailing ellipsis marking the cut", got)
	}
	if w := runewidth.StringWidth(got); w != 10 {
		t.Errorf("padDisplay(...) = %q with display width %d, want exactly 10", got, w)
	}
}

// Issue #1785: otherwise the detail modal box's right border drifts out of column
// (issue #1758).
func TestPadDisplay_WideCharacterStraddlesBoundary_LandsExactlyOnWidth(t *testing.T) {
	s := "中文标题超长测试文字" // 10 runes, 20 display columns
	got := padDisplay(s, 10)
	if w := runewidth.StringWidth(got); w != 10 {
		t.Errorf("padDisplay(%q, 10) = %q with display width %d, want exactly 10", s, got, w)
	}
}

// The width<=1 edge case clip guards against too: too narrow to fit even one cut
// character plus the ellipsis itself.
func TestPadDisplay_WidthOne_TruncatesWithoutEllipsis(t *testing.T) {
	got := padDisplay("overflow", 1)
	if w := runewidth.StringWidth(got); w != 1 {
		t.Errorf("padDisplay(...) = %q with display width %d, want exactly 1", got, w)
	}
	if strings.Contains(got, "…") {
		t.Errorf("padDisplay(...) = %q, want no ellipsis when width is too narrow to fit one", got)
	}
}

// Issue #1779: an over-wide label is marked with a trailing ellipsis rather than silently
// cut. A single unbroken label wider than the interior stands alone on its wrapText line
// (issue #1772), then hits the label row's clip-before-style truncation (issue #1832);
// the leading "[" is part of that same word, so the cut lands one column earlier than
// the label text alone would. TERM is forced color-capable so the styled path runs.
func TestView_DetailModal_OverWideLabel_ShowsEllipsis(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	label := "an-extremely-long-unbroken-label-token-that-overflows-the-box"
	m := Update(NewModel(), SizeChangedMsg{Width: 40, Height: 24})
	m = Update(m, DetailModalOpenMsg{Number: "42", Title: "fix the thing", Labels: []string{label}})

	out := View(m)
	innerWidth, _ := detailModalInnerSize(40, 24)
	want := "[" + label[:innerWidth-2] + "…"
	if !strings.Contains(out, want) {
		t.Errorf("View() = %q, want %q marking the over-wide label's cut", out, want)
	}
	if strings.Contains(out, label) {
		t.Errorf("View() = %q, want the over-wide label truncated, not shown in full", out)
	}
}

// Issue #1785: every "│...│" row has to measure exactly innerWidth display columns
// between its borders or the right border drifts, breaking issue #1758's invariant. TERM
// is forced color-capable so the label row's RoleDim styling (issue #1832) is in play and
// ansi.StringWidth has escape bytes to exclude.
func TestView_DetailModal_WideCharacterLabel_BorderStaysAligned(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	label := "中文标题超长测试文字标签内容溢出方框边界"
	m := Update(NewModel(), SizeChangedMsg{Width: 40, Height: 24})
	m = Update(m, DetailModalOpenMsg{Number: "42", Title: "fix the thing", Labels: []string{label}})

	out := View(m)
	innerWidth, _ := detailModalInnerSize(40, 24)
	for _, line := range strings.Split(out, "\n") {
		first := strings.IndexRune(line, '│')
		if first < 0 {
			continue
		}
		last := strings.LastIndex(line, "│")
		if first == last {
			continue
		}
		content := line[first+len("│") : last]
		if w := ansi.StringWidth(content); w != innerWidth {
			t.Errorf("row %q content width %d, want exactly innerWidth %d — right border drifted", line, w, innerWidth)
		}
	}
}

// The detail modal body's own word wrap (issue #1632). No markdown renderer in the
// dependency tree, so this is hand-rolled rather than glamour.
func TestWrapText_GreedilyFillsLinesToWidth(t *testing.T) {
	got := wrapText("the quick brown fox jumps", 10)
	want := []string{"the quick", "brown fox", "jumps"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("wrapText(...) = %v, want %v", got, want)
	}
}

func TestWrapText_PreservesBlankLines(t *testing.T) {
	got := wrapText("first paragraph\n\nsecond paragraph", 40)
	want := []string{"first paragraph", "", "second paragraph"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("wrapText(...) = %v, want %v", got, want)
	}
}

func TestWrapText_WordWiderThanWidth_StandsAlone(t *testing.T) {
	got := wrapText("a supercalifragilisticexpialidocious word", 10)
	want := []string{"a", "supercalifragilisticexpialidocious", "word"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("wrapText(...) = %v, want %v", got, want)
	}
}

// Issue #1772: the labels line wraps across further interior rows once it overflows
// width, instead of staying one unwrapped string for padDisplay to truncate. Issue #1832
// makes the wrapped block read as labels: bracketed and dim-styled, with the bracket
// characters counted toward the width budget rather than added on top of it.
func TestDetailModalLabelLines_WrapsOntoMultipleLines(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	got := detailModalLabelLines([]string{"alpha", "bravo", "charlie"}, 15)
	want := []string{"\x1b[90m[alpha, bravo,\x1b[0m", "\x1b[90mcharlie]\x1b[0m"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("detailModalLabelLines(...) = %v, want %v", got, want)
	}
}

// A tracker label is untrusted input (issue #862), so it is stripped before wrapping, and
// the bracketed dim-styled treatment (issue #1832) still applies once sanitized.
func TestDetailModalLabelLines_SanitizesControlSequences(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	got := detailModalLabelLines([]string{"evil\x1b[2Jlabel"}, 40)
	want := []string{"\x1b[90m[evillabel]\x1b[0m"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("detailModalLabelLines(...) = %v, want %v", got, want)
	}
}

// Issue #3019: plainText has to be a faithful unstyled twin of styledText so
// detailModalScrollBudget can predict the wrapped line count without paying for a real
// lipgloss render. The corpus crosses the shapes that also drive the capped fold (no
// labels, one short label, many labels wrapping several rows, one label wider than width,
// wide CJK runes) under both NO_COLOR and a color-capable TERM.
func TestDetailModalLabelLinesWith_PlainText_MatchesStyledStripped(t *testing.T) {
	corpus := [][]string{
		nil,
		{"bug"},
		{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf"},
		{"a-single-label-far-wider-than-any-width-tested-here"},
		{"日本語", "ラベル", "テスト"},
	}
	widths := []int{1, 8, 15, 40}
	envs := []struct {
		name    string
		noColor string
		term    string
	}{
		{"NoColor", "1", "xterm-256color"},
		{"Colored", "", "xterm-256color"},
	}

	for _, env := range envs {
		t.Run(env.name, func(t *testing.T) {
			t.Setenv("NO_COLOR", env.noColor)
			t.Setenv("TERM", env.term)

			for _, labels := range corpus {
				for _, width := range widths {
					styled := detailModalLabelLines(labels, width)
					plain := detailModalLabelLinesWith(labels, width, plainText)

					if len(styled) != len(plain) {
						t.Fatalf("labels=%v width=%d: detailModalLabelLines returned %d lines, detailModalLabelLinesWith(..., plainText) returned %d",
							labels, width, len(styled), len(plain))
					}
					stripped := make([]string, len(styled))
					for i, l := range styled {
						stripped[i] = ansi.Strip(l)
					}
					if !reflect.DeepEqual(plain, stripped) {
						t.Errorf("labels=%v width=%d: detailModalLabelLinesWith(..., plainText) = %v, want ansi.Strip(detailModalLabelLines(...)) = %v",
							labels, width, plain, stripped)
					}
				}
			}
		})
	}
}

// Issue #3019: the capped variant's own trial call has to thread the same style through,
// or the plain and styled forms fold into the "+N more labels" indicator at different
// label counts.
func TestDetailModalLabelLinesCappedWith_PlainText_MatchesStyledLineCount(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	labels := []string{"alpha", "bravo", "charlie", "delta", "echo"}
	const width = 24
	const maxLines = 1

	styled := detailModalLabelLinesCapped(labels, width, maxLines)
	plain := detailModalLabelLinesCappedWith(labels, width, maxLines, plainText)

	if len(styled) != len(plain) {
		t.Errorf("detailModalLabelLinesCapped(...) returned %d lines (%v), detailModalLabelLinesCappedWith(..., plainText) returned %d lines (%v), want equal",
			len(styled), styled, len(plain), plain)
	}
}

// Issue #862: a tracker title is untrusted input, and Bubble Tea does not filter
// arbitrary control sequences before writing to the operator's terminal.
func TestView_Backlog_SanitizesTitleAndLabelControlSequences(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{
		{Number: "1", Title: "evil\x1b[2Jtitle\x1b]0;pwned\x07here", Labels: []string{"evil\x1b[2Jlabel"}},
	}})

	out := View(m)
	// The header carries legitimate styling escapes of its own (ADR 0031), so
	// the check below scopes "no raw ESC byte" to the row rendering the
	// untrusted title and label. Anything past the sanitizer trust boundary in
	// that row is still caught, and styled header lines are not a false positive.
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

// Issue #862: a pick's Title and Reason are both tracker- and dispatch-derived free text.
func TestView_Queue_SanitizesTitleAndReasonControlSequences(t *testing.T) {
	// A work-Section row's state cell carries its own legitimate role styling
	// (ADR 0031) on the same line as the sanitized title and reason, unlike the
	// Backlog row. NO_COLOR keeps that styling from ever emitting an escape
	// byte, so the check below stays a test of sanitization alone.
	t.Setenv("NO_COLOR", "1")

	m := Update(NewModel(), SizeChangedMsg{Width: 300, Height: 24})
	m = Update(m, QueueSnapshotMsg{Picks: []Pick{
		{Number: "42", Title: "evil\x1b[2Jtitle", State: PickHeld, Reason: "bad\x1b]0;pwned\x07reason"},
	}})
	m = Update(m, SectionJumpMsg{Section: SectionHeld})

	out := View(m)
	// The header carries legitimate styling escapes of its own (ADR 0031), so
	// the check below scopes "no raw ESC byte" to the row rendering the
	// untrusted title and reason. Anything past the sanitizer trust boundary
	// in that row is still caught.
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

// Issue #1040, ADR 0030: the guard lives in the shared helper renderBacklogSection and
// renderWorkSection both use. Viewport is never asked to represent a non-positive item
// budget itself (issue #1540: SetHeight(0) means unbounded, not zero rows).
func TestRenderTable_NonPositiveItemBudgetRendersHeaderOnly(t *testing.T) {
	got := renderTable("header\n", []string{"row1\n"}, Viewport{}, 1, 0, "")
	if want := "header\n"; got != want {
		t.Errorf("renderTable() with itemBudget 0 = %q, want %q", got, want)
	}
}

// Matches the convention renderBacklogSection and renderWorkSection applied inline
// before the helper was extracted.
func TestRenderTable_RendersHeaderAndWindowedRows(t *testing.T) {
	got := renderTable("header\n", []string{"row1\n", "row2\n"}, Viewport{}, 2, 2, "")
	want := "header\nrow1\nrow2\n"
	if got != want {
		t.Errorf("renderTable() = %q, want %q", got, want)
	}
}

// renderTable windows through vp's own offset rather than always starting at row 0, so
// the Section's scroll position both callers pass through actually takes effect.
func TestRenderTable_PassesOffsetThroughToWindow(t *testing.T) {
	vp := Viewport{offset: 1}
	got := renderTable("header\n", []string{"row1\n", "row2\n", "row3\n"}, vp, 3, 2, "")
	want := "header\nrow2\nrow3\n"
	if got != want {
		t.Errorf("renderTable() with offset 1 = %q, want %q", got, want)
	}
}

// Issue #1061: the "… N more below" affordance itself has to fit within itemBudget, so a
// budget too small for every row holds one row back rather than overflowing by a line.
func TestRenderTable_TruncatedWindow_HoldsBackOneRowForMoreBelow(t *testing.T) {
	rows := make([]string, 50)
	for i := range rows {
		rows[i] = fmt.Sprintf("row%d\n", i)
	}
	got := renderTable("header\n", rows, Viewport{}, 50, 4, "")
	want := "header\nrow0\nrow1\nrow2\n… 47 more below\n"
	if got != want {
		t.Errorf("renderTable() = %q, want %q", got, want)
	}
}

// Issue #1797: a titled panel's top border reads "╭─ <title> ─…─╮", the title folded
// into the rule itself rather than sitting on an interior content row.
func TestRenderBoxedColumn_TitleFoldedIntoTopBorder(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	got := renderBoxedColumn("hello", 20, "my title", RoleDim)
	top := strings.SplitN(got, "\n", 2)[0]
	if !strings.Contains(top, "╭─ ") {
		t.Errorf("renderBoxedColumn(...) top border = %q, want it to contain the lead-in %q", top, "╭─ ")
	}
	if !strings.Contains(top, "my title") {
		t.Errorf("renderBoxedColumn(...) top border = %q, want it to contain the title %q", top, "my title")
	}
	if !strings.Contains(top, "╮") {
		t.Errorf("renderBoxedColumn(...) top border = %q, want a top-right corner", top)
	}
	if w := ansi.StringWidth(top); w != 22 {
		t.Errorf("renderBoxedColumn(...) top border width = %d, want 22 (width 20 + 2 border columns)", w)
	}
}

// Issue #1797: the detail modal's old hand-rolled Unicode-only top border never degraded
// at all, unlike every other border in the package.
func TestRenderBoxedColumn_Titled_NoColor_DegradesToAscii(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	got := renderBoxedColumn("hello", 20, "my title", RoleDim)
	top := strings.SplitN(got, "\n", 2)[0]
	if strings.Contains(top, "╭") {
		t.Errorf("renderBoxedColumn(...) top border = %q, want no rounded border glyph under NO_COLOR", top)
	}
	if !strings.Contains(top, "+- ") {
		t.Errorf("renderBoxedColumn(...) top border = %q, want the ASCII lead-in %q", top, "+- ")
	}
	if !strings.Contains(top, "my title") {
		t.Errorf("renderBoxedColumn(...) top border = %q, want it to contain the title %q", top, "my title")
	}
	if strings.Contains(got, "\x1b[") {
		t.Errorf("renderBoxedColumn(...) = %q, want no escape sequences at all under NO_COLOR", got)
	}
}

// Issue #1797 AC: the top rule lands on exactly the panel's width however long the title
// is, and the cut is marked with a trailing ellipsis.
func TestRenderBoxedColumn_TitleWiderThanPanel_TruncatesWithEllipsis(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	got := renderBoxedColumn("hello", 10, "a title far too long to fit in this narrow panel", RoleDim)
	top := strings.SplitN(got, "\n", 2)[0]
	if !strings.Contains(top, "…") {
		t.Errorf("renderBoxedColumn(...) top border = %q, want a trailing ellipsis marking the cut", top)
	}
	if w := ansi.StringWidth(top); w != 12 {
		t.Errorf("renderBoxedColumn(...) top border width = %d, want 12 (width 10 + 2 border columns)", w)
	}
}

// Issue #1797 AC: the header, docked list and docked sidebar call sites' shape, unchanged
// by title support landing in the same helper.
func TestRenderBoxedColumn_NoTitle_PlainRule(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	got := renderBoxedColumn("hello", 20, "", RoleDim)
	top := strings.SplitN(got, "\n", 2)[0]
	want := "+" + strings.Repeat("-", 20) + "+"
	if top != want {
		t.Errorf("renderBoxedColumn(...) top border = %q, want %q", top, want)
	}
}

// Issue #1797 review: a panel narrower than the title's own structural "─ " lead-in and
// trailing space has to clamp the whole rule together rather than overflow it.
func TestRenderBoxedColumn_TitledNarrowPanel_TopRuleStaysExactWidth(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	for width := 0; width <= 6; width++ {
		got := renderBoxedColumn("x", width, "AB", RoleDim)
		top := strings.SplitN(got, "\n", 2)[0]
		if w := ansi.StringWidth(top); w != width+2 {
			t.Errorf("renderBoxedColumn(%q, %d, ...) top border = %q with width %d, want %d", "x", width, top, w, width+2)
		}
	}
}

// referenceBoxedHeader is a test-only reproduction of renderBoxedHeader's pre-#3019 body:
// it renders through renderBoxedColumn and counts the result instead of asking
// headerGeometry. renderBoxedHeader now takes its boxed-or-not verdict from
// headerGeometry, so comparing the two directly would be circular (issue #3019 review).
func referenceBoxedHeader(m Model) (lines int, boxed bool) {
	header := renderHeader(m)
	if headerWidth := m.Width - boxBorderCols; headerWidth > 0 {
		if b := renderBoxedColumn(header, headerWidth, headerTitle, RoleDim) + "\n"; strings.Count(b, "\n") < m.Height {
			return strings.Count(b, "\n"), true
		}
	}
	return strings.Count(header, "\n"), false
}

// Issue #3019: headerGeometry is an exact, cheaper stand-in for rendering
// renderBoxedHeader and counting its newlines, the equivalence bodyBudget now relies on.
// Checked in both directions, against the independent referenceBoxedHeader and against
// renderBoxedHeader's own line count, so a boxed/unboxed disagreement at the fitness
// boundary cannot hide behind renderBoxedHeader deferring to headerGeometry.
func TestHeaderGeometry_MirrorsRenderBoxedHeader(t *testing.T) {
	longMsg := strings.Repeat("wrap this message across many columns ", 6)
	wideRuneMsg := "宽度测试 emoji 😀 more plain text so the line has to wrap across several rows"

	variants := []struct {
		name string
		m    Model
	}{
		{"no-alerts", Model{Live: 3, Cap: 5}},
		{"one-alert", Model{Live: 3, Cap: 5, RebuildStatus: RebuildStatus{Stale: true, Message: "rebuild needed"}}},
		{"all-alerts", Model{
			Live: 3, Cap: 5,
			RebuildStatus: RebuildStatus{
				Stale:              true,
				Message:            "rebuild needed",
				Rebuilding:         true,
				Err:                "boom went the build",
				BranchSwitchNotice: "switched branch",
				StaleDrainSummary:  "==> drained 3",
			},
			OrphanRecoveryErr: "adopt failed",
		}},
		{"long-wrapping-message", Model{Live: 3, Cap: 5, RebuildStatus: RebuildStatus{Stale: true, Message: longMsg}}},
		{"wide-rune-message", Model{Live: 3, Cap: 5, RebuildStatus: RebuildStatus{Stale: true, Message: wideRuneMsg}}},
		{"tab-message", Model{Live: 3, Cap: 5, RebuildStatus: RebuildStatus{Stale: true, Message: "a\tb\tc tabbed rebuild reason"}}},
		// BranchSwitchNotice, not Err: clipBannerErr collapses Err through
		// strings.Fields/Join before it ever reaches wrap, which erases the
		// very "\r\n" this variant means to exercise.
		{"crlf-notice", Model{Live: 3, Cap: 5, RebuildStatus: RebuildStatus{BranchSwitchNotice: "switched branch\r\nsecond line\r\nthird line"}}},
		{"fullwidth-rune-message", Model{Live: 3, Cap: 5, RebuildStatus: RebuildStatus{Stale: true, Message: "ｗｉｄｅ　ｒｕｎｅｓ here too"}}},
		{"emoji-zwj-message", Model{Live: 3, Cap: 5, RebuildStatus: RebuildStatus{Stale: true, Message: "emoji 👨‍👩‍👧‍👦 family cluster wrap"}}},
	}
	// Width 9 and height 130 look arbitrary beside the round numbers, but they
	// are where the wrap variants above bite: the tab and fullwidth-rune
	// divergence only surfaces once headerWidth lands mid-word around 7
	// columns, and the emoji ZWJ cluster's one-column miscount only changes
	// the answer past 126 boxed rows. Drop either and those variants stay green.
	widths := []int{0, 1, 2, 3, 9, 20, 40, 80, 200}
	heights := []int{0, 1, 2, 3, 5, 24, 100, 130}

	for _, env := range []struct{ name, noColor, term string }{
		{"no-color", "1", ""},
		{"color", "", "xterm-256color"},
	} {
		t.Run(env.name, func(t *testing.T) {
			t.Setenv("NO_COLOR", env.noColor)
			t.Setenv("TERM", env.term)
			for _, v := range variants {
				for _, width := range widths {
					for _, height := range heights {
						m := v.m
						m.Width = width
						m.Height = height
						gotLines, gotBoxed := headerGeometry(m)
						wantLines, wantBoxed := referenceBoxedHeader(m)
						if gotLines != wantLines || gotBoxed != wantBoxed {
							t.Errorf("%s width=%d height=%d: headerGeometry = (%d, %v), want (%d, %v) (referenceBoxedHeader)", v.name, width, height, gotLines, gotBoxed, wantLines, wantBoxed)
						}
						if renderedLines := strings.Count(renderBoxedHeader(m), "\n"); renderedLines != gotLines {
							t.Errorf("%s width=%d height=%d: renderBoxedHeader rendered %d lines, want %d (headerGeometry)", v.name, width, height, renderedLines, gotLines)
						}
					}
				}
			}
		})
	}
}

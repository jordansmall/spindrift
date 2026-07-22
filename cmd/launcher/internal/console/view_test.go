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

// TestView_ModeList_ShowsPinnedFooter verifies the main list view renders a
// pinned keystroke-hint footer via the shared renderer (issue #1791), the
// same "shortcuts pinned to the bottom" treatment the zoomed log view
// already has, closing the one Console view issue #1792 called out as
// lacking it.
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

// TestView_ModeList_NarrowWidth_FooterClipsWithoutOverflow verifies the main
// list view's pinned footer degrades gracefully on a narrow terminal —
// width-clipped via the shared renderer's own clip-with-ellipsis behaviour,
// never wrapped or left to overflow the terminal width (issue #1792 AC3).
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

// TestView_ModeList_FooterSurvivesScrollClamp verifies the pinned footer
// still renders — and the whole frame still fits the terminal — after
// scrolling a long backlog to its clamped last page, not just on a short,
// unscrolled list (issue #1792 AC2).
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

// TestView_Backlog_TruncatedLabelsShowPlusN verifies a backlog row whose
// labels don't all fit the label column shows as many labels as fit followed
// by a "+N" count for the remainder, rather than the ellipsis-clipped joined
// string title/other cells use (issue #1631).
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

// TestView_Backlog_FittingLabelsShowNoPlusN verifies a backlog row whose
// labels all fit the label column renders the plain joined list with no
// "+N" suffix (issue #1631).
func TestView_Backlog_FittingLabelsShowNoPlusN(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{
		{Number: "1", Title: "x", Labels: []string{"ready-for-agent", "bug"}},
	}})

	// The exact "[ready-for-agent, bug]" match already proves clipLabels
	// didn't truncate — a truncated cell would read "[ready-for-agent, +1]"
	// instead. A bare strings.Contains(out, "+") check would also trip on
	// the header's own bordered panel (issue #1756) rendering ASCII "+"
	// corners under this test's ambient (unset) TERM.
	out := View(m)
	if !strings.Contains(out, "[ready-for-agent, bug]") {
		t.Errorf("View() = %q, want the label cell rendered as \"[ready-for-agent, bug]\" with no +N suffix", out)
	}
}

// TestView_Backlog_LabelCellNeverOverflowsWidth verifies the "+N" suffix
// itself counts against the label column's width budget — even on a
// terminal too narrow to fit any label plus its count, the cell is clipped
// rather than left to overflow (issue #1631).
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

// TestView_Backlog_OrphanLabelCountsTowardPlusN verifies the synthetic
// "orphan" label (issue #1619) is just another entry in the label list for
// truncation purposes — it counts toward both the shown labels and the "+N"
// remainder like any tracker-supplied label (issue #1631).
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

// TestView_Backlog_TitleStillEllipsisClippedNotPlusN verifies an
// over-width title still clips with a trailing ellipsis — the "+N" scheme
// this issue adds is scoped to the label cell, not the title column (issue
// #1631, acceptance criterion 4).
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

// TestView_Header_Title_FoldedInBorder_CollapsesWhenTooShortToBox verifies
// the "spindrift" wordmark folds into the header panel's top border rule
// (issue #1798) rather than rendering as a separate interior banner: the
// literal "====" rule is gone, and the title only disappears once the
// terminal is too short to afford the bordered header at all — the same
// unboxed fallback renderBoxedHeader already applies for an oversized boxed
// render (issue #1035 AC1/AC2) — not a banner-specific collapse rule.
func TestView_Header_Title_FoldedInBorder_CollapsesWhenTooShortToBox(t *testing.T) {
	tall := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	out := View(tall)
	if !strings.Contains(out, "spindrift") {
		t.Errorf("View() on a tall terminal = %q, want the spindrift title", out)
	}
	if strings.Contains(out, "====") {
		t.Errorf("View() on a tall terminal = %q, want no literal ==== banner rule", out)
	}

	// The boxed header's minimum render is 3 rows (top border, one status
	// line, bottom border) — a height of 2 is one row short of affording it,
	// so renderBoxedHeader falls back to the unboxed header entirely,
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

// TestView_Header_RendersBordered verifies the header/status block renders
// inside a muted rounded border — the same renderBoxedColumn look the docked
// list/sidebar panels already use — so the header reads as its own panel
// rather than running straight into the Section tabs below it (issue #1756).
func TestView_Header_RendersBordered(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	out := View(m)
	if got := strings.Count(out, "╭"); got != 1 {
		t.Errorf("View() has %d rounded top-left corners, want 1 (the header's own bordered panel): %q", got, out)
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

// TestView_RunningPick_SanitizesHeartbeatControlSequences verifies a running
// pick's Heartbeat — box-log-derived, untrusted content (issue #1639) — is
// stripped of control sequences the same way Title/Reason already are,
// mirroring TestView_Queue_SanitizesTitleAndReasonControlSequences.
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

// TestView_ModeFilterEdit_NarrowWidth_FooterFitsWidth verifies the
// filter-edit prompt's "/filter  " prefix plus its enter/esc hint clips to
// the terminal's own width — accounting for the prefix's own columns, not
// just the hint text — rather than wrapping past bodyBudget's single
// reserved row for it on a narrow terminal (issue #1818).
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

// TestView_ModeTerminateConfirm_NarrowWidth_FooterFitsWidth verifies the
// terminate-confirm prompt's "terminate #N? " prefix plus its y hint clips
// to the terminal's own width — accounting for the prefix's own columns,
// not just the hint text — rather than wrapping past bodyBudget's single
// reserved row for it on a narrow terminal (issue #1818).
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

// TestView_ModeQuitConfirm_NarrowWidth_FooterFitsWidth verifies the
// quit-confirm footer hint — long enough to wrap past one row when rendered
// unclipped — is clipped to the terminal's own width like every other footer
// in this file, so bodyBudget's single reserved row for it is never
// exceeded on a narrow terminal (issue #1818).
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

// TestView_ModeFilterEdit_ShowsInputLine verifies an in-progress filter edit
// renders a visible input line with the text typed so far (issue #784).
func TestView_ModeFilterEdit_ShowsInputLine(t *testing.T) {
	m := NewModel()
	m = Update(m, FilterEditStartMsg{})
	m = Update(m, FilterChangedMsg{Filter: "bug"})

	out := View(m)
	if !strings.Contains(out, "/bug") && !strings.Contains(out, "/ bug") {
		t.Errorf("View() = %q, want the in-progress filter text shown", out)
	}
}

// TestView_ModeFilterEdit_FooterStyledDim verifies the filter-edit prompt's
// enter/esc hints render dim (RoleDim, "\x1b[90m") via the shared footer
// renderer, the same treatment the other migrated footers already got
// (issue #1793).
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

// TestView_ModeHelp_ListsBoundKeys verifies the help overlay lists every key
// the tea layer binds, replacing the normal backlog rendering (issue #784).
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

// TestView_ModeHelp_ListsSectionKeys verifies the help overlay describes
// H/L (previous/next Section) and 1-5 (direct jump) — the section-switched
// list's navigation, replacing the retired "tab" focus-switch binding (ADR
// 0030, issue #1500).
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

// TestView_ModeHelp_DescribesContextSensitiveEnter verifies the help
// overlay's "enter" entry documents both context-sensitive behaviors —
// opening the highlighted Backlog row's ticket detail modal and opening a
// work Section pick's live-tail sidebar — not just the bare word "enter"
// (issue #995, reworded for ADR 0030's Section-switched body by issue #1500,
// then for the sidebar by #1501, then for the detail modal by #1632 — Enter
// no longer picks; picking moved to "p").
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

// TestView_ModeHelp_ListsNewKeybindings verifies the help overlay lists the
// picks/queue-driving keys wired in issue #785, and documents "X" as the
// Terminate key now that "k" reverted to vim's cursor-up (issue #1500).
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

// TestView_ModeHelp_ListsAdoptOrphanKey verifies the help overlay documents
// "A", the explicit adopt gesture on an orphan-flagged Backlog row (issue
// #1619) — startup only ever detects an orphan now, so the operator needs a
// discoverable way to learn how to adopt one.
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

// TestView_ModeHelp_ListsRebuildOutputKey verifies the help overlay lists
// "o", the rebuild-output pane's open key added by issue #1128.
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

// TestView_ModeHelp_ListsBodyScrollKeys verifies the help overlay lists
// pgup/pgdown as the backlog/queue viewport's own line-scroll keys,
// distinct from the sidebar's identically-named scroll keys
// (issue #1036 AC — help overlay documents the new scroll keys).
func TestView_ModeHelp_ListsBodyScrollKeys(t *testing.T) {
	m := Update(NewModel(), HelpToggleMsg{})

	out := View(m)
	if !strings.Contains(out, "pgup/pgdown  jump a full page of the active Section's live") {
		t.Errorf("View() = %q, want it to mention the dynamic Section page jump", out)
	}
}

// TestView_ModeHelp_ListsJumpKeys verifies the help overlay documents "G"
// and "gg" — the list body's jump-to-bottom/top motions — alongside the
// existing j/k and pgup/pgdown entries (issue #1628 AC7).
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

// TestView_ModeHelp_ListsSidebarJumpToTop verifies the help overlay documents
// the sidebar's own "gg" — detach follow and jump to the sidebar's top —
// alongside its existing "G / end" jump-to-bottom entry (issue #1629 AC4).
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

// TestView_ModeHelp_ContrastsSidebarFixedPage verifies the help overlay's
// sidebar pgup/pgdown line calls out that its page jump is a fixed size,
// unlike the backlog/queue's live-viewport-derived one — the two keys share
// a name but not a page size (issue #1059).
func TestView_ModeHelp_ContrastsSidebarFixedPage(t *testing.T) {
	m := Update(NewModel(), HelpToggleMsg{})

	out := View(m)
	want := fmt.Sprintf("fixed at %d lines", fixedPaneScrollDelta)
	if !strings.Contains(out, want) {
		t.Errorf("View() = %q, want it to contain %q", out, want)
	}
}

// TestView_ModeHelp_ListsCtrlFCtrlBAlongsidePageKeys verifies the help
// overlay lists the vim page chords ctrl+f/ctrl+b next to each pane's
// existing pgup/pgdown entry — the list body, the sidebar, and the
// rebuild-output pane (issue #1647 AC).
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

// TestView_ModeHelp_ListsCtrlDCtrlUAlongsideHalfPageKeys verifies the help
// overlay lists the vim half-page chords ctrl+d/ctrl+u for each pane — the
// list body, the sidebar, and the rebuild-output pane (issue #1648 AC).
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

// TestView_ModeHelp_ListsRebuildOutputJumpKeys verifies the help overlay
// documents "G" and "gg" for the rebuild-output pane, alongside the existing
// "o"/j/k/pgup/pgdown entry (issue #1630 AC).
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
	// Height 5 leaves a 2-line content budget (headerFooterLines, plus the
	// trailing-"\n" reservation issue #1827 added), too small to show all 5
	// lines at once — so a scroll actually slides the window instead of
	// clamping straight back to the top like a short transcript that already
	// fits (mirrored from DrillIn's own clamp, model.go).
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

// TestView_RebuildOutputExactFit_FitsHeightWithFooterPinned verifies the
// rebuild-output pane never renders more physical lines than m.Height, even
// when the captured output exactly fills or overflows the old (unreserved)
// budget — the same trailing-"\n" off-by-one issue #1825 fixed for the list
// view (issue #1827). Split, not TrimRight-then-count: View()'s output
// always ends in exactly one trailing "\n" (its own documented convention),
// and that trailing "\n" costs the terminal a physical row of its own.
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

// TestView_RebuildOutputOpen_FooterStyledDim verifies the rebuild-output
// pane's close-key hint renders dim (RoleDim, "\x1b[90m") via the shared
// footer renderer, the same treatment the other three migrated footers
// already got (issue #1791).
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

// TestView_DetailModal_FloatsOverList_BannerStillVisible verifies the ticket
// detail modal renders as a box floating over the still-rendered list rather
// than a fullscreen takeover: the header banner above the box's top edge
// stays visible instead of being replaced entirely (issue #1758).
func TestView_DetailModal_FloatsOverList_BannerStillVisible(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 100, Height: 40})
	m = Update(m, DetailModalOpenMsg{Number: "42", Title: "fix the thing"})

	out := View(m)
	if !strings.Contains(out, "spindrift") {
		t.Errorf("View() = %q, want the banner still visible above the floating detail modal instead of a fullscreen takeover", out)
	}
}

// TestView_DetailModal_TinyTerminal_FallsBackToFullscreen verifies a
// terminal too narrow or short for a legible floating box (below
// detailModalFits' threshold) renders the detail modal via the existing
// fullscreen renderer instead of a cramped floating box — mirroring the
// sidebar's sidebarFits degradation (issue #1759 AC).
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

// TestView_DetailModal_FullscreenFallback_FooterStyledDim verifies the
// tiny-terminal fullscreen fallback's keystroke-hint footer renders dim
// (RoleDim, "\x1b[90m") via the shared footer renderer, the same treatment
// the fullscreen sidebar and docked-sidebar footers already got (issue
// #1791).
func TestView_DetailModal_FullscreenFallback_FooterStyledDim(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	m := Update(NewModel(), SizeChangedMsg{Width: detailModalBoxMinWidth - 1, Height: 24})
	m = Update(m, DetailModalOpenMsg{Number: "42", Title: "fix the thing"})

	out := View(m)
	if !strings.Contains(out, "\x1b[90m[esc] close · [p] pick\x1b[0m") {
		t.Errorf("View() = %q, want the fullscreen fallback footer dim-styled with its hint text intact", out)
	}
}

// TestView_DetailModal_FullscreenFallback_LabelsStyledDim verifies the
// tiny-terminal fullscreen fallback's pinned label row gets the identical
// bracketed, dim-styled treatment (RoleDim, "\x1b[90m") the floating box
// gets — the backlog row's own `[bug, console]` idiom — so the two
// renderings stay in parity (issue #1832).
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

// TestView_DetailModal_FullscreenFallback_NarrowWidth_FooterFitsWidth
// verifies the tiny-terminal fullscreen fallback's "[esc] close" footer
// clips to the terminal's own width like every other footer in this file
// (issue #1818). The title is kept short so the width picked here (narrower
// than "[esc] close" itself) exercises the footer's own clip path rather
// than tripping on an unrelated, longer line first.
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

// detailModalBoxTopBorderLine returns out's floating detail modal box's own
// top border row, identified by title — the same "╭" plus "#number title"
// combination TestView_DetailModal_BorderShowsNumberAndTitle already keys
// on — rather than the first line containing a bare "╭". A bare-"╭" search
// is ambiguous once the header/sidebar panels grow their own RoleDim
// rounded border (issue #1756): renderBoxedColumn degrades to ASCII glyphs
// under termenv.Ascii, so a bare "╭" search only happened to find the
// modal's box by accident on terminals where that degradation kicks in —
// and reliably found the header's border instead, wherever the environment
// resolves a non-Ascii profile (nix's sandboxed test runner, real color
// terminals). detailModalBoxTopBorder always renders "#number title" into
// its own border line and nowhere else, so pairing it with "╭" pins the
// match to the modal's border regardless of what other boxes are on screen.
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

// detailModalBoxBorderWidth returns the display width of out's floating
// detail modal box itself — just the "╭...╮" span of its top border row,
// not the full composited terminal row around it (compositeOverlay splices
// the box into a still-visible base row, so the row as a whole is always
// terminal-width; the box's own width is the corner-to-corner span) — the
// box's actual rendered outer width, independent of detailModalBoxSize's own
// math, so tests can check the box View() produced without recomputing the
// expected value the same way the code does.
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

// detailModalBoxOriginX returns the display column out's floating detail
// modal box's top-left corner ("╭") lands at — the leading prefix on that
// row measured with ansi.StringWidth so any styled base content (e.g. a
// colored Section tab sharing the same physical row) doesn't skew the
// count, unlike a plain byte or rune index into the line.
func detailModalBoxOriginX(t *testing.T, out, title string) int {
	t.Helper()
	line := detailModalBoxTopBorderLine(t, out, title)
	return ansi.StringWidth(line[:strings.Index(line, "╭")])
}

// TestView_DetailModal_Resize_RecentersAndResizesBox verifies a resize while
// the detail modal is open re-sizes and re-centers the floating box rather
// than leaving it pinned at whatever size it opened at (issue #1759 AC).
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

// TestView_DetailModal_BorderShowsNumberAndTitle verifies the floating
// detail modal box has a visible border, with the ticket's "#number title"
// set in the box's top border line rather than as its own interior content
// row (issue #1758 AC).
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

// TestView_DetailModal_NoColor_BorderDegradesToAscii verifies the floating
// detail modal's border — including its titled top rule — degrades to plain
// ASCII glyphs under NO_COLOR, closing the gap its old hand-rolled
// Unicode-only top/bottom border left: every other panel in the package
// already degrades this way (issue #1755), but the modal never did until it
// moved onto the shared titled-border helper (issue #1797 AC).
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

// TestView_DetailModal_FloatingBox_FooterStyledDim verifies the floating
// detail-modal box's own footer line — the primary path View actually
// renders once detailModalFits (issue #1758), not just the tiny-terminal
// fullscreen fallback — renders dim (RoleDim, "\x1b[90m") via the shared
// footer renderer too, so the same modal doesn't show its footer dim on a
// tiny terminal but plain on a normal one (issue #1791).
func TestView_DetailModal_FloatingBox_FooterStyledDim(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	m := Update(NewModel(), SizeChangedMsg{Width: 100, Height: 40})
	m = Update(m, DetailModalOpenMsg{Number: "42", Title: "fix the thing"})

	out := View(m)
	if !strings.Contains(out, "\x1b[90m[esc] close · [p] pick") {
		t.Errorf("View() = %q, want the floating box's footer dim-styled with its hint text intact", out)
	}
}

// TestView_DetailModal_ShowsBlockedByAndBlocksSections verifies the ticket
// detail modal renders both its Blocked-by and Blocks sections, each entry
// as number + source + open/closed state + title (issue #1632 AC), e.g.
// `✗ #1540 (native) open "Waves core"`.
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

// TestFormatBlockerRef_UnresolvedTitleAndState_RendersUnknown verifies a
// BlockerRef whose title/state couldn't be resolved (resolveBlockerRef's
// fallback: neither the backlog nor an Issue fetch found it) renders
// "unknown" in place of the blank state/title, rather than a bare double
// space and an empty quoted string (issue #1632 review finding).
func TestFormatBlockerRef_UnresolvedTitleAndState_RendersUnknown(t *testing.T) {
	got := formatBlockerRef(BlockerRef{Number: "123", Source: forge.DepSourceNative})
	want := `✗ #123 (native) unknown "unknown"`
	if got != want {
		t.Errorf("formatBlockerRef(...) = %q, want %q", got, want)
	}
}

// TestView_DetailModal_Err_ShowsFailedToLoad verifies a body fetch that
// failed (openDetailModalCmd's tracker.Issue call erred) surfaces the error
// in place of a blank or stuck-loading modal, instead of silently rendering
// nothing (issue #1632 review finding — the error render path had no test
// coverage).
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

// TestView_DetailModal_SanitizesErr verifies a body-fetch error carrying
// CSI/OSC escape sequences (an untrusted tracker error message, e.g. from a
// forge API response echoed back) renders with the escapes stripped, the
// same trust boundary every other piece of tracker-derived text in the
// modal already crosses (issue #1632 review finding).
func TestView_DetailModal_SanitizesErr(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	m = Update(m, DetailModalOpenMsg{Number: "42", Title: "fix the thing"})
	m = Update(m, DetailModalLoadedMsg{Number: "42", Err: errors.New("evil\x1b[2Jerrtext\x1b]0;pwned\x07here")})

	out := View(m)
	// Checked as specific injected byte sequences rather than "no \x1b
	// anywhere on this line": the floating box (issue #1758) can now share
	// a physical row with styled base UI (e.g. colored Section tabs), whose
	// own legitimate SGR escapes must not trip a check meant to catch the
	// untrusted error text's own control sequences surviving sanitization.
	for _, escape := range []string{"\x1b[2J", "\x1b]0;pwned\x07"} {
		if strings.Contains(out, escape) {
			t.Errorf("View() = %q, want the injected escape sequence %q stripped by sanitization", out, escape)
		}
	}
	if !strings.Contains(out, "evilerrtexthere") {
		t.Errorf("View() = %q, want the surrounding error text intact after stripping escapes", out)
	}
}

// TestView_DetailModal_DimsListBehind verifies the base layer the floating
// detail modal composites over renders in RoleDim's foreground while the
// modal is open (issue #1760's scrim) — a running Pick's own RoleRunning
// escape is replaced by RoleDim's — and closing the modal restores the row's
// normal RoleRunning styling.
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

// TestView_DetailModal_NoBlockersOrBlocks_ShowsNoSectionClutter verifies a
// ticket with nothing declared in either direction doesn't grow empty
// "Blocked by"/"Blocks" section headers with nothing under them (issue
// #1632).
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

// TestView_DetailModal_ScrollOffset_HidesLinesBeforeOffset verifies the
// detail modal's body scrolls: once its content overflows the viewport,
// DetailModalScrollMsg moves which lines are visible, hiding everything
// before the new offset (issue #1632 AC — "the body scrolls with j/k and
// the arrow keys"). Height is pinned to detailModalBoxMinHeight, the
// smallest terminal still on the floating path (issue #1759) — its interior
// body budget (2 border rows, the no-labels case's 1 label line, and 1
// footer line subtracted, issue #1772) leaves bodyBudget rows, short of the
// 8-line body so the scroll actually clips something, the same "content
// overflows the viewport" setup the fullscreen renderer's version of this
// test uses against its own dynamic title/label/footer budget.
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

// TestDetailModalBoxSize_WideTerminal_ClampsToMax verifies the floating
// detail modal box never grows edge-to-edge on a large terminal: its width
// and height are capped at detailModalBoxMax{Width,Height} rather than
// scaling without bound (issue #1759 AC), and pins the width cap's actual
// value at 100 columns, not the old 84 (issue #1796 AC1/AC2).
func TestDetailModalBoxSize_WideTerminal_ClampsToMax(t *testing.T) {
	width, height := detailModalBoxSize(300, 100)
	if width != 100 {
		t.Errorf("detailModalBoxSize(300, 100) width = %d, want 100 (new max-width cap)", width)
	}
	if height != detailModalBoxMaxHeight {
		t.Errorf("detailModalBoxSize(300, 100) height = %d, want %d (clamped to max)", height, detailModalBoxMaxHeight)
	}
}

// TestDetailModalBoxSize_MidTerminal_SizedToFraction verifies the floating
// detail modal box scales with the terminal rather than shrinking by a fixed
// margin: on a terminal well under the max clamp, the box width/height are a
// fraction of the terminal's own dimensions (issue #1759 AC).
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

// TestDetailModalBoxSize_NearFloorTerminal_ClampsToMin verifies a terminal
// just above detailModalFits' own threshold — where the width/height
// fraction would otherwise fall short of the floor — clamps the box up to
// detailModalBoxMin{Width,Height} rather than the smaller fraction (issue
// #1759 AC's "clamped to a minimum").
func TestDetailModalBoxSize_NearFloorTerminal_ClampsToMin(t *testing.T) {
	width, height := detailModalBoxSize(detailModalBoxMinWidth, detailModalBoxMinHeight)
	if width != detailModalBoxMinWidth {
		t.Errorf("detailModalBoxSize(%d, %d) width = %d, want %d (clamped to min)", detailModalBoxMinWidth, detailModalBoxMinHeight, width, detailModalBoxMinWidth)
	}
	if height != detailModalBoxMinHeight {
		t.Errorf("detailModalBoxSize(%d, %d) height = %d, want %d (clamped to min)", detailModalBoxMinWidth, detailModalBoxMinHeight, height, detailModalBoxMinHeight)
	}
}

// TestDetailModalFits_BelowMinDimension_ReturnsFalse verifies detailModalFits
// — the single predicate gating floating-vs-fullscreen (issue #1759 AC),
// sidebarFits' detail-modal analogue — rejects a terminal narrower or
// shorter than the box's own legibility floor, and accepts one that meets
// both floors.
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

// TestView_DetailModal_SingleLabel_VisuallyDistinctFromBody is the issue
// #1832 regression test: a single short label pinned atop the modal used to
// render as bare comma-joined text — indistinguishable from a stranded line
// of body text at a glance. It must now read as labels: bracketed like the
// backlog row's own `[bug]` idiom, and dim-styled (RoleDim) on a
// color-capable terminal so it visually recedes from the plain body text
// beneath it, while degrading to plain bracketed text (no escape bytes)
// under NO_COLOR per ADR 0031.
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

// TestView_DetailModal_LabelsUnclipped verifies the detail modal shows every
// label in full, unlike the backlog row's clipLabels "+N" truncation (issue
// #1631) — the modal exists precisely so an operator can see what a clipped
// backlog row hides (issue #1632 AC).
func TestView_DetailModal_LabelsUnclipped(t *testing.T) {
	labels := []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel"}
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	m = Update(m, DetailModalOpenMsg{Number: "42", Title: "fix the thing", Labels: labels})

	// Every label present unclipped (the loop below) already proves
	// clipLabels-style "+N" truncation didn't happen. A bare
	// strings.Contains(out, "+") check would also trip on the header's own
	// bordered panel (issue #1756) rendering ASCII "+" corners under this
	// test's ambient (unset) TERM.
	out := View(m)
	for _, label := range labels {
		if !strings.Contains(out, label) {
			t.Errorf("View() = %q, want every label present unclipped, missing %q", out, label)
		}
	}
}

// TestView_DetailModal_LabelsWrapOnOverflow verifies the floating detail
// modal wraps a labels line that overflows the box's interior width onto
// further interior rows instead of silently truncating it (issue #1772):
// TestView_DetailModal_LabelsUnclipped's 8 short labels never exceed even
// an 80-col terminal's 74-col interior (detailModalInnerSize), so it never
// exercised the overflow path padDisplay's runewidth.Truncate hits once the
// joined labels line runs past innerWidth. This test uses a terminal wide
// enough to cap the box at detailModalBoxMaxWidth (issue #1772 AC2's "the
// box's default max width"), a 98-col interior, rather than a narrower
// uncapped box.
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

// TestDetailModalLabelLinesCapped_OverflowIndicatorSharesBracket verifies
// the "+N more labels" overflow indicator (issue #1631's multi-row analogue,
// issue #1778) is folded into the same bracketed, dim-styled block the
// retained labels render in — "[alpha, +3 more labels]" — rather than
// appended as a separate unbracketed, unstyled line after the closing
// bracket (issue #1832): the indicator is still part of the same "these are
// labels" row, not a stray second line.
func TestDetailModalLabelLinesCapped_OverflowIndicatorSharesBracket(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	got := detailModalLabelLinesCapped([]string{"alpha", "bravo", "charlie", "delta"}, 24, 1)
	want := []string{"\x1b[90m[alpha, +3 more labels]\x1b[0m"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("detailModalLabelLinesCapped(...) = %v, want %v", got, want)
	}
}

// TestDetailModalLabelLinesCapped_ZeroBudget_FallsBackToBareIndicator
// verifies the maxLines <= 0 degenerate case — no room for even one label
// inside a bracket — falls back to the bare, unbracketed, unstyled "+N more
// labels" indicator documented on detailModalLabelLinesCapped, rather than
// an empty bracket or a styled line the zero-row budget has no room to show.
func TestDetailModalLabelLinesCapped_ZeroBudget_FallsBackToBareIndicator(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	got := detailModalLabelLinesCapped([]string{"alpha", "bravo"}, 24, 0)
	want := []string{"+2 more labels"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("detailModalLabelLinesCapped(...) = %v, want %v", got, want)
	}
}

// TestView_DetailModal_LabelOverflowShowsIndicator verifies that when
// wrapped label lines alone would consume the box's entire interior height,
// renderDetailModalContent caps them and appends a "+N more labels"
// indicator instead of letting the tail-truncate at the end of the function
// silently drop trailing label lines and/or the footer (issue #1778 — a gap
// left by #1772/#1780's wrap-instead-of-truncate fix). Each label here is
// longer than the box's interior width, so wrapText places exactly one
// label per rendered line, making the overflow point (innerHeight -
// detailModalFooterLines lines) deterministic regardless of wrapText's
// internals.
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
	// truncates every row to — so checking for the full label string would
	// pass whether or not it was capped (it never fits a row intact either
	// way). The short "label-NN-" prefix does fit a row intact, so its
	// absence actually distinguishes "dropped by the cap" from "rendered and
	// merely wrapped/truncated".
	lastPrefix := fmt.Sprintf("label-%02d-", len(labels)-1)
	if strings.Contains(out, lastPrefix) {
		t.Errorf("View() = %q, want the last label (prefix %q) dropped behind the overflow indicator, not rendered", out, lastPrefix)
	}
}

// TestView_DetailModal_SanitizesTitleLabelsBodyAndBlockerTitles verifies the
// ticket detail modal strips CSI/OSC escape sequences from every piece of
// untrusted tracker text it renders — title, labels, body, and each
// Blocked-by/Blocks entry's title — the same trust boundary the backlog row
// and sidebar transcript already enforce (issue #862, extended to the
// detail modal by issue #1632 review finding): a tracker title/body/label is
// untrusted input, and Bubble Tea does not filter arbitrary control
// sequences before writing to the operator's terminal.
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
	// anywhere on this line": the floating box (issue #1758) can now share
	// a physical row with styled base UI (e.g. colored Section tabs), whose
	// own legitimate SGR escapes must not trip a check meant to catch the
	// untrusted title/label/body/blocker text's own control sequences
	// surviving sanitization.
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

// TestView_SidebarOpen_RendersActivityInsteadOfBacklog verifies an open
// sidebar, on a terminal too narrow to dock it, replaces the backlog/queue
// rendering with the sidebar's Activity feed — the default view, not the
// Transcript — the operator's view of the work, not just liveness (#648,
// #1501).
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

// TestView_SidebarToggle_RendersTranscriptThenRaw verifies advancing the
// toggle swaps the sidebar from the Activity feed to the rendered Transcript,
// then to the raw byte-exact form.
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

// TestView_SidebarOffset_HidesLinesBeforeOffset verifies scrolling (a
// non-zero Offset) drops the leading lines from the sidebar instead of
// always showing its start (issue #786, inherited). Height is small enough
// that the content outruns the viewport budget, or the viewport clamp (issue
// #829) would pin Offset at 0 since the whole thing already fits. The
// Transcript (rendered) view is toggled on so the content matches the plain
// "l0".."l3" lines the old drill-in test exercised.
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

// TestView_SidebarFullscreen_WindowsToViewportHeight verifies the fullscreen
// sidebar joins only as many lines as the viewport can show, instead of the
// whole tail from Offset to the end of a (potentially multi-MB) transcript,
// so scrolling near the top of a huge transcript doesn't re-serialize
// content nowhere near the screen (issue #722, inherited). The Transcript
// (rendered) view is toggled on so the content matches the head/tail markers
// the old drill-in test exercised.
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

// TestView_SidebarOpen_WideTerminal_DocksBesideList verifies a terminal wide
// enough for sidebarFits shows the sidebar docked beside the still-visible
// list, rather than the list disappearing behind a fullscreen takeover — the
// core of ADR 0030's docked layout (#1501).
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

// TestView_SidebarOpen_WideTerminal_PanelsRenderBordered verifies the docked
// list and the docked sidebar each render inside a muted rounded border
// once the sidebar is open — the bordered-panel look that replaces the bare
// column divider, so the split reads as two distinct boxes rather than one
// continuous surface (issue #1755) — alongside the header's own bordered
// panel (issue #1756), for 3 boxes total.
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

// TestView_SidebarOpen_NoColor_PanelsRenderAsciiBorder verifies the docked
// panels' border degrades to plain ASCII glyphs (no rounded Unicode
// box-drawing characters, no stray escape bytes) under NO_COLOR, the same
// degradation colorProfile() already applies to role coloring elsewhere in
// the package (issue #1755).
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

// TestView_SidebarOpen_DumbTerminal_PanelsRenderAsciiBorder verifies the
// docked panels' border also degrades to plain ASCII glyphs on a non-color
// terminal (TERM=dumb) — the other half of colorProfile()'s degradation the
// NO_COLOR border test covers (issue #1755).
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

// TestView_SidebarOpen_QueueEnterNotice_DockedPanelsRespectHeight verifies
// that a docked sidebar with the QueueEnterNotice line also showing never
// renders more than Height total lines — bodyBudget must reserve exactly the
// same lines View itself reserves, or the bordered panels' row budget comes
// out too generous and the render spills a line past the terminal (issue
// #1755, the #1035/#1500 "never overflow Height" invariant).
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

// TestView_SidebarOpen_MinimumFittingWidth_PanelsFitTerminalWidth verifies
// that at the narrowest width sidebarFits allows docking, the two bordered
// panels' combined rendered width — border overhead included — never
// exceeds the terminal's actual width, so nothing overflows and wraps at
// the threshold (issue #1755).
func TestView_SidebarOpen_MinimumFittingWidth_PanelsFitTerminalWidth(t *testing.T) {
	width := sidebarMinListWidth + sidebarWidth + dockedBorderCols
	m := Update(NewModel(), SizeChangedMsg{Width: width, Height: 24})
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{{Number: "1", Title: "still visible"}}})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "#42 · hi"}}})

	out := View(m)
	// lipgloss.Width, not runewidth.StringWidth: on a color-capable ambient
	// TERM the panel border carries ANSI codes (ADR 0031), which
	// runewidth.StringWidth counts as display width and lipgloss's
	// ANSI-aware measurement does not (mirrors the rebuild-banner width
	// check above).
	for i, line := range strings.Split(out, "\n") {
		if got := lipgloss.Width(line); got > width {
			t.Errorf("View() line %d is %d columns wide, want at most the terminal's %d: %q", i, got, width, line)
		}
	}
}

// TestView_SidebarOpen_UnevenContent_PanelBottomsAlign verifies that when
// the list and sidebar panels render a different number of content lines,
// their bordered boxes still close at the same row — the shorter panel's
// content pads out to the taller one's height before the border wraps it —
// so the two boxes read as aligned panels instead of one panel's bottom
// edge floating above a gap while the other continues (issue #1755).
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

// TestView_SidebarOpen_ShortContent_DividerDoesNotFillWholeBudget verifies
// the divider between the docked list and sidebar spans only as many rows
// as the taller of the two actually rendered, not the whole body budget —
// one Backlog issue and a one-line Activity feed must not force blank
// divider rows down to the bottom of a tall terminal (#1501 review finding).
func TestView_SidebarOpen_ShortContent_DividerDoesNotFillWholeBudget(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: sidebarMinListWidth + sidebarWidth + dockedBorderCols, Height: 24})
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

// TestView_SidebarFullscreen_NarrowWidth_FooterFitsWidth verifies the
// fullscreen sidebar's "[t] cycle activity/transcript · [x] close · [z]
// ..." footer — long enough to wrap past one row when rendered unclipped —
// clips to the terminal's own width like every other footer in this file
// (issue #1818).
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

// TestView_SidebarFullscreen_LongTranscriptLine_ClipsToWidth verifies a
// rendered-transcript line longer than the pane width clips to exactly one
// physical row, mirroring renderSidebarDocked's own per-line clip — an
// unclipped line soft-wraps past windowSidebarLines' logical-line height
// budget and pushes the modal's top border (and footer) off the viewport
// (issue #1841).
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

// TestView_SidebarFullscreen_RawViewLongLine_ClipsToWidth verifies the clip
// fix holds for the raw JSONL `[t]` view too, not just the rendered one —
// windowSidebarLines and the clip loop it feeds don't care which of the
// three views populated Sidebar.Lines (issue #1841 AC3).
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

// TestView_SidebarFullscreen_ZoomedLongLine_ClipsToWidth verifies the clip
// fix holds on the [z]-zoom fullscreen trigger, not only the too-narrow-to-
// dock fallback — both reach the same renderSidebarFullscreen (viewBody's
// `m.SidebarZoom || !sidebarFits(m)` branch), but a terminal wide enough to
// dock exercises a different width value than the narrow-fallback tests
// above (issue #1841 AC3).
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

// TestView_SidebarFullscreen_ExactFitContent_FitsHeightWithFooterPinned
// verifies a fullscreen sidebar whose content exactly fills its windowed
// budget still renders no more physical lines than m.Height: View()'s own
// guaranteed trailing "\n" (the same class of off-by-one #1825/#1827 fixed
// for the list body and rebuild-output pane) costs the terminal a physical
// row of its own, and renderSidebarFullscreen never reserved one — unlike
// renderSidebarDocked, which inherits it via bodyBudget (issue #1841).
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

// assertSidebarFitsHeightBudget is #1842's shared regression guard, reused by
// both the fullscreen and docked sidebar tests below so a future sidebar view
// is covered by construction instead of a copy-pasted assertion per case: no
// rendered row may exceed width, and the total physical (post-wrap) row count
// may not exceed height. Checking both together matters because a renderer
// that satisfies only one half — clips every line but forgets a chrome row,
// or reserves the right row count but joins one unclipped wide line — still
// overflows the viewport the same way #1841 did. The height check counts
// "\n"-split rows rather than re-wrapping the output itself, so it only
// proves what it claims — no logical line silently ballooned into more than
// one physical row — alongside the per-row width check right above it,
// never on its own.
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

// sidebarWideLinesFixture returns count deliberately over-wide (width*3
// rune) lines, as both an Activity feed and a joined transcript string, for
// the two tests below — the shared setup half of #1842's guard, so the
// fullscreen and docked cases differ only in the width/height/Update
// sequence each renderer actually needs.
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

// TestView_SidebarFullscreen_WideLines_FitHeightBudgetAcrossAllViews is
// #1842's fullscreen regression guard: enough deliberately over-wide lines to
// fill the renderer's whole viewport, in each of the three [t] views, checked
// through the one assertSidebarFitsHeightBudget helper its docked
// counterpart below also uses. The existing *_ClipsToWidth tests (issue
// #1841) already catch an unclipped renderer on their own per-row width
// check, but each uses only a single wide line — never enough to fill a
// renderer's whole logical-line budget at once, and never shared with a
// docked-renderer test. This is what actually covers "no sidebar view, in
// any renderer" per the issue's AC, rather than one more per-renderer
// one-off.
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

// TestView_SidebarDocked_WideLines_FitHeightBudgetAcrossAllViews is #1842's
// docked-renderer counterpart to the fullscreen test above, sharing both
// sidebarWideLinesFixture and assertSidebarFitsHeightBudget so the same
// invariant covers both render paths by construction rather than a second
// copy-pasted test. renderSidebarDocked already clips (issue #1799,
// predating #1841's fullscreen fix), so this is expected to pass without
// further changes — its value is guarding the docked path against ever
// regressing the same way.
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

// TestQueueNarrowed_SidebarDocked_ReportsTrue verifies queueNarrowed reports
// true once the sidebar is open and docked beside the list — the trigger for
// the compact/wrapped queue-row form (issue #1752).
func TestQueueNarrowed_SidebarDocked_ReportsTrue(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: sidebarMinListWidth + sidebarWidth + dockedBorderCols, Height: 24})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "hi"}}})

	if !queueNarrowed(m) {
		t.Errorf("queueNarrowed(m) = false, want true once the sidebar is docked beside the list")
	}
}

// TestQueueNarrowed_SidebarClosed_ReportsFalse verifies queueNarrowed reports
// false with no sidebar open — the list renders at full width, unchanged from
// today (issue #1752 AC).
func TestQueueNarrowed_SidebarClosed_ReportsFalse(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: sidebarMinListWidth + sidebarWidth + dockedBorderCols, Height: 24})

	if queueNarrowed(m) {
		t.Errorf("queueNarrowed(m) = true, want false with no sidebar open")
	}
}

// TestQueueNarrowed_SidebarFullscreen_ReportsFalse verifies queueNarrowed
// reports false when the sidebar takes over fullscreen (narrow terminal) —
// the list isn't rendered at all in that layout, so it has no queue column to
// narrow (issue #1752).
func TestQueueNarrowed_SidebarFullscreen_ReportsFalse(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: sidebarMinListWidth + sidebarWidth, Height: 24})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "hi"}}})

	if queueNarrowed(m) {
		t.Errorf("queueNarrowed(m) = true, want false while the sidebar is fullscreen, not docked")
	}
}

// TestQueueNarrowed_SidebarZoomed_ReportsFalse verifies queueNarrowed
// reports false once the operator zooms the sidebar to fullscreen, even on a
// terminal wide enough to dock — View forces the fullscreen takeover on zoom
// regardless of sidebarFits (issue #1502), hiding the list the same way the
// too-narrow-to-dock case does, so this must never disagree (issue #1752).
func TestQueueNarrowed_SidebarZoomed_ReportsFalse(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: sidebarMinListWidth + sidebarWidth + dockedBorderCols, Height: 24})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "hi"}}})
	m = Update(m, SidebarZoomToggleMsg{})

	if queueNarrowed(m) {
		t.Errorf("queueNarrowed(m) = true, want false while the sidebar is zoomed to fullscreen")
	}
}

// TestCompactColumnItemBudget_ExactFit_ReturnsWholeItems verifies
// compactColumnItemBudget returns exactly as many compact entries as fit
// with no wasted budget: a column budget of 6 (1 header row + 5 available)
// fits exactly 2 entries — 2*compactRowLines (4) plus 1 separator between
// them — with none left over (issue #1752).
func TestCompactColumnItemBudget_ExactFit_ReturnsWholeItems(t *testing.T) {
	if got := compactColumnItemBudget(6); got != 2 {
		t.Errorf("compactColumnItemBudget(6) = %d, want 2", got)
	}
}

// TestCompactColumnItemBudget_TooSmallForOneEntry_ReturnsZero verifies a
// column budget too small to fit even one compact entry's header+title
// block returns zero rather than a negative or fractional count (issue
// #1752).
func TestCompactColumnItemBudget_TooSmallForOneEntry_ReturnsZero(t *testing.T) {
	if got := compactColumnItemBudget(1); got != 0 {
		t.Errorf("compactColumnItemBudget(1) = %d, want 0", got)
	}
}

// TestCompactColumnItemBudget_NonPositive_ReturnsZero verifies a
// non-positive column budget (a terminal too short to show anything past
// the header) yields zero compact entries, matching columnItemBudget's own
// guard (issue #1752).
func TestCompactColumnItemBudget_NonPositive_ReturnsZero(t *testing.T) {
	if got := compactColumnItemBudget(0); got != 0 {
		t.Errorf("compactColumnItemBudget(0) = %d, want 0", got)
	}
}

// TestSectionPageSize_Compact_SmallerThanClassic verifies a pgup/pgdown page
// jump (sectionPageSize) shrinks once the sidebar docks and the compact form
// takes over — each entry now spends more than one screen line, so a page
// holds fewer of them — proving sectionPageSize actually picks up
// queueItemBudget's compact branch rather than reusing the classic
// one-line-per-item budget regardless of layout (issue #1752).
func TestSectionPageSize_Compact_SmallerThanClassic(t *testing.T) {
	base := Update(NewModel(), SizeChangedMsg{Width: sidebarMinListWidth + sidebarWidth + dockedBorderCols, Height: 24})
	picks := make([]Pick, 20)
	for i := range picks {
		picks[i] = Pick{Number: fmt.Sprintf("%d", i+1), Title: "t", State: PickRunning, Age: "1m"}
	}
	base = Update(base, QueueSnapshotMsg{Picks: picks})
	base = Update(base, SectionJumpMsg{Section: SectionRunning})

	classic := sectionPageSize(base)
	docked := Update(base, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "hi"}}})
	compact := sectionPageSize(docked)

	if compact >= classic {
		t.Errorf("sectionPageSize() docked = %d, classic = %d, want the docked/compact page size smaller — compact rows spend more than one line each", compact, classic)
	}
}

// TestComputeSidebarWidth_MinimumFittingWidth_ReturnsFloor verifies that at
// the narrowest width sidebarFits still allows docking (sidebarMinListWidth +
// sidebarWidth + 1), the computed sidebar width is exactly the sidebarWidth
// floor — there's no room to grow past it without violating the queue list's
// own sidebarMinListWidth floor (issue #1751).
func TestComputeSidebarWidth_MinimumFittingWidth_ReturnsFloor(t *testing.T) {
	got := computeSidebarWidth(sidebarMinListWidth + sidebarWidth + dockedBorderCols)
	if got != sidebarWidth {
		t.Errorf("computeSidebarWidth(%d) = %d, want the floor %d", sidebarMinListWidth+sidebarWidth+dockedBorderCols, got, sidebarWidth)
	}
}

// TestComputeSidebarWidth_WideTerminal_TargetsFortyFivePercent verifies a
// 160-column terminal — the issue's own worked example, where the sidebar
// used to be pinned at 42 and the queue absorbed the other ~117 — grows the
// sidebar to 45% of the window (72) with plenty of room left for the queue's
// own sidebarMinListWidth floor (issue #1751).
func TestComputeSidebarWidth_WideTerminal_TargetsFortyFivePercent(t *testing.T) {
	got := computeSidebarWidth(160)
	if want := 72; got != want {
		t.Errorf("computeSidebarWidth(160) = %d, want %d (45%% of 160)", got, want)
	}
}

// TestComputeSidebarWidth_ModeratelyWideTerminal_ClampsToQueueFloor verifies
// a terminal too narrow for a full 45% share to leave the queue list its
// sidebarMinListWidth floor clamps the sidebar down instead — the queue must
// never shrink below its floor even though there's room to dock at all
// (issue #1751).
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

// TestView_SidebarOpen_WideTerminal_SidebarGrowsPastFloor verifies the
// docked sidebar's actual rendered content clips to computeSidebarWidth's
// wider column, not the old fixed 42-column floor, once the terminal is wide
// enough to grow it (issue #1751). A long Activity line makes the clip
// boundary observable: it truncates to exactly the computed width.
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

// TestView_SidebarOpen_CompactQueueRows_SidebarKeepsComputedWidth verifies
// the compact/wrapped queue form doesn't claw back the width #1751's
// rebalance granted the docked sidebar: with compact rows rendering (a work
// pick present, log open), the sidebar's own content still clips to exactly
// computeSidebarWidth, the same as with an empty queue (issue #1752 AC: "the
// activity stream retains the extra width granted by the rebalanced split").
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

// TestView_SidebarOpen_WideTerminal_QueueNarrowsForWiderSidebar verifies the
// docked queue list's title column shrinks to make room for the wider
// sidebar computeSidebarWidth grants it, rather than staying pinned at the
// width it had when the sidebar was a fixed 42 columns (issue #1751). A long
// Backlog title makes the clip boundary observable the same way the sidebar
// content test does.
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

// TestView_SidebarClose_WideTerminal_QueueRestoresFullWidth verifies closing
// the docked sidebar on a wide terminal restores the queue list's title
// column to the width it would have with no sidebar at all — the rebalanced
// split must not leave a stale narrower list behind once the log closes
// (issue #1751).
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

// TestView_SidebarFooter_WideTerminal_RendersFullHintsUnclipped verifies the
// docked footer's fixed, tight-spaced hint text — hand-tuned to survive
// clipping at the 42-column floor — still renders in full, unclipped, once
// computeSidebarWidth grows the sidebar past that floor (issue #1751).
func TestView_SidebarFooter_WideTerminal_RendersFullHintsUnclipped(t *testing.T) {
	const width = 160
	m := Update(NewModel(), SizeChangedMsg{Width: width, Height: 24})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "hi"}}})

	out := View(m)
	if !strings.Contains(out, "[t] cycle ·[h] list ·[x] close ·[z] ·H/L") {
		t.Errorf("View() = %q, want the docked footer's full, unclipped hint text at the wider computed sidebar width", out)
	}
}

// TestView_SidebarDocked_FooterStyledDim verifies the docked sidebar's
// keystroke-hint footer renders dim (RoleDim, ANSI slot 8 — "\x1b[90m",
// confirmed against the panel border's own existing golden fixture) via the
// shared footer renderer, without disturbing the tight "·" separators and
// footerHintCompact wording the 42-column docked budget already needs
// (issue #1791).
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

// TestView_SidebarDocked_FooterAdvertisesHL verifies the docked (log view)
// footer, even at the tight 42-column floor, includes the "H/L" hint — H/L
// closing the log and switching Section is discoverable there too, not just
// in the fullscreen layout (issue #1846).
func TestView_SidebarDocked_FooterAdvertisesHL(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: sidebarMinListWidth + sidebarWidth + dockedBorderCols, Height: 24})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "hi"}}})

	out := View(m)
	if !strings.Contains(out, "[t] cycle ·[h] list ·[x] close ·[z] ·H/L") {
		t.Errorf("View() = %q, want the docked sidebar's footer to advertise H/L within the 42-column floor", out)
	}
}

// TestRenderFooterHints_NarrowWidth_ClipsWithEllipsis verifies
// renderFooterHints' own width param actually truncates an over-wide hint
// line — no caller today ever hands it a width narrower than its hints
// (the docked sidebar's 42-column floor comfortably fits its 40-column
// compact footer), so this exercises the clip branch directly rather than
// leaving it uncovered (issue #1791 review).
func TestRenderFooterHints_NarrowWidth_ClipsWithEllipsis(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	got := renderFooterHints(ModeSidebar, []string{"t", "h", "x", "z"}, 10, true)
	if want := "\x1b[90m[t] cycle…\x1b[0m"; got != want {
		t.Errorf("renderFooterHints(...) = %q, want %q — clipped to 10 columns with a trailing ellipsis", got, want)
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

// TestView_SidebarDocked_LabelFoldedIntoTopBorder verifies the docked
// sidebar's label rides in its panel's top border rule, not as a separate
// interior row above the content — the same move the header and detail
// modal make with their own titles (issue #1799).
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

// TestView_SidebarDocked_TranscriptRawLabelFoldedIntoTopBorder verifies the
// border-fold covers every sidebarLabel mode, not just the Activity feed's
// default "[follow]" form — here the Transcript view's "(raw)" tag (issue
// #1799).
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

// TestView_SidebarDocked_BorderTitleColoredByFocus verifies the docked
// sidebar's border title carries the same focus signal the old interior
// label row did: accent when the sidebar is focused, dim otherwise (issue
// #1799).
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

// TestView_SidebarFooter_ShowsZoomHint verifies both the docked and
// fullscreen sidebar footers advertise the "z" zoom key — "[z]" in the
// docked footer's compact wording (shortened from "[z] zoom" to make room
// for the "H/L" hint within the 42-column floor, issue #1846), "[z] zoom"
// in fullscreen's uncompacted one (issue #1502).
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

// TestView_SidebarFullscreen_FooterStyledDim verifies the fullscreen
// sidebar's keystroke-hint footer renders dim, the same RoleDim treatment
// every other hint/border in the module already gets, via the shared
// footer renderer (issue #1791).
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

// TestView_SidebarZoom_WideTerminal_ForcesFullscreen verifies SidebarZoom
// forces the fullscreen takeover even on a terminal wide enough to dock —
// the "deep reading" zoom is an operator choice independent of sidebarFits'
// own narrow-terminal fallback (issue #1502, ADR 0030).
func TestView_SidebarZoom_WideTerminal_ForcesFullscreen(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: sidebarMinListWidth + sidebarWidth + dockedBorderCols, Height: 24})
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

// TestView_WorkSection_Compact_ShowsTwoLineRowWithFullTitle verifies a work
// row renders in the compact/wrapped two-line form — a "#num · state · age"
// header line, the title unclipped on its own line — once the queue column
// is narrowed by a docked sidebar, instead of the classic single-line
// table's aggressive clip() truncation (issue #1752).
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

// TestRenderWorkSection_Compact_LongExtrasClippedToWidth verifies a compact
// row's header line — cursor marker, number, state, age, and any trailing
// extras (blocker/reason/heartbeat) — never exceeds the column's width, the
// same extrasBudget discipline the classic single-line row applies (issue
// #1500), even though the compact header carries the extras unclipped in raw
// Sprintf form before this fix (issue #1752).
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

// TestRenderWorkSection_Compact_LongNumberAndAgeClippedToWidth verifies a
// compact row's header line stays within the column's width even given a
// pathologically long Number or Age — the classic form clips both to their
// own column widths, so the compact header applies the same defensive cap
// rather than leaving them unbounded (issue #1752 review).
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

// TestView_SidebarOpen_AtMinimumFittingWidth_CompactRowRendersCleanly pins
// compact-form behavior right at sidebarFits' own minimum fitting width —
// the narrowest the queue column ever renders at while docked
// (sidebarMinListWidth, the floor computeSidebarWidth clamps down to) — with
// a realistic pick, not a pathological one: the row must still show its
// number, state, age, and title with no line exceeding the column's width
// (issue #1752 review).
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

// TestRenderBacklogSection_Compact_LongLabelsClippedToWidth verifies a
// compact Backlog row's header line — cursor marker, number, and labels —
// never exceeds the column's width, accounting for every literal character
// ("#", the brackets, and their surrounding spaces) the "%s #%s [%s]\n"
// format spends outside marker/number/labels (issue #1752).
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

// TestRenderBacklogSection_Compact_LongNumberClippedToWidth verifies a
// compact Backlog row's header line stays within the column's width even
// given a pathologically long issue number — parity with compactWorkRow's
// own number clip and the classic row's numberColWidth clip (issue #1752
// review).
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

// TestCompactQueueSeparator_ZeroWidth_ClampsToOne verifies a non-positive
// width still renders a one-glyph rule rather than an empty or negative
// strings.Repeat count, which would panic (issue #1752).
func TestCompactQueueSeparator_ZeroWidth_ClampsToOne(t *testing.T) {
	got := strings.TrimSuffix(compactQueueSeparator(0), "\n")
	// Styled through the same roleStyle call, not a bare literal: on a
	// color-capable profile the glyph carries ANSI escapes a raw string
	// comparison would miss (issue #1752 review's ANSI-boundary lesson).
	if want := roleStyle(RoleDim).Render(compactQueueSeparatorGlyph); got != want {
		t.Errorf("compactQueueSeparator(0) = %q, want the single-glyph floor %q", got, want)
	}
}

// TestCompactWorkRow_ZeroWidth_DoesNotPanic verifies compactWorkRow clamps
// its title column to at least one, rather than panicking or emitting a
// pathological clip() call, at a width too small for any real column (issue
// #1752).
func TestCompactWorkRow_ZeroWidth_DoesNotPanic(t *testing.T) {
	got := compactWorkRow(0, ">", Pick{Number: "1", State: PickRunning, Age: "1m"}, "title", RoleRunning, "")
	if !strings.Contains(got, "\n") {
		t.Errorf("compactWorkRow(0, ...) = %q, want at least the header and title lines", got)
	}
}

// TestCompactBacklogRow_ZeroWidth_DoesNotPanic verifies compactBacklogRow
// clamps its title column to at least one at a width too small for any real
// column (issue #1752).
func TestCompactBacklogRow_ZeroWidth_DoesNotPanic(t *testing.T) {
	got := compactBacklogRow(0, ">", "1", "title", nil)
	if !strings.Contains(got, "\n") {
		t.Errorf("compactBacklogRow(0, ...) = %q, want at least the header and title lines", got)
	}
}

// TestView_WorkSection_Compact_SeparatorBetweenAdjacentIssues verifies the
// compact/wrapped form separates two adjacent issues with exactly one faint
// delimiter row — the subtle rule the two-line stacked entries need so they
// don't run together (issue #1752).
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
	// the rule in the joined output — match the rule's content only.
	sep := strings.TrimSuffix(compactQueueSeparator(listWidth), "\n")
	if got := strings.Count(out, sep); got != 1 {
		t.Errorf("View() = %q, want exactly one separator %q between the two issues, got %d", out, sep, got)
	}
}

// TestView_WorkSection_Compact_CursorMarksHighlightedRow verifies the
// compact/wrapped form still marks the row at m.Cursor — selection and
// highlight keep working once the queue column narrows (issue #1752 AC).
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

// TestRenderWorkSection_Compact_ItemBudgetNeverOverflowsColumnBudget verifies
// the compact/wrapped form's item budget accounts for its own multi-line,
// separator-bearing rows rather than reusing the classic form's
// one-line-per-item assumption — otherwise a handful of picks blows well
// past the column's row budget instead of windowing down to what fits
// (issue #1752).
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

// TestView_WorkSection_SidebarClosed_RendersClassicSingleLineForm verifies
// that with no sidebar open — full window width — a work row renders exactly
// as the classic single-line clip()ped table, never the compact/wrapped form
// (issue #1752 AC: "at full window width, queue rows render unchanged from
// today").
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

// TestView_BacklogSection_SidebarClosed_RendersClassicSingleLineForm verifies
// that with no sidebar open — full window width — a Backlog row renders
// exactly as the classic single-line clip()ped table, never the
// compact/wrapped form (issue #1752 AC), the Backlog counterpart to
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

// TestView_BacklogSection_Compact_ShowsTwoLineRowWithFullTitle verifies a
// Backlog row renders in the compact/wrapped two-line form — a "#num"
// header line, the title unclipped on its own line — once the queue column
// is narrowed by a docked sidebar, instead of the classic single-line
// table's aggressive clip() truncation (issue #1752).
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

// TestView_BacklogSection_Compact_SeparatorBetweenAdjacentIssues verifies
// the Backlog Section's compact/wrapped form also separates two adjacent
// issues with exactly one faint delimiter row (issue #1752).
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

// TestView_ResearchPick_ShowsMarker verifies a research-kind pick's row
// carries a marker distinct from a work pick's row, driven off Pick.Kind
// (issue #1710) — the console needs a way to tell an operator, at a glance,
// which queued/in-flight picks are research-only versus real work.
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

// TestView_WorkPick_HasNoResearchMarker verifies a work-kind pick's row
// stays free of the research marker — the marker tags research picks only,
// leaving a work pick's row unchanged (issue #1710).
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

	out := renderBacklogSection(m, len(issues)+1, false)
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

	out := renderWorkSection(m, len(picks)+1, false)
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

// TestView_SidebarFullscreen_FooterAdvertisesHL verifies the fullscreen
// sidebar's (log view's) footer includes the "H/L" hint, so H/L closing the
// log and switching Section — previously a silent no-op there — is
// discoverable the same way the list's own H/L hint already is (issue
// #1846).
func TestView_SidebarFullscreen_FooterAdvertisesHL(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 100, Height: 24})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "hi"}}})

	out := View(m)
	if !strings.Contains(out, "H/L") {
		t.Errorf("View() = %q, want the fullscreen sidebar's footer to advertise H/L", out)
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

// TestView_LongBacklog_FitsHeightWithBannerAndFooterPinned verifies the
// top banner and bottom footer both survive at full budget: a Backlog long
// enough to show "… N more below" must still render no more physical lines
// than m.Height, counted without trimming the trailing newline (issue
// #1794) — the existing height tests' own strings.TrimRight(out, "\n")
// before counting is exactly why this regression slipped through.
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
	// Split, not TrimRight-then-count: View()'s output always ends in
	// exactly one trailing "\n" (its own documented convention), and that
	// trailing "\n" costs the terminal a physical row of its own — printing
	// it at the very bottom of an already-full m.Height budget is what
	// scrolls the pinned top banner off-screen. Trimming first (the
	// existing height tests' approach) throws away exactly the row this
	// regression turns on.
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

// TestView_ExactFitBacklog_FitsHeightWithBannerAndFooterPinned verifies a
// Backlog sized to land exactly on the pre-reservation item budget — total
// == itemBudget, the one case #1794's own fix left unreserved since its
// condition (total > itemBudget) only fires once total spills past the
// budget, not when it just fills it — still never renders more physical
// lines than m.Height (issue #1825). Split, not TrimRight-then-count:
// View()'s output always ends in exactly one trailing "\n" (its own
// documented convention), and that trailing "\n" costs the terminal a
// physical row of its own — reserving it here converts what would have been
// an invisible, unreserved exact-fit into a correctly-labeled "… N more
// below" instead of silently overrunning Height.
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

// TestView_VeryShortTerminal_FitsHeightWithBannerPinned verifies a Backlog
// on a terminal too short to show any item row at all (m.Height <= ~6)
// still never renders more physical lines than m.Height, and the top
// banner survives regardless — even at the very bottom of that range,
// where the header alone (unboxed, with the Section tabs line also
// collapsed) is all that fits. The bottom footer joins it once Height
// leaves room for it. Split, not TrimRight-then-count, for the same reason
// TestView_ExactFitBacklog_FitsHeightWithBannerAndFooterPinned uses it:
// View()'s own guaranteed trailing "\n" costs the terminal a physical row
// that trimming would hide (issue #1825).
func TestView_VeryShortTerminal_FitsHeightWithBannerPinned(t *testing.T) {
	issues := make([]forge.Issue, 20)
	for i := range issues {
		issues[i] = forge.Issue{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("issue %d", i)}
	}

	// wantFooter only turns true at height 6: below that, the header (and,
	// once there's room, the Section tabs line) already consume the whole
	// budget, leaving no row for the footer to claim — its absence there
	// isn't a regression, just this range's own collapse order.
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
	// Height 10 plus boxBorderRows pays for the header's own bordered panel
	// (issue #1756); the header itself now costs only 3 rows — the
	// "spindrift" wordmark folds into its top border rule rather than a
	// separate banner (issue #1798) — preserving the item budget (and so
	// the exact position ranges below) this test was written against. Plus
	// listFooterLines, ModeList's own pinned footer row (issue #1792), so
	// that reservation doesn't eat into the item budget this test pins.
	// viewBody's own budget holds one further row back for View()'s
	// guaranteed trailing "\n" (issue #1825), so this stays "5"/"10" rather
	// than "6"/"11".
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

// TestView_WorkSection_ShowsPositionIndicator verifies a work Section's
// column header carries the same position indicator as the Backlog Section,
// and that it is absent when the Section is empty rather than reading
// "(1-0 of 0)" (issue #1037 AC3/AC4).
func TestView_WorkSection_ShowsPositionIndicator(t *testing.T) {
	// Height 10 plus boxBorderRows pays for the header's own bordered panel
	// (issue #1756); the header itself now costs only 3 rows — the
	// "spindrift" wordmark folds into its top border rule rather than a
	// separate banner (issue #1798) — preserving the item budget (and so
	// the exact position range below) this test was written against. Plus
	// listFooterLines, ModeList's own pinned footer row (issue #1792), so
	// that reservation doesn't eat into the item budget this test pins.
	// viewBody's own budget holds one further row back for View()'s
	// guaranteed trailing "\n" (issue #1825), so this stays "(1-5 of 50)"
	// rather than "(1-6 of 50)".
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

// TestView_HeaderHeight_TooShortToBox_StillBudgetsBody verifies the body
// windowing still leaves the status line visible and never overruns Height
// on a terminal too short to afford the titled-border header at all — the
// unboxed header's own (smaller) height, not the boxed one, must drive the
// budget (issue #1035 AC3, extended to the titled border by issue #1798). At
// Height 2, renderBoxedHeader's own fitness check (its minimum boxed render
// is 3 rows) falls back to the unboxed header, leaving no room for any
// backlog row at all. sectionTabsReserved now also collapses the Section
// tabs line here: showing it (unboxed header's 1 row plus the tabs line's
// own 1 row) would land right on Height, leaving no row over for View()'s
// own guaranteed trailing "\n" (issue #1825) — so the header alone renders,
// under Height rather than exactly filling it.
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
	if w := runewidth.StringWidth(got); w != 10 {
		t.Errorf("clip(%q, 10, false) = %q with display width %d, want exactly 10", s, got, w)
	}
}

// TestClip_Pad_WideCharacterStraddlesBoundary_LandsExactlyOnWidth verifies
// clip(..., pad: true) — the shape Backlog/Work fixed-width table columns
// use — lands on exactly the requested width even when a wide (2-column)
// rune straddles the truncation boundary (issue #1785), not one column
// short, so a fixed-width column never drifts by a space.
func TestClip_Pad_WideCharacterStraddlesBoundary_LandsExactlyOnWidth(t *testing.T) {
	got := clip("ab中文", 4, true)
	if w := runewidth.StringWidth(got); w != 4 {
		t.Errorf("clip(%q, 4, true) = %q with display width %d, want exactly 4", "ab中文", got, w)
	}
}

// TestPadDisplay_TruncatesWithEllipsis verifies padDisplay marks a
// truncated overflow with a trailing "…", mirroring clip's ellipsis
// (issue #1779) instead of silently dropping the cut content.
func TestPadDisplay_TruncatesWithEllipsis(t *testing.T) {
	got := padDisplay("supercalifragilisticexpialidocious", 10)
	if !strings.HasSuffix(got, "…") {
		t.Errorf("padDisplay(...) = %q, want a trailing ellipsis marking the cut", got)
	}
	if w := runewidth.StringWidth(got); w != 10 {
		t.Errorf("padDisplay(...) = %q with display width %d, want exactly 10", got, w)
	}
}

// TestPadDisplay_WideCharacterStraddlesBoundary_LandsExactlyOnWidth verifies
// padDisplay's truncation lands on exactly the requested width even when a
// wide (2-column) rune straddles the boundary (issue #1785) — the detail
// modal box's right border drifts out of column otherwise (issue #1758).
func TestPadDisplay_WideCharacterStraddlesBoundary_LandsExactlyOnWidth(t *testing.T) {
	s := "中文标题超长测试文字" // 10 runes, 20 display columns
	got := padDisplay(s, 10)
	if w := runewidth.StringWidth(got); w != 10 {
		t.Errorf("padDisplay(%q, 10) = %q with display width %d, want exactly 10", s, got, w)
	}
}

// TestPadDisplay_WidthOne_TruncatesWithoutEllipsis verifies padDisplay
// falls back to a plain truncate, without the ellipsis, when width is too
// narrow to fit even one cut character plus the ellipsis itself — the same
// width<=1 edge case clip guards against.
func TestPadDisplay_WidthOne_TruncatesWithoutEllipsis(t *testing.T) {
	got := padDisplay("overflow", 1)
	if w := runewidth.StringWidth(got); w != 1 {
		t.Errorf("padDisplay(...) = %q with display width %d, want exactly 1", got, w)
	}
	if strings.Contains(got, "…") {
		t.Errorf("padDisplay(...) = %q, want no ellipsis when width is too narrow to fit one", got)
	}
}

// TestView_DetailModal_OverWideLabel_ShowsEllipsis verifies the floating
// detail modal marks an over-wide label with a trailing ellipsis rather
// than silently cutting it (issue #1779): a single unbroken label wider
// than the box's interior stands alone on its own wrapText line (issue
// #1772's TestWrapText_WordWiderThanWidth_StandsAlone), which then hits
// the label row's own clip-before-style truncation (issue #1832). The
// pinned row's leading "[" is part of that same wrapText word — brackets
// count toward the width budget rather than sitting on top of it — so the
// cut lands one column earlier than the label's own text alone would.
// Forces a color-capable terminal (rather than trusting whatever TERM the
// test happens to inherit) so this actually exercises the styled path: a
// dumb/unset TERM makes roleStyle a no-op, which would hide a regression
// where the label is clipped after — not before — RoleDim wraps it in SGR
// bytes that a runewidth-based truncate would then miscount and mangle.
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

// TestView_DetailModal_WideCharacterLabel_BorderStaysAligned verifies the
// floating detail modal's right-hand border rune stays in column even when
// an over-wide CJK label straddles the truncation boundary (issue #1785):
// every "│...│" row must measure exactly innerWidth display columns between
// its borders, or the right border drifts (issue #1758's invariant). Forces
// a color-capable terminal so the label row's own RoleDim styling (issue
// #1832) is actually in play — ansi.StringWidth must still measure the
// styled row's plain-text width correctly, escape bytes excluded.
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

// TestWrapText_GreedilyFillsLinesToWidth verifies wrapText packs words onto
// each line up to width display columns, wrapping to a new line only once
// the next word would overflow it — the detail modal body's own word-wrap
// (issue #1632; no markdown renderer in the dependency tree, so this is
// hand-rolled rather than glamour).
func TestWrapText_GreedilyFillsLinesToWidth(t *testing.T) {
	got := wrapText("the quick brown fox jumps", 10)
	want := []string{"the quick", "brown fox", "jumps"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("wrapText(...) = %v, want %v", got, want)
	}
}

// TestWrapText_PreservesBlankLines verifies wrapText keeps paragraph breaks
// (blank lines) in the source text as blank lines in the output, rather than
// collapsing them into the surrounding wrapped text.
func TestWrapText_PreservesBlankLines(t *testing.T) {
	got := wrapText("first paragraph\n\nsecond paragraph", 40)
	want := []string{"first paragraph", "", "second paragraph"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("wrapText(...) = %v, want %v", got, want)
	}
}

// TestWrapText_WordWiderThanWidth_StandsAlone verifies a single word wider
// than the wrap width is placed alone on its own line rather than broken
// mid-word.
func TestWrapText_WordWiderThanWidth_StandsAlone(t *testing.T) {
	got := wrapText("a supercalifragilisticexpialidocious word", 10)
	want := []string{"a", "supercalifragilisticexpialidocious", "word"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("wrapText(...) = %v, want %v", got, want)
	}
}

// TestDetailModalLabelLines_WrapsOntoMultipleLines verifies the labels line
// wraps across further interior rows once it overflows width, rather than
// staying a single unwrapped string for padDisplay to silently truncate
// (issue #1772), and that the wrapped block reads as labels at a glance: the
// backlog row's own bracketed idiom (issue #1832), dim-styled (RoleDim,
// "\x1b[90m") the same way the footer hints already are, with the bracket
// characters themselves counted toward the width budget rather than added on
// top of it.
func TestDetailModalLabelLines_WrapsOntoMultipleLines(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	got := detailModalLabelLines([]string{"alpha", "bravo", "charlie"}, 15)
	want := []string{"\x1b[90m[alpha, bravo,\x1b[0m", "\x1b[90mcharlie]\x1b[0m"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("detailModalLabelLines(...) = %v, want %v", got, want)
	}
}

// TestDetailModalLabelLines_SanitizesControlSequences verifies a label
// carrying CSI/OSC escape sequences is stripped before wrapping — a tracker
// label is untrusted input (issue #862) — and that the bracketed, dim-styled
// treatment (issue #1832) still applies once sanitized.
func TestDetailModalLabelLines_SanitizesControlSequences(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	got := detailModalLabelLines([]string{"evil\x1b[2Jlabel"}, 40)
	want := []string{"\x1b[90m[evillabel]\x1b[0m"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("detailModalLabelLines(...) = %v, want %v", got, want)
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
	got := renderTable("header\n", []string{"row1\n"}, Viewport{}, 1, 0, "")
	if want := "header\n"; got != want {
		t.Errorf("renderTable() with itemBudget 0 = %q, want %q", got, want)
	}
}

// TestRenderTable_RendersHeaderAndWindowedRows verifies renderTable writes
// the header line followed by every row when they all fit within itemBudget,
// matching the convention renderBacklogSection/renderWorkSection applied
// inline before extraction.
func TestRenderTable_RendersHeaderAndWindowedRows(t *testing.T) {
	got := renderTable("header\n", []string{"row1\n", "row2\n"}, Viewport{}, 2, 2, "")
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
	got := renderTable("header\n", []string{"row1\n", "row2\n", "row3\n"}, vp, 3, 2, "")
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
	got := renderTable("header\n", rows, Viewport{}, 50, 4, "")
	want := "header\nrow0\nrow1\nrow2\n… 47 more below\n"
	if got != want {
		t.Errorf("renderTable() = %q, want %q", got, want)
	}
}

// TestRenderBoxedColumn_TitleFoldedIntoTopBorder verifies a titled panel's
// top border reads "╭─ <title> ─…─╮" — the title folded into the rule
// itself rather than sitting on an interior content row (issue #1797).
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

// TestRenderBoxedColumn_Titled_NoColor_DegradesToAscii verifies a titled
// panel's border degrades to the plain ASCII glyph set under NO_COLOR — the
// gap the detail modal's old hand-rolled Unicode-only top border left
// (issue #1797): previously the modal never degraded at all, unlike every
// other border in the package.
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

// TestRenderBoxedColumn_TitleWiderThanPanel_TruncatesWithEllipsis verifies a
// title too wide for the panel truncates with a trailing ellipsis and the
// top rule still lands on exactly the panel's width (issue #1797 AC) —
// never overflowing past width regardless of how long the title is.
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

// TestRenderBoxedColumn_NoTitle_PlainRule verifies an untitled panel's top
// border is the plain rounded rule, with no title-folding machinery
// engaged — the header/docked-list/docked-sidebar call sites' shape,
// unchanged by title support landing in the same helper (issue #1797 AC).
func TestRenderBoxedColumn_NoTitle_PlainRule(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	got := renderBoxedColumn("hello", 20, "", RoleDim)
	top := strings.SplitN(got, "\n", 2)[0]
	want := "+" + strings.Repeat("-", 20) + "+"
	if top != want {
		t.Errorf("renderBoxedColumn(...) top border = %q, want %q", top, want)
	}
}

// TestRenderBoxedColumn_TitledNarrowPanel_TopRuleStaysExactWidth verifies a
// titled panel too narrow even for the dash lead-in still lands the top
// rule on exactly the requested width columns — a panel narrower than the
// title's own structural "─ " lead-in and trailing space must clamp the
// whole rule together rather than overflow it (issue #1797 review).
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

package passmachine

import (
	"bufio"
	"regexp"
	"strings"

	"spindrift.dev/launcher/internal/outcome"
)

// verdictBlockToken and verdictApproveToken are the exact reviewer verdict
// markers this file greps a rendered transcript for, composed from
// outcome.ReviewVerdictToken the same way orchestrator/markers.go builds its
// own VerdictBlock/VerdictApprove -- so neither package hardcodes the bare
// "VERDICT:" literal a second time (lib/prompt-contract.nix's markerChannels
// registry is the one source of truth for it).
const (
	verdictBlockToken   = outcome.ReviewVerdictToken + " BLOCK"
	verdictApproveToken = outcome.ReviewVerdictToken + " APPROVE"
)

// ScanResult is passmachine.Scan's own result.
type ScanResult struct {
	Verdict Verdict
	// BlockLine is the index (into strings.Split(rendered, "\n")) of the
	// rendered line whose match determined Verdict -- meaningful only for
	// KindReview (a caller extracting the reviewer's findings text continues
	// reading from here), -1 for every other kind or when Verdict is
	// VerdictNone.
	BlockLine int
}

// Scan is passmachine's pure verdict scanner (issue #2980): given a driver's
// already-rendered transcript text (Driver.RenderTranscript's own
// "[role] text" / "[role]   -> summary" / "[role]   -> [subagentRole]
// summary" convention) and the pass kind that produced it, returns the
// verdict the loop should act on, using the match rule that pass kind's own
// contract requires.
func Scan(rendered string, kind PassKind) ScanResult {
	if kind == KindReview {
		return scanReviewVerdict(rendered)
	}
	return scanSubagentReviewVerdict(rendered)
}

// renderedRolePrefixRe extracts the bracketed role name a rendered line
// leads with -- "[role] text" for an assistant-authored event, "[role]  ->
// summary" for a tool_result echo. Capture group 1 is the role name. A line
// with no match at all is a bare physical-line continuation of a prior
// multi-line rendered entry.
var renderedRolePrefixRe = regexp.MustCompile(`^\[([^\]]*)\]`)

// renderedEventPrefix matches RenderTranscript's own "[role] " event prefix
// at the start of a line.
var renderedEventPrefix = regexp.MustCompile(`^\[\S+\] `)

// scanReviewVerdict ports run.go's scanReviewLog verdict half verbatim
// (strict-first-line, last-block-wins): a review pass's verdict is its own
// top-level final assistant message, so only a top-level "[reviewer]"-role
// block's FIRST line counts, and only when that line strictly STARTS WITH a
// verdict token -- unlike the non-review fold below, a finding quoting
// "VERDICT: APPROVE" elsewhere in the same message never counts. Kept as its
// own function, physically disjoint from scanSubagentReviewVerdict, so
// review-prompt.md's own-message contract and the tool_result-tag contract
// below it can never be accidentally merged or reordered against each
// other by a future edit to either.
func scanReviewVerdict(rendered string) ScanResult {
	lines := strings.Split(rendered, "\n")
	blockLine := -1
	var verdict Verdict
	for i, line := range lines {
		m := renderedRolePrefixRe.FindStringSubmatch(line)
		if m == nil || m[1] != "reviewer" {
			continue
		}
		text := line
		if loc := renderedEventPrefix.FindStringIndex(text); loc != nil {
			text = text[loc[1]:]
		}
		switch {
		case strings.HasPrefix(text, verdictBlockToken):
			verdict = VerdictBlock
			blockLine = i
		case strings.HasPrefix(text, verdictApproveToken):
			verdict = VerdictApprove
			blockLine = i
		}
	}
	if blockLine == -1 {
		return ScanResult{Verdict: VerdictNone, BlockLine: -1}
	}
	return ScanResult{Verdict: verdict, BlockLine: blockLine}
}

// reviewerToolResultPrefixRe matches a tool_result line RenderTranscript
// tagged with a completed "reviewer" subagent report (transcript_render.go's
// "user" case, issue #2980 slice 1): "[outerRole]   -> [reviewer] summary".
// An ordinary tool_result (no recorded Task/Agent spawn behind its
// tool_use_id) or one tagged with any OTHER subagent role never matches this
// prefix, so it's never eligible for the fold below -- the actual security
// fix this issue lands: attacker-controlled Bash/Read output echoing the
// literal verdict string can no longer flip a non-review pass's fold.
var reviewerToolResultPrefixRe = regexp.MustCompile(`^\[[^\]]*\]   -> \[reviewer\] `)

// scanSubagentReviewVerdict ports run.go's scanPassLog verdict half
// (BLOCK-dominant fold), narrowed to only the lines a completed reviewer
// subagent's own tool_result actually carries. BLOCK-dominant, not
// last-match-wins, remains correct even after scoping: an eligible tagged
// line can still itself carry attacker-influenced text after a genuine
// reviewer verdict word within the same rendered summary (the subagent's
// own report can quote earlier tool output), so a BLOCK anywhere among
// eligible lines still wins outright over an APPROVE anywhere else among
// eligible lines, regardless of order. Kept as its own function, physically
// disjoint from scanReviewVerdict, for the same reason implementFixTransition
// and terminalLandTransition are kept apart in passmachine.go: the two rule
// sets must never be reordered against each other by a future case added to
// either one.
func scanSubagentReviewVerdict(rendered string) ScanResult {
	var sawBlock, sawApprove bool
	sc := bufio.NewScanner(strings.NewReader(rendered))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !reviewerToolResultPrefixRe.MatchString(line) {
			continue
		}
		switch {
		case strings.Contains(line, verdictBlockToken):
			sawBlock = true
		case strings.Contains(line, verdictApproveToken):
			sawApprove = true
		}
	}
	switch {
	case sawBlock:
		return ScanResult{Verdict: VerdictBlock, BlockLine: -1}
	case sawApprove:
		return ScanResult{Verdict: VerdictApprove, BlockLine: -1}
	default:
		return ScanResult{Verdict: VerdictNone, BlockLine: -1}
	}
}

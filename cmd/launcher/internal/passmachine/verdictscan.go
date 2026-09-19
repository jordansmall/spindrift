package passmachine

import (
	"bufio"
	"regexp"
	"strings"

	"spindrift.dev/launcher/internal/outcome"
)

// Composed from outcome.ReviewVerdictToken the same way orchestrator/markers.go
// builds its own VerdictBlock/VerdictApprove, so neither package hardcodes the
// bare "VERDICT:" literal. lib/prompt-contract.nix's markerChannels registry is
// the one source of truth for it.
const (
	verdictBlockToken   = outcome.ReviewVerdictToken + " BLOCK"
	verdictApproveToken = outcome.ReviewVerdictToken + " APPROVE"
)

// ScanResult is what Scan returns.
type ScanResult struct {
	Verdict Verdict
	// BlockLine indexes into strings.Split(rendered, "\n"), for a caller that
	// reads on from there to collect the reviewer's findings. Only KindReview
	// sets it; every other kind, and VerdictNone, leave it -1.
	BlockLine int
}

// Scan returns the verdict the loop should act on, reading the "[role]" line
// format Driver.RenderTranscript produces (issue #2980). Each pass kind has its
// own match rule, so the caller must pass the kind that produced the text.
func Scan(rendered string, kind PassKind) ScanResult {
	if kind == KindReview {
		return scanReviewVerdict(rendered)
	}
	return scanSubagentReviewVerdict(rendered)
}

// Capture group 1 is the bracketed role name. A line that does not match is a
// bare physical-line continuation of a prior multi-line rendered entry.
var renderedRolePrefixRe = regexp.MustCompile(`^\[([^\]]*)\]`)

var renderedEventPrefix = regexp.MustCompile(`^\[\S+\] `)

// scanReviewVerdict is strict-first-line, last-block-wins: a review pass states
// its verdict in its own top-level final assistant message, so only a top-level
// "[reviewer]" line counts, and only when it starts with a verdict token. A
// finding quoting "VERDICT: APPROVE" mid-message never counts. Kept physically
// apart from scanSubagentReviewVerdict so the two rule sets cannot be merged.
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

// Matches only a tool_result line tagged with a completed "reviewer" subagent
// report (issue #2980). An ordinary tool_result, or one tagged with another
// subagent role, cannot match, which is the security fix: attacker-controlled
// Bash or Read output echoing the literal verdict string can no longer flip a
// non-review pass's fold.
var reviewerToolResultPrefixRe = regexp.MustCompile(`^\[[^\]]*\]   -> \[reviewer\] `)

// scanSubagentReviewVerdict folds BLOCK-dominant over the reviewer-tagged lines
// only. BLOCK-dominant rather than last-match-wins because an eligible line can
// still carry attacker-influenced text after a genuine verdict word (the
// subagent's report may quote earlier tool output), so a BLOCK anywhere wins
// over an APPROVE anywhere, whatever the order.
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

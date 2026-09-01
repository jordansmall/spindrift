package settle

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
)

// hostMediateDraftPR resolves num's real PR URL under
// BOX_FORGE_AND_ISSUE_ACCESS=read-only for a PR-shaped Code Forge: the Box
// holds no push or PR-create token, so before selfHeal can watch CI on a PR,
// settle must relay the Box's finished branch (forge.BundleRelay) and open the
// draft PR itself (forge.DraftPRCreator), ordinarily from the Box's PR-intent
// line.
//
// The relay always runs, whether or not a usable PR-intent line was found: a
// Box can finish real, mergeable work and simply fail to print its last line,
// leaving the branch itself fine. Title/body are then reconstructed host-side
// from the relayed branch's commits rather than blocking a hand-off with
// nothing actually wrong with it, and an issue comment says so — an operator
// reading only the issue must still be able to tell. Both the stdout line and
// the comment fire only when CreateDraftPR actually created a fresh PR: when
// it adopts a pre-existing box-authored PR the reconstructed title/body were
// never applied, so claiming reconstruction would be false.
//
// Returns the resolved PR URL and true on success. Any failure along the way
// posts a comment and leaves the issue in-progress (blockHandoff: visibly
// not-done, never demoted to agent-failed and never mistakable for
// agent-complete), then returns ok=false so the caller skips CI-watch rather
// than polling a PR that was never opened.
//
// branch is derived from cf.AgentBranch(num), never from the outcome line's
// own landing= field: a prompt-injected read-only Box holds no write token,
// but it does control both landing= and its own bundle's ref names, so
// trusting landing= here would let it steer the Launcher's force-push and
// draft-PR head at will.
func (s *Settle) hostMediateDraftPR(num string, result dispatch.Result) (string, bool) {
	branch, m := s.mediationFor(num)

	// Open's own upfront capability/config checks are unreachable outside a
	// misconfigured test double — the startup capability gate guarantees a
	// read-only PR-shaped Code Forge implements both BundleRelay and
	// DraftPRCreator. Any failure Open returns blocks here rather than
	// silently stranding the issue in agent-in-progress.
	url, created, source, err := m.Open(num, branch, result, FallbackReconstruct)
	if err != nil {
		return s.blockHandoff(num, branch, err)
	}
	if source == TextSourceReconstructed && created {
		fmt.Printf("    #%s  landing=%s  status=reconstructed  note=no PR-intent line found in the box's log; description derived host-side from the relayed branch's commits\n", num, branch)
		// Best-effort, matching postUsageComment's log-but-don't-propagate
		// contract: a failure here never un-does the draft PR already opened.
		if commentErr := s.it.Comment(num, "This PR was reconstructed host-side: the box's own hand-off was incomplete (no usable PR-intent line found in its log), so the title/body above were derived from the relayed branch's own commits instead of the box's own description."); commentErr != nil {
			fmt.Fprintf(os.Stderr, "    ?? #%s: could not post reconstructed-hand-off comment: %v\n", num, commentErr)
		}
	}
	return url, true
}

// relayBlockedWork gives a read-only Box's finished-but-blocked branch the
// same host-mediated relay hostMediateDraftPR gives the "ready" path: relay
// the outbox bundle so the branch itself is never lost, then open a draft PR
// from a PR-intent line if the Box left one — a blocked run may reach here
// before ever printing one (e.g. review never cleared), in which case only the
// relay runs.
//
// Unlike hostMediateDraftPR, every failure here just logs: the caller's
// "blocked" transition and comment already recorded the real outcome, so there
// is nothing to downgrade and no CI to skip watching. A RelayBundle failure
// wrapping forge.ErrBundleNotFound is doubly benign — an empty branch range
// left nothing in the outbox to relay — so it logs informationally. branch is
// derived from cf.AgentBranch(num), never o.Landing, for the same reason
// hostMediateDraftPR derives it.
//
// A push-only cf (forge.BundleRelay but not forge.DraftPRCreator, e.g. local)
// can't go through Mediation.Open at all — Open requires DraftPRCreator
// unconditionally, since every other caller is only ever reached for a
// PR-shaped forge. relayBlockedWork is the one caller that must also serve a
// push-only forge, so that shape keeps its own direct RelayBundle call.
func (s *Settle) relayBlockedWork(num string, result dispatch.Result) {
	branch, m := s.mediationFor(num)
	if m.br == nil || s.cfg.OutboxDir == nil {
		return
	}

	if m.dpc == nil {
		// Push-only shape: no draft-PR step to unify, so relay directly.
		if err := m.br.RelayBundle(s.cfg.OutboxDir(num), branch); err != nil {
			if errors.Is(err, forge.ErrBundleNotFound) {
				// An absent outbox bundle on the blocked path means the branch
				// range was empty — no work to preserve, not a relay failure.
				// With nothing to hand off there is also no branch to open a
				// draft PR against, so stop here as the error path does.
				logNoBlockedHandoffBundle(num)
				return
			}
			logBlockedHandoffRelayFailure(num, err)
		}
		return
	}

	if _, _, _, err := m.Open(num, branch, result, FallbackNone); err != nil {
		switch {
		case errors.Is(err, ErrNoPRIntent):
			// The relay already ran inside Open, so there is nothing more to do.
			return
		case errors.Is(err, forge.ErrBundleNotFound):
			logNoBlockedHandoffBundle(num)
		case errors.Is(err, errRelayBundle):
			logBlockedHandoffRelayFailure(num, err)
		default:
			fmt.Fprintf(os.Stderr, "    ?? #%s: could not create draft PR for blocked hand-off: %v\n", num, err)
		}
	}
}

// logNoBlockedHandoffBundle reports the benign case where a blocked run's
// outbox held nothing to relay: an empty branch range, not a relay failure.
func logNoBlockedHandoffBundle(num string) {
	fmt.Fprintf(os.Stderr, "    .. #%s: no blocked-hand-off bundle to relay (empty branch range; nothing to preserve)\n", num)
}

// logBlockedHandoffRelayFailure reports a genuine relay failure during the
// blocked hand-off — the alarming counterpart to logNoBlockedHandoffBundle.
func logBlockedHandoffRelayFailure(num string, err error) {
	fmt.Fprintf(os.Stderr, "    ?? #%s: could not relay blocked-hand-off bundle: %v\n", num, err)
}

// blockHandoff posts a merge-blocked comment and leaves num visibly not-done
// — the shared outcome for every hostMediateDraftPR failure mode, the most
// common being a read-only Box whose PR-intent nudge was exhausted without
// ever yielding a usable SPINDRIFT_PR_INTENT line.
//
// The issue is deliberately left in-progress rather than transitioned:
//
//   - agent-complete reads as "merged and green" to an operator, so a run
//     where nothing landed would look done.
//   - agent-failed (ADR 0012) is reserved for a Box that exited non-zero and
//     needs crash triage; this Box exited cleanly at status=ready and did real
//     work (its branch was even relayed), so the crash queue mis-files it.
//   - agent-in-progress is the honest "blocked, not failed, not complete"
//     state: the comment just posted explains why the run stopped, and the
//     issue stays visibly unfinished for a human to pick up.
func (s *Settle) blockHandoff(num, branch string, err error) (string, bool) {
	fmt.Printf("    #%s  landing=%s  status=merge-blocked  !! %v\n", num, branch, err)
	s.it.Comment(num, fmt.Sprintf("merge blocked: %v", err))
	return "", false
}

// closingKeywordPattern matches GitHub's recognized closing keywords
// (close/fix/resolve and their inflections), case-insensitively, an optional
// colon, whitespace, then a "#<digits>" issue reference. The digits are
// captured and compared against the num a call cares about rather than
// interpolated into the pattern, so "#1919" never matches as a reference to
// "191" and "#19195" never as one to "1919".
var closingKeywordPattern = regexp.MustCompile(`(?i)\b(?:close|closes|closed|fix|fixes|fixed|resolve|resolves|resolved):?\s+#(\d+)\b`)

// defuseClosingKeywords neutralizes any GitHub-recognized closing-keyword
// reference inside s without visibly mangling it, so it is safe to embed
// verbatim in a reconstructed PR body. s comes from the relayed branch's own
// commit subjects — box-authored, untrusted text a prompt-injected Box fully
// controls — which reconstructPRText bullets straight into the body. A subject
// shaped like "fix: closes #999" would otherwise be picked up by GitHub's own
// scanner and auto-close an entirely unrelated issue on merge, the same hazard
// ensureClosesReference guards for the host-synthesized "Closes #<num>" line.
//
// A zero-width space (U+200B) is inserted between the "#" and the digits: both
// closingKeywordPattern and GitHub's real scanner require "#" to be
// immediately followed by a digit, so that breaks the match for either, and
// U+200B has no visible glyph, so the subject still reads the same to a human.
func defuseClosingKeywords(s string) string {
	const zeroWidthSpace = "​" // U+200B ZERO WIDTH SPACE
	return closingKeywordPattern.ReplaceAllStringFunc(s, func(match string) string {
		hashIdx := strings.IndexByte(match, '#')
		return match[:hashIdx+1] + zeroWidthSpace + match[hashIdx+1:]
	})
}

// hasClosingReference reports whether body already carries a
// GitHub-recognized closing keyword referencing issue num.
func hasClosingReference(body, num string) bool {
	for _, match := range closingKeywordPattern.FindAllStringSubmatch(body, -1) {
		if match[1] == num {
			return true
		}
	}
	return false
}

// ensureClosesReference returns body unchanged when it is not this Launcher's
// job to guarantee a closing reference: the tracker is not GithubTracker-
// shaped, or body already carries a closing keyword referencing #num. The
// tracker test is a positive allow-list rather than merely "not
// LandingRecorder-shaped (local)", because a forgejo tracker also fails a
// LandingRecorder check yet must never get a GitHub Closes-keyword injected:
// forgejo issue numbers are a foreign namespace, so "Closes #N" on a GitHub PR
// would falsely reference (and could auto-close) an unrelated real GitHub
// issue #N. Otherwise it appends a literal "Closes #<num>" so a merge
// auto-closes the issue.
func ensureClosesReference(body, num string, it forge.IssueTracker) string {
	if _, ok := it.(forge.GithubTracker); !ok {
		return body
	}
	if hasClosingReference(body, num) {
		return body
	}
	if strings.TrimSpace(body) == "" {
		return "Closes #" + num
	}
	return body + "\n\nCloses #" + num
}

// parsePRIntent extracts a title and body from result's decoded PR-intent
// payload: the first line is the title, the remainder — after a blank line, or
// immediately when the box omitted one — is the body. Returns ok=false when no
// line was found or it carried no title, the two malformed shapes
// hostMediateDraftPR must block on rather than pass an empty title to
// CreateDraftPR. An empty body (a title-only payload) is a valid shape.
func parsePRIntent(result dispatch.Result) (title, body string, ok bool) {
	if !result.PRIntentFound || strings.TrimSpace(result.PRIntent) == "" {
		return "", "", false
	}
	title, rest, _ := strings.Cut(result.PRIntent, "\n")
	title = strings.TrimSpace(title)
	if title == "" {
		return "", "", false
	}
	return title, strings.TrimPrefix(rest, "\n"), true
}

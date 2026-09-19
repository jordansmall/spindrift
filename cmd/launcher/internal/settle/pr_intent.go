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

// hostMediateDraftPR relays a read-only Box's finished branch and opens its
// draft PR host-side (issue #1919), returning false when the hand-off is
// blocked so the caller skips the CI watch. The relay runs even when the Box
// printed no PR-intent line (issue #2447). branch comes from cf.AgentBranch,
// never the Box-controlled landing= (issue #1949).
func (s *Settle) hostMediateDraftPR(num string, result dispatch.Result) (string, bool) {
	branch, m := s.mediationFor(num)

	// The startup capability gate (main.go, issue #1916) guarantees a
	// read-only PR-shaped Code Forge implements both BundleRelay and
	// DraftPRCreator, so Open's own nil checks are unreachable outside a
	// misconfigured test double.
	url, created, source, err := m.Open(num, branch, result, FallbackReconstruct)
	if err != nil {
		return s.blockHandoff(num, branch, err)
	}
	// created is false when Open adopted a pre-existing box-authored PR (issue
	// #2407), which never received the reconstructed title and body, so
	// announcing a reconstructed hand-off there would be false.
	if source == TextSourceReconstructed && created {
		fmt.Printf("    #%s  landing=%s  status=reconstructed  note=no PR-intent line found in the box's log; description derived host-side from the relayed branch's commits\n", num, branch)
		// An operator reading only the issue, never the launcher's stdout,
		// must still see that the box's hand-off was incomplete (issue #2447,
		// AC5). Best-effort, like postUsageComment: a failure here must not
		// undo the draft PR already opened above.
		if commentErr := s.it.Comment(num, "This PR was reconstructed host-side: the box's own hand-off was incomplete (no usable PR-intent line found in its log), so the title/body above were derived from the relayed branch's own commits instead of the box's own description."); commentErr != nil {
			fmt.Fprintf(os.Stderr, "    ?? #%s: could not post reconstructed-hand-off comment: %v\n", num, commentErr)
		}
	}
	return url, true
}

// relayBlockedWork relays a blocked Box's branch so the work is not lost, then
// opens a draft PR if the Box left a PR-intent line (issue #1933). Failures
// only log: the caller's blocked transition already recorded the outcome.
// branch comes from cf.AgentBranch, never o.Landing (issue #1949). A push-only
// forge cannot use Open, which requires DraftPRCreator, so it relays directly.
func (s *Settle) relayBlockedWork(num string, result dispatch.Result) {
	branch, m := s.mediationFor(num)
	if m.br == nil || s.cfg.OutboxDir == nil {
		return
	}

	if m.dpc == nil {
		if err := m.br.RelayBundle(s.cfg.OutboxDir(num), branch); err != nil {
			if errors.Is(err, forge.ErrBundleNotFound) {
				// An empty branch range leaves no bundle, so there is no work
				// to preserve and no branch to open a PR against (issue #2096).
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
			// Open already relayed the bundle, so a missing PR-intent line
			// leaves nothing more to do.
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

// logNoBlockedHandoffBundle reports a blocked run whose outbox held nothing to
// relay, which is benign rather than a relay failure (issue #2096).
func logNoBlockedHandoffBundle(num string) {
	fmt.Fprintf(os.Stderr, "    .. #%s: no blocked-hand-off bundle to relay (empty branch range; nothing to preserve)\n", num)
}

func logBlockedHandoffRelayFailure(num string, err error) {
	fmt.Fprintf(os.Stderr, "    ?? #%s: could not relay blocked-hand-off bundle: %v\n", num, err)
}

// blockHandoff posts a merge-blocked comment, the shared outcome for every
// hostMediateDraftPR failure, and leaves the issue in agent-in-progress rather
// than transitioning it (issue #2046): agent-complete reads as merged and
// green (issue #2036), and agent-failed (ADR 0012) is reserved for a Box that
// exited non-zero, which this one did not.
func (s *Settle) blockHandoff(num, branch string, err error) (string, bool) {
	fmt.Printf("    #%s  landing=%s  status=merge-blocked  !! %v\n", num, branch, err)
	s.it.Comment(num, fmt.Sprintf("merge blocked: %v", err))
	return "", false
}

// closingKeywordPattern matches GitHub's closing keywords (close, fix, resolve
// and their inflections) and the "#<digits>" reference they close. Callers
// compare the captured digits against their own num rather than interpolating
// it into the pattern, so "#19195" never matches a reference to "1919".
var closingKeywordPattern = regexp.MustCompile(`(?i)\b(?:close|closes|closed|fix|fixes|fixed|resolve|resolves|resolved):?\s+#(\d+)\b`)

// defuseClosingKeywords breaks every closing-keyword reference in s by
// inserting a zero-width space after the "#" (issue #2447). s holds untrusted
// box-authored commit subjects that reconstructPRText embeds verbatim, so
// "fix: closes #999" would otherwise auto-close an unrelated issue on merge.
// GitHub's scanner needs a digit right after "#", and U+200B is invisible.
func defuseClosingKeywords(s string) string {
	const zeroWidthSpace = "​" // U+200B ZERO WIDTH SPACE
	return closingKeywordPattern.ReplaceAllStringFunc(s, func(match string) string {
		hashIdx := strings.IndexByte(match, '#')
		return match[:hashIdx+1] + zeroWidthSpace + match[hashIdx+1:]
	})
}

func hasClosingReference(body, num string) bool {
	for _, match := range closingKeywordPattern.FindAllStringSubmatch(body, -1) {
		if match[1] == num {
			return true
		}
	}
	return false
}

// ensureClosesReference appends "Closes #<num>" to body so a merge auto-closes
// the issue, unless body already closes it. The tracker check is a positive
// allow-list for GithubTracker (issue #2341), not "anything but local": a
// forgejo tracker also fails a LandingRecorder check, and its issue numbers
// are a foreign namespace, so a Closes keyword would hit an unrelated issue.
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

// parsePRIntent splits result's PR-intent payload into a title (the first
// line) and a body (the rest, after an optional blank line). ok is false when
// the payload is missing or has no title, which hostMediateDraftPR must block
// on rather than pass an empty title to CreateDraftPR. An empty body is valid.
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

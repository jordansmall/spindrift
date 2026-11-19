package settle

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/forge"
)

// hostMediateDraftPR relays a read-only Box's finished branch and opens its
// draft PR host-side (issue #1919). The relay runs even when the Box printed no
// PR-intent line (issue #2447). branch comes from forge.AgentBranchName, never the
// Box-controlled landing= (issue #1949). A non-nil error means no PR is open
// and the caller skips the CI watch; errAbandoned also means a stop landed
// during the relay's backoff, so the caller must post nothing (issue #3523).
func (s *Settle) hostMediateDraftPR(num string, gen uint64, result dispatch.Result) (string, error) {
	branch, m := s.mediationFor(num, gen)

	// The startup capability gate (main.go, issue #1916) guarantees a
	// read-only PR-shaped Code Forge implements both BundleRelay and
	// DraftPRCreator, so Open's own nil checks are unreachable outside a
	// misconfigured test double.
	url, created, source, err := m.Open(num, branch, result, FallbackReconstruct)
	if err != nil {
		if errors.Is(err, errAbandoned) {
			return "", err
		}
		switch {
		case relayFailureParkable(err):
			s.parkRelayFailure(num, branch, err)
		case errors.Is(err, errCreateDraftPR):
			s.parkDraftPRFailure(num, branch, err)
		default:
			s.blockHandoff(num, branch, err)
		}
		return "", err
	}
	// created is false when Open adopted a pre-existing box-authored PR (issue
	// #2407), which never received the reconstructed title and body, so
	// announcing a reconstructed hand-off there would be false. It is also false
	// when the retry wrapper adopted the host's own earlier attempt (issue
	// #5173), which did carry that text. Residual edge: any failed first attempt
	// that did not itself create a PR (pre-POST connection error, 5xx, rate-limit
	// 403, timeout) followed by adopting a PR already open on the branch (box-
	// authored, or a prior dispatch's) is misread as the host's own; an
	// OpenPRForBranch before every first attempt isn't worth that.
	if source == TextSourceReconstructed && (created || m.adoptedOwn) {
		s.cfg.Progress.Step(Step{Key: num, Landing: branch, Status: StatusReconstructed, Detail: "note=no PR-intent line found in the box's log; description derived host-side from the relayed branch's commits"})
		// An operator reading only the issue, never the launcher's stdout,
		// must still see that the box's hand-off was incomplete (issue #2447,
		// AC5). Best-effort, like postUsageComment: a failure here must not
		// undo the draft PR already opened above.
		if commentErr := s.it.Comment(num, "This PR was reconstructed host-side: the box's own hand-off was incomplete (no usable PR-intent line found in its log), so the title/body above were derived from the relayed branch's own commits instead of the box's own description."); commentErr != nil {
			fmt.Fprintf(os.Stderr, "    ?? #%s: could not post reconstructed-hand-off comment: %v\n", num, commentErr)
		}
	}
	return url, nil
}

// relayBlockedWork relays a blocked Box's branch so the work is not lost, then
// opens a draft PR if the Box left a PR-intent line (issue #1933). Failures
// only log: the caller's blocked transition already recorded the outcome.
// branch comes from forge.AgentBranchName, never o.Landing (issue #1949). A push-only
// forge cannot use Open, which requires DraftPRCreator, so it relays directly.
// It returns errAbandoned when a stop landed during the relay's backoff, so the
// caller must post nothing on the issue the operator stopped (issue #3523);
// every other failure is logged and returns nil.
func (s *Settle) relayBlockedWork(num string, gen uint64, result dispatch.Result) error {
	branch, m := s.mediationFor(num, gen)
	if m.br == nil || s.cfg.OutboxDir == nil {
		return nil
	}

	if m.dpc == nil {
		if err := m.br.RelayBundle(s.cfg.OutboxDir(num), branch); err != nil {
			if errors.Is(err, errAbandoned) {
				logBlockedHandoffRelayAbandoned(num)
				return errAbandoned
			}
			if errors.Is(err, forge.ErrBundleNotFound) {
				// An empty branch range leaves no bundle, so there is no work
				// to preserve and no branch to open a PR against (issue #2096).
				logNoBlockedHandoffBundle(num)
				return nil
			}
			logBlockedHandoffRelayFailure(num, err)
		}
		return nil
	}

	if _, _, _, err := m.Open(num, branch, result, FallbackNone); err != nil {
		// Checked ahead of the switch: Open wraps errAbandoned and errRelayBundle
		// around the same error.
		if errors.Is(err, errAbandoned) {
			logBlockedHandoffRelayAbandoned(num)
			return errAbandoned
		}
		switch {
		case errors.Is(err, ErrNoPRIntent):
			// Open already relayed the bundle, so a missing PR-intent line
			// leaves nothing more to do.
		case errors.Is(err, forge.ErrBundleNotFound):
			logNoBlockedHandoffBundle(num)
		case errors.Is(err, errRelayBundle):
			logBlockedHandoffRelayFailure(num, err)
		default:
			fmt.Fprintf(os.Stderr, "    ?? #%s: could not create draft PR for blocked hand-off: %v\n", num, err)
		}
	}
	return nil
}

// logNoBlockedHandoffBundle reports a blocked run whose outbox held nothing to
// relay, which is benign rather than a relay failure (issue #2096).
func logNoBlockedHandoffBundle(num string) {
	fmt.Fprintf(os.Stderr, "    .. #%s: no blocked-hand-off bundle to relay (empty branch range; nothing to preserve)\n", num)
}

// logBlockedHandoffRelayAbandoned reports a stop that interrupted the relay's
// backoff, which is the operator's call rather than a relay failure.
func logBlockedHandoffRelayAbandoned(num string) {
	fmt.Fprintf(os.Stderr, "    .. #%s: blocked-hand-off relay abandoned (issue stopped during retry backoff)\n", num)
}

func logBlockedHandoffRelayFailure(num string, err error) {
	fmt.Fprintf(os.Stderr, "    ?? #%s: could not relay blocked-hand-off bundle: %v\n", num, err)
}

// blockHandoff posts a merge-blocked comment for a hostMediateDraftPR failure
// other than an exhausted relay or a draft-PR create failure, and leaves the
// issue in agent-in-progress rather than transitioning it (issue #2046):
// agent-complete reads as merged and green (issue #2036), and agent-failed
// would send the operator to `spindrift recover`, which has no bundle to land
// here (unlike parkRelayFailure and parkDraftPRFailure).
func (s *Settle) blockHandoff(num, branch string, err error) {
	s.cfg.Progress.Step(Step{Key: num, Landing: branch, Status: StatusMergeBlocked, Detail: fmt.Sprintf("!! %v", err)})
	s.it.Comment(num, fmt.Sprintf("merge blocked: %v", err))
	// Never transitions, so Settled never sees this Dispatch, yet the
	// all-finished banner must not count it delivered (issue #5145).
	s.latchUnrecorded(num, ForReason(dispatchrecord.ReasonHandoffBlocked), "")
}

// parkRelayFailure fails an issue whose bundle relay failed (issue #4651).
// Unlike blockHandoff's cases, the bundle is intact in the outbox, so
// `spindrift recover` can land it; agent-failed is what routes the operator
// there. A missing bundle (forge.ErrBundleNotFound) preserves nothing and never
// reaches here.
func (s *Settle) parkRelayFailure(num, branch string, err error) {
	s.cfg.Progress.Step(Step{Key: num, Landing: branch, Status: StatusRelayFailed, Detail: fmt.Sprintf("!! %v", err)})
	next := relayFailureNext(num, err, fmt.Sprintf("run `spindrift recover %s` to retry relaying and landing it.", num))
	s.it.Comment(num, fmt.Sprintf("bundle relay failed: %v. The Box's bundle is preserved in the outbox; %s", err, next))
	s.transitionState(num, ForReason(dispatchrecord.ReasonRelayFailed), "bundle relay failed; work preserved in outbox")
}

// relayFailureNext swaps a park comment's recover hint for the workflow-scope
// remedy when err is the forge refusing a workflow-file push: recover would
// hit the same refusal.
func relayFailureNext(num string, err error, fallback string) string {
	if errors.Is(err, forge.ErrWorkflowPermissionDenied) {
		return forge.WorkflowPermissionRemedy(num)
	}
	return fallback
}

// parkDraftPRFailure fails an issue whose bundle relayed but whose host-side
// draft-PR create then failed (issue #5144). The bundle is still in the outbox
// and the branch already pushed, so `spindrift recover` can open the PR.
func (s *Settle) parkDraftPRFailure(num, branch string, err error) {
	s.cfg.Progress.Step(Step{Key: num, Landing: branch, Status: StatusDraftPRFailed, Detail: fmt.Sprintf("!! %v", err)})
	s.it.Comment(num, fmt.Sprintf("%v. The branch was relayed and the Box's bundle is preserved in the outbox; run `spindrift recover %s` to retry opening and landing the PR.", err, num))
	s.transitionState(num, ForReason(dispatchrecord.ReasonDraftPRFailed), "draft PR create failed; work preserved in outbox")
}

// closingKeywords finds the nine default closing keywords (close, fix,
// resolve and their inflections) case-insensitively. It is unanchored, so it
// also hits inside "prefix" and "closest"; keywordBounded decides whole words.
// Defusing splits the keyword, not the reference: the keyword set is closed,
// reference syntaxes are open-ended (issues #2447, #7079, #7248). Rules:
//   - Forgejo (modules/references/references.go): makeKeywordsPat wants the
//     keyword right before the reference, so a split keyword defuses every
//     form after it; mdstripper makes "**Closes** #N" and "_Closes #N_" live.
//   - GitHub is closed-source and documents only "KEYWORD #N", "KEYWORD
//     OWNER/REPO#N" and the colon variant. GH-N, !N, NBSP, scheme-less, www.
//     and multi-/issues/N URLs rest unverified on one assumption: a U+200B
//     inside the keyword stops it matching.
//   - Not covered: Forgejo's configurable CLOSE_KEYWORDS, and HTML character
//     references ("Clo&#115;es"), which pass through undecoded.
var closingKeywords = regexp.MustCompile(`(?i)close[sd]?|fix(?:e[sd])?|resolve[sd]?`)

// keywordBounded reports whether s[start:end] is a whole word: RE2 has no
// lookbehind, and consuming the boundary runes would swallow an adjacent
// keyword ("fix fix"). Only ASCII letters and digits are non-boundaries, since
// GitHub's boundary is unverified and a wider one would leave "见Closes #9"
// live; "_", "*", "(", NBSP and U+200B all count as boundaries.
func keywordBounded(s string, start, end int) bool {
	if start > 0 && isASCIIAlnum(s[start-1]) {
		return false
	}
	if end < len(s) && isASCIIAlnum(s[end]) {
		return false
	}
	return true
}

func isASCIIAlnum(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// defuseClosingKeywords inserts U+200B after the first letter of every closing
// keyword in s, whatever follows it. s holds untrusted box-authored text, so
// "fix: closes #999" would otherwise auto-close an unrelated issue on merge.
// Identifiers and URL path segments split too ("close_fd", "fix-login.md"), and
// a split link target breaks the link; the text is read on the forge, not
// copied, so that cost is accepted.
func defuseClosingKeywords(s string) string {
	out, _ := defuseClosingKeywordsExcept(s, "")
	return out
}

// defuseClosingKeywordsExcept is defuseClosingKeywords but spares each keyword
// for which followedByRef(rest, keep) holds and the Forgejo keyword prefix is
// met, and reports whether it spared any. keep "" spares nothing.
func defuseClosingKeywordsExcept(s, keep string) (string, bool) {
	const zeroWidthSpace = "\u200b" // U+200B ZERO WIDTH SPACE
	var b strings.Builder
	last, kept := 0, false
	for _, m := range closingKeywords.FindAllStringIndex(s, -1) {
		start, end := m[0], m[1]
		if !keywordBounded(s, start, end) {
			continue
		}
		if keep != "" && isASCII(s[start:end]) && keywordPrefixed(s, start) && followedByRef(s[end:], keep) {
			kept = true
			continue
		}
		b.WriteString(s[last : start+1])
		b.WriteString(zeroWidthSpace)
		last = start + 1
	}
	b.WriteString(s[last:])
	return b.String(), kept
}

// keywordPrefixed reports whether the byte before s[start:] satisfies Forgejo's
// makeKeywordsPat prefix `(?:\s|^|\(|\[)` in the raw text: "x_Closes #1" never
// closes there, so sparing it would leave no live closing reference.
func keywordPrefixed(s string, start int) bool {
	if start == 0 {
		return true
	}
	switch s[start-1] {
	case ' ', '\t', '\n', '\f', '\r', '(', '[':
		return true
	}
	return false
}

// refTerminator is the tail of Forgejo's issueNumericPattern, which wants
// whitespace or punctuation after the number: what must follow "#N" for it to
// be a reference at all ("#1919abc" is none).
var refTerminator = regexp.MustCompile(`^(?:\s|$|\)|\]|'|"|[:;,.?!]\s|[:;,.?!]$)`)

// followedByRef reports whether rest starts with `:?`, exactly one space, then
// a "#<num>" reference: Forgejo's makeKeywordsPat, `<keywords>:? $`, so
// "Closes#9", "Closes:#9", NBSP, a tab or two spaces never close there.
func followedByRef(rest, num string) bool {
	after, ok := strings.CutPrefix(strings.TrimPrefix(rest, ":"), " #"+num)
	return ok && refTerminator.MatchString(after)
}

// closeOnlyOwnIssue leaves body closing only the dispatched issue: body may
// be Box-authored, so every closing keyword is defused (issue #7079) except
// each one naming "#<num>" (followedByRef), and "Closes #<num>" is appended
// when none did.
// backend.ClosesRef is a positive allow-list (issues #2341, #7058), not
// "anything but local": a Closes keyword only auto-closes when the forge
// shares the tracker's issue namespace, so without it the own "#<num>" is
// defused too and nothing is appended. It is the same predicate the prompt's
// PR_BODY_CLOSES gate reads, so the Box and the host agree.
func closeOnlyOwnIssue(body, num string, forgeRow, trackerRow backend.Descriptor) string {
	if !backend.ClosesRef(forgeRow, trackerRow) {
		return defuseClosingKeywords(body)
	}
	body, kept := defuseClosingKeywordsExcept(body, num)
	if kept {
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

// isASCII reports whether s holds only ASCII bytes. Go's (?i) folds U+017F
// "ſ" to "s", so closingKeywords matches "cloſes", which a forge need not read
// as a keyword; sparing it could leave the body with no live closing reference.
func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

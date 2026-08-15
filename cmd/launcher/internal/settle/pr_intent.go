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
// BOX_FORGE_AND_ISSUE_ACCESS=read-only for a PR-shaped Code Forge (issue
// #1919): the Box holds no push or PR-create token, so before selfHeal can
// watch CI on a PR, settle must relay the Box's finished branch (its
// forge.BundleRelay hook) and open the draft PR itself (its
// forge.DraftPRCreator hook), ordinarily from the Box's own PR-intent line.
//
// RelayBundle always runs, whether or not a usable PR-intent line was found
// (issue #2447): a Box can finish real, mergeable work and simply fail to
// print its last line, leaving the branch itself fine — only the wording is
// missing. When PR-intent is missing but the relay succeeds, title/body are
// reconstructed host-side from the relayed branch's own commits
// (reconstructPRText) rather than blocking a hand-off with nothing actually
// wrong with it. A reconstructed hand-off is never silently indistinguishable
// from a normal one (issue #2447, AC5): besides the "Reconstructed host-side"
// note already in the PR body itself, this also posts a comment on the issue
// explaining the box's own hand-off was incomplete — an operator reading only
// the issue, not the launcher's own stdout log, can still tell. That
// stdout line and comment only fire when CreateDraftPR actually created a
// fresh PR (issue #2407's adopt-existing retry path returns created=false):
// when it instead adopts a pre-existing box-authored PR, the reconstructed
// title/body were never applied to it, so claiming the hand-off was
// reconstructed would be false.
//
// Returns the resolved PR URL and true on success. Any failure along the
// way — a missing/malformed bundle, a missing/malformed PR-intent line with
// no commits to reconstruct from either, or the draft-PR-create call itself
// failing — posts a comment and leaves the issue in-progress (blockHandoff: a
// blocked hand-off, visibly not-done, never demoted to agent-failed and
// never mistakable for agent-complete — see issue #2046), then returns
// ok=false so the caller skips CI-watch entirely rather than polling a PR
// that was never opened.
//
// branch is derived from cf.AgentBranch(num), never from the outcome line's
// own landing= field (issue #1949): a prompt-injected read-only Box holds no
// write token, but it does control both landing= and its own bundle's ref
// names, so trusting landing= here would let it steer the Launcher's
// force-push and draft-PR head at will. Deriving branch host-side pins both
// to the one ref the Box's PR is actually expected to hand off on.
func (s *Settle) hostMediateDraftPR(num string, result dispatch.Result) (string, bool) {
	cf := s.cfForNum(num)
	branch := cf.AgentBranch(num)
	m := NewMediation(cf, s.it, s.cfg.OutboxDir, s.cfg.BaseBranch)

	// Mediation.Open's own upfront capability/config checks (steps 1-3)
	// return the exact same unwrapped error strings the inline checks here
	// used to produce directly — the startup capability gate (main.go, issue
	// #1916) guarantees a read-only PR-shaped Code Forge always implements
	// both BundleRelay and DraftPRCreator, so these are unreachable outside a
	// misconfigured test double, and block rather than silently stranding
	// the issue in agent-in-progress.
	url, created, source, err := m.Open(num, branch, result, FallbackReconstruct)
	if err != nil {
		return s.blockHandoff(num, branch, err)
	}
	if source == TextSourceReconstructed && created {
		fmt.Printf("    #%s  landing=%s  status=reconstructed  note=no PR-intent line found in the box's log; description derived host-side from the relayed branch's commits\n", num, branch)
		// Posted alongside the stdout log line above so both settle's own
		// console log and the issue itself tell the same story (issue #2447,
		// AC5): an operator reading only the issue — never the launcher's own
		// log — must still be able to tell the box's own hand-off was
		// incomplete. Best-effort, matching postUsageComment's log-but-don't-
		// propagate contract: a failure here never un-does the draft PR
		// already opened above.
		if commentErr := s.it.Comment(num, "This PR was reconstructed host-side: the box's own hand-off was incomplete (no usable PR-intent line found in its log), so the title/body above were derived from the relayed branch's own commits instead of the box's own description."); commentErr != nil {
			fmt.Fprintf(os.Stderr, "    ?? #%s: could not post reconstructed-hand-off comment: %v\n", num, commentErr)
		}
	}
	return url, true
}

// relayBlockedWork gives a read-only Box's finished-but-blocked branch the
// same host-mediated relay hostMediateDraftPR gives the "ready" path (issue
// #1933). Relays the outbox bundle so the branch itself is never lost, then
// opens a draft PR from a PR-intent line if the Box left one — a blocked run
// may reach here before ever printing one (e.g. review never cleared), in
// which case only the relay runs.
//
// Unlike hostMediateDraftPR, every failure here just logs: the caller's
// "blocked" transition and comment already recorded the real outcome, so
// there is nothing to downgrade and no CI to skip watching. A RelayBundle
// failure wrapping forge.ErrBundleNotFound is doubly benign — an empty
// branch range left nothing in the outbox to relay in the first place — so
// it logs informationally rather than as a warning (issue #2096).
//
// branch is derived from cf.AgentBranch(num), never o.Landing, for the same
// reason hostMediateDraftPR derives it (issue #1949).
//
// A PR-shaped cf (forge.DraftPRCreator, e.g. github) delegates the relay,
// intent parse, closes-ref, and create steps to Mediation.Open. A push-only
// cf (forge.BundleRelay but not forge.DraftPRCreator, e.g. local) can't go
// through Open at all — Open requires forge.DraftPRCreator unconditionally,
// since every other caller (hostMediateDraftPR, adoptRelayedBranch) is only
// ever reached for a PR-shaped forge. relayBlockedWork is the one caller
// that must also serve a push-only forge with nothing to create a PR
// against, so that shape keeps its own direct RelayBundle call instead.
func (s *Settle) relayBlockedWork(num string, result dispatch.Result) {
	cf := s.cfForNum(num)
	branch := cf.AgentBranch(num)
	br, ok := cf.(forge.BundleRelay)
	if !ok || s.cfg.OutboxDir == nil {
		return
	}

	if _, ok := cf.(forge.DraftPRCreator); !ok {
		// Push-only shape: no draft-PR step to unify, so relay directly.
		if err := br.RelayBundle(s.cfg.OutboxDir(num), branch); err != nil {
			if errors.Is(err, forge.ErrBundleNotFound) {
				// An absent outbox bundle on the blocked path means the
				// branch range was empty — there was simply no work to
				// preserve (issue #2096). Benign: report it informationally,
				// not as a relay failure. A blocked run with nothing to hand
				// off also has no branch to open a draft PR against, so stop
				// here as the error path does.
				fmt.Fprintf(os.Stderr, "    .. #%s: no blocked-hand-off bundle to relay (empty branch range; nothing to preserve)\n", num)
				return
			}
			fmt.Fprintf(os.Stderr, "    ?? #%s: could not relay blocked-hand-off bundle: %v\n", num, err)
		}
		return
	}

	m := NewMediation(cf, s.it, s.cfg.OutboxDir, s.cfg.BaseBranch)
	if _, _, _, err := m.Open(num, branch, result, FallbackNone); err != nil {
		switch {
		case errors.Is(err, ErrNoPRIntent):
			// No usable PR-intent line: the relay above already ran inside
			// Open, so there is simply nothing more to do (same as the old
			// !ok-from-parsePRIntent early return).
			return
		case errors.Is(err, forge.ErrBundleNotFound):
			fmt.Fprintf(os.Stderr, "    .. #%s: no blocked-hand-off bundle to relay (empty branch range; nothing to preserve)\n", num)
		case strings.HasPrefix(err.Error(), "relay bundle:"):
			fmt.Fprintf(os.Stderr, "    ?? #%s: could not relay blocked-hand-off bundle: %v\n", num, errors.Unwrap(err))
		default:
			fmt.Fprintf(os.Stderr, "    ?? #%s: could not create draft PR for blocked hand-off: %v\n", num, errors.Unwrap(err))
		}
	}
}

// blockHandoff posts a merge-blocked comment and leaves num visibly not-done
// — the shared outcome for every hostMediateDraftPR failure mode, the most
// common being a read-only Box whose PR-intent nudge (issue #2045) was
// exhausted without ever yielding a usable SPINDRIFT_PR_INTENT line.
//
// The issue is deliberately left in-progress rather than transitioned
// anywhere (issue #2046). Three terminal states were on the table and each is
// wrong here but the one chosen:
//
//   - agent-complete (the pre-#2046 posture) reads as "merged and green" to
//     an operator, so a run where nothing landed looks done — the exact #2036
//     confusion of an OPEN issue wearing agent-complete with no PR.
//   - agent-failed (ADR 0012) is reserved for a Box that exited non-zero and
//     needs crash triage; this Box exited cleanly at status=ready and did
//     real work (its branch was even relayed to the outbox), so demoting it
//     to the crash queue mis-files it.
//   - agent-in-progress — left untouched — is the honest "blocked, not
//     failed, and certainly not complete" state: the merge-blocked comment
//     just posted explains why the run stopped, and the issue stays visibly
//     unfinished for a human to pick up rather than masquerading as done.
func (s *Settle) blockHandoff(num, branch string, err error) (string, bool) {
	fmt.Printf("    #%s  landing=%s  status=merge-blocked  !! %v\n", num, branch, err)
	s.it.Comment(num, fmt.Sprintf("merge blocked: %v", err))
	return "", false
}

// closingKeywordPattern matches GitHub's recognized closing keywords
// (close/closes/closed, fix/fixes/fixed, resolve/resolves/resolved),
// case-insensitively, followed by an optional colon (GitHub also recognizes
// the "Closes: #N" colon form boxes sometimes emit) and whitespace, then a
// "#<digits>" issue reference, capturing the digits. Compiled once at
// package scope; the digits capture is compared against the specific num a
// call cares about rather than interpolated into the pattern, so e.g.
// "#1919" never matches as a reference to "191" the way naive substring
// interpolation could, and "#19195" never matches as a reference to "1919".
var closingKeywordPattern = regexp.MustCompile(`(?i)\b(?:close|closes|closed|fix|fixes|fixed|resolve|resolves|resolved):?\s+#(\d+)\b`)

// defuseClosingKeywords neutralizes any GitHub-recognized closing-keyword
// reference inside s without visibly mangling it, so it's safe to embed
// verbatim in a reconstructed PR body (issue #2447). s comes from the
// relayed branch's own commit subjects — box-authored, untrusted text a
// prompt-injected Box fully controls — and reconstructPRText otherwise
// bullets every subject straight into the body. A subject shaped like "fix:
// closes #999" would then be picked up by GitHub's own closing-keyword
// scanner and auto-close an entirely unrelated issue #999 on merge, exactly
// the hazard ensureClosesReference exists to guard against for the
// host-synthesized "Closes #<num>" line — reconstructPRText's verbatim
// embedding would otherwise reopen that same hole from the commit list
// itself.
//
// For every match of closingKeywordPattern, a zero-width space (U+200B) is
// inserted immediately after the "#" and before the digits. Both
// closingKeywordPattern and GitHub's real scanner require "#" to be
// immediately followed by a digit, so the inserted character breaks the
// match for either; U+200B has no visible glyph, so the subject still reads
// the same to a human reviewer.
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

// ensureClosesReference returns body unchanged when it's not this Launcher's
// job to guarantee a closing reference: either it is not a GithubTracker-
// shaped tracker (issue #2341) — a positive allow-list scoped to the github
// adapter specifically, not merely "not LandingRecorder-shaped (local)",
// since a forgejo tracker also fails a LandingRecorder check yet must never
// get a GitHub Closes-keyword injected: forgejo issue numbers are a foreign
// namespace from GitHub's, so "Closes #N" on a GitHub PR would falsely
// reference (and could auto-close) an unrelated real GitHub issue #N — or
// body already carries a GitHub-recognized closing keyword (close/fix/
// resolve and their inflections) referencing #num. Otherwise it appends a
// literal "Closes #<num>" so a merge auto-closes the issue: a blank-line
// separator when body is non-empty, or just "Closes #<num>" when body is
// empty.
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
// payload: by convention the first line is the title and the remainder —
// after a blank line, or immediately when the box omitted one — is the
// body. Returns ok=false when no line was found or it carried no title, the
// two malformed shapes hostMediateDraftPR must block on rather than pass an
// empty title to CreateDraftPR. An empty body (a title-only payload) is a
// valid shape and passes through unchanged — CreateDraftPR accepts "" the
// same way `gh pr create --body ""` does.
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

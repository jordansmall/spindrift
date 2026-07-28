package settle

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
)

// hostMediateDraftPR resolves num's real PR URL under
// BOX_FORGE_AND_ISSUE_ACCESS=read-only for a PR-shaped Code Forge (issue
// #1919): the Box holds no push or PR-create token, so before selfHeal can
// watch CI on a PR, settle must relay the Box's finished branch (its
// forge.BundleRelay hook) and open the draft PR itself (its
// forge.DraftPRCreator hook) from the Box's PR-intent line.
//
// Returns the resolved PR URL and true on success. Any failure along the
// way — a missing/malformed bundle, a missing/malformed PR-intent line, or
// the draft-PR-create call itself failing — posts a comment and leaves the
// issue in-progress (blockHandoff: a blocked hand-off, visibly not-done,
// never demoted to agent-failed and never mistakable for agent-complete —
// see issue #2046), then returns ok=false so the caller skips CI-watch
// entirely rather than polling a PR that was never opened.
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
	br, ok := cf.(forge.BundleRelay)
	if !ok {
		// The startup capability gate (main.go, issue #1916) guarantees a
		// read-only PR-shaped Code Forge always implements both BundleRelay
		// and DraftPRCreator; unreachable outside a misconfigured test
		// double, so it blocks rather than silently stranding the issue in
		// agent-in-progress.
		return s.blockHandoff(num, branch, errors.New("settle: Code Forge does not implement forge.BundleRelay"))
	}
	dpc, ok := cf.(forge.DraftPRCreator)
	if !ok {
		return s.blockHandoff(num, branch, errors.New("settle: Code Forge does not implement forge.DraftPRCreator"))
	}

	// Parsed and type-asserted before RelayBundle runs: RelayBundle is a
	// genuine side effect against the remote (a real force-push), and a box
	// that left no usable PR-intent line has nothing worth relaying a
	// branch for.
	title, body, ok := parsePRIntent(result)
	if !ok {
		return s.blockHandoff(num, branch, errors.New("no usable PR-intent line found in the box's log"))
	}

	if s.cfg.OutboxDir == nil {
		return s.blockHandoff(num, branch, errors.New("settle: Config.OutboxDir is unset but the Code Forge implements forge.BundleRelay"))
	}
	if err := br.RelayBundle(s.cfg.OutboxDir(num), branch); err != nil {
		return s.blockHandoff(num, branch, err)
	}

	url, err := dpc.CreateDraftPR(title, body, s.cfg.BaseBranch, branch)
	if err != nil {
		return s.blockHandoff(num, branch, fmt.Errorf("draft PR create: %w", err))
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
func (s *Settle) relayBlockedWork(num string, result dispatch.Result) {
	cf := s.cfForNum(num)
	branch := cf.AgentBranch(num)
	br, ok := cf.(forge.BundleRelay)
	if !ok || s.cfg.OutboxDir == nil {
		return
	}
	if err := br.RelayBundle(s.cfg.OutboxDir(num), branch); err != nil {
		if errors.Is(err, forge.ErrBundleNotFound) {
			// An absent outbox bundle on the blocked path means the branch
			// range was empty — there was simply no work to preserve (issue
			// #2096). Benign: report it informationally, not as a relay
			// failure. A blocked run with nothing to hand off also has no
			// branch to open a draft PR against, so stop here as the error
			// path does.
			fmt.Fprintf(os.Stderr, "    .. #%s: no blocked-hand-off bundle to relay (empty branch range; nothing to preserve)\n", num)
			return
		}
		fmt.Fprintf(os.Stderr, "    ?? #%s: could not relay blocked-hand-off bundle: %v\n", num, err)
		return
	}

	title, body, ok := parsePRIntent(result)
	if !ok {
		return
	}
	dpc, ok := cf.(forge.DraftPRCreator)
	if !ok {
		return
	}
	if _, err := dpc.CreateDraftPR(title, body, s.cfg.BaseBranch, branch); err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: could not create draft PR for blocked hand-off: %v\n", num, err)
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

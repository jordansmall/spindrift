package settle

import (
	"errors"
	"fmt"
	"strings"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
)

// hostMediateDraftPR resolves num's real PR URL under
// BOX_FORGE_AND_ISSUE_ACCESS=read-only for a PR-shaped Code Forge (issue
// #1919): the Box holds no push or PR-create token, so before selfHeal can
// watch CI on a PR, settle must relay the Box's finished branch (its
// forge.BundleRelay hook) and open the draft PR itself (its
// forge.DraftPRCreator hook) from the Box's PR-intent line. branch is the
// outcome line's own landing= field, which under read-only carries the
// branch name (the Box has no PR URL to report) rather than a PR URL.
//
// Returns the resolved PR URL and true on success. Any failure along the
// way — a missing/malformed bundle, a missing/malformed PR-intent line, or
// the draft-PR-create call itself failing — posts a comment, marks the issue
// agent-complete (mirroring RelayBundle's own missing-bundle posture: this
// is a blocked hand-off, never a demotion to agent-failed), and returns
// ok=false so the caller skips CI-watch entirely rather than polling a PR
// that was never opened.
func (s *Settle) hostMediateDraftPR(num, branch string, result dispatch.Result) (string, bool) {
	cf := s.cfForNum(num)
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

// blockHandoff posts a merge-blocked comment and marks num agent-complete —
// the shared "blocked, not failed" outcome for every hostMediateDraftPR
// failure mode.
func (s *Settle) blockHandoff(num, branch string, err error) (string, bool) {
	fmt.Printf("    #%s  landing=%s  status=merge-blocked  !! %v\n", num, branch, err)
	s.it.Comment(num, fmt.Sprintf("merge blocked: %v", err))
	s.transitionState(num, forge.InProgress, forge.Complete)
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

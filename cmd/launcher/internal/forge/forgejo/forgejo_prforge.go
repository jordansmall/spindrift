package forgejo

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"spindrift.dev/launcher/internal/forge"
)

// forgejoPullRef is the head/base ref shape embedded in a
// forgejoPullPayload.
type forgejoPullRef struct {
	Ref string `json:"ref"`
	Sha string `json:"sha"`
}

// forgejoPullPayload is the subset of the Forgejo pull-request REST
// representation this adapter reads.
type forgejoPullPayload struct {
	Number    int            `json:"number"`
	HTMLURL   string         `json:"html_url"`
	State     string         `json:"state"`
	Merged    bool           `json:"merged"`
	Mergeable bool           `json:"mergeable"`
	Draft     bool           `json:"draft"`
	Title     string         `json:"title"`
	Head      forgejoPullRef `json:"head"`
	Base      forgejoPullRef `json:"base"`
}

// parsePRIndex extracts the pull index from a Forgejo PR html_url — its
// last path segment, e.g. ".../pulls/206" -> "206". It rejects an empty or
// non-numeric trailing segment.
func parsePRIndex(prURL string) (string, error) {
	trimmed := strings.TrimRight(prURL, "/")
	idx := strings.LastIndex(trimmed, "/")
	if idx < 0 || idx == len(trimmed)-1 {
		return "", fmt.Errorf("forgejo: invalid PR URL %q: no trailing path segment", prURL)
	}
	seg := trimmed[idx+1:]
	if seg == "" {
		return "", fmt.Errorf("forgejo: invalid PR URL %q: empty PR index", prURL)
	}
	if _, err := strconv.Atoi(seg); err != nil {
		return "", fmt.Errorf("forgejo: invalid PR URL %q: PR index %q is not numeric: %w", prURL, seg, err)
	}
	return seg, nil
}

// forgejoWIPPrefix is the title prefix Forgejo's WIP-title draft convention
// uses; isDraftTitle/stripWIPPrefix/MarkDraft all key off this one constant
// so the marker stays consistent everywhere it's read or written.
const forgejoWIPPrefix = "WIP:"

// isDraftTitle reports whether title carries Forgejo's WIP-prefix draft
// marker ("WIP:" or "WIP: ...", case-insensitive) — Forgejo encodes draft
// state as a title prefix rather than a first-class field alone.
func isDraftTitle(title string) bool {
	return strings.HasPrefix(strings.ToUpper(strings.TrimSpace(title)), forgejoWIPPrefix)
}

// stripWIPPrefix removes a leading, case-insensitive "WIP:" (plus any
// following spaces) from title. Titles without the prefix are returned
// unchanged.
func stripWIPPrefix(title string) string {
	trimmed := strings.TrimSpace(title)
	if !isDraftTitle(trimmed) {
		return title
	}
	rest := trimmed[len(forgejoWIPPrefix):]
	return strings.TrimLeft(rest, " ")
}

// isDraftPull reports whether p represents a draft pull, ORing its draft
// field with the WIP-title convention (isDraftTitle) — Forgejo instances
// may signal draft state through either.
func isDraftPull(p forgejoPullPayload) bool {
	return p.Draft || isDraftTitle(p.Title)
}

// getPull fetches the single pull request identified by prURL from the
// configured repo (the adapter is single-repo: owner/repo is never parsed
// out of prURL itself, only the trailing index is).
func (f *forgejoCodeForge) getPull(prURL string) (forgejoPullPayload, error) {
	index, err := parsePRIndex(prURL)
	if err != nil {
		return forgejoPullPayload{}, err
	}
	var payload forgejoPullPayload
	status, err := f.rest.do(http.MethodGet, f.rest.repoPath()+"/pulls/"+index, nil, &payload)
	if err != nil {
		return forgejoPullPayload{}, err
	}
	if status != http.StatusOK {
		return forgejoPullPayload{}, fmt.Errorf("forgejo: pull %s: unexpected status %d", index, status)
	}
	return payload, nil
}

// PRState returns the canonical state of the pull at prURL: merged pulls
// report forge.PRMerged regardless of their raw state string (Forgejo
// reports a merged pull as state=closed, merged=true), otherwise a closed
// pull reports forge.PRClosed and anything else forge.PROpen.
func (f *forgejoCodeForge) PRState(prURL string) (forge.PRState, error) {
	p, err := f.getPull(prURL)
	if err != nil {
		return "", err
	}
	switch {
	case p.Merged:
		return forge.PRMerged, nil
	case p.State == "closed":
		return forge.PRClosed, nil
	default:
		return forge.PROpen, nil
	}
}

// HeadCommitSHA returns the pull's current head commit SHA.
func (f *forgejoCodeForge) HeadCommitSHA(prURL string) (string, error) {
	p, err := f.getPull(prURL)
	if err != nil {
		return "", err
	}
	return p.Head.Sha, nil
}

// Mergeable returns the pull's content-mergeability state, translating
// Forgejo's boolean mergeable field into the canonical two-value
// forge.MergeableState (Forgejo has no third "unknown" state to report).
func (f *forgejoCodeForge) Mergeable(prURL string) (forge.MergeableState, error) {
	p, err := f.getPull(prURL)
	if err != nil {
		return forge.MergeableUnknown, err
	}
	if p.Mergeable {
		return forge.MergeableMergeable, nil
	}
	return forge.MergeableConflicting, nil
}

// listPulls fetches a page of pulls in the given state ("open" or "all")
// from the configured repo, bounded by forge.ResultPageLimit.
func (f *forgejoCodeForge) listPulls(state string) ([]forgejoPullPayload, error) {
	q := url.Values{
		"state": {state},
		"limit": {strconv.Itoa(forge.ResultPageLimit)},
	}
	var payload []forgejoPullPayload
	status, err := f.rest.do(http.MethodGet, f.rest.repoPath()+"/pulls?"+q.Encode(), nil, &payload)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("forgejo: pull list (state=%s): unexpected status %d", state, status)
	}
	return payload, nil
}

// OpenPRForBranch returns the open, non-draft pull whose head matches
// branch, if any. Draft status is read from either the pull's draft field
// or its WIP-title convention (isDraftPull) — a draft match is never
// adopted.
func (f *forgejoCodeForge) OpenPRForBranch(branch string) (forge.PR, bool, error) {
	pulls, err := f.listPulls("open")
	if err != nil {
		return forge.PR{}, false, err
	}
	for _, p := range pulls {
		if p.Head.Ref == branch && !isDraftPull(p) {
			return forge.PR{URL: p.HTMLURL, IsDraft: false}, true, nil
		}
	}
	return forge.PR{}, false, nil
}

// PRForBranch returns the URL of any pull (any state, any draft status)
// whose head matches branch, if any.
func (f *forgejoCodeForge) PRForBranch(branch string) (string, bool, error) {
	pulls, err := f.listPulls("all")
	if err != nil {
		return "", false, err
	}
	for _, p := range pulls {
		if p.Head.Ref == branch {
			return p.HTMLURL, true, nil
		}
	}
	return "", false, nil
}

// maxFailureDetailBytes bounds the string FailureDetail returns, so a large
// CI log excerpt cannot blow the fix Box's env/prompt budget.
const maxFailureDetailBytes = 4000

// forgejoCombinedStatus is the shape Forgejo's combined commit-status
// endpoint (/commits/{sha}/status) returns: an already-aggregated state
// across every status posted against the commit, plus how many contributed.
type forgejoCombinedStatus struct {
	State      string `json:"state"`
	TotalCount int    `json:"total_count"`
}

// forgejoRollupStates maps Forgejo's combined-status state string to the
// canonical forge.RollupState.
var forgejoRollupStates = map[string]forge.RollupState{
	"success": forge.StateSuccess,
	"pending": forge.StatePending,
	"failure": forge.StateFailure,
	"error":   forge.StateError,
}

// CheckState returns the aggregate CI status of the PR's head commit, read
// from Forgejo's combined commit-status endpoint — which, like GitHub's
// GraphQL statusCheckRollup CheckState trusts, already computes the
// aggregate across every status posted against the commit, so this does not
// recompute it from the individual statuses itself. Returns forge.StateNone
// when no statuses are registered on the commit (an empty state string or a
// zero total_count).
func (f *forgejoCodeForge) CheckState(prURL string) (forge.RollupState, error) {
	p, err := f.getPull(prURL)
	if err != nil {
		return forge.StateNone, err
	}
	var combined forgejoCombinedStatus
	status, err := f.rest.do(http.MethodGet, f.rest.repoPath()+"/commits/"+url.PathEscape(p.Head.Sha)+"/status", nil, &combined)
	if err != nil {
		return forge.StateNone, err
	}
	if status != http.StatusOK {
		return forge.StateNone, fmt.Errorf("forgejo: commit status %s: unexpected status %d", p.Head.Sha, status)
	}
	if combined.State == "" || combined.TotalCount == 0 {
		return forge.StateNone, nil
	}
	if rs, ok := forgejoRollupStates[combined.State]; ok {
		return rs, nil
	}
	return forge.StateNone, nil
}

// forgejoStatus is one entry of Forgejo's commit-status list
// (/commits/{sha}/statuses): a single reported status, distinct from the
// combined/aggregate shape forgejoCombinedStatus reads.
type forgejoStatus struct {
	Context     string `json:"context"`
	State       string `json:"state"`
	Description string `json:"description"`
}

// forgejoFailingStatusStates are the forgejoStatus.State values that
// represent a genuine failure, as opposed to success or pending.
var forgejoFailingStatusStates = map[string]bool{
	"failure": true,
	"error":   true,
}

// FailureDetail renders the PR head commit's failing statuses into a
// bounded, human-readable excerpt: one "context: STATE" header per failing
// status (state upper-cased, mirroring the github adapter's
// FAILURE/ERROR/... conclusion rendering) plus its description, truncated
// to maxFailureDetailBytes. Returns "" when nothing is currently failing.
// The fetch is best-effort in intent — callers should treat a non-nil error
// as "detail unavailable" — but a genuine HTTP failure is still surfaced as
// an error rather than silently swallowed.
func (f *forgejoCodeForge) FailureDetail(prURL string) (string, error) {
	p, err := f.getPull(prURL)
	if err != nil {
		return "", err
	}
	var statuses []forgejoStatus
	status, err := f.rest.do(http.MethodGet, f.rest.repoPath()+"/commits/"+url.PathEscape(p.Head.Sha)+"/statuses", nil, &statuses)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("forgejo: commit statuses %s: unexpected status %d", p.Head.Sha, status)
	}
	var b strings.Builder
	for _, s := range statuses {
		if !forgejoFailingStatusStates[s.State] {
			continue
		}
		fmt.Fprintf(&b, "%s: %s\n", s.Context, strings.ToUpper(s.State))
		if s.Description != "" {
			fmt.Fprintf(&b, "%s\n", s.Description)
		}
		b.WriteString("---\n")
	}
	out := strings.TrimSpace(b.String())
	if len(out) > maxFailureDetailBytes {
		out = out[:maxFailureDetailBytes]
	}
	return out, nil
}

// forgejoPRFile is the shape Forgejo's pulls/{index}/files endpoint returns
// for each changed file.
type forgejoPRFile struct {
	Filename string `json:"filename"`
}

// ListPRFiles returns every path changed by the PR (added, modified, and
// deleted alike). A deleted file is still reported under its old path.
func (f *forgejoCodeForge) ListPRFiles(prURL string) ([]string, error) {
	index, err := parsePRIndex(prURL)
	if err != nil {
		return nil, err
	}
	var payload []forgejoPRFile
	status, err := f.rest.do(http.MethodGet, f.rest.repoPath()+"/pulls/"+index+"/files", nil, &payload)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("forgejo: pull %s files: unexpected status %d", index, status)
	}
	var files []string
	for _, file := range payload {
		if file.Filename != "" {
			files = append(files, file.Filename)
		}
	}
	return files, nil
}

// forgejoCompare is the subset of Forgejo's compare-API response NeedsUpdate
// reads: total_commits is the number of commits on the compare's head side
// that its base side does not contain.
type forgejoCompare struct {
	TotalCommits int `json:"total_commits"`
}

// NeedsUpdate reports whether the PR's base branch has commits its head
// branch has not yet incorporated. Forgejo's compare API
// (/compare/{base}...{head}) returns only the commits reachable from head
// but not base — its own "ahead" set, with no behind_by counterpart like
// GitHub's compare. To count the reverse — commits the base branch has that
// the PR's head is missing — the two refs are swapped, so the head side of
// the compare is the PR's base branch: total_commits then counts exactly the
// commits the PR is behind by. Ref names are path-escaped since this
// project's own agent branches (agent/issue-N) contain a slash.
func (f *forgejoCodeForge) NeedsUpdate(prURL string) (bool, error) {
	p, err := f.getPull(prURL)
	if err != nil {
		return false, err
	}
	// {head}...{base}: commits on base not reachable from the PR's head.
	basehead := url.PathEscape(p.Head.Ref) + "..." + url.PathEscape(p.Base.Ref)
	var cmp forgejoCompare
	status, err := f.rest.do(http.MethodGet, f.rest.repoPath()+"/compare/"+basehead, nil, &cmp)
	if err != nil {
		return false, err
	}
	if status != http.StatusOK {
		return false, fmt.Errorf("forgejo: compare %s: unexpected status %d", basehead, status)
	}
	return cmp.TotalCommits > 0, nil
}

// forgejoAutoMergeStylesPayload is the subset of the Forgejo repository
// object CanAutoMerge reads: the three merge styles a repo can permit.
type forgejoAutoMergeStylesPayload struct {
	AllowMergeCommits bool `json:"allow_merge_commits"`
	AllowRebase       bool `json:"allow_rebase"`
	AllowSquashMerge  bool `json:"allow_squash_merge"`
}

// CanAutoMerge reports whether the repo permits at least one merge style.
// Forgejo has no single "auto-merge allowed" repo flag the way GitHub's
// autoMergeAllowed does; its native scheduled/merge-when-checks-succeed
// merge is available whenever the repo permits any merge style at all, so
// that (rather than a dedicated flag) is the signal read here.
func (f *forgejoCodeForge) CanAutoMerge() (bool, error) {
	var repo forgejoAutoMergeStylesPayload
	status, err := f.rest.do(http.MethodGet, f.rest.repoPath(), nil, &repo)
	if err != nil {
		return false, err
	}
	if status != http.StatusOK {
		return false, fmt.Errorf("forgejo: repo: unexpected status %d", status)
	}
	return repo.AllowMergeCommits || repo.AllowRebase || repo.AllowSquashMerge, nil
}

// EnqueueAutoMerge enqueues Forgejo's native merge-when-checks-succeed
// (scheduled merge) for the PR: POSTing to the pull's merge endpoint with
// merge_when_checks_succeed=true queues the merge rather than performing it
// immediately, mirroring GitHub's native auto-merge semantics. Requests
// f.mergeMethod's style (forgejoMergeDo), the same knob Merge itself uses.
func (f *forgejoCodeForge) EnqueueAutoMerge(prURL string) error {
	index, err := parsePRIndex(prURL)
	if err != nil {
		return err
	}
	body := map[string]any{
		"Do":                        forgejoMergeDo(f.mergeMethod),
		"merge_when_checks_succeed": true,
		"delete_branch_after_merge": true,
	}
	status, err := f.rest.do(http.MethodPost, f.rest.repoPath()+"/pulls/"+index+"/merge", body, nil)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("forgejo: enqueue auto-merge %s: unexpected status %d", index, status)
	}
	return nil
}

// MarkReady flips the PR out of draft by PATCHing its title with the
// WIP-prefix stripped. Idempotent: a PR that is already not a draft is a
// no-op that issues no request, mirroring the github adapter's
// `gh pr ready` idempotency without relying on Forgejo returning a
// particular status for a redundant call.
func (f *forgejoCodeForge) MarkReady(prURL string) error {
	p, err := f.getPull(prURL)
	if err != nil {
		return err
	}
	if !isDraftTitle(p.Title) {
		return nil
	}
	index, err := parsePRIndex(prURL)
	if err != nil {
		return err
	}
	body := map[string]any{"title": stripWIPPrefix(p.Title)}
	status, err := f.rest.do(http.MethodPatch, f.rest.repoPath()+"/pulls/"+index, body, nil)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("forgejo: mark ready %s: unexpected status %d", index, status)
	}
	return nil
}

// MarkDraft flips the PR back to draft by PATCHing its title with a leading
// WIP prefix — the inverse of MarkReady. Idempotent the same way: a PR
// that's already draft (WIP-titled) is a no-op that issues no request.
func (f *forgejoCodeForge) MarkDraft(prURL string) error {
	p, err := f.getPull(prURL)
	if err != nil {
		return err
	}
	if isDraftTitle(p.Title) {
		return nil
	}
	index, err := parsePRIndex(prURL)
	if err != nil {
		return err
	}
	body := map[string]any{"title": forgejoWIPPrefix + " " + p.Title}
	status, err := f.rest.do(http.MethodPatch, f.rest.repoPath()+"/pulls/"+index, body, nil)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("forgejo: mark draft %s: unexpected status %d", index, status)
	}
	return nil
}

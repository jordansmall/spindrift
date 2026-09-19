package forgejo

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"spindrift.dev/launcher/internal/forge"
)

type forgejoPullRef struct {
	Ref string `json:"ref"`
	Sha string `json:"sha"`
}

// forgejoPullPayload is the subset of Forgejo's pull-request REST shape this
// adapter reads.
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

// parsePRIndex extracts the pull index from the last path segment of a
// Forgejo PR html_url, rejecting an empty or non-numeric segment.
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

// forgejoWIPPrefix is the only WIP-title prefix the write side produces, so
// every PR this adapter flips to draft carries this canonical spelling.
const forgejoWIPPrefix = "WIP:"

// forgejoWIPPrefixes are the markers the read side accepts, matching Forgejo's
// default setting.Repository.PullRequest.WorkInProgressPrefixes. An instance
// may write a draft title with either convention, so both must be recognized.
var forgejoWIPPrefixes = []string{forgejoWIPPrefix, "[WIP]:"}

// isDraftTitle reports whether title carries a WIP-prefix draft marker,
// case-insensitively. Forgejo can encode draft state in the title alone.
func isDraftTitle(title string) bool {
	upper := strings.ToUpper(strings.TrimSpace(title))
	for _, prefix := range forgejoWIPPrefixes {
		if strings.HasPrefix(upper, strings.ToUpper(prefix)) {
			return true
		}
	}
	return false
}

// stripWIPPrefix removes a leading, case-insensitive WIP marker and the spaces
// after it, returning an unrecognized title unchanged.
func stripWIPPrefix(title string) string {
	trimmed := strings.TrimSpace(title)
	upper := strings.ToUpper(trimmed)
	for _, prefix := range forgejoWIPPrefixes {
		if strings.HasPrefix(upper, strings.ToUpper(prefix)) {
			return strings.TrimLeft(trimmed[len(prefix):], " ")
		}
	}
	return title
}

// isDraftPull reports whether p is a draft, by the draft field or the WIP
// title, since Forgejo instances signal draft state through either.
func isDraftPull(p forgejoPullPayload) bool {
	return p.Draft || isDraftTitle(p.Title)
}

// getPull fetches the pull identified by prURL from the configured repo. The
// adapter is single-repo: only the trailing index is parsed out of prURL, never
// owner/repo.
func (f *forgejoCodeForge) getPull(prURL string) (forgejoPullPayload, error) {
	index, err := parsePRIndex(prURL)
	if err != nil {
		return forgejoPullPayload{}, err
	}
	var payload forgejoPullPayload
	if err := f.rest.Do(http.MethodGet, f.repoPath()+"/pulls/"+index, nil, &payload); err != nil {
		return forgejoPullPayload{}, err
	}
	return payload, nil
}

// PRState returns the canonical state of the pull at prURL. The merged field
// wins over the state string, because Forgejo reports a merged pull as
// state=closed, merged=true.
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

// Mergeable returns the pull's content-mergeability state. Forgejo's mergeable
// field is a boolean, so this never reports forge.MergeableUnknown on success.
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

// listPulls walks every page of the pulls listing in the given state ("open" or
// "all"), rather than fetching one page bounded by forge.ResultPageLimit
// (issue #2265).
func (f *forgejoCodeForge) listPulls(state string) ([]forgejoPullPayload, error) {
	var pulls []forgejoPullPayload
	err := f.rest.Paginate(func(page int) (bool, error) {
		q := url.Values{
			"state": {state},
			"limit": {strconv.Itoa(forge.ResultPageLimit)},
			"page":  {strconv.Itoa(page)},
		}
		var payload []forgejoPullPayload
		if err := f.rest.Do(http.MethodGet, f.repoPath()+"/pulls?"+q.Encode(), nil, &payload); err != nil {
			return false, err
		}
		pulls = append(pulls, payload...)
		return len(payload) < forge.ResultPageLimit, nil
	})
	if err != nil {
		return nil, err
	}
	return pulls, nil
}

// OpenPRForBranch returns the open pull whose head matches branch, draft or not
// (issue #2408). A stranded draft is as adoptable as a ready one, since
// SettleAdopted calls the idempotent MarkReady at green. The returned forge.PR
// deliberately omits draft status, keeping adoption draft-blind.
func (f *forgejoCodeForge) OpenPRForBranch(branch string) (forge.PR, bool, error) {
	pulls, err := f.listPulls("open")
	if err != nil {
		return forge.PR{}, false, err
	}
	for _, p := range pulls {
		if p.Head.Ref != branch {
			continue
		}
		return forge.PR{URL: p.HTMLURL}, true, nil
	}
	return forge.PR{}, false, nil
}

// PRForBranch returns the URL of any pull, in any state, whose head matches
// branch.
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

// forgejoCombinedStatus is what /commits/{sha}/status returns: a state already
// aggregated across every status on the commit, plus how many contributed.
type forgejoCombinedStatus struct {
	State      string `json:"state"`
	TotalCount int    `json:"total_count"`
}

var forgejoRollupStates = map[string]forge.RollupState{
	"success": forge.StateSuccess,
	"pending": forge.StatePending,
	"failure": forge.StateFailure,
	"error":   forge.StateError,
}

// CheckState returns the aggregate CI status of the PR's head commit. The
// combined endpoint already aggregates, so this does not recompute from the
// individual statuses. An empty state or a zero total_count means no statuses
// are registered, reported as forge.StateNone.
func (f *forgejoCodeForge) CheckState(prURL string) (forge.RollupState, error) {
	p, err := f.getPull(prURL)
	if err != nil {
		return forge.StateNone, err
	}
	var combined forgejoCombinedStatus
	if err := f.rest.Do(http.MethodGet, f.repoPath()+"/commits/"+url.PathEscape(p.Head.Sha)+"/status", nil, &combined); err != nil {
		return forge.StateNone, err
	}
	if combined.State == "" || combined.TotalCount == 0 {
		return forge.StateNone, nil
	}
	if rs, ok := forgejoRollupStates[combined.State]; ok {
		return rs, nil
	}
	return forge.StateNone, nil
}

// forgejoStatus is one entry of /commits/{sha}/statuses, a single reported
// status rather than the aggregate forgejoCombinedStatus holds.
type forgejoStatus struct {
	Context     string `json:"context"`
	State       string `json:"state"`
	Description string `json:"description"`
}

var forgejoFailingStatusStates = map[string]bool{
	"failure": true,
	"error":   true,
}

// FailureDetail renders the head commit's failing statuses into a bounded
// excerpt, returning "" when nothing is failing. forge.RenderFailureDetail owns
// the formatting and the forge.MaxFailureDetailBytes truncation. Callers should
// treat a non-nil error as "detail unavailable".
func (f *forgejoCodeForge) FailureDetail(prURL string) (string, error) {
	p, err := f.getPull(prURL)
	if err != nil {
		return "", err
	}
	var statuses []forgejoStatus
	if err := f.rest.Do(http.MethodGet, f.repoPath()+"/commits/"+url.PathEscape(p.Head.Sha)+"/statuses", nil, &statuses); err != nil {
		return "", err
	}
	var entries []forge.FailureDetailEntry
	for _, s := range statuses {
		if !forgejoFailingStatusStates[s.State] {
			continue
		}
		entries = append(entries, forge.FailureDetailEntry{
			Name:    s.Context,
			State:   strings.ToUpper(s.State),
			Summary: s.Description,
		})
	}
	return forge.RenderFailureDetail(entries), nil
}

type forgejoPRFile struct {
	Filename string `json:"filename"`
}

// ListPRFiles returns every path the PR changes. A deleted file is reported
// under its old path.
func (f *forgejoCodeForge) ListPRFiles(prURL string) ([]string, error) {
	index, err := parsePRIndex(prURL)
	if err != nil {
		return nil, err
	}
	var payload []forgejoPRFile
	if err := f.rest.Do(http.MethodGet, f.repoPath()+"/pulls/"+index+"/files", nil, &payload); err != nil {
		return nil, err
	}
	var files []string
	for _, file := range payload {
		if file.Filename != "" {
			files = append(files, file.Filename)
		}
	}
	return files, nil
}

// forgejoCompare reads one compare-API field: total_commits counts the commits
// on the compare's head side that its base side does not contain.
type forgejoCompare struct {
	TotalCommits int `json:"total_commits"`
}

// NeedsUpdate reports whether the PR's base branch has commits its head branch
// is missing. Forgejo's compare API counts only what its own head side adds and
// has no behind_by like GitHub's, so the PR's head goes on the compare's base
// side ({head}...{base}) and total_commits is what the PR is behind by. Ref
// names are path-escaped because agent branches (agent/issue-N) contain a slash.
func (f *forgejoCodeForge) NeedsUpdate(prURL string) (bool, error) {
	p, err := f.getPull(prURL)
	if err != nil {
		return false, err
	}
	headBase := url.PathEscape(p.Head.Ref) + "..." + url.PathEscape(p.Base.Ref)
	var cmp forgejoCompare
	if err := f.rest.Do(http.MethodGet, f.repoPath()+"/compare/"+headBase, nil, &cmp); err != nil {
		return false, err
	}
	return cmp.TotalCommits > 0, nil
}

type forgejoAutoMergeStylesPayload struct {
	AllowMergeCommits bool `json:"allow_merge_commits"`
	AllowRebase       bool `json:"allow_rebase"`
	AllowSquashMerge  bool `json:"allow_squash_merge"`
}

// CanAutoMerge reports whether the repo permits at least one merge style.
// Forgejo has no autoMergeAllowed flag like GitHub's; its scheduled merge works
// whenever any merge style is permitted, so that is the signal read here.
func (f *forgejoCodeForge) CanAutoMerge() (bool, error) {
	var repo forgejoAutoMergeStylesPayload
	if err := f.rest.Do(http.MethodGet, f.repoPath(), nil, &repo); err != nil {
		return false, err
	}
	return repo.AllowMergeCommits || repo.AllowRebase || repo.AllowSquashMerge, nil
}

// EnqueueAutoMerge queues Forgejo's scheduled merge for the PR.
// merge_when_checks_succeed=true makes the merge endpoint queue rather than
// merge immediately. The style comes from f.mergeMethod, as in Merge.
func (f *forgejoCodeForge) EnqueueAutoMerge(prURL string) error {
	index, err := parsePRIndex(prURL)
	if err != nil {
		return err
	}
	return f.postMerge(index, map[string]any{"merge_when_checks_succeed": true})
}

// MarkReady flips the PR out of draft by PATCHing its title with the WIP prefix
// stripped. A PR that is already not a draft is a no-op issuing no request. The
// gate is isDraftPull, not isDraftTitle, so MarkReady can act on every draft
// OpenPRForBranch adopts, including one signaled by the draft field alone.
func (f *forgejoCodeForge) MarkReady(prURL string) error {
	p, err := f.getPull(prURL)
	if err != nil {
		return err
	}
	if !isDraftPull(p) {
		return nil
	}
	index, err := parsePRIndex(prURL)
	if err != nil {
		return err
	}
	body := map[string]any{"title": stripWIPPrefix(p.Title)}
	return f.rest.Do(http.MethodPatch, f.repoPath()+"/pulls/"+index, body, nil)
}

// MarkDraft flips the PR back to draft by PATCHing a WIP prefix onto its title.
// A PR that is already draft is a no-op issuing no request. The gate is
// isDraftPull, not isDraftTitle, because a pull whose draft field is true but
// whose title has no WIP prefix would otherwise be PATCHed redundantly.
func (f *forgejoCodeForge) MarkDraft(prURL string) error {
	p, err := f.getPull(prURL)
	if err != nil {
		return err
	}
	if isDraftPull(p) {
		return nil
	}
	index, err := parsePRIndex(prURL)
	if err != nil {
		return err
	}
	body := map[string]any{"title": forgejoWIPPrefix + " " + p.Title}
	return f.rest.Do(http.MethodPatch, f.repoPath()+"/pulls/"+index, body, nil)
}

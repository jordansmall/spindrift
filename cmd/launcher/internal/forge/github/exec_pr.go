package github

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	neturl "net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/gitplumbing"
)

// rebaseForcePushTimeout bounds Rebase's trailing force-push so a remote that
// accepts the connection and then hangs server-side can't block it forever.
// Scoped narrowly to this one call rather than porting git.go's full
// opTimeout/WithOpTimeout pattern to execClient: Rebase's other subprocesses
// (gh pr view, gh repo clone, checkout, rebase) are unbounded too, but that's
// tracked as separate follow-up work rather than folded into this fix.
const rebaseForcePushTimeout = 5 * time.Minute

func (e *execClient) OpenPRForBranch(branch string) (forge.PR, bool, error) {
	cmd := exec.Command("gh", "pr", "list",
		"--repo", e.repo,
		"--head", branch,
		"--state", "open",
		"--json", "url",
		"--jq", `.[0].url // ""`,
	)
	out, err := cmd.Output()
	if err != nil {
		return forge.PR{}, false, fmt.Errorf("gh pr list: %w", err)
	}
	url := strings.TrimSpace(string(out))
	if url == "" {
		return forge.PR{}, false, nil
	}
	viewCmd := exec.Command("gh", "pr", "view", url, "--json", "isDraft", "--jq", ".isDraft")
	out, err = viewCmd.Output()
	if err != nil {
		// Cannot determine draft status — do not adopt.
		return forge.PR{}, false, fmt.Errorf("gh pr view %s isDraft: %w", url, err)
	}
	isDraft := strings.TrimSpace(string(out)) == "true"
	return forge.PR{URL: url, IsDraft: isDraft}, true, nil
}

func (e *execClient) PRForBranch(branch string) (string, bool, error) {
	cmd := exec.Command("gh", "pr", "list",
		"--repo", e.repo,
		"--head", branch,
		"--state", "all",
		"--json", "url",
		"--jq", `.[0].url // ""`,
	)
	out, err := cmd.Output()
	if err != nil {
		return "", false, fmt.Errorf("gh pr list: %w", err)
	}
	url := strings.TrimSpace(string(out))
	if url == "" {
		return "", false, nil
	}
	return url, true, nil
}

func (e *execClient) PRState(url string) (forge.PRState, error) {
	cmd := exec.Command("gh", "pr", "view", url, "--json", "state", "--jq", ".state")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("gh pr view %s state: %w", url, err)
	}
	return forge.PRState(strings.TrimSpace(string(out))), nil
}

// CheckState queries the aggregate statusCheckRollup state of the PR's head
// commit via GraphQL and returns the result as a RollupState. Returns StateNone
// when no checks are registered or the rollup is absent.
func (e *execClient) CheckState(url string) (forge.RollupState, error) {
	// Parse https://github.com/OWNER/REPO/pull/NUMBER
	parts := strings.Split(url, "/")
	if len(parts) < 7 {
		return forge.StateNone, fmt.Errorf("invalid PR URL: %s", url)
	}
	owner, repo, number := parts[3], parts[4], parts[6]
	const gql = `query($owner:String!,$repo:String!,$number:Int!){repository(owner:$owner,name:$repo){pullRequest(number:$number){commits(last:1){nodes{commit{statusCheckRollup{state}}}}}}}`
	cmd := exec.Command("gh", "api", "graphql",
		"-f", "query="+gql,
		"-f", "owner="+owner,
		"-f", "repo="+repo,
		"-F", "number="+number,
		"--jq", `.data.repository.pullRequest.commits.nodes[0].commit.statusCheckRollup.state // ""`,
	)
	out, err := cmd.Output()
	if err != nil {
		return forge.StateNone, fmt.Errorf("gh api graphql: %w", err)
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return forge.StateNone, nil
	}
	return forge.RollupState(s), nil
}

// Mergeable queries the PR's content-mergeability state via GraphQL — the
// `mergeable` field, distinct from the statusCheckRollup CheckState queries —
// so Merge can tell a genuine conflict (CONFLICTING) apart from a PR that is
// merely blocked by pending or failing checks (MERGEABLE).
func (e *execClient) Mergeable(url string) (forge.MergeableState, error) {
	parts := strings.Split(url, "/")
	if len(parts) < 7 {
		return forge.MergeableUnknown, fmt.Errorf("invalid PR URL: %s", url)
	}
	owner, repo, number := parts[3], parts[4], parts[6]
	const gql = `query($owner:String!,$repo:String!,$number:Int!){repository(owner:$owner,name:$repo){pullRequest(number:$number){mergeable}}}`
	cmd := exec.Command("gh", "api", "graphql",
		"-f", "query="+gql,
		"-f", "owner="+owner,
		"-f", "repo="+repo,
		"-F", "number="+number,
		"--jq", `.data.repository.pullRequest.mergeable // ""`,
	)
	out, err := cmd.Output()
	if err != nil {
		return forge.MergeableUnknown, fmt.Errorf("gh api graphql: %w", err)
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return forge.MergeableUnknown, nil
	}
	return forge.MergeableState(s), nil
}

// NeedsUpdate reports whether the PR's base branch has commits its head
// branch has not yet incorporated — via the REST compare API's `behind_by`,
// a pure git-ancestry count between two refs, not GitHub's GraphQL
// mergeStateStatus BEHIND. mergeStateStatus only reports BEHIND when branch
// protection requires branches to be up to date before merging; this
// project's fine-grained PAT cannot even read that setting (403 on the
// branch-protection endpoint), let alone rely on it being enabled, so a
// check gated on it would silently never fire (issue #936). The compare API
// needs no such setting: it always reports the commit-graph relationship
// between the two refs.
//
// This assumes the PR's head ref resolves inside e.repo: basehead below is
// built from the bare headRefName/baseRefName GitHub returns, with no
// owner:branch form, so the compare call only finds a head that lives in
// this same repo — true for this project's own agent/issue-N branches
// (docs/reference.md: "Agent PR branches live in-repo (not forks)"; this
// project requires a single-repo PAT). A fork-sourced head would 404 here
// instead of resolving. That 404 is not specially handled: it comes back as
// an ordinary error, which the caller (preflightStaleBase in
// settle/ready.go) already logs and swallows, falling through to its normal
// Merge attempt.
func (e *execClient) NeedsUpdate(prURL string) (bool, error) {
	out, err := exec.Command("gh", "pr", "view", prURL,
		"--json", "headRefName,baseRefName",
		"--jq", "[.headRefName,.baseRefName]|@tsv",
	).Output()
	if err != nil {
		return false, fmt.Errorf("gh pr view %s: %w", prURL, err)
	}
	fields := strings.SplitN(strings.TrimSpace(string(out)), "\t", 2)
	if len(fields) != 2 {
		return false, fmt.Errorf("gh pr view: unexpected output %q", string(out))
	}
	head, base := fields[0], fields[1]

	// basehead is "base...head": behind_by then counts commits reachable
	// from base but not head — i.e. how many commits the PR's branch is
	// missing from its base's current tip. Ref names are path-escaped since
	// this project's own agent branches (agent/issue-N) contain a slash.
	basehead := neturl.PathEscape(base) + "..." + neturl.PathEscape(head)
	cmpOut, err := exec.Command("gh", "api",
		fmt.Sprintf("repos/%s/compare/%s", e.repo, basehead),
		"--jq", ".behind_by",
	).Output()
	if err != nil {
		return false, fmt.Errorf("gh api compare %s: %w", basehead, err)
	}
	behindBy, convErr := strconv.Atoi(strings.TrimSpace(string(cmpOut)))
	if convErr != nil {
		return false, fmt.Errorf("gh api compare %s: unexpected output %q", basehead, string(cmpOut))
	}
	return behindBy > 0, nil
}

// ListPRFiles returns every path changed by the PR (added, modified, and
// deleted alike) via the REST pulls/files endpoint, which — unlike
// check-runs — works under a fine-grained PAT scoped to Pull requests RW.
// A deleted file is still reported under its old path.
func (e *execClient) ListPRFiles(url string) ([]string, error) {
	parts := strings.Split(url, "/")
	if len(parts) < 7 {
		return nil, fmt.Errorf("invalid PR URL: %s", url)
	}
	owner, repo, number := parts[3], parts[4], parts[6]
	cmd := exec.Command("gh", "api",
		fmt.Sprintf("repos/%s/%s/pulls/%s/files", owner, repo, number),
		"--paginate",
		"--jq", ".[].filename",
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gh api pulls files: %w", err)
	}
	var files []string
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		if f := strings.TrimSpace(sc.Text()); f != "" {
			files = append(files, f)
		}
	}
	return files, nil
}

func (e *execClient) Merge(url string) error {
	var stderr bytes.Buffer
	cmd := exec.Command("gh", "pr", "merge", url, "--rebase", "--delete-branch")
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return e.classifyMergeFailure(url, err, stderr.String())
	}
	return nil
}

// classifyMergeFailure distinguishes a genuine merge conflict from a PR that
// is merely blocked by pending or failing required checks. gh's stderr
// carries the same "not mergeable" wording for both refusals, so the
// distinction is made by querying the PR's mergeable state instead
// (issue #566). A mergeable state this function cannot map to either outcome
// is surfaced as its own error rather than folded into ErrMergeConflict.
func (e *execClient) classifyMergeFailure(url string, mergeErr error, stderr string) error {
	if !gitplumbing.IsMergeConflict(stderr) {
		return fmt.Errorf("gh pr merge %s: %w: %s", url, mergeErr, strings.TrimSpace(stderr))
	}
	state, err := e.Mergeable(url)
	if err != nil {
		return fmt.Errorf("gh pr merge %s: %w (mergeable state unavailable: %v)", url, mergeErr, err)
	}
	switch state {
	case forge.MergeableConflicting:
		return forge.ErrMergeConflict
	case forge.MergeableMergeable:
		return forge.ErrMergeBlockedByChecks
	default:
		return fmt.Errorf("gh pr merge %s: %w (mergeable state %q undetermined)", url, mergeErr, state)
	}
}

// CanAutoMerge queries whether the repo allows GitHub's native auto-merge feature.
func (e *execClient) CanAutoMerge() (bool, error) {
	parts := strings.SplitN(e.repo, "/", 2)
	if len(parts) != 2 {
		return false, fmt.Errorf("invalid repo slug: %q", e.repo)
	}
	owner, repo := parts[0], parts[1]
	const gql = `query($owner:String!,$repo:String!){repository(owner:$owner,name:$repo){autoMergeAllowed}}`
	cmd := exec.Command("gh", "api", "graphql",
		"-f", "query="+gql,
		"-f", "owner="+owner,
		"-f", "repo="+repo,
		"--jq", ".data.repository.autoMergeAllowed",
	)
	out, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("gh api graphql (autoMergeAllowed): %w", err)
	}
	return strings.TrimSpace(string(out)) == "true", nil
}

// EnqueueAutoMerge enqueues GitHub's native auto-merge for the PR. GitHub will
// merge the PR automatically once all branch-protection requirements are met.
func (e *execClient) EnqueueAutoMerge(prURL string) error {
	cmd := exec.Command("gh", "pr", "merge", prURL, "--auto", "--rebase", "--delete-branch")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gh pr merge --auto %s: %w", prURL, err)
	}
	return nil
}

// MarkReady flips the PR out of draft via `gh pr ready`. Already idempotent
// on gh's own side: `gh pr ready` on a PR that's already ready for review
// prints a notice to stderr but exits 0, so the caller (settle's self-heal
// merge gate) can call this unconditionally on every green PR — whether or
// not the driver already flipped it itself — without any extra
// already-ready classification here.
func (e *execClient) MarkReady(prURL string) error {
	var stderr bytes.Buffer
	cmd := exec.Command("gh", "pr", "ready", prURL)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		suffix := ""
		if s := strings.TrimSpace(stderr.String()); s != "" {
			suffix = ": " + s
		}
		return fmt.Errorf("gh pr ready %s: %w%s", prURL, err, suffix)
	}
	return nil
}

// Probe checks that gh is authenticated and the configured repository is
// reachable. It returns the resolved repo slug on success, ErrAuthFailure if
// the credential check fails, or ErrRepoNotFound if the repo cannot be found.
func (e *execClient) Probe() (string, error) {
	if err := exec.Command("gh", "auth", "status").Run(); err != nil {
		return "", fmt.Errorf("%w: %s", forge.ErrAuthFailure, err)
	}
	var stderr bytes.Buffer
	cmd := exec.Command("gh", "repo", "view", e.repo,
		"--json", "nameWithOwner", "--jq", ".nameWithOwner",
	)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%w: %s", forge.ErrRepoNotFound, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

// ListLabels returns the names of all labels defined in the repository.
func (e *execClient) ListLabels() ([]string, error) {
	out, err := exec.Command("gh", "label", "list",
		"--repo", e.repo,
		"--json", "name",
		"--jq", ".[].name",
		"--limit", "100",
	).Output()
	if err != nil {
		return nil, fmt.Errorf("gh label list: %w", err)
	}
	var labels []string
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		if name := strings.TrimSpace(sc.Text()); name != "" {
			labels = append(labels, name)
		}
	}
	return labels, nil
}

// CreateLabel creates a new label in the repository with the given name,
// description, and hex color (without the leading #).
func (e *execClient) CreateLabel(name, description, color string) error {
	out, err := exec.Command("gh", "label", "create", name,
		"--repo", e.repo,
		"--description", description,
		"--color", color,
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("gh label create %q: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Rebase checks out the PR's head branch into a temporary clone of the target
// repository, rebases it onto origin/<base>, and force-pushes the result.
// Returns ErrMergeConflict if the rebase cannot be completed automatically,
// or an error wrapping ErrTransientPushFailure if the force-push fails for a
// reason unrelated to the branch state (callers may retry).
func (e *execClient) Rebase(prURL string) error {
	out, err := exec.Command("gh", "pr", "view", prURL,
		"--json", "headRefName,baseRefName",
		"--jq", "[.headRefName,.baseRefName]|@tsv",
	).Output()
	if err != nil {
		return fmt.Errorf("gh pr view %s: %w", prURL, err)
	}
	fields := strings.SplitN(strings.TrimSpace(string(out)), "\t", 2)
	if len(fields) != 2 {
		return fmt.Errorf("gh pr view: unexpected output %q", string(out))
	}
	head, base := fields[0], fields[1]

	dir, err := os.MkdirTemp("", "spindrift-rebase-*")
	if err != nil {
		return fmt.Errorf("mkdtemp: %w", err)
	}
	defer os.RemoveAll(dir)

	if err := exec.Command("gh", "repo", "clone", e.repo, dir,
		"--", "--no-single-branch").Run(); err != nil {
		return fmt.Errorf("gh repo clone: %w", err)
	}

	gitIn := func(args ...string) *exec.Cmd {
		return exec.Command("git", append([]string{"-C", dir}, args...)...)
	}

	if err := gitIn("checkout", head).Run(); err != nil {
		return fmt.Errorf("git checkout %s: %w", head, err)
	}
	if err := gitIn("rebase", "origin/"+base).Run(); err != nil {
		_ = gitIn("rebase", "--abort").Run()
		return forge.ErrMergeConflict
	}
	ctx, cancel := context.WithTimeout(context.Background(), rebaseForcePushTimeout)
	defer cancel()
	return gitplumbing.GitForcePush(ctx, dir)
}

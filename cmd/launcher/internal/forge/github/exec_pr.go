package github

import (
	"bufio"
	"bytes"
	"context"
	"errors"
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
		return forge.PR{}, false, ghCommandErr("gh pr list", err)
	}
	url := strings.TrimSpace(string(out))
	if url == "" {
		return forge.PR{}, false, nil
	}
	return forge.PR{URL: url}, true, nil
}

// BranchExists reports whether branch exists on the remote, independent of
// any PR. matching-refs prefix-matches, so the result is filtered to an
// exact "refs/heads/<branch>" match rather than trusting a non-empty
// response. branch becomes one path segment of the API URL rather than a
// standalone gh argument, so it can't be misparsed as a flag the way
// gitClient's ls-remote-based BranchExists guards against; an empty branch
// is still rejected since it would otherwise query every ref under heads/.
func (e *execClient) BranchExists(branch string) (bool, error) {
	if branch == "" {
		return false, fmt.Errorf("branch must not be empty")
	}
	cmd := exec.Command("gh", "api",
		fmt.Sprintf("repos/%s/git/matching-refs/heads/%s", e.repo, branch),
		"--jq", ".[].ref",
	)
	out, err := cmd.Output()
	if err != nil {
		return false, ghCommandErr(fmt.Sprintf("gh api matching-refs heads/%s", branch), err)
	}
	want := "refs/heads/" + branch
	for _, ref := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if ref == want {
			return true, nil
		}
	}
	return false, nil
}

// BranchProtected reports whether branch has protection configured, via
// GET repos/{repo}/branches/{branch}/protection. GitHub returns 404 "Branch
// not protected" when the branch carries no *classic* protection rule --
// but that endpoint never sees a branch protected solely by a repository
// ruleset (the mechanism README.md and SECURITY.md instruct operators to
// configure), so a "Branch not protected" 404 falls through to
// branchProtectedByRuleset, and the classic mechanism is known false: the
// ruleset count alone decides the definitive answer.
//
// The classic endpoint also requires the token's Administration: read
// permission, which this project's own documented fine-grained PAT scope
// (Contents/Pull requests/Issues RW + Metadata R -- see docs/reference.md)
// does not grant, so on the documented deployment the classic endpoint
// returns HTTP 403 rather than the 404 body above. A 403 falls through to
// branchProtectedByRuleset too, since Metadata: read is sufficient for that
// endpoint -- but unlike the 404 case, a 403 means the classic mechanism
// was never actually read, so a ruleset count of zero here does NOT license
// a definitive false: the branch could still carry a classic-only rule this
// token simply can't see. Only ruleset count > 0 is definitive on the 403
// path (a ruleset alone is sufficient to protect); count == 0 degrades to
// an error, per BranchProtectionForge's contract that a non-nil error means
// the probe couldn't determine the answer, never "determined unprotected".
//
// Any other gh api failure (network, a scope insufficient for both
// endpoints, etc.) means the probe itself couldn't determine the answer --
// returned as a non-nil error, never as a false "not protected".
func (e *execClient) BranchProtected(branch string) (bool, error) {
	if branch == "" {
		return false, fmt.Errorf("branch must not be empty")
	}
	var stderr bytes.Buffer
	cmd := exec.Command("gh", "api",
		fmt.Sprintf("repos/%s/branches/%s/protection", e.repo, branch),
	)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		classicKnownUnprotected := strings.Contains(stderr.String(), "Branch not protected")
		classicUnreadable := strings.Contains(stderr.String(), "HTTP 403")
		if classicKnownUnprotected || classicUnreadable {
			protected, rerr := e.branchProtectedByRuleset(branch)
			if rerr != nil {
				return false, rerr
			}
			if protected || classicKnownUnprotected {
				return protected, nil
			}
			return false, fmt.Errorf("gh api branches/%s/protection: HTTP 403 (classic protection unreadable) and no ruleset applies -- cannot determine whether %s carries a classic-only protection rule", branch, branch)
		}
		return false, ghCommandErrText(fmt.Sprintf("gh api branches/%s/protection", branch), err, stderr.String())
	}
	return true, nil
}

// branchProtectedByRuleset covers the GitHub branch-protection mechanism
// the classic branches/{branch}/protection endpoint can't see: repository
// rulesets. GET repos/{repo}/rules/branches/{branch} returns every ruleset
// rule that currently applies to branch, evaluated server-side (so a
// wildcard target like "release/*" is matched without the caller having to
// replicate GitHub's own targeting logic) -- 200 with an empty array when
// none apply, never a 404, so a bare gh api failure here is always a
// genuine probe failure. --jq length collapses the array to a count so the
// answer is a single line of stdout, mirroring BranchExists' --jq usage.
func (e *execClient) branchProtectedByRuleset(branch string) (bool, error) {
	cmd := exec.Command("gh", "api",
		fmt.Sprintf("repos/%s/rules/branches/%s", e.repo, branch),
		"--jq", "length",
	)
	out, err := cmd.Output()
	if err != nil {
		return false, ghCommandErr(fmt.Sprintf("gh api rules/branches/%s", branch), err)
	}
	n, parseErr := strconv.Atoi(strings.TrimSpace(string(out)))
	if parseErr != nil {
		return false, fmt.Errorf("gh api rules/branches/%s: parse response: %w", branch, parseErr)
	}
	return n > 0, nil
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
		return "", false, ghCommandErr("gh pr list", err)
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
		return "", ghCommandErr(fmt.Sprintf("gh pr view %s state", url), err)
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
		return forge.StateNone, ghCommandErr("gh api graphql (statusCheckRollup)", err)
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return forge.StateNone, nil
	}
	return forge.RollupState(s), nil
}

// HeadCommitSHA returns the PR's current head commit SHA via `gh pr view`.
func (e *execClient) HeadCommitSHA(url string) (string, error) {
	out, err := exec.Command("gh", "pr", "view", url, "--json", "headRefOid", "--jq", ".headRefOid").Output()
	if err != nil {
		return "", ghCommandErr(fmt.Sprintf("gh pr view %s headRefOid", url), err)
	}
	return strings.TrimSpace(string(out)), nil
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
		return forge.MergeableUnknown, ghCommandErr("gh api graphql (mergeable)", err)
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
		return false, ghCommandErr(fmt.Sprintf("gh pr view %s", prURL), err)
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
		return false, ghCommandErr(fmt.Sprintf("gh api compare %s", basehead), err)
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
		return nil, ghCommandErr("gh api pulls files", err)
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
	cmd := exec.Command("gh", "pr", "merge", url, mergeMethodFlag(e.mergeMethod), "--delete-branch")
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return e.classifyMergeFailure(url, err, stderr.String())
	}
	return nil
}

// mergeMethodFlag maps the MERGE_METHOD knob's value onto gh pr merge's
// native flag. An empty method (unset) resolves to --rebase, matching the
// literal `--rebase` this package hard-coded before the knob existed, so an
// unset MERGE_METHOD stays byte-identical to prior behavior.
func mergeMethodFlag(method string) string {
	switch method {
	case "merge":
		return "--merge"
	case "squash":
		return "--squash"
	default:
		return "--rebase"
	}
}

// classifyMergeFailure distinguishes a genuine merge conflict from a PR that
// is merely blocked by pending or failing required checks. gh's stderr
// carries the same "not mergeable" wording for both refusals, so the
// distinction is made by querying the PR's mergeable state instead
// (issue #566) and mapping it via the shared forge.ClassifyMergeFailure. A
// mergeable state that function cannot map to either outcome is surfaced as
// its own error rather than folded into ErrMergeConflict.
func (e *execClient) classifyMergeFailure(url string, mergeErr error, stderr string) error {
	base := ghCommandErrText(fmt.Sprintf("gh pr merge %s", url), mergeErr, stderr)
	if !gitplumbing.IsMergeConflict(stderr) {
		if gitplumbing.IsMergeTransient(stderr) {
			return fmt.Errorf("%w: %w", base, forge.ErrMergeTransient)
		}
		return base
	}
	state, err := e.Mergeable(url)
	if err != nil {
		return fmt.Errorf("%w (mergeable state unavailable: %v)", base, err)
	}
	if sentinel, ok := forge.ClassifyMergeFailure(state); ok {
		return sentinel
	}
	return fmt.Errorf("%w (mergeable state %q undetermined)", base, state)
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
		return false, ghCommandErr("gh api graphql (autoMergeAllowed)", err)
	}
	return strings.TrimSpace(string(out)) == "true", nil
}

// EnqueueAutoMerge enqueues GitHub's native auto-merge for the PR. GitHub will
// merge the PR automatically once all branch-protection requirements are met.
func (e *execClient) EnqueueAutoMerge(prURL string) error {
	cmd := exec.Command("gh", "pr", "merge", prURL, "--auto", mergeMethodFlag(e.mergeMethod), "--delete-branch")
	if _, err := cmd.Output(); err != nil {
		return ghCommandErr(fmt.Sprintf("gh pr merge --auto %s", prURL), err)
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
	return runGHReadyToggle(prURL, "pr", "ready", prURL)
}

// MarkDraft flips the PR back to draft via `gh pr ready --undo` — the
// inverse of MarkReady. Idempotent on gh's own side the same way: `gh pr
// ready --undo` on a PR that's already a draft prints a notice to stderr
// but exits 0.
func (e *execClient) MarkDraft(prURL string) error {
	return runGHReadyToggle(prURL, "pr", "ready", "--undo", prURL)
}

// runGHReadyToggle runs a `gh` command that flips a PR's ready/draft state
// (MarkReady's `gh pr ready` or MarkDraft's `gh pr ready --undo`), wrapping
// any failure with the command's own stderr for context.
func runGHReadyToggle(prURL string, args ...string) error {
	cmd := exec.Command("gh", args...)
	if _, err := cmd.Output(); err != nil {
		return ghCommandErr(fmt.Sprintf("gh %s %s", strings.Join(args[:len(args)-1], " "), prURL), err)
	}
	return nil
}

// Probe checks that gh is authenticated and the configured repository is
// reachable. It returns the resolved repo slug on success, ErrAuthFailure if
// the credential check fails, ErrRepoNotFound if the repo cannot be found,
// or ErrRateLimit if either gh call failed because GitHub is rate-limiting
// the caller — mutually exclusive with the other two, so a caller checking
// ErrAuthFailure/ErrRepoNotFound first doesn't misreport a throttled
// operator's real cause.
func (e *execClient) Probe() (string, error) {
	if _, err := exec.Command("gh", "auth", "status").Output(); err != nil {
		wrapped := ghCommandErr("gh auth status", err)
		if errors.Is(wrapped, forge.ErrRateLimit) {
			return "", wrapped
		}
		return "", fmt.Errorf("%w: %w", forge.ErrAuthFailure, wrapped)
	}
	var stderr bytes.Buffer
	cmd := exec.Command("gh", "repo", "view", e.repo,
		"--json", "nameWithOwner", "--jq", ".nameWithOwner",
	)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		wrapped := ghCommandErrText(fmt.Sprintf("gh repo view %s", e.repo), err, stderr.String())
		if errors.Is(wrapped, forge.ErrRateLimit) {
			return "", wrapped
		}
		return "", fmt.Errorf("%w: %w", forge.ErrRepoNotFound, wrapped)
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
		return nil, ghCommandErr("gh label list", err)
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
	_, err := exec.Command("gh", "label", "create", name,
		"--repo", e.repo,
		"--description", description,
		"--color", color,
	).Output()
	if err != nil {
		return ghCommandErr(fmt.Sprintf("gh label create %q", name), err)
	}
	return nil
}

// Rebase checks out the PR's head branch into a temporary clone of the target
// repository, rebases it onto origin/<base>, and force-pushes the result.
// When the client's sync method (WithSyncMethod) is "merge", it merges
// origin/<base> in instead of rebasing onto it. Returns ErrMergeConflict if
// the sync cannot be completed automatically, or an error wrapping
// ErrTransientPushFailure if the force-push fails for a reason unrelated to
// the branch state (callers may retry).
func (e *execClient) Rebase(prURL string) error {
	out, err := exec.Command("gh", "pr", "view", prURL,
		"--json", "headRefName,baseRefName",
		"--jq", "[.headRefName,.baseRefName]|@tsv",
	).Output()
	if err != nil {
		return ghCommandErr(fmt.Sprintf("gh pr view %s", prURL), err)
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

	if _, err := exec.Command("gh", "repo", "clone", e.repo, dir,
		"--", "--no-single-branch").Output(); err != nil {
		return ghCommandErr("gh repo clone", err)
	}

	gitIn := func(args ...string) *exec.Cmd {
		return exec.Command("git", append([]string{"-C", dir}, args...)...)
	}

	if err := gitIn("checkout", head).Run(); err != nil {
		return fmt.Errorf("git checkout %s: %w", head, err)
	}
	syncVerb := "rebase"
	if e.syncMethod == "merge" {
		syncVerb = "merge"
	}
	if err := gitIn(syncVerb, "origin/"+base).Run(); err != nil {
		_ = gitIn(syncVerb, "--abort").Run()
		return forge.ErrMergeConflict
	}
	ctx, cancel := context.WithTimeout(context.Background(), rebaseForcePushTimeout)
	defer cancel()
	return gitplumbing.GitForcePush(ctx, dir)
}

var _ forge.BranchProtectionForge = (*execClient)(nil)

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
	"spindrift.dev/launcher/internal/forge/bundlerelay"
	"spindrift.dev/launcher/internal/forge/gitplumbing"
)

// rebaseForcePushTimeout bounds Rebase's trailing force-push so a remote that
// accepts the connection and then hangs server-side cannot block it forever.
// Rebase's other subprocesses stay unbounded.
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

// BranchExists reports whether branch exists on the remote, independent of any
// PR. matching-refs prefix-matches, so the result is filtered to an exact
// "refs/heads/<branch>" match. An empty branch would query every ref under
// heads/, so it is rejected.
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

// BranchProtected reports whether branch has protection, classic rule or
// repository ruleset. A 404 "Branch not protected" rules out a classic rule,
// so the ruleset count decides. A 403 (the documented fine-grained PAT lacks
// Administration: read) means the classic rule went unread, so only a ruleset
// count > 0 is definitive; zero is an error, never "unprotected".
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
		base := ghCommandErrText(fmt.Sprintf("gh api branches/%s/protection", branch), err, stderr.String())
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
			return false, fmt.Errorf("classic protection unreadable and no ruleset applies -- cannot determine whether %s carries a classic-only protection rule: %w", branch, base)
		}
		return false, base
	}
	return true, nil
}

// branchProtectedByRuleset covers what the classic protection endpoint cannot
// see: repository rulesets. GitHub evaluates targeting server-side, so a
// wildcard like "release/*" matches without replicating that logic here. The
// endpoint returns 200 with an empty array when none apply and never 404, so
// any gh failure here is a genuine probe failure.
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

// CheckState returns the aggregate statusCheckRollup state of the PR's head
// commit, or StateNone when no checks are registered.
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

// HeadCommitSHA returns the PR's current head commit SHA.
func (e *execClient) HeadCommitSHA(url string) (string, error) {
	out, err := exec.Command("gh", "pr", "view", url, "--json", "headRefOid", "--jq", ".headRefOid").Output()
	if err != nil {
		return "", ghCommandErr(fmt.Sprintf("gh pr view %s headRefOid", url), err)
	}
	return strings.TrimSpace(string(out)), nil
}

// Mergeable returns the PR's content-mergeability state: the GraphQL
// `mergeable` field, not the statusCheckRollup that CheckState queries, so
// Merge can tell a genuine conflict (CONFLICTING) apart from a PR merely
// blocked by pending or failing checks (MERGEABLE).
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

// NeedsUpdate reports whether the PR's base branch has commits its head branch
// lacks, via the REST compare API's behind_by. GraphQL mergeStateStatus BEHIND
// will not do: it only reports BEHIND when branch protection requires branches
// to be up to date, which this project's PAT cannot even read (issue #936).
// A fork-sourced head 404s here; the caller logs and swallows that error.
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

	// "base...head" makes behind_by count commits reachable from base but not
	// head. Ref names are path-escaped since agent/issue-N contains a slash.
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

// ListPRFiles returns every path changed by the PR, added, modified, and
// deleted alike, a deleted file under its old path. The REST pulls/files
// endpoint works under a fine-grained PAT scoped to Pull requests RW; the
// check-runs endpoint does not.
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

// mergeMethodFlag maps the MERGE_METHOD knob onto gh pr merge's native flag.
// An unset method resolves to --rebase.
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

// classifyMergeFailure tells a genuine merge conflict from a PR merely blocked
// by pending or failing required checks. gh's stderr carries the same "not
// mergeable" wording for both refusals, so the PR's mergeable state decides
// instead (issue #566). A state forge.ClassifyMergeFailure cannot map gets its
// own error rather than being folded into ErrMergeConflict.
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

// CanAutoMerge reports whether the repo allows GitHub's native auto-merge.
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

// EnqueueAutoMerge enqueues GitHub's native auto-merge for the PR, which merges
// once every branch-protection requirement is met.
func (e *execClient) EnqueueAutoMerge(prURL string) error {
	cmd := exec.Command("gh", "pr", "merge", prURL, "--auto", mergeMethodFlag(e.mergeMethod), "--delete-branch")
	if _, err := cmd.Output(); err != nil {
		return ghCommandErr(fmt.Sprintf("gh pr merge --auto %s", prURL), err)
	}
	return nil
}

// MarkReady flips the PR out of draft. gh pr ready on an already-ready PR
// prints a notice to stderr but exits 0, so the caller can call this
// unconditionally on every green PR.
func (e *execClient) MarkReady(prURL string) error {
	return runGHReadyToggle(prURL, "pr", "ready", prURL)
}

// MarkDraft flips the PR back to draft. Idempotent the same way MarkReady is:
// gh pr ready --undo on a PR that is already a draft exits 0.
func (e *execClient) MarkDraft(prURL string) error {
	return runGHReadyToggle(prURL, "pr", "ready", "--undo", prURL)
}

func runGHReadyToggle(prURL string, args ...string) error {
	cmd := exec.Command("gh", args...)
	if _, err := cmd.Output(); err != nil {
		return ghCommandErr(fmt.Sprintf("gh %s %s", strings.Join(args[:len(args)-1], " "), prURL), err)
	}
	return nil
}

// Probe checks that gh is authenticated and the configured repository is
// reachable, returning the resolved repo slug. It returns ErrAuthFailure,
// ErrRepoNotFound, or ErrRateLimit. ErrRateLimit is mutually exclusive with
// the other two, so a caller checking those first does not misreport a
// throttled operator's real cause.
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
// --paginate is load-bearing: one page holds at most demandPageSize labels.
func (e *execClient) ListLabels() ([]string, error) {
	out, err := exec.Command("gh", "api",
		fmt.Sprintf("repos/%s/labels?per_page=%d", e.repo, demandPageSize),
		"--paginate",
		"--jq", ".[].name",
	).Output()
	if err != nil {
		return nil, ghCommandErr("gh api labels", err)
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

// CreateLabel creates a label in the repository. color is hex without the
// leading #.
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

// Rebase checks out the PR's head branch into a temporary clone, rebases it
// onto origin/<base>, and force-pushes. With sync method "merge"
// (WithSyncMethod) it merges origin/<base> in instead. Returns ErrMergeConflict
// only when the sync stops on unmerged paths; any other sync failure is
// returned wrapping git's own output. A force-push that fails with a transport
// or server fault, or that rebaseForcePushTimeout kills, wraps
// ErrTransientPushFailure.
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

	if err := e.httpsClone("rebase")(dir); err != nil {
		return err
	}

	gitIn := func(args ...string) *exec.Cmd {
		return exec.Command("git", append([]string{"-C", dir}, args...)...)
	}

	if err := e.setCommitIdentity(gitIn); err != nil {
		return err
	}
	if out, err := runRedacted(gitIn("checkout", head)); err != nil {
		return fmt.Errorf("git checkout %s: %w: %s", head, err, out)
	}
	syncVerb := "rebase"
	if e.syncMethod == "merge" {
		syncVerb = "merge"
	}
	if syncOut, err := runRedacted(gitIn(syncVerb, "origin/"+base)); err != nil {
		// Unmerged index entries cover content, modify/delete and rename
		// conflicts alike, which matching git's output text misses.
		conflict := hasUnmergedPaths(gitIn)
		_ = gitIn(syncVerb, "--abort").Run()
		if conflict {
			return forge.ErrMergeConflict
		}
		return fmt.Errorf("git %s origin/%s: %w: %s", syncVerb, base, err, syncOut)
	}
	ctx, cancel := context.WithTimeout(context.Background(), rebaseForcePushTimeout)
	defer cancel()
	return gitplumbing.GitForcePush(ctx, dir)
}

// runRedacted runs cmd and returns its trimmed, credential-redacted stdout+stderr.
func runRedacted(cmd *exec.Cmd) (string, error) {
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	return forge.RedactURLCredentials(strings.TrimSpace(buf.String())), err
}

// setCommitIdentity pins the configured committer name and email on the clone
// gitIn runs in, skipping whichever is empty.
func (e *execClient) setCommitIdentity(gitIn func(args ...string) *exec.Cmd) error {
	for _, kv := range [][2]string{{"user.name", e.userName}, {"user.email", e.userEmail}} {
		if kv[1] == "" {
			continue
		}
		if err := gitIn("config", kv[0], kv[1]).Run(); err != nil {
			return fmt.Errorf("git config %s: %w", kv[0], err)
		}
	}
	return nil
}

// hasUnmergedPaths reports whether the clone gitIn runs in has unmerged index
// entries, i.e. a sync stopped on a genuine conflict. A failing ls-files reads
// as no conflict, which fails safe: Rebase returns a plain error, so no
// ResolveConflict Box is dispatched.
func hasUnmergedPaths(gitIn func(args ...string) *exec.Cmd) bool {
	out, err := gitIn("ls-files", "-u").Output()
	return err == nil && len(bytes.TrimSpace(out)) > 0
}

// httpsClone builds the clone closure bundlerelay's Relay, CommitSubjects, and
// PushBranch take as their clone step, and Rebase clones with: op names the
// caller for the wrapped ghCommandErr's description. It clones GitRemote's
// HTTPS URL, not `gh repo clone`, which follows the operator's per-host gh
// git_protocol and so can pick a personal SSH key (issues #4647, #4650). The -c
// pairs after `clone` are clone's own --config: they land in the new repo's
// config, so the plain `git push` the callers run later authenticates through
// the same gh credential helper.
func (e *execClient) httpsClone(op string) func(dir string) error {
	return func(dir string) error {
		url, gitArgs := GitRemote(e.repo)
		args := append([]string{"clone"}, gitArgs...)
		args = append(args, "--no-single-branch", url, dir)
		if _, err := exec.Command("git", args...).Output(); err != nil {
			return ghCommandErr("github: "+op+": git clone", err)
		}
		return nil
	}
}

// PushBranch creates branch on the target repo from localRef in the git repo
// at srcDir, over HTTPS through the gh credential helper (issue #4071, ADR
// 0057); refuses if branch already exists, or is base (issue #4104).
func (e *execClient) PushBranch(srcDir, localRef, branch, base string) error {
	return bundlerelay.PushBranch("github", srcDir, localRef, branch, base, e.httpsClone("push branch"))
}

// DeleteBranch deletes branch from the target repo with the launcher's own
// gh-cli credential (issue #4112).
func (e *execClient) DeleteBranch(branch, base string) error {
	if err := forge.ValidateBranchDelete("github", branch, base); err != nil {
		return err
	}
	cmd := exec.Command("gh", "api", "-X", "DELETE",
		fmt.Sprintf("repos/%s/git/refs/heads/%s", e.repo, branch))
	if _, err := cmd.Output(); err != nil {
		return ghCommandErr("github: delete branch: gh api", err)
	}
	return nil
}

// CreateDraftPR opens a draft PR from head onto base. head and base are
// branch names in e.repo, never a fork's owner:branch form. Read-write and
// read-only settle alike may call it (issue #4071); settle's own gating
// (Open checks BundleRelay first) is what keeps a read-write land from ever
// making a host-side create that conflicts with the Box's own.
func (e *execClient) CreateDraftPR(title, body, base, head string) (string, bool, error) {
	var stderr bytes.Buffer
	cmd := exec.Command("gh", "pr", "create",
		"--repo", e.repo,
		"--draft",
		"--base", base,
		"--head", head,
		"--title", title,
		"--body", body,
	)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		createErr := ghCommandErrText("github: create draft PR: gh pr create", err, stderr.String())
		// A retried or raced host-side create (issue #2407) fails with gh's
		// "already exists" stderr, not a sentinel error. Adopt the branch's
		// open PR rather than report a settled hand-off as blocked, returning
		// created=false so settle's reconstructed-PR path (issue #2447) knows
		// the title and body are not the ones supplied here.
		if strings.Contains(stderr.String(), "already exists") {
			if pr, ok, openErr := e.OpenPRForBranch(head); openErr == nil && ok {
				return pr.URL, false, nil
			}
		}
		return "", false, createErr
	}
	return strings.TrimSpace(string(out)), true, nil
}

var _ forge.BranchProtectionForge = (*execClient)(nil)
var _ forge.BranchPusher = (*execClient)(nil)
var _ forge.BranchDeleter = (*execClient)(nil)
var _ forge.DraftPRCreator = (*execClient)(nil)

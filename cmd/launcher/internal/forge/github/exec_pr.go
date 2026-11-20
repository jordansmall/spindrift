package github

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/bundlerelay"
	"spindrift.dev/launcher/internal/forge/gitplumbing"
	"spindrift.dev/launcher/internal/gitexec"
)

// rebaseForcePushTimeout bounds Rebase's trailing force-push so a remote that
// accepts the connection and then hangs server-side cannot block it forever.
// Rebase's other subprocesses stay unbounded.
const rebaseForcePushTimeout = 5 * time.Minute

func (e *execClient) OpenPRForBranch(branch string) (forge.PR, bool, error) {
	row, err := readRow()
	if err != nil {
		return forge.PR{}, false, err
	}
	return e.openPRForBranchViaConnector(row, branch)
}

// nativeOpenPRForBranch is the gh read OpenPRForBranch was, kept for the native
// write that calls it.
func (e *execClient) nativeOpenPRForBranch(branch string) (forge.PR, bool, error) {
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
	row, err := readRow()
	if err != nil {
		return false, err
	}
	return e.branchExistsViaConnector(row, branch)
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
	row, err := readRow()
	if err != nil {
		return false, err
	}
	return e.branchProtectedViaConnector(row, branch)
}

func (e *execClient) PRForBranch(branch string) (string, bool, error) {
	row, err := readRow()
	if err != nil {
		return "", false, err
	}
	return e.prForBranchViaConnector(row, branch)
}

func (e *execClient) PRState(url string) (forge.PRState, error) {
	row, err := readRow()
	if err != nil {
		return "", err
	}
	return e.prStateViaConnector(row, url)
}

// MergeCommit returns the oid of the commit the merged PR at url landed as, ""
// when GitHub reports none (an unmerged PR).
func (e *execClient) MergeCommit(url string) (string, error) {
	row, err := readRow()
	if err != nil {
		return "", err
	}
	return e.mergeCommitViaConnector(row, url)
}

// CheckState returns the aggregate statusCheckRollup state of the PR's head
// commit, or StateNone when no checks are registered.
func (e *execClient) CheckState(url string) (forge.RollupState, error) {
	row, err := readRow()
	if err != nil {
		return forge.StateNone, err
	}
	return e.checkStateViaConnector(row, url)
}

// CheckRun implements forge.CIRunReporter. The run URL is the first Actions run
// of the head commit still in flight, else the first one, so the link follows
// live work rather than a finished sibling workflow. Only runs under the PR's
// own host, owner and repo count.
func (e *execClient) CheckRun(url string) (forge.RollupState, string, error) {
	row, err := readRow()
	if err != nil {
		return forge.StateNone, "", err
	}
	return e.checkRunViaConnector(row, url)
}

// HeadCommitSHA returns the PR's current head commit SHA.
func (e *execClient) HeadCommitSHA(url string) (string, error) {
	row, err := readRow()
	if err != nil {
		return "", err
	}
	return e.headCommitSHAViaConnector(row, url)
}

// Mergeable returns the PR's content-mergeability state: the GraphQL
// `mergeable` field, not the statusCheckRollup that CheckState queries, so
// Merge can tell a genuine conflict (CONFLICTING) apart from a PR merely
// blocked by pending or failing checks (MERGEABLE).
func (e *execClient) Mergeable(url string) (forge.MergeableState, error) {
	row, err := readRow()
	if err != nil {
		return forge.MergeableUnknown, err
	}
	return e.mergeableViaConnector(row, url)
}

// nativeMergeable is the gh read Mergeable was, kept for the native Merge that
// classifies its failure by it.
func (e *execClient) nativeMergeable(url string) (forge.MergeableState, error) {
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
	row, err := readRow()
	if err != nil {
		return false, err
	}
	return e.needsUpdateViaConnector(row, prURL)
}

// ListPRFiles returns every path changed by the PR, added, modified, and
// deleted alike, a deleted file under its old path. The REST pulls/files
// endpoint works under a fine-grained PAT scoped to Pull requests RW; the
// check-runs endpoint does not.
func (e *execClient) ListPRFiles(url string) ([]string, error) {
	row, err := readRow()
	if err != nil {
		return nil, err
	}
	return e.listPRFilesViaConnector(row, url)
}

func (e *execClient) Merge(url string) error {
	if row, ok := routedRow(); ok {
		return e.mergeViaConnector(row, url)
	}
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
	state, err := e.nativeMergeable(url)
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
	row, err := readRow()
	if err != nil {
		return false, err
	}
	return e.canAutoMergeViaConnector(row)
}

// EnqueueAutoMerge enqueues GitHub's native auto-merge for the PR, which merges
// once every branch-protection requirement is met.
func (e *execClient) EnqueueAutoMerge(prURL string) error {
	if row, ok := routedRow(); ok {
		return e.enqueueAutoMergeViaConnector(row, prURL)
	}
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
	if row, ok := routedRow(); ok {
		return e.markReadyViaConnector(row, prURL)
	}
	err := runGHReadyToggle(prURL, "pr", "ready", prURL)
	if err != nil && gitplumbing.IsMergeTransient(err.Error()) {
		return fmt.Errorf("%w: %w", err, forge.ErrMarkReadyTransient)
	}
	return err
}

// CommentPR posts a comment on the PR itself.
func (e *execClient) CommentPR(prURL, body string) error {
	if row, ok := routedRow(); ok {
		return e.commentPRViaConnector(row, prURL, body)
	}
	cmd := exec.Command("gh", "pr", "comment", prURL, "--body", body)
	if _, err := cmd.Output(); err != nil {
		return ghCommandErr(fmt.Sprintf("gh pr comment %s", prURL), err)
	}
	return nil
}

// MarkDraft flips the PR back to draft. Idempotent the same way MarkReady is:
// gh pr ready --undo on a PR that is already a draft exits 0.
func (e *execClient) MarkDraft(prURL string) error {
	if row, ok := routedRow(); ok {
		return e.markDraftViaConnector(row, prURL)
	}
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
	row, err := readRow()
	if err != nil {
		return "", err
	}
	return e.probeViaConnector(row)
}

// ListLabels returns the names of all labels defined in the repository.
// --paginate is load-bearing: one page holds at most demandPageSize labels.
func (e *execClient) ListLabels() ([]string, error) {
	row, err := readRow()
	if err != nil {
		return nil, err
	}
	return e.listLabelsViaConnector(row)
}

// CreateLabel creates a label in the repository. color is hex without the
// leading #.
func (e *execClient) CreateLabel(name, description, color string) error {
	if row, ok := routedRow(); ok {
		return e.createLabelViaConnector(row, name, description, color)
	}
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
// ErrTransientPushFailure. Under a routed row the head comes from a guest
// Connector, so an unpinned Rebase is refused there; RebasePRHead is the way in.
func (e *execClient) Rebase(prURL string) error {
	if row, ok := routedRow(); ok {
		return e.forgeWrite(row, "rebase a PR", func(cf forge.CodeForge, _ forge.Capabilities) error {
			return cf.Rebase(prURL)
		})
	}
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
	return e.syncBranch(fields[0], fields[1], e.httpsClone("rebase"), gitexec.Credential{})
}

// RebasePRHead is forge.PRHeadRebaser. Under a routed row the PR's head is
// read from a guest Connector core does not trust, so the composed forge
// refuses a head other than the one the caller expects. Without one the head
// comes from gh, which core trusts, so head is unused and the native Rebase
// runs.
func (e *execClient) RebasePRHead(prURL, head string) error {
	if row, ok := routedRow(); ok {
		return e.rebasePRHeadViaConnector(row, prURL, head)
	}
	return e.Rebase(prURL)
}

// syncBranch is Rebase's git half: clone with clone, sync head onto
// origin/<base> the way e.syncMethod says, and force-push with cred (the zero
// Credential leaves the push to the clone's own persisted helper).
func (e *execClient) syncBranch(head, base string, clone func(dir string) error, cred gitexec.Credential) error {
	dir, err := os.MkdirTemp("", "spindrift-rebase-*")
	if err != nil {
		return fmt.Errorf("mkdtemp: %w", err)
	}
	defer os.RemoveAll(dir)

	if err := clone(dir); err != nil {
		return err
	}

	gitIn := func(args ...string) *exec.Cmd {
		return exec.Command("git", gitexec.GuardedArgs(dir, args...)...)
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
	return gitplumbing.GitForcePush(ctx, dir, cred)
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
// the same gh credential helper. The auto-maintenance guard persists the same
// way, covering commands that run bare `git -C dir`.
func (e *execClient) httpsClone(op string) func(dir string) error {
	return func(dir string) error {
		url, gitArgs := GitRemote(e.repo)
		args := append([]string{"clone"}, gitexec.NoAutoMaintenance("--config")...)
		args = append(args, gitArgs...)
		args = append(args, "--no-single-branch", url, dir)
		if _, err := exec.Command("git", args...).Output(); err != nil {
			return ghCommandErr("github: "+op+": git clone", err)
		}
		return nil
	}
}

// SeedMirror clones the target repo bare into dir with the launcher's own gh
// credential, so the credential-less Box clones dir instead of the forge.
// GitRemote's pins ride as top-level `git -c`, not clone's --config: nothing
// pushes from the mirror, so the credential helper must not persist into a
// repo the Box mounts, and command scope (unlike repo-local config) lets an
// outer insteadOf route the clone, as it does the Ledger's.
func (e *execClient) SeedMirror(dir string) error {
	if row, ok := routedRow(); ok {
		return e.seedMirrorViaConnector(row, dir)
	}
	url, gitArgs := GitRemote(e.repo)
	args := append(slices.Clone(gitArgs), gitexec.NoAutoMaintenance("-c")...)
	args = append(args, "clone", "--bare", url, dir)
	if _, err := exec.Command("git", args...).Output(); err != nil {
		return ghCommandErr("github: seed mirror: git clone", err)
	}
	return nil
}

// PushBranch creates branch on the target repo from localRef in the git repo
// at srcDir, over HTTPS through the gh credential helper (issue #4071, ADR
// 0057); refuses if branch already exists, or is base (issue #4104).
func (e *execClient) PushBranch(srcDir, localRef, branch, base string) error {
	if row, ok := routedRow(); ok {
		return e.pushBranchViaConnector(row, srcDir, localRef, branch, base)
	}
	return bundlerelay.PushBranch("github", srcDir, localRef, branch, base, gitexec.Credential{}, e.httpsClone("push branch"))
}

// DeleteBranch deletes branch from the target repo with the launcher's own
// gh-cli credential (issue #4112).
func (e *execClient) DeleteBranch(branch, base string) error {
	if err := forge.ValidateBranchDelete("github", branch, base); err != nil {
		return err
	}
	if row, ok := routedRow(); ok {
		return e.deleteBranchViaConnector(row, branch, base)
	}
	cmd := exec.Command("gh", "api", "-X", "DELETE",
		fmt.Sprintf("repos/%s/git/refs/heads/%s", e.repo, branch))
	if _, err := cmd.Output(); err != nil {
		return ghCommandErr("github: delete branch: gh api", err)
	}
	return nil
}

// CreateDraftPR opens a draft PR from head onto base. head and base are
// branch names in e.repo, never a fork's owner:branch form.
func (e *execClient) CreateDraftPR(title, body, base, head string) (string, bool, error) {
	if row, ok := routedRow(); ok {
		return e.createDraftPRViaConnector(row, title, body, base, head)
	}
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
			if pr, ok, openErr := e.nativeOpenPRForBranch(head); openErr == nil && ok {
				return pr.URL, false, nil
			}
		}
		return "", false, createErr
	}
	return strings.TrimSpace(string(out)), true, nil
}

var _ forge.BranchProtectionForge = (*execClient)(nil)
var _ forge.BranchPusher = (*execClient)(nil)
var _ forge.MirrorSeeder = (*execClient)(nil)
var _ forge.BranchDeleter = (*execClient)(nil)
var _ forge.DraftPRCreator = (*execClient)(nil)
var _ forge.CIRunReporter = (*execClient)(nil)
var _ forge.MergeCommitReader = (*execClient)(nil)

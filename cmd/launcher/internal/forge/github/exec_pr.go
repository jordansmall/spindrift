package github

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/gitplumbing"
	"spindrift.dev/launcher/internal/gitexec"
)

// rebaseForcePushTimeout bounds Rebase's trailing force-push so a remote that
// accepts the connection and then hangs server-side cannot block it forever.
// Rebase's other subprocesses stay unbounded.
const rebaseForcePushTimeout = 5 * time.Minute

func (e *execClient) OpenPRForBranch(branch string) (forge.PR, bool, error) {
	row, err := routeRow()
	if err != nil {
		return forge.PR{}, false, err
	}
	return e.openPRForBranchViaConnector(row, branch)
}

// BranchExists reports whether branch exists on the remote, independent of any
// PR. matching-refs prefix-matches, so the result is filtered to an exact
// "refs/heads/<branch>" match. An empty branch would query every ref under
// heads/, so it is rejected.
func (e *execClient) BranchExists(branch string) (bool, error) {
	if branch == "" {
		return false, fmt.Errorf("branch must not be empty")
	}
	row, err := routeRow()
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
	row, err := routeRow()
	if err != nil {
		return false, err
	}
	return e.branchProtectedViaConnector(row, branch)
}

func (e *execClient) PRForBranch(branch string) (string, bool, error) {
	row, err := routeRow()
	if err != nil {
		return "", false, err
	}
	return e.prForBranchViaConnector(row, branch)
}

func (e *execClient) PRState(url string) (forge.PRState, error) {
	row, err := routeRow()
	if err != nil {
		return "", err
	}
	return e.prStateViaConnector(row, url)
}

// MergeCommit returns the oid of the commit the merged PR at url landed as, ""
// when GitHub reports none (an unmerged PR).
func (e *execClient) MergeCommit(url string) (string, error) {
	row, err := routeRow()
	if err != nil {
		return "", err
	}
	return e.mergeCommitViaConnector(row, url)
}

// CheckState returns the aggregate statusCheckRollup state of the PR's head
// commit, or StateNone when no checks are registered.
func (e *execClient) CheckState(url string) (forge.RollupState, error) {
	row, err := routeRow()
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
	row, err := routeRow()
	if err != nil {
		return forge.StateNone, "", err
	}
	return e.checkRunViaConnector(row, url)
}

// HeadCommitSHA returns the PR's current head commit SHA.
func (e *execClient) HeadCommitSHA(url string) (string, error) {
	row, err := routeRow()
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
	row, err := routeRow()
	if err != nil {
		return forge.MergeableUnknown, err
	}
	return e.mergeableViaConnector(row, url)
}

// NeedsUpdate reports whether the PR's base branch has commits its head branch
// lacks, via the REST compare API's behind_by. GraphQL mergeStateStatus BEHIND
// will not do: it only reports BEHIND when branch protection requires branches
// to be up to date, which this project's PAT cannot even read (issue #936).
// A fork-sourced head 404s here; the caller logs and swallows that error.
func (e *execClient) NeedsUpdate(prURL string) (bool, error) {
	row, err := routeRow()
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
	row, err := routeRow()
	if err != nil {
		return nil, err
	}
	return e.listPRFilesViaConnector(row, url)
}

func (e *execClient) Merge(url string) error {
	row, err := routeRow()
	if err != nil {
		return err
	}
	return e.mergeViaConnector(row, url)
}

// CanAutoMerge reports whether the repo allows GitHub's native auto-merge.
func (e *execClient) CanAutoMerge() (bool, error) {
	row, err := routeRow()
	if err != nil {
		return false, err
	}
	return e.canAutoMergeViaConnector(row)
}

// EnqueueAutoMerge enqueues GitHub's native auto-merge for the PR, which merges
// once every branch-protection requirement is met.
func (e *execClient) EnqueueAutoMerge(prURL string) error {
	row, err := routeRow()
	if err != nil {
		return err
	}
	return e.enqueueAutoMergeViaConnector(row, prURL)
}

// MarkReady flips the PR out of draft. The caller may call this
// unconditionally on every green PR.
func (e *execClient) MarkReady(prURL string) error {
	row, err := routeRow()
	if err != nil {
		return err
	}
	return e.markReadyViaConnector(row, prURL)
}

// CommentPR posts a comment on the PR itself.
func (e *execClient) CommentPR(prURL, body string) error {
	row, err := routeRow()
	if err != nil {
		return err
	}
	return e.commentPRViaConnector(row, prURL, body)
}

// MarkDraft flips the PR back to draft. Idempotent the same way MarkReady is.
func (e *execClient) MarkDraft(prURL string) error {
	row, err := routeRow()
	if err != nil {
		return err
	}
	return e.markDraftViaConnector(row, prURL)
}

// Probe checks that the credential is accepted and the configured repository is
// reachable, returning the resolved repo slug. It returns ErrAuthFailure,
// ErrRepoNotFound, or ErrRateLimit. ErrRateLimit is mutually exclusive with
// the other two, so a caller checking those first does not misreport a
// throttled operator's real cause.
func (e *execClient) Probe() (string, error) {
	row, err := routeRow()
	if err != nil {
		return "", err
	}
	return e.probeViaConnector(row)
}

// ListLabels returns the names of all labels defined in the repository, read
// through the routed Connector row.
func (e *execClient) ListLabels() ([]string, error) {
	row, err := routeRow()
	if err != nil {
		return nil, err
	}
	return e.listLabelsViaConnector(row)
}

// CreateLabel creates a label in the repository. color is hex without the
// leading #.
func (e *execClient) CreateLabel(name, description, color string) error {
	row, err := routeRow()
	if err != nil {
		return err
	}
	return e.createLabelViaConnector(row, name, description, color)
}

// Rebase syncs the PR's head branch onto its base and force-pushes. With sync
// method "merge" (WithSyncMethod) it merges the base in instead. Returns
// ErrMergeConflict only when the sync stops on unmerged paths; any other sync
// failure is returned wrapping git's own output. A force-push that fails with a
// transport or server fault, or that rebaseForcePushTimeout kills, wraps
// ErrTransientPushFailure. The head comes from a guest Connector, so an
// unpinned Rebase is refused; RebasePRHead is the way in.
func (e *execClient) Rebase(prURL string) error {
	row, err := routeRow()
	if err != nil {
		return err
	}
	return e.forgeWrite(row, "rebase a PR", func(cf forge.CodeForge, _ forge.Capabilities) error {
		return cf.Rebase(prURL)
	})
}

// RebasePRHead is forge.PRHeadRebaser. The PR's head is read from a guest
// Connector core does not trust, so the composed forge refuses a head other
// than the one the caller expects.
func (e *execClient) RebasePRHead(prURL, head string) error {
	row, err := routeRow()
	if err != nil {
		return err
	}
	return e.rebasePRHeadViaConnector(row, prURL, head)
}

// syncBranch is Rebase's git half: clone with clone, sync head onto
// origin/<base> the way e.syncMethod says, and force-push with cred (the zero
// Credential leaves the push to the clone's own persisted helper).
func (e *execClient) syncBranch(head, base string, clone func(dir string) error, cred gitexec.Credential, remoteURL string) error {
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
	return gitplumbing.GitForcePush(ctx, dir, cred, remoteURL)
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

// SeedMirror clones the target repo bare into dir, so the credential-less Box
// clones dir instead of the forge.
func (e *execClient) SeedMirror(dir string) error {
	row, err := routeRow()
	if err != nil {
		return err
	}
	return e.seedMirrorViaConnector(row, dir)
}

// PushBranch creates branch on the target repo from localRef in the git repo
// at srcDir (issue #4071, ADR 0057); refuses if branch already exists, or is
// base (issue #4104).
func (e *execClient) PushBranch(srcDir, localRef, branch, base string) error {
	row, err := routeRow()
	if err != nil {
		return err
	}
	return e.pushBranchViaConnector(row, srcDir, localRef, branch, base)
}

// DeleteBranch deletes branch from the target repo (issue #4112).
func (e *execClient) DeleteBranch(branch, base string) error {
	if err := forge.ValidateBranchDelete("github", branch, base); err != nil {
		return err
	}
	row, err := routeRow()
	if err != nil {
		return err
	}
	return e.deleteBranchViaConnector(row, branch, base)
}

// CreateDraftPR opens a draft PR from head onto base. head and base are
// branch names in e.repo, never a fork's owner:branch form.
func (e *execClient) CreateDraftPR(title, body, base, head string) (string, bool, error) {
	row, err := routeRow()
	if err != nil {
		return "", false, err
	}
	return e.createDraftPRViaConnector(row, title, body, base, head)
}

var _ forge.BranchProtectionForge = (*execClient)(nil)
var _ forge.BranchPusher = (*execClient)(nil)
var _ forge.MirrorSeeder = (*execClient)(nil)
var _ forge.BranchDeleter = (*execClient)(nil)
var _ forge.DraftPRCreator = (*execClient)(nil)
var _ forge.CIRunReporter = (*execClient)(nil)
var _ forge.MergeCommitReader = (*execClient)(nil)

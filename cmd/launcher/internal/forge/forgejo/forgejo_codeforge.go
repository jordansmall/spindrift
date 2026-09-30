package forgejo

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"path"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/bundlerelay"
	"spindrift.dev/launcher/internal/forge/git"
	"spindrift.dev/launcher/internal/forge/rest"
)

// defaultForgejoHTTPTimeout bounds every Forgejo REST call so a hung instance
// cannot block one forever. The tracker's own client needs the bound too:
// when both seams point at the same repo, newForgejoCodeForge reuses that
// client rather than building a second one (issue #2256).
const defaultForgejoHTTPTimeout = 30 * time.Second

// GitRemoteURL builds a token-authenticated clone URL for repo (an
// owner/repo slug), carrying the token as the URL's userinfo with no password
// half. If baseURL fails to parse, it concatenates the token in as userinfo, so
// the fallback remote can still push instead of being anonymous.
func GitRemoteURL(baseURL, repo, token string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		base := strings.TrimSuffix(baseURL, "/")
		slug := strings.Trim(repo, "/")
		if i := strings.Index(base, "://"); i >= 0 {
			return base[:i+3] + token + "@" + base[i+3:] + "/" + slug + ".git"
		}
		return token + "@" + base + "/" + slug + ".git"
	}
	u.User = url.User(token)
	u.Path = "/" + strings.Trim(repo, "/") + ".git"
	return u.String()
}

// ForgejoCodeForgeConfig configures the Forgejo CodeForge adapter.
type ForgejoCodeForgeConfig struct {
	BaseURL string // Forgejo instance base URL, e.g. https://codeberg.org
	Repo    string // owner/repo slug
	Token   string

	BaseBranch   string // target branch Merge merges onto / Rebase rebases onto for MERGE_MODE=immediate
	UserName     string // commit identity for Rebase's throwaway clone
	UserEmail    string
	BranchPrefix string // baked into AgentBranch's output

	// MergeMethod is the value Forgejo's merge endpoint wants in its "Do"
	// field: "merge", "squash", or "rebase". Empty resolves to "rebase".
	MergeMethod string

	// HTTPClient overrides the client used for REST calls; nil uses a 30s timeout.
	HTTPClient *http.Client
}

// errMergeRefused signals that Forgejo's merge endpoint refused the merge as
// "not mergeable" (405 and 409 both mean that). classifyMergeFailure turns it
// into forge.ErrMergeConflict or forge.ErrMergeBlockedByChecks, so it never
// escapes this file.
var errMergeRefused = errors.New("forgejo: merge refused")

// forgejoStatusMap is the HTTP-status to sentinel-error table shared by every
// *rest.Client this package builds, so the tracker and CodeForge seams cannot
// drift apart on which status means what.
func forgejoStatusMap() rest.StatusMap {
	return rest.StatusMap{
		http.StatusUnauthorized:     forge.ErrAuthFailure,
		http.StatusForbidden:        forge.ErrAuthFailure,
		http.StatusNotFound:         forge.ErrNotFound,
		http.StatusMethodNotAllowed: errMergeRefused,
		http.StatusConflict:         errMergeRefused,
	}
}

// forgejoCodeForge is the Forgejo CodeForge adapter. Probe and Merge drive the
// REST API directly, so Probe also validates the token and the instance's
// reachability instead of only the repo's git-level presence.
type forgejoCodeForge struct {
	rest        *rest.Client
	repo        string // owner/repo slug, for repoPath
	git         forge.CodeForge
	mergeMethod string
	// remote is the token-authenticated clone/push URL the git adapter uses, kept
	// here so the read-only wrapper can clone it directly for RelayBundle.
	remote string
}

func (f *forgejoCodeForge) repoPath() string {
	return "/api/v1/repos/" + f.repo
}

// NewForgejoCodeForge returns a forge.CodeForge backed by a Forgejo repo: REST
// for Probe and Merge, git plumbing for AgentBranch, BranchExists and Rebase.
// When tracker is a Forgejo IssueTracker from NewForgejoClient, this CodeForge
// reuses its *rest.Client; any other tracker, nil included, gets a fresh one.
func NewForgejoCodeForge(cfg ForgejoCodeForgeConfig, tracker forge.IssueTracker) forge.CodeForge {
	return newForgejoCodeForge(cfg, tracker, "")
}

// NewForgejoCodeForgeForTest is NewForgejoCodeForge with an explicit remote
// override, test-only: the git plumbing points at a local bare repo fixture
// while REST calls still go to cfg.BaseURL.
func NewForgejoCodeForgeForTest(cfg ForgejoCodeForgeConfig, tracker forge.IssueTracker, gitRemoteURL string) forge.CodeForge {
	return newForgejoCodeForge(cfg, tracker, gitRemoteURL)
}

func newForgejoCodeForge(cfg ForgejoCodeForgeConfig, tracker forge.IssueTracker, gitRemoteURL string) *forgejoCodeForge {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = defaultForgejoBaseURL
	}
	baseURL = strings.TrimSuffix(baseURL, "/")

	remote := gitRemoteURL
	if remote == "" {
		remote = GitRemoteURL(baseURL, cfg.Repo, cfg.Token)
	}

	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: defaultForgejoHTTPTimeout}
	}

	var restCli *rest.Client
	if fc, ok := tracker.(*forgejoClient); ok {
		// Reuse the tracker's client so both seams share one instance (issue
		// #2256); cfg.BaseURL, Token and HTTPClient are ignored on this branch.
		// Nothing checks that the tracker and cfg name the same repo and
		// instance, so a caller that mixes the two merges silently wrong.
		restCli = fc.rest
	} else {
		restCli = rest.New(baseURL, rest.TokenAuth{Scheme: "token", Token: cfg.Token}, "forgejo", forgejoStatusMap(), hc)
	}

	gitCF := git.NewGitClient(remote, cfg.BaseBranch, cfg.UserName, cfg.UserEmail, cfg.BranchPrefix)
	return &forgejoCodeForge{rest: restCli, repo: cfg.Repo, git: gitCF, mergeMethod: cfg.MergeMethod, remote: remote}
}

func (f *forgejoCodeForge) AgentBranch(num string) string { return f.git.AgentBranch(num) }

func (f *forgejoCodeForge) BranchExists(branch string) (bool, error) {
	return f.git.BranchExists(branch)
}

// forgejoMergeDo maps MergeMethod onto the merge endpoint's "Do" value. An
// unset method resolves to "rebase", matching the github adapter's default.
func forgejoMergeDo(method string) string {
	switch method {
	case "merge":
		return "merge"
	case "squash":
		return "squash"
	default:
		return "rebase"
	}
}

// postMerge POSTs a merge request for the PR at index, adding extra to the base
// fields. rest.Client wraps a 405 or 409 as errMergeRefused, which Merge
// disambiguates and EnqueueAutoMerge propagates raw.
func (f *forgejoCodeForge) postMerge(index string, extra map[string]any) error {
	body := map[string]any{
		"Do":                        forgejoMergeDo(f.mergeMethod),
		"delete_branch_after_merge": true,
	}
	for k, v := range extra {
		body[k] = v
	}
	return f.rest.Do(http.MethodPost, f.repoPath()+"/pulls/"+index+"/merge", body, nil)
}

// Merge merges the pull request at prURL through Forgejo's REST merge endpoint.
func (f *forgejoCodeForge) Merge(prURL string) error {
	index, err := parsePRIndex(prURL)
	if err != nil {
		return err
	}
	err = f.postMerge(index, nil)
	if err == nil {
		return nil
	}
	if errors.Is(err, errMergeRefused) {
		return f.classifyMergeFailure(prURL, err)
	}
	return err
}

// classifyMergeFailure tells a genuine merge conflict apart from a PR merely
// blocked by pending or failing checks, by querying the PR's mergeable state and
// handing it to forge.ClassifyMergeFailure. Only the 405 and 409 refusals reach
// here; every other status (403 without merge scope, 429, 500) is a real failure
// Merge returns as-is rather than masking it as a conflict.
func (f *forgejoCodeForge) classifyMergeFailure(prURL string, cause error) error {
	state, err := f.Mergeable(prURL)
	if err != nil {
		return fmt.Errorf("forgejo: merge %s: %w (mergeable state unavailable: %w)", prURL, cause, err)
	}
	if sentinel, ok := forge.ClassifyMergeFailure(state); ok {
		return sentinel
	}
	return fmt.Errorf("forgejo: merge %s: %w (mergeable state %q undetermined)", prURL, cause, state)
}

// Rebase resolves prURL to its head branch via REST, then delegates to the git
// adapter, which rebases that branch onto the base branch and force-pushes it.
func (f *forgejoCodeForge) Rebase(prURL string) error {
	p, err := f.getPull(prURL)
	if err != nil {
		return err
	}
	return f.git.Rebase(p.Head.Ref)
}

// Probe returns the repository's full name (owner/repo). It asks REST rather
// than git ls-remote, so it validates the token and the instance's
// reachability too.
func (f *forgejoCodeForge) Probe() (string, error) {
	var payload forgejoRepoPayload
	if err := f.rest.Do(http.MethodGet, f.repoPath(), nil, &payload); err != nil {
		if errors.Is(err, forge.ErrAuthFailure) {
			return "", err
		}
		return "", fmt.Errorf("%w: %s", forge.ErrRepoNotFound, err)
	}
	return payload.FullName, nil
}

// forgejoBranchProtection is the part of Forgejo's branch-protection payload
// BranchProtected needs: rule_name is a glob, not a literal branch name.
type forgejoBranchProtection struct {
	RuleName string `json:"rule_name"`
}

// BranchProtected reports whether branch matches any branch-protection rule. It
// lists the rules instead of looking one up by name because each rule_name is
// matched as a glob (path.Match): "release/*" protects "release/1.0" without
// appearing verbatim. A repo with no rules answers 200 with an empty array, so a
// 404 or any other error means the probe failed and must be returned as one.
func (f *forgejoCodeForge) BranchProtected(branch string) (bool, error) {
	var rules []forgejoBranchProtection
	if err := f.rest.Do(http.MethodGet, f.repoPath()+"/branch_protections", nil, &rules); err != nil {
		return false, err
	}
	for _, r := range rules {
		if ok, err := path.Match(r.RuleName, branch); err == nil && ok {
			return true, nil
		}
	}
	return false, nil
}

// relayClone builds the `git clone` closure bundlerelay's Relay,
// CommitSubjects, and PushBranch all take as their clone step: op names the
// caller for the wrapped error's description. f.remote carries the token as
// userinfo, so the clone's CombinedOutput stays out of the error: git's
// diagnostics echo the tokened URL back.
func (f *forgejoCodeForge) relayClone(op string) func(dir string) error {
	return func(dir string) error {
		if _, err := exec.Command("git", "clone", "--no-single-branch", f.remote, dir).CombinedOutput(); err != nil {
			return fmt.Errorf("forgejo: %s: git clone %s: %w", op, forge.RedactURLCredentials(f.remote), err)
		}
		return nil
	}
}

// PushBranch creates branch on the target repo from localRef in the git repo
// at srcDir, with the launcher's own token (issue #4071, ADR 0057); refuses if
// branch already exists (issue #4104).
func (f *forgejoCodeForge) PushBranch(srcDir, localRef, branch string) error {
	return bundlerelay.PushBranch("forgejo", srcDir, localRef, branch, f.relayClone("push branch"))
}

// CreateDraftPR opens a draft PR from head onto base. Forgejo has no
// create-time draft field and encodes the state as a title prefix
// (forgejoWIPPrefix), which MarkReady strips before merge. A retried create
// for the same head adopts that branch's open PR and reports created=false,
// so a caller (issues #2407, #2447) knows the title and body are not its own.
// Read-write and read-only settle alike may call it (issue #4071); settle's
// own gating (Open checks BundleRelay first) is what keeps a read-write land
// from ever making a host-side create that conflicts with the Box's own.
func (f *forgejoCodeForge) CreateDraftPR(title, body, base, head string) (string, bool, error) {
	reqBody := map[string]any{
		"title": forgejoWIPPrefix + " " + title,
		"head":  head,
		"base":  base,
		"body":  body,
	}
	var payload forgejoPullPayload
	err := f.rest.Do(http.MethodPost, f.repoPath()+"/pulls", reqBody, &payload)
	if err == nil {
		return payload.HTMLURL, true, nil
	}
	createErr := fmt.Errorf("forgejo: create draft PR: %w", err)
	// A 409 means a PR for this head already exists. forgejoStatusMap maps that
	// status onto errMergeRefused too, so match the create call's own
	// StatusError rather than errors.Is, which would also match a 405.
	// OpenPRForBranch is draft-inclusive (issue #2408), and this PR is a draft.
	var statusErr rest.StatusError
	if errors.As(err, &statusErr) && statusErr.Status == http.StatusConflict {
		if pr, ok, openErr := f.OpenPRForBranch(head); openErr == nil && ok {
			return pr.URL, false, nil
		}
	}
	return "", false, createErr
}

var _ forge.BranchProtectionForge = (*forgejoCodeForge)(nil)
var _ forge.BranchPusher = (*forgejoCodeForge)(nil)
var _ forge.DraftPRCreator = (*forgejoCodeForge)(nil)

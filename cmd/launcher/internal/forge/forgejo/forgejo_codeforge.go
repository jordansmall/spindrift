package forgejo

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/git"
	"spindrift.dev/launcher/internal/forge/rest"
)

// defaultForgejoHTTPTimeout bounds the default HTTP client for all Forgejo
// REST calls so a hung instance can't block forever. It must cover the
// tracker adapter too, not just the CodeForge: when both seams point at the
// same repo, newForgejoCodeForge reuses the tracker's own *rest.Client.
const defaultForgejoHTTPTimeout = 30 * time.Second

// forgejoGitRemoteURL builds a token-authenticated git clone URL, e.g.
// ("https://codeberg.org", "owner/repo", "tok") ->
// "https://tok@codeberg.org/owner/repo.git" — the shape `git clone`/`git
// push` expect for HTTP(S) token auth, with the token as userinfo and no
// password half.
func forgejoGitRemoteURL(baseURL, repo, token string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		// Splice the token in by hand rather than giving up: a malformed
		// FORGEJO_BASE_URL should still yield a push-authenticated remote, not
		// an anonymous one that would fail to push.
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

	// MergeMethod is "merge", "squash", or "rebase". Empty resolves to
	// "rebase" (forgejoMergeDo), mirroring the github adapter's MERGE_METHOD
	// default.
	MergeMethod string

	// HTTPClient overrides the HTTP client used for the REST Probe call; nil
	// uses a client with a default 30s timeout.
	HTTPClient *http.Client
}

// errMergeRefused signals that the merge endpoint refused the merge as "not
// mergeable" (405 or 409 — Forgejo uses both for the same refusal).
// classifyMergeFailure disambiguates a genuine conflict from
// checks-still-pending by querying Mergeable. It never escapes this file:
// callers only ever see forge.ErrMergeConflict, forge.ErrMergeBlockedByChecks,
// or a raw wrapped error.
var errMergeRefused = errors.New("forgejo: merge refused")

// forgejoStatusMap is the HTTP-status -> sentinel-error table shared by every
// *rest.Client this package builds, so the tracker and CodeForge seams can't
// drift on which status maps to which sentinel.
func forgejoStatusMap() rest.StatusMap {
	return rest.StatusMap{
		http.StatusUnauthorized:     forge.ErrAuthFailure,
		http.StatusForbidden:        forge.ErrAuthFailure,
		http.StatusNotFound:         forge.ErrNotFound,
		http.StatusMethodNotAllowed: errMergeRefused,
		http.StatusConflict:         errMergeRefused,
	}
}

// forgejoCodeForge is the Forgejo CodeForge adapter. AgentBranch/BranchExists
// delegate to a plain git.CodeForge against a token-authenticated remote;
// Probe drives the Forgejo REST client directly instead of git ls-remote, so
// it also validates the token and instance reachability. Merge drives
// Forgejo's REST merge endpoint (a PR URL, not a branch); Rebase resolves the
// PR's head branch via REST and delegates to the git adapter.
type forgejoCodeForge struct {
	rest        *rest.Client
	repo        string // owner/repo slug, for repoPath
	git         forge.CodeForge
	mergeMethod string
	// remote is the token-authenticated git clone/push URL the underlying git
	// adapter uses, kept here too so the read-only wrapper
	// (forgejo_readonly.go) can clone it directly for RelayBundle without
	// threading a second config surface through NewForgejoCodeForge.
	remote string
}

func (f *forgejoCodeForge) repoPath() string {
	return "/api/v1/repos/" + f.repo
}

// NewForgejoCodeForge returns a forge.CodeForge backed by a Forgejo repo: the
// REST API for Probe/Merge, git plumbing for AgentBranch/BranchExists/Rebase.
// A tracker that is itself a Forgejo IssueTracker lets this CodeForge reuse
// that tracker's *rest.Client, so both seams share one client instance; any
// other tracker (including nil) falls back to a fresh client.
func NewForgejoCodeForge(cfg ForgejoCodeForgeConfig, tracker forge.IssueTracker) forge.CodeForge {
	return newForgejoCodeForge(cfg, tracker, "")
}

// NewForgejoCodeForgeForTest is NewForgejoCodeForge with an explicit git
// remote override, so a test can point the git plumbing at a local bare repo
// fixture while REST calls still hit cfg.BaseURL. Test-only; production
// wiring always uses NewForgejoCodeForge.
func NewForgejoCodeForgeForTest(cfg ForgejoCodeForgeConfig, tracker forge.IssueTracker, gitRemoteURL string) forge.CodeForge {
	return newForgejoCodeForge(cfg, tracker, gitRemoteURL)
}

// newForgejoCodeForge is the shared constructor behind NewForgejoCodeForge
// and NewForgejoCodeForgeForTest.
func newForgejoCodeForge(cfg ForgejoCodeForgeConfig, tracker forge.IssueTracker, gitRemoteURL string) *forgejoCodeForge {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = defaultForgejoBaseURL
	}
	baseURL = strings.TrimSuffix(baseURL, "/")

	remote := gitRemoteURL
	if remote == "" {
		remote = forgejoGitRemoteURL(baseURL, cfg.Repo, cfg.Token)
	}

	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: defaultForgejoHTTPTimeout}
	}

	var restCli *rest.Client
	if fc, ok := tracker.(*forgejoClient); ok {
		// cfg.BaseURL/Token/HTTPClient are silently ignored on this branch.
		// Safe only because production wiring always builds the tracker and
		// this CodeForge from the same base URL/token/repo, so the reused
		// client's config can't diverge from cfg's. Nothing enforces that, so
		// a caller feeding a tracker and cfg for different repos or instances
		// would merge silently wrong.
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

// forgejoMergeDo maps the MergeMethod knob onto Forgejo's merge endpoint "Do"
// field. Empty resolves to "rebase", mirroring the github adapter's default
// so an unset MergeMethod behaves the same across both forges.
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

// postMerge POSTs a merge request for the PR at index with the base merge
// fields plus any extra ones. A 405 or 409 comes back wrapping
// errMergeRefused, which Merge disambiguates via classifyMergeFailure;
// EnqueueAutoMerge instead propagates any error from this call raw.
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

// Merge merges the pull request at prURL via Forgejo's REST merge endpoint.
// A merge refusal is classified by f.classifyMergeFailure; any other error is
// returned as-is.
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

// classifyMergeFailure distinguishes a genuine merge conflict from a PR
// merely blocked by pending or failing required checks, by querying the PR's
// mergeable state and handing it to forge.ClassifyMergeFailure. Only Forgejo's
// "not mergeable" refusals reach here; any other non-2xx (403, 429, 500) is a
// genuine failure Merge returns as-is rather than masking behind
// ErrMergeConflict or ErrMergeBlockedByChecks. A state that maps to neither
// outcome is likewise surfaced as its own error.
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

// Rebase resolves prURL's PR to its head branch via REST, then delegates to
// the git adapter, which rebases onto the configured base branch and
// force-pushes the result back to the remote.
func (f *forgejoCodeForge) Rebase(prURL string) error {
	p, err := f.getPull(prURL)
	if err != nil {
		return err
	}
	return f.git.Rebase(p.Head.Ref)
}

// Probe checks Forgejo connectivity/auth and returns the repository's full
// name (owner/repo).
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

// forgejoBranchProtection is the subset of Forgejo's branch-protection
// payload BranchProtected needs: rule_name is a glob (e.g. "release/*"), not
// a literal branch name, so a single rule can cover many branches.
type forgejoBranchProtection struct {
	RuleName string `json:"rule_name"`
}

// BranchProtected reports whether branch is covered by any Forgejo
// branch-protection rule. It uses the list endpoint rather than the per-name
// lookup because each rule_name is a glob matched against branch, not a
// literal name: "release/*" protects "release/1.0" without ever appearing
// verbatim. A repo with no rules returns 200 and an empty array — the
// definitive (false, nil) result. A 404 therefore never means "no rules";
// like any other rest.Do failure it means the probe couldn't determine the
// answer, and per BranchProtectionForge's contract that is a non-nil error,
// never a false "not protected".
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

var _ forge.BranchProtectionForge = (*forgejoCodeForge)(nil)

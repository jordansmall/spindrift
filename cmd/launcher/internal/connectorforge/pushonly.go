package connectorforge

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"slices"
	"strings"

	"spindrift.dev/launcher/internal/connectorcall"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/bundlerelay"
	"spindrift.dev/launcher/internal/gitexec"
	"spindrift.dev/launcher/internal/retry"
)

// The capability names a forge Connector advertises. sdk/forge declares the
// same strings, rendered from the protocol description that
// TestProtocolForgeAxisAgrees holds these to. The three that select a
// contract are exported for connectorconformance.
const (
	capForge            = "forge"
	CapBranches         = "forge.branches"
	CapPushOnly         = "forge.pushOnly"
	CapPRs              = "forge.prs"
	capAutoMerge        = "forge.autoMerge"
	capCIRun            = "forge.ciRun"
	capCIRerun          = "forge.ciRerun"
	capBranchProtection = "forge.branchProtection"
	capProbe            = "forge.probe"
	capDraftPRs         = "forge.draftPRs"
	capTokenGrant       = "forge.tokenGrant"
)

// ErrPRForgeUnsupported marks a forge Connector that is neither push-only nor
// declares "forge.prs", so no Code Forge can be composed from it.
var ErrPRForgeUnsupported = errors.New("connector forge is neither push-only nor PR-capable")

// ErrGitRemote marks a Git remote declaration or Target repository core cannot
// derive a clone URL from.
var ErrGitRemote = errors.New("cannot derive a clone URL")

// CloneURL derives the repository's clone URL, <remote>/<owner>/<name>.git,
// from the forge's declared https web base. A base subpath is kept: the git
// credential is scoped to this URL's path and git matches paths by whole
// segments, so every site that names the repository (push, relay, mirror seed,
// the Ledger) must use this one derivation.
func CloneURL(remote, repo string) (string, error) {
	cloneURL, _, _, err := deriveRemote(remote, repo)
	return cloneURL, err
}

// deriveRemote returns the clone URL CloneURL names and the web host and subpath
// (host[:port], path without a trailing slash) the PR URLs are pinned to.
func deriveRemote(remote, repo string) (cloneURL, webHost, webPath string, err error) {
	u, perr := url.Parse(remote)
	switch {
	case perr != nil || u.Scheme != "https" || u.Host == "":
		return "", "", "", fmt.Errorf("%w: Git remote is not an https URL with a host", ErrGitRemote)
	case u.User != nil || strings.ContainsAny(remote, "?#"):
		return "", "", "", fmt.Errorf("%w: Git remote carries userinfo, a query or a fragment", ErrGitRemote)
	}
	owner, name, ok := strings.Cut(repo, "/")
	bad := func(seg string) bool { return seg == "" || seg == "." || seg == ".." || strings.Contains(seg, "/") }
	if !ok || bad(owner) || bad(name) {
		return "", "", "", fmt.Errorf("%w: repository %q is not owner/name", ErrGitRemote, repo)
	}
	return strings.TrimSuffix(remote, "/") + "/" + repo + ".git", u.Host, strings.TrimRight(u.Path, "/"), nil
}

// Git is the core-side git plumbing the composed forge runs Merge and Rebase
// in: the Connector never touches git or the credential.
type Git struct {
	Credential gitexec.Credential
	// Plumbing builds core's git plumbing over the clone URL, getting
	// Credential only after the URL is derived. Injected because this package sits
	// in every backend's go test sources, so it cannot import a backend
	// package. Nil is a construction error.
	Plumbing func(cloneURL string, cred gitexec.Credential) forge.CodeForge
	// Clock paces the retry backoff; the zero value is retry.RealClock().
	Clock retry.Clock
	// WebBase is the forge's https web base from its row's Git remote
	// declaration. Compose derives the clone URL and the web host and subpath
	// the PR URLs are pinned to from it and Target.Repo.
	WebBase string
	// Note, when set, sees each guest call of the composed forge that fails
	// after retries and classification, by method. Compose's own refusals are
	// not noted: its caller records the Compose error, so a failure lands once.
	Note func(method string, err error)
}

// pushOnly is a Code Forge with no PR concept: the Connector answers the forge
// questions (liveness, branch existence and deletion) and git runs in core's own
// plumbing against the clone URL core derived.
type pushOnly struct {
	conn HostPolicy
	// raw is the guest call after retries and classification, which Git.Note
	// wraps; call is raw followed by the method's result check.
	raw  guestCall
	call guestCall
	// rd is the PR reads' state, which the result checks of prShape and its
	// kin read; composePR pins its web host and path.
	rd *reads
	// clock paces the recheck of a conflicting merge.
	clock  retry.Clock
	target Target
	git    forge.CodeForge
	// cloneURL and cred are what core's own git (relayClone) clones with.
	cloneURL string
	cred     gitexec.Credential
}

// Compose builds a Code Forge from a forge Connector advertising "forge.branches"
// and "forge.probe", and either "forge.pushOnly" (a BranchDeleter, no PRForge) or
// "forge" with "forge.prs" (the PR surface, with each optional capability only
// when declared; "forge.draftPRs" adds draft PRs, branch pushes and the bundle
// relay). The returned Capabilities are the composed forge resolved by type,
// so any later type probe agrees with them. The clone URL and the web host PR
// URLs are pinned to are derived from Git.WebBase and the Target repository
// (CloneURL), so the git credential never reaches a host the Connector merely
// named.
func Compose(c *connectorhost.Connector, target Target, g Git) (forge.CodeForge, forge.Capabilities, error) {
	return ComposeCalling(c, c.Call, c, target, g)
}

// ComposeCalling is Compose with guest calls routed through call and core's
// refusals audited through policy, for a caller that owns the Connector's
// lifetime and reloads it behind both; c supplies the handshake.
func ComposeCalling(c *connectorhost.Connector, call connectorcall.Call, policy HostPolicy, target Target, g Git) (forge.CodeForge, forge.Capabilities, error) {
	has := func(capName string) bool { return slices.Contains(c.Capabilities, capName) }
	isPushOnly := has(CapPushOnly)
	switch {
	case !has(CapBranches):
		return nil, forge.Capabilities{}, fmt.Errorf("%w: capabilities %v lack %q", ErrNotForge, c.Capabilities, CapBranches)
	case !has(capProbe):
		return nil, forge.Capabilities{}, fmt.Errorf("%w: capabilities %v lack %q", ErrNotForge, c.Capabilities, capProbe)
	case !isPushOnly && !(has(capForge) && has(CapPRs)):
		return nil, forge.Capabilities{}, fmt.Errorf("%w: capabilities %v lack %q and %q", ErrPRForgeUnsupported, c.Capabilities, CapPushOnly, CapPRs)
	}
	if g.Plumbing == nil {
		return nil, forge.Capabilities{}, errors.New("connectorforge: Git.Plumbing is nil")
	}
	cloneURL, webHost, webPath, err := deriveRemote(g.WebBase, target.Repo)
	if err != nil {
		return nil, forge.Capabilities{}, err
	}
	p := newPushOnly(call, c.Capabilities, policy, target, g.Clock)
	if g.Note != nil {
		raw := p.raw
		p.raw = func(ctx context.Context, method string, params, result any) error {
			err := raw(ctx, method, params, result)
			if err != nil {
				g.Note(method, err)
			}
			return err
		}
	}
	p.git = g.Plumbing(cloneURL, g.Credential)
	p.cloneURL, p.cred = cloneURL, g.Credential
	var cf forge.CodeForge = p
	if !isPushOnly {
		cf = composePR(p, c.Capabilities, webHost, webPath)
		if has(capDraftPRs) {
			cf = relayLayer{cf, p}
		}
	}
	return cf, forge.ResolveCodeForge(cf), nil
}

func (p *pushOnly) Merge(ref string) error  { return p.git.Merge(ref) }
func (p *pushOnly) Rebase(ref string) error { return p.git.Rebase(ref) }

// Probe asks the Connector for the repository's name through forge.probe, so a
// Connector or backend that no longer answers fails it.
func (p *pushOnly) Probe() (string, error) {
	return probeRepo(p.call, p.target)
}

func (p *pushOnly) BranchExists(branch string) (bool, error) {
	var res struct {
		Exists bool `json:"exists"`
	}
	if err := p.call(context.Background(), "forge.branchExists", branchParams{p.target, branch}, &res); err != nil {
		return false, err
	}
	return res.Exists, nil
}

// DeleteBranch refuses the base branch and option-shaped names before any
// Connector call.
func (p *pushOnly) DeleteBranch(branch, base string) error {
	if err := forge.ValidateBranchDelete("connector forge", branch, base); err != nil {
		return err
	}
	return p.call(context.Background(), "forge.deleteBranch", branchParams{p.target, branch}, nil)
}

// cloneInto is a `git clone` step in core's git over the validated clone URL;
// the credential rides the environment, never the URL, and the error carries
// only redacted output.
func (p *pushOnly) cloneInto(op, shapeFlag string) func(dir string) error {
	return func(dir string) error {
		out, err := p.cred.Apply(exec.Command("git", "clone", shapeFlag, p.cloneURL, dir), p.cloneURL).CombinedOutput()
		if err != nil {
			return fmt.Errorf("connector forge: %s: git clone %s: %w%s", op, forge.RedactURLCredentials(p.cloneURL), p.cred.Explain(err, out, p.cloneURL), forge.RedactedOutputTail(out))
		}
		return nil
	}
}

// relayClone is the clone step bundlerelay's helpers take.
func (p *pushOnly) relayClone(op string) func(dir string) error {
	return p.cloneInto(op, "--no-single-branch")
}

// SeedMirror clones the repo bare into dir for a credential-less Box,
// whatever the Box kind: the caller decides when a mirror is wanted.
func (p *pushOnly) SeedMirror(dir string) error {
	return p.cloneInto("seed mirror", "--bare")(dir)
}

var _ forge.MirrorSeeder = (*pushOnly)(nil)

// pushBranch creates branch from localRef in the repo at srcDir, create-only.
func (p *pushOnly) pushBranch(srcDir, localRef, branch, base string) error {
	return bundlerelay.PushBranch("connector forge", srcDir, localRef, branch, base, p.cred, p.cloneURL, p.relayClone("push branch"))
}

// newPushOnly builds a pushOnly whose clock, a zero one included, sleeps.
func newPushOnly(call connectorcall.Call, caps []string, policy HostPolicy, target Target, clock retry.Clock) *pushOnly {
	clock = connectorcall.RealIfZero(clock)
	return pushOnlyOver(policy, connectorcall.Classify(call, clock, caps), clock, target)
}

// pushOnlyOver is newPushOnly over a given raw guest call. Each of its calls,
// and its reads', then runs forgeChecks.
func pushOnlyOver(conn HostPolicy, raw guestCall, clock retry.Clock, target Target) *pushOnly {
	p := &pushOnly{conn: conn, raw: raw, clock: clock, target: target, rd: &reads{target: target}}
	p.call = connectorcall.Checked(func(ctx context.Context, method string, params, result any) error {
		return p.raw(ctx, method, params, result)
	}, forgeChecks(p))
	p.rd.call = p.call
	return p
}

// HostPolicy is the part of *connectorhost.Connector core's own refusals read:
// the audit log. An interface so a test can stand in without a wasm guest.
type HostPolicy interface {
	AuditRefusal(method string, refusal error) error
}

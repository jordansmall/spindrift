package connectorforge

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"slices"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/bundlerelay"
	"spindrift.dev/launcher/internal/gitexec"
	"spindrift.dev/launcher/internal/retry"
)

// The capability names a forge Connector advertises. sdk/forge declares the
// same strings, and its parity test pins them.
const (
	capForge            = "forge"
	capBranches         = "forge.branches"
	capPushOnly         = "forge.pushOnly"
	capPRs              = "forge.prs"
	capAutoMerge        = "forge.autoMerge"
	capCIRun            = "forge.ciRun"
	capBranchProtection = "forge.branchProtection"
	capProbe            = "forge.probe"
	capDraftPRs         = "forge.draftPRs"
)

// ErrPRForgeUnsupported marks a forge Connector that is neither push-only nor
// declares "forge.prs", so no Code Forge can be composed from it.
var ErrPRForgeUnsupported = errors.New("connector forge is neither push-only nor PR-capable")

// ErrRemoteCredentials marks a Connector remote URL that carries credentials.
// Core's git attaches its own, so a URL's userinfo is never honoured.
var ErrRemoteCredentials = errors.New("connector remote URL carries credentials")

// ErrRemoteHost marks a Connector remote URL that is not https or names a host
// outside the Connector's declared routes. Core attaches its git credential to
// the clone URL's host, so this is the check that credential rides on.
var ErrRemoteHost = errors.New("connector remote URL is not https on a declared host")

// The retry schedule for every Connector call core makes: three attempts, two
// seconds longer between each.
const (
	callAttempts    = 3
	callBackoffUnit = 2 * time.Second
)

// Git is the core-side git plumbing the composed forge runs Merge and Rebase
// in: the Connector never touches git or the credential.
type Git struct {
	Credential gitexec.Credential
	// Plumbing builds core's git plumbing over the validated clone URL (the
	// push-only git adapter's NewGitClient in production); Compose hands it
	// Credential only after the clone URL passed the host check. It is injected
	// because this package sits in every backend's go test sources, so it cannot
	// import a backend package. Nil is a construction error.
	Plumbing func(cloneURL string, cred gitexec.Credential) forge.CodeForge
	// Clock paces the retry backoff; the zero value is retry.RealClock().
	Clock retry.Clock
	// ReadOnlyBox is set when the Box cannot push (BOX_FORGE_AND_ISSUE_ACCESS=
	// read-only), so core relays its bundle: Compose then offers BundleRelay and
	// BundleCommitSubjects, and only on a forge declaring "forge.draftPRs",
	// matching the native read-only constructors.
	ReadOnlyBox bool
}

// notRetried are the methods whose request is not idempotent: a retry after a
// transient failure could repeat a merge, open a second PR or post a second
// comment when the first attempt landed and only its response was lost. Their
// caller owns the retry decision.
var notRetried = []string{"forge.merge", "forge.openPR", "forge.comment"}

// classedCall is c.Call made to read as core does: a transient failure is
// retried, then the Connector's error class is mapped onto core's sentinels (a
// rate limit matches forge.ErrRateLimit and an auth failure forge.ErrAuthFailure).
// The class stays readable through connectorhost.ErrorClass.
func classedCall(c *connectorhost.Connector, clock retry.Clock) guestCall {
	return classify(c.Call, clock)
}

// classify is classedCall over any raw call, a seam for tests.
func classify(raw guestCall, clock retry.Clock) guestCall {
	if clock.Sleep == nil {
		clock = retry.RealClock()
	}
	b := retry.LinearBackoff{Unit: callBackoffUnit, Clock: clock}
	return func(ctx context.Context, method string, params, result any) error {
		attempt := 0
		attempts := callAttempts
		if slices.Contains(notRetried, method) {
			attempts = 1
		}
		err := retryTransient(attempts, b, func() error {
			attempt++
			err := raw(ctx, method, params, result)
			// A retried DELETE may find its own earlier attempt already landed,
			// its 5xx lost on the way back: that not-found is success.
			if attempt > 1 && method == "forge.deleteBranch" && connectorhost.ErrorClass(err) == connectorhost.NotFound {
				return nil
			}
			return err
		})
		switch connectorhost.ErrorClass(err) {
		case connectorhost.RateLimited:
			return &forge.RateLimitError{Err: err}
		case connectorhost.Auth:
			return fmt.Errorf("%w: %w", forge.ErrAuthFailure, err)
		}
		return err
	}
}

// pushOnly is a Code Forge with no PR concept: the Connector answers the forge
// questions (the remote's identity, branch existence and deletion) and git
// runs in core's own plumbing against the Connector's clone URL.
type pushOnly struct {
	conn   *connectorhost.Connector
	call   guestCall
	target Target
	git    forge.CodeForge
	// cloneURL and cred are what core's own git (relayClone) clones with; both
	// are set only once cloneURL passed the host check.
	cloneURL string
	cred     gitexec.Credential
}

// Compose builds a Code Forge from a forge Connector whose handshake advertises
// "forge.branches" and either "forge.pushOnly" (a push-only forge: a
// BranchDeleter and no PRForge) or "forge" with "forge.prs" (a PR forge: the PR
// surface, branch deletion and the merge commit, plus a CIRunReporter and
// BranchProtectionForge only when declared, and a DraftPRCreator and
// BranchPusher only on "forge.draftPRs"; with Git.ReadOnlyBox that surface also
// relays the Box's bundle and reads its commit subjects). The returned
// Capabilities are the composed forge resolved by type, so they and any later
// type probe (doctor's, for one) agree on exactly what it offers.
// The remote's identity is read once here, and refused (and audited) unless
// both its URLs are plain https on a host the Connector declared, so the git
// credential never reaches a host the Connector merely named.
func Compose(c *connectorhost.Connector, target Target, g Git) (forge.CodeForge, forge.Capabilities, error) {
	has := func(capName string) bool { return slices.Contains(c.Capabilities, capName) }
	isPushOnly := has(capPushOnly)
	switch {
	case !has(capBranches):
		return nil, forge.Capabilities{}, fmt.Errorf("%w: capabilities %v lack %q", ErrNotForge, c.Capabilities, capBranches)
	case !isPushOnly && !(has(capForge) && has(capPRs)):
		return nil, forge.Capabilities{}, fmt.Errorf("%w: capabilities %v lack %q and %q", ErrPRForgeUnsupported, c.Capabilities, capPushOnly, capPRs)
	}
	if g.Plumbing == nil {
		return nil, forge.Capabilities{}, errors.New("connectorforge: Git.Plumbing is nil")
	}
	p := &pushOnly{conn: c, call: classedCall(c, g.Clock), target: target}
	remote, err := p.remote()
	if err != nil {
		return nil, forge.Capabilities{}, err
	}
	p.git = g.Plumbing(remote.CloneURL, g.Credential)
	p.cloneURL, p.cred = remote.CloneURL, g.Credential
	var cf forge.CodeForge = p
	if !isPushOnly {
		// remote() accepted the web URL, so it parses with a host.
		web, _ := url.Parse(remote.WebURL)
		cf = composePR(p, c.Capabilities, web.Host, g.ReadOnlyBox)
	}
	return cf, forge.ResolveCodeForge(cf), nil
}

// remote reads and validates the repository's identity.
func (p *pushOnly) remote() (wireRemote, error) {
	var r wireRemote
	params := struct {
		Target Target `json:"target"`
	}{p.target}
	if err := p.call(context.Background(), "forge.remote", params, &r); err != nil {
		return wireRemote{}, err
	}
	for _, field := range []struct{ name, raw string }{{"clone URL", r.CloneURL}, {"web URL", r.WebURL}} {
		if err := p.checkURL(field.name, field.raw); err != nil {
			return wireRemote{}, p.conn.AuditRefusal("forge.remote", err)
		}
	}
	return r, nil
}

// checkURL refuses raw unless it is an https URL, with no userinfo, on a
// declared host. The errors describe the URL without its userinfo: they reach
// the audit log.
func (p *pushOnly) checkURL(name, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: %s is not a parseable URL", ErrRemoteHost, name)
	}
	hasUser := u.User != nil
	u.User = nil
	declared := p.conn.DeclaredHosts()
	switch {
	case hasUser:
		return fmt.Errorf("%w: %s %q", ErrRemoteCredentials, name, u.String())
	case u.Scheme != "https" || u.Host == "":
		return fmt.Errorf("%w: %s %q is not https with a host", ErrRemoteHost, name, u.String())
	case !slices.ContainsFunc(declared, func(h string) bool { return strings.EqualFold(h, u.Host) }):
		return fmt.Errorf("%w: %s host %q is not among %v", ErrRemoteHost, name, u.Host, declared)
	}
	return nil
}

func (p *pushOnly) Merge(ref string) error  { return p.git.Merge(ref) }
func (p *pushOnly) Rebase(ref string) error { return p.git.Rebase(ref) }

// Probe re-reads the remote's identity, so a Connector or backend that no
// longer answers fails it, and returns the target repository.
func (p *pushOnly) Probe() (string, error) {
	if _, err := p.remote(); err != nil {
		return "", err
	}
	return p.target.Repo, nil
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

// retryTransient calls fn up to attempts times, sleeping b.Do(n) after the nth
// failure, and retries only a Transient Connector error. Any other error
// returns at once: RateLimited belongs to core's rate-limit hold, and a short
// loop there would only burn its budget. It returns the last error.
func retryTransient(attempts int, b retry.LinearBackoff, fn func() error) error {
	var err error
	for n := 1; ; n++ {
		if err = fn(); err == nil || connectorhost.ErrorClass(err) != connectorhost.Transient || n >= attempts {
			return err
		}
		b.Do(n)
	}
}

// relayClone is the `git clone` step bundlerelay's helpers take, run in core's
// git over the validated clone URL with core's credential: the Connector never
// sees git or the credential. op names the caller; the credential rides the
// environment, never the URL, and the clone's output stays out of the error.
func (p *pushOnly) relayClone(op string) func(dir string) error {
	return func(dir string) error {
		if _, err := p.cred.Apply(exec.Command("git", "clone", "--no-single-branch", p.cloneURL, dir)).CombinedOutput(); err != nil {
			return fmt.Errorf("connector forge: %s: git clone %s: %w", op, forge.RedactURLCredentials(p.cloneURL), err)
		}
		return nil
	}
}

// pushBranch creates branch from localRef in the repo at srcDir, create-only.
func (p *pushOnly) pushBranch(srcDir, localRef, branch, base string) error {
	return bundlerelay.PushBranch("connector forge", srcDir, localRef, branch, base, p.cred, p.relayClone("push branch"))
}

package gitexec

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// Credential is the forge username and password git presents to an http(s)
// remote; either may carry the token. The zero value means no credential: Apply
// is a no-op on it.
type Credential struct{ Username, Password string }

// CredentialSource yields the Credential for one git operation, read at the
// moment of use so a token rotated since the source was built is honoured. A
// nil source is the zero Credential.
type CredentialSource func() (Credential, error)

// StaticCredential is the source of a Credential that never changes.
func StaticCredential(c Credential) CredentialSource {
	return func() (Credential, error) { return c, nil }
}

// Get reads the source; a nil one yields the zero Credential.
func (s CredentialSource) Get() (Credential, error) {
	if s == nil {
		return Credential{}, nil
	}
	return s()
}

// String redacts the password so a stray %v cannot log it.
func (c Credential) String() string { return "Credential{redacted}" }

// GoString redacts the password for %#v.
func (c Credential) GoString() string { return c.String() }

// SplitCredential splits the userinfo off an http or https URL, returning the
// credential-free URL and the credential. A username-only URL is split too:
// the username may be the token (the Forgejo and Gitea shape). Any other input
// (a local path, file://, ssh://, scp-style, unparseable) comes back unchanged
// with the zero Credential: an ssh user is not a secret and git needs it in
// the URL.
func SplitCredential(rawURL string) (string, Credential) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User == nil {
		return rawURL, Credential{}
	}
	pass, _ := u.User.Password()
	cred := Credential{Username: u.User.Username(), Password: pass}
	u.User = nil
	return u.String(), cred
}

const (
	usernameEnv = "GITEXEC_USERNAME"
	passwordEnv = "GITEXEC_PASSWORD"

	// Git runs a "!" helper as `sh -c '<value> get'`, so the trailing " get"
	// lands in f's $1. Store and erase are ignored.
	envHelper = `!f() { test "$1" = get || exit 0; printf 'username=%s\npassword=%s\n' "$` +
		usernameEnv + `" "$` + passwordEnv + `"; }; f`
)

// Apply makes cmd's git authenticate with c against repoURL and returns cmd.
// repoURL is the exact URL git talks to (for a push to origin, origin's URL):
// the helper is registered as credential.<repoURL>.helper with useHttpPath, so
// an LFS endpoint or a second repository on the same host is never offered the
// token. http.<repoURL>.followRedirects=false stops git carrying an
// Authorization header it already holds across a redirect. A zero Credential,
// or a repoURL that is not http(s), makes Apply a no-op: other remotes carry no
// credential.
//
// The secret travels only as an environment value read by an inline credential
// helper, never in argv, a URL, or on-disk config. Ambient helpers are reset
// first, unscoped, since a scoped entry cannot clear them: git offers every
// helper the credential on success, so one could store it in
// ~/.git-credentials. A `-c credential.helper` on cmd's argv is not reset (git
// reads GIT_CONFIG_PARAMETERS after GIT_CONFIG_COUNT), so callers must not pair
// one with a non-zero Credential. Both fields are always emitted, or git would
// prompt and fail under GIT_TERMINAL_PROMPT=0.
func (c Credential) Apply(cmd *exec.Cmd, repoURL string) *exec.Cmd {
	if u, err := url.Parse(repoURL); c == (Credential{}) || err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return cmd
	}
	// git matches a config URL without a username against any username, and the
	// secret must not land in a config key.
	repoURL = withoutUserinfo(repoURL)
	env := cmd.Env
	if env == nil {
		env = os.Environ()
	}
	n := 0
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "GIT_CONFIG_COUNT="); ok {
			if parsed, err := strconv.Atoi(v); err == nil && parsed >= 0 {
				n = parsed
			} else {
				n = 0
			}
		}
	}
	scope := "credential." + repoURL
	entries := [][2]string{
		{"credential.helper", ""}, // reset first so ambient helpers never see the credential
		{scope + ".helper", envHelper},
		{scope + ".useHttpPath", "true"},
		{"http." + repoURL + ".followRedirects", "false"},
	}
	for i, e := range entries {
		idx := strconv.Itoa(n + i)
		env = append(env, "GIT_CONFIG_KEY_"+idx+"="+e[0], "GIT_CONFIG_VALUE_"+idx+"="+e[1])
	}
	cmd.Env = append(env,
		"GIT_CONFIG_COUNT="+strconv.Itoa(n+len(entries)),
		usernameEnv+"="+c.Username,
		passwordEnv+"="+c.Password,
		"GIT_TERMINAL_PROMPT=0",
		// Explain matches git's English stderr.
		"LC_ALL=C",
	)
	return cmd
}

// withoutUserinfo drops any userinfo from rawURL by string surgery, so it also
// works on a URL url.Parse rejects and leaves the rest byte-for-byte.
func withoutUserinfo(rawURL string) string {
	i := strings.Index(rawURL, "://")
	if i < 0 {
		return rawURL
	}
	rest := rawURL[i+3:]
	authority := rest
	// Only "/" ends it: an unescaped password may hold "?" or "#", and
	// forge.RedactURLCredentials ends the authority the same way.
	if j := strings.IndexByte(rest, '/'); j >= 0 {
		authority = rest[:j]
	}
	at := strings.LastIndex(authority, "@")
	if at < 0 {
		return rawURL
	}
	return rawURL[:i+3] + rest[at+1:]
}

// redirectFailure is how git fails once a remote redirects repoURL elsewhere.
// Apply's followRedirects=false makes git stop at the 30x ("returned error:
// 301"), whether the forge redirected before or after authenticating.
var redirectFailure = regexp.MustCompile(`could not read Username|returned error: 30[12378]\b`)

// Explain says why a failed git run reached a URL other than repoURL: the
// remote redirected (a renamed or transferred repository). git's own message
// names only the host, so Explain wraps err naming repoURL minus any userinfo.
// out is the run's output; a *exec.ExitError's captured Stderr is read too.
// Any other failure, and a zero Credential, come back unchanged.
func (c Credential) Explain(err error, out []byte, repoURL string) error {
	if err == nil || c == (Credential{}) {
		return err
	}
	var exit *exec.ExitError
	if !redirectFailure.Match(out) && !(errors.As(err, &exit) && redirectFailure.Match(exit.Stderr)) {
		return err
	}
	return fmt.Errorf("git reached a URL other than the configured Target repo %s; it most likely redirected (renamed or transferred?), so update the configured Target repo: %w", withoutUserinfo(repoURL), err)
}

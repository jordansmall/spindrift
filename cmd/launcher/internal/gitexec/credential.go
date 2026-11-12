package gitexec

import (
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// Credential is the forge username and password git presents to an http(s)
// remote; either may carry the token. The zero value means no credential: Apply
// is a no-op on it.
type Credential struct{ Username, Password string }

// String redacts the password so a stray %v cannot log it.
func (c Credential) String() string { return "Credential{redacted}" }

// GoString redacts the password for %#v.
func (c Credential) GoString() string { return c.String() }

// SplitCredential splits the userinfo off an http or https URL, returning the
// credential-free URL and the credential. A username-only URL is split too:
// the username may itself be the token (the Forgejo and Gitea shape). Every
// other input (a local path, file://, ssh://, scp-style git@host:path, an
// unparseable string) comes back unchanged with the zero Credential: an ssh
// user is not a secret and git needs it in the URL.
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

// Apply makes cmd's git authenticate with c and returns cmd. The secret
// travels only as an environment value read by an inline credential helper:
// it is in no argv, no URL, and no config git writes to disk. Helpers from
// config files and the environment are always reset, so none can store or see
// it (git offers every helper the credential on success, and a
// token-as-username would land in ~/.git-credentials). A `-c
// credential.helper` on the same command's argv is not reset (git reads
// GIT_CONFIG_PARAMETERS after GIT_CONFIG_COUNT), so a caller must not pair a
// non-zero Credential with one. With an empty password the helper answers an
// empty one, the token-as-username shape. Both fields are emitted, or git
// would prompt and GIT_TERMINAL_PROMPT=0 would fail the call. An existing
// GIT_CONFIG_COUNT is extended, not overwritten.
func (c Credential) Apply(cmd *exec.Cmd) *exec.Cmd {
	if c == (Credential{}) {
		return cmd
	}
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
	// Reset first so ambient helpers never see the credential, then add ours.
	helpers := []string{"", envHelper}
	for i, h := range helpers {
		idx := strconv.Itoa(n + i)
		env = append(env, "GIT_CONFIG_KEY_"+idx+"=credential.helper", "GIT_CONFIG_VALUE_"+idx+"="+h)
	}
	cmd.Env = append(env,
		"GIT_CONFIG_COUNT="+strconv.Itoa(n+len(helpers)),
		usernameEnv+"="+c.Username,
		passwordEnv+"="+c.Password,
		"GIT_TERMINAL_PROMPT=0",
	)
	return cmd
}

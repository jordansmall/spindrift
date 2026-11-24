// Package forgejo is the Forgejo adapter. The IssueTracker, CodeForge and
// PRForge seams are all served by the Connector (connector_tracker.go,
// connector_forge.go); this file holds the git and env helpers around it.
package forgejo

import (
	"fmt"
	"net/url"
	"strings"

	"spindrift.dev/launcher/internal/gitexec"
)

// GitRemoteURL builds the clone URL for repo (an owner/repo slug). It carries
// no userinfo: git authenticates with GitCredential, out of band, so the token
// never reaches an argv, a persisted remote or an error string. It must equal
// the Connector's CloneURL for the same base and slug (connectors/forgejo
// remote): the credential is scoped to this URL's path and git matches paths
// by whole segments, so a base subpath is kept. If baseURL fails to parse, it
// concatenates the slug onto the raw base.
func GitRemoteURL(baseURL, repo string) string {
	baseURL = strings.TrimSuffix(baseURL, "/")
	u, err := url.Parse(baseURL)
	if err != nil {
		return baseURL + "/" + strings.Trim(repo, "/") + ".git"
	}
	u.User, u.RawQuery, u.Fragment = nil, "", ""
	u.Path = strings.TrimSuffix(u.Path, "/") + "/" + strings.Trim(repo, "/") + ".git"
	u.RawPath = ""
	return u.String()
}

// GitCredential is the credential git presents to a Forgejo instance for
// token. Forgejo/Gitea basic auth takes a non-empty password as an access
// token, so the username is a placeholder, the same oauth2:<token> shape the
// git backend documents. An empty token yields the zero (anonymous)
// Credential.
func GitCredential(token string) gitexec.Credential {
	if token == "" {
		return gitexec.Credential{}
	}
	return gitexec.Credential{Username: "oauth2", Password: token}
}

// MergeStyle resolves MergeMethod to a merge style. An unset method resolves
// to "rebase", matching the github adapter's default; the forge build gates on
// rebase and AutoMergeRemedy names the style.
func MergeStyle(method string) string {
	switch method {
	case "merge":
		return "merge"
	case "squash":
		return "squash"
	default:
		return "rebase"
	}
}

// ValidateForgejoEnv checks the FORGEJO_* knobs required when <knob>=forgejo
// and returns an error for the first unmet requirement. knob names the
// selecting env var and is quoted in the error.
func ValidateForgejoEnv(knob, baseURL, token string) error {
	if baseURL == "" {
		return errBaseURLRequired(knob)
	}
	if token == "" {
		return fmt.Errorf("set FORGEJO_TOKEN when %s=forgejo", knob)
	}
	return nil
}

// errBaseURLRequired is the missing-FORGEJO_BASE_URL error for the axis knob.
func errBaseURLRequired(knob string) error {
	return fmt.Errorf("set FORGEJO_BASE_URL (Forgejo instance base URL) when %s=forgejo", knob)
}

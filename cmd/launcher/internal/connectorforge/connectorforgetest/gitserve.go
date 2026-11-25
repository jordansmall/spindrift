package connectorforgetest

import (
	"strings"

	"spindrift.dev/launcher/internal/credsource"
)

// Option tunes how a harness is built.
type Option func(*options)

type options struct {
	gitOverHTTPS bool
	token        credsource.Source
	webPath      string
}

// WithGitOverHTTPS serves the bare repo's git from the backend's own TLS server
// at the exact clone URL, gated on the harness Credential, and sets no insteadOf
// rewrite. Core's git then has to authenticate the way it does against a real
// forge, so a credential scoped to any URL but the one git uses fails.
func WithGitOverHTTPS() Option { return func(o *options) { o.gitOverHTTPS = true } }

// WithWebBasePath serves the forge's web, clone and PR URLs under path (for
// example "/forgejo"), as a self-hosted instance behind a subpath does, and
// sets the harness WebBase to match. The API stays at the host root.
func WithWebBasePath(path string) Option {
	return func(o *options) { o.webPath = strings.TrimRight(path, "/") }
}

// WithToken makes the harness's one credential token's value, read at each use
// as production does: the Connector bearer is read at each request and core's
// git credential at each git call. The backend and the git server then accept
// only the value token held when the harness was built, until Backend.SetToken
// changes it. Pair it with WithGitOverHTTPS to gate git too.
func WithToken(token credsource.Source) Option { return func(o *options) { o.token = token } }

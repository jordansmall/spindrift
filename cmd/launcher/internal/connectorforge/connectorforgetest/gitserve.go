package connectorforgetest

// Option tunes how a harness is built.
type Option func(*options)

type options struct {
	gitOverHTTPS bool
	tokenEnv     string
}

// WithGitOverHTTPS serves the bare repo's git from the backend's own TLS server
// at the exact clone URL, gated on the harness Credential, and sets no insteadOf
// rewrite. Core's git then has to authenticate the way it does against a real
// forge, so a credential scoped to any URL but the one git uses fails.
func WithGitOverHTTPS() Option { return func(o *options) { o.gitOverHTTPS = true } }

// WithTokenEnv makes the harness's one credential the value of the tokenEnv
// environment variable, read at each use as production does: the Connector
// bearer is read at each request and core's git credential at each git call.
// The backend and the git server then accept only the value the variable held
// when the harness was built, until Backend.SetToken changes it. Pair it with
// WithGitOverHTTPS to gate git too.
func WithTokenEnv(tokenEnv string) Option { return func(o *options) { o.tokenEnv = tokenEnv } }

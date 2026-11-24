package connectorforgetest

// Option tunes how a harness is built.
type Option func(*options)

type options struct{ gitOverHTTPS bool }

// WithGitOverHTTPS serves the bare repo's git from the backend's own TLS server
// at the exact clone URL, gated on the harness Credential, and sets no insteadOf
// rewrite. Core's git then has to authenticate the way it does against a real
// forge, so a credential scoped to any URL but the one git uses fails.
func WithGitOverHTTPS() Option { return func(o *options) { o.gitOverHTTPS = true } }

// Package runnertest holds test-only helpers shared by the runner package's
// adapter test suites: a scripted fake runtime CLI, stdout/stderr capture, a
// short-path unix socket, and argv matchers. It never imports runner, so the
// runner package's own internal tests can import it.
package runnertest

// Package runnertest holds test-only helpers shared by the oci and bwrap
// adapter test suites: a scripted fake runtime CLI, output capture, a
// short-path unix socket, argv matchers, the image-build report pipe, the
// issue-text-reaches-the-prompt assertion, the mount-table fixture, and the
// knob lookup. It never imports runner, so runner's own internal tests can
// import it.
package runnertest

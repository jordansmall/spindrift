// Package runnertest holds test-only helpers shared by the oci and bwrap
// packages' adapter test suites: a scripted fake runtime CLI, stdout/stderr
// capture, a short-path unix socket, argv matchers, an image-build report pipe
// (InstallImageBuildPipe, WantImageBuildStates), and the issue-text-reaches-
// the-prompt end-to-end assertion (AssertIssueTextReachesPrompt). It never
// imports runner, so the runner package's own internal tests can import it.
package runnertest

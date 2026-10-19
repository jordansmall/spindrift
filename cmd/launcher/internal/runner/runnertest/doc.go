// Package runnertest holds test-only helpers shared by the oci and bwrap
// packages' adapter test suites: a scripted fake runtime CLI, stdout/stderr
// capture, a short-path unix socket, argv matchers, an image-build report pipe
// (InstallImageBuildPipe, WantImageBuildStates), the issue-text-reaches-the-
// prompt end-to-end assertion (AssertIssueTextReachesPrompt), and the shared
// mount-table fixture and adapter-file marker check (MountFixture,
// AssertNoMountDecisionMarkers), and the knob lookup (Lookup) that stands in for
// the launcher's. It never imports runner, so the runner
// package's own internal tests can import it.
package runnertest

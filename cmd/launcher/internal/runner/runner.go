// Package runner is the seam through which the Launcher dispatches each Box.
// Isolation is a pluggable strategy: the OCI adapter wraps podman/docker;
// the bwrap adapter wraps bubblewrap. The fake is for unit tests.
package runner

import "io"

// Box carries everything needed to run one agent instance.
type Box struct {
	// Issue is the GitHub issue number (string, e.g. "42").
	Issue string
	// Name is the container/sandbox name (e.g. "agent-issue-42").
	Name string
	// Env is the complete set of env vars forwarded from the host into the Box.
	// The caller builds this map; the adapter passes it through without adding more.
	Env map[string]string
	// Stdout and Stderr receive the Box's output. nil → os.Stdout / os.Stderr.
	Stdout io.Writer
	Stderr io.Writer
}

// Runner is the isolation seam — all container/sandbox operations go through here.
type Runner interface {
	// EnsureReady guarantees the runner's prerequisite is satisfied before Run.
	// OCI: check image present; host-build or container-fallback if absent.
	// bwrap: realise the agent store closures.
	EnsureReady() error
	// Run launches the Box and blocks until it exits. Non-zero exit → error.
	Run(box Box) error
	// Reap removes any leftover Box from a prior interrupted run (best-effort).
	Reap(name string) error
}

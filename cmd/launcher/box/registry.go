package main

import (
	"io"
	"os"
	"os/exec"

	"spindrift.dev/launcher/internal/bindregistry"
	"spindrift.dev/launcher/internal/ecosystem"
)

// spawnTCPForwarder spawns the TCP Forwarder through driver-exec: box cannot
// serve forward-registry-tcp itself.
func spawnTCPForwarder(upstreamHost string, upstreamPort int, secret string, port int) (int, error) {
	bin, err := exec.LookPath("driver-exec")
	if err != nil {
		return 0, err
	}
	return bindregistry.SpawnHTTPForwarder(bin, upstreamHost, upstreamPort, secret, port)
}

// registryDeps are the registry proxy binding steps (ADR 0044, ADR 0045),
// split out so tests can drive each outcome without a live Forwarder.
type registryDeps struct {
	// Gate resolves REGISTRY_PROXY_MANIFEST and ensures the Forwarder is
	// listening, once for the whole run.
	Gate func(w io.Writer) bindregistry.Gate
	// Every step reports success.
	BindHomes   func(w io.Writer, gate bindregistry.Gate, publish bindregistry.PublishFunc) bool
	ApplyInTree func(w io.Writer, workDir string, gate bindregistry.Gate, publish bindregistry.PublishFunc) bool
	Revert      func(workDir string, w io.Writer) bool
	// Setenv exports a binding to box's environment, which every child inherits.
	Setenv func(key, value string) error
}

func realRegistryDeps() registryDeps {
	return registryDeps{
		Gate: func(w io.Writer) bindregistry.Gate {
			return bindregistry.ResolveGate(w, bindregistry.GateDeps{
				Probe:        bindregistry.DialProbe,
				Spawn:        bindregistry.SpawnSocat,
				LookPath:     exec.LookPath,
				SpawnTCP:     spawnTCPForwarder,
				Timeout:      bindregistry.ForwarderReadyTimeout,
				PollInterval: bindregistry.ForwarderPollInterval,
			})
		},
		BindHomes:   bindregistry.BindHomes,
		ApplyInTree: bindregistry.ApplyInTree,
		Revert: func(workDir string, w io.Writer) bool {
			return !bindregistry.RevertInTreeBindings(workDir, bindregistry.InTreeBindings(), "driver-exec bind-registry", w)
		},
		Setenv: os.Setenv,
	}
}

// bindRegistry points cargo/npm/pnpm/yarn/Go at the in-Box Forwarder, and for a
// cloned repo applies the in-tree bindings. It runs before anything that could
// first build with them, the prefetch hook included. A failed step warns and the
// run carries on without it, so nothing here returns an error. The returned
// func reverts the in-tree bindings; call it on every exit.
func (r *boxRun) bindRegistry() (revert func()) {
	reg, w := r.d.Registry, r.d.Stdout
	gate := reg.Gate(w)

	// An export reaches the environment only once its step succeeded: a failed
	// step is skipped whole, never half-applied.
	step := func(run func(publish bindregistry.PublishFunc) bool, failure string) bool {
		var exports []ecosystem.EnvExport
		ok := run(func(e []ecosystem.EnvExport) bool { exports = e; return true })
		if !ok {
			r.say("%s", failure)
			return false
		}
		for _, e := range exports {
			if err := reg.Setenv(e.Name, e.Value); err != nil {
				r.say("==> WARNING: registry proxy binding %s could not be exported: %v", e.Name, err)
			}
		}
		return true
	}

	step(func(p bindregistry.PublishFunc) bool { return reg.BindHomes(w, gate, p) },
		"==> WARNING: driver-exec bind-registry failed (exit 1) — skipping registry proxy bindings")
	if r.env.SelfContained {
		return func() {}
	}
	revert = func() {
		if !reg.Revert(r.in.WorkDir, w) {
			r.say("==> WARNING: driver-exec bind-registry (in-tree revert) failed (exit 1)")
		}
	}
	if !step(func(p bindregistry.PublishFunc) bool { return reg.ApplyInTree(w, r.in.WorkDir, gate, p) },
		"==> WARNING: driver-exec bind-registry (in-tree apply) failed (exit 1) — skipping in-tree registry binding") {
		// No partial apply may stay on disk (issues #2932, #3027).
		revert()
	}
	return revert
}

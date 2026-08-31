package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"spindrift.dev/launcher/internal/bindregistry"
)

// registryProxyForwarderTimeout/PollInterval mirror the deleted
// entrypoint.sh phase_registry_proxy_forwarder's own readiness-poll
// constants (see git history: 50 tries @ 100ms, ~5s total) verbatim -- not
// exposed as flags, since entrypoint.sh never overrode them either.
const (
	registryProxyForwarderTimeout      = 5 * time.Second
	registryProxyForwarderPollInterval = 100 * time.Millisecond
)

// isBindRegistryInvocation reports whether args (os.Args[1:]) selects the
// bind-registry subcommand: a distinct verb, not a top-level flag,
// mirroring isReadonlyGuardsInvocation/isAssemblePromptInvocation.
func isBindRegistryInvocation(args []string) bool {
	return len(args) > 0 && args[0] == "bind-registry"
}

// runBindRegistry is the `bind-registry` subcommand's public entry point
// (ADR 0007's thin-exec-glue tier, ADR 0036 amendment #6, issue #2930/#2931):
// it wires the real bindregistry.DialProbe/SpawnSocat into
// runBindRegistryWithDeps. Returns the process exit code.
func runBindRegistry(args []string, stdout io.Writer) int {
	return runBindRegistryWithDeps(args, stdout, bindregistry.DialProbe, bindregistry.SpawnSocat, registryProxyForwarderTimeout, registryProxyForwarderPollInterval)
}

// runBindRegistryWithDeps does the actual flag parsing and orchestration for
// bind-registry, taking probe/spawn as parameters so tests can exercise the
// bindings-mode readiness paths (already-listening, timeout, ready) without
// a real socat process or a real TCP listener. timeout/pollInterval are
// threaded through to runBindRegistryBindings so tests can shrink them below
// the real registryProxyForwarderTimeout/PollInterval constants -- production
// callers (runBindRegistry) always pass those two constants unchanged. It
// covers three independent modes, each gated on its own pair of flags:
//   - classification mode (-work-dir/-ecosystem-env-output): classifies
//     -work-dir's lockfiles via bindregistry.Classify and writes the result
//     as a sourceable NUDGE_ECOSYSTEM env file (unchanged since #2930).
//   - bindings mode (-registry-proxy-socket/-bindings-env-output): ensures
//     the Forwarder is listening, then computes and writes the Go/npm-family
//     env bindings plus the cargo config.toml.
//   - in-tree mode (-intree-work-dir/-intree-action, issue #2932): applies or
//     reverts the in-tree config-file rewrite (e.g. cargo's
//     .cargo/config.toml) that points a tracked ecosystem config file at the
//     local Forwarder instead of the real upstream registry, gated on the
//     same Forwarder readiness bindings mode uses.
//
// Any mode's flag pair may be given alone, or together with another (the
// entrypoint.sh call sites this verb serves run at different points in
// main() -- see docs/adr/0036 and the coordinator's slice notes -- so a
// single entrypoint.sh invocation only ever needs one mode at a time today,
// but the verb itself doesn't assume that).
func runBindRegistryWithDeps(args []string, stdout io.Writer, probe bindregistry.ProbeFunc, spawn bindregistry.SpawnFunc, timeout, pollInterval time.Duration) int {
	fs := flag.NewFlagSet("bind-registry", flag.ContinueOnError)
	fs.SetOutput(stdout)
	workDir := fs.String("work-dir", "", "the cloned Target repo to scan for lockfiles (optional, pairs with -ecosystem-env-output)")
	ecosystemEnvOutput := fs.String("ecosystem-env-output", "", "path to write the sourceable NUDGE_ECOSYSTEM env file to (optional, pairs with -work-dir)")
	registryProxySocket := fs.String("registry-proxy-socket", "", "path to the mounted registry proxy unix socket (optional, pairs with -bindings-env-output and/or -intree-action=apply)")
	// 27182 duplicates agent/entrypoint.sh's own
	// REGISTRY_PROXY_FORWARDER_PORT default, but entrypoint.sh always passes
	// -forwarder-port explicitly on every real call site, so this default
	// never actually fires in production -- it's a CLI/test convenience
	// only. Keep the two literals in sync by hand if either ever changes.
	forwarderPort := fs.Int("forwarder-port", 27182, "TCP port the Forwarder listens on at 127.0.0.1")
	bindingsEnvOutput := fs.String("bindings-env-output", "", "path to write the sourceable registry-binding env file to (optional, pairs with -registry-proxy-socket)")
	intreeWorkDir := fs.String("intree-work-dir", "", "the cloned Target repo root to apply/revert in-tree bindings in (optional, pairs with -intree-action)")
	intreeAction := fs.String("intree-action", "", "in-tree binding operation: \"apply\" or \"revert\" (optional, pairs with -intree-work-dir)")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	if (*workDir == "") != (*ecosystemEnvOutput == "") {
		fmt.Fprintln(stdout, "driver-exec bind-registry: -work-dir and -ecosystem-env-output must be given together")
		return 1
	}
	if (*intreeWorkDir == "") != (*intreeAction == "") {
		fmt.Fprintln(stdout, "driver-exec bind-registry: -intree-work-dir and -intree-action must be given together")
		return 1
	}
	if *intreeAction != "" && *intreeAction != "apply" && *intreeAction != "revert" {
		fmt.Fprintln(stdout, "driver-exec bind-registry: -intree-action must be \"apply\" or \"revert\", got "+strconv.Quote(*intreeAction))
		return 1
	}
	// -registry-proxy-socket is shared by two independent pairings now
	// (bindings mode and intree-apply), either of which alone justifies its
	// presence -- only "given with neither" is an error. Unlike the other
	// pairs above, this is deliberately not a strict two-flag XOR anymore.
	if *registryProxySocket == "" {
		if *bindingsEnvOutput != "" {
			fmt.Fprintln(stdout, "driver-exec bind-registry: -registry-proxy-socket and -bindings-env-output must be given together")
			return 1
		}
	} else if *bindingsEnvOutput == "" && *intreeAction != "apply" {
		fmt.Fprintln(stdout, "driver-exec bind-registry: -registry-proxy-socket requires -bindings-env-output or -intree-action=apply")
		return 1
	}
	if *workDir == "" && *ecosystemEnvOutput == "" && *registryProxySocket == "" && *bindingsEnvOutput == "" && *intreeWorkDir == "" && *intreeAction == "" {
		fmt.Fprintln(stdout, "driver-exec bind-registry: at least one of -work-dir/-ecosystem-env-output, -registry-proxy-socket/-bindings-env-output, or -intree-work-dir/-intree-action is required")
		return 1
	}

	if *workDir != "" {
		if rc := runBindRegistryClassification(stdout, *workDir, *ecosystemEnvOutput); rc != 0 {
			return rc
		}
	}

	if *intreeAction != "" {
		if rc := runBindRegistryIntree(stdout, *intreeAction, *intreeWorkDir, *registryProxySocket, *forwarderPort, probe, spawn, timeout, pollInterval); rc != 0 {
			return rc
		}
	}

	if *registryProxySocket != "" && *bindingsEnvOutput != "" {
		return runBindRegistryBindings(stdout, *registryProxySocket, *forwarderPort, *bindingsEnvOutput, probe, spawn, timeout, pollInterval)
	}

	return 0
}

// runBindRegistryClassification is the unchanged #2930 classification-mode
// body, extracted verbatim so it can be called conditionally now that
// -work-dir/-ecosystem-env-output are no longer unconditionally required.
func runBindRegistryClassification(stdout io.Writer, workDir, ecosystemEnvOutput string) int {
	classification := bindregistry.Classify(workDir)

	// %q emits Go quoting, not shell quoting -- safe here only because
	// classification is always one of bindregistry's own fixed constant
	// strings, never attacker- or repo-controlled input.
	env := fmt.Sprintf("NUDGE_ECOSYSTEM=%q\n", classification)
	if err := os.WriteFile(ecosystemEnvOutput, []byte(env), 0o644); err != nil {
		fmt.Fprintln(stdout, "driver-exec bind-registry: write ecosystem env output:", err)
		return 1
	}

	return 0
}

// isMountedSocket reports whether path exists and is a unix socket -- the
// same `[ -S "$path" ]`-equivalent guard both bindings mode and intree-apply
// mode use to detect a disabled registry proxy (empty/unmounted socket path)
// and silently no-op rather than error.
func isMountedSocket(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode()&os.ModeSocket != 0
}

// runBindRegistryBindings is bindings mode: it ports the deleted
// entrypoint.sh phase_registry_proxy_forwarder + phase_go_binding (see git
// history) into Go, using probe/spawn (real or fake) via
// bindregistry.EnsureForwarderReady instead of bash's own /dev/tcp probe and
// backgrounded socat job.
func runBindRegistryBindings(stdout io.Writer, socketPath string, port int, bindingsEnvOutput string, probe bindregistry.ProbeFunc, spawn bindregistry.SpawnFunc, timeout, pollInterval time.Duration) int {
	if !isMountedSocket(socketPath) {
		// Mirrors bash's `[ -S "$REGISTRY_PROXY_SOCKET_PATH" ] || return 0`:
		// the socket isn't mounted (proxy disabled), so silently no-op and
		// leave bindings-env-output untouched.
		return 0
	}

	// The socat PATH check only gates the *spawn* path: EnsureForwarderReady
	// probes first and only calls spawn if nothing is listening yet, so an
	// already-ready Forwarder needs socat on PATH not at all -- checking
	// unconditionally would wrongly warn and skip all bindings for a
	// Forwarder that's already up.
	if !probe(port) {
		if _, err := exec.LookPath("socat"); err != nil {
			fmt.Fprintln(stdout, "==> WARNING: "+socketPath+" is mounted but socat is not on PATH — cargo, npm, pnpm, yarn, and gradle will fall back to the public registry")
			return 0
		}
	}

	ready, err := bindregistry.EnsureForwarderReady(socketPath, port, probe, spawn, timeout, pollInterval)
	if err != nil {
		fmt.Fprintln(stdout, "==> WARNING: registry proxy Forwarder failed to start: "+err.Error()+" — cargo, npm, pnpm, yarn, and gradle will fall back to the public registry")
		return 0
	}
	if !ready {
		fmt.Fprintln(stdout, "==> WARNING: registry proxy Forwarder did not start listening on 127.0.0.1:"+strconv.Itoa(port)+" within "+timeout.String()+" — cargo, npm, pnpm, yarn, and gradle will fall back to the public registry")
		return 0
	}

	goBindings := bindregistry.ComputeGoBindings(port, bindregistry.GoBindingInput{
		GOTOOLCHAIN: os.Getenv("GOTOOLCHAIN"),
		GONOPROXY:   os.Getenv("GONOPROXY"),
		GOPRIVATE:   os.Getenv("GOPRIVATE"),
		GOSUMDB:     os.Getenv("GOSUMDB"),
		GONOSUMDB:   os.Getenv("GONOSUMDB"),
	})

	exports := append(append([]bindregistry.EnvExport{}, goBindings.Exports...), bindregistry.NpmFamilyBindings(port)...)

	// %q emits Go quoting, not shell quoting -- safe here only because
	// exports' values are always port-derived (http://127.0.0.1:<port>/...)
	// or one of bindregistry's own fixed constant strings ("none", "off",
	// "local", ...), never attacker- or repo-controlled input, so %q's
	// Go-quoting output happens to still be valid shell input for this
	// file's later `source` by agent/entrypoint.sh -- a property of the
	// current callers, not a general guarantee.
	var rendered string
	for _, e := range exports {
		rendered += fmt.Sprintf("export %s=%q\n", e.Name, e.Value)
	}
	rendered += "FORWARDER_READY=\"1\"\n"

	if err := os.WriteFile(bindingsEnvOutput, []byte(rendered), 0o644); err != nil {
		fmt.Fprintln(stdout, "driver-exec bind-registry: write bindings env output:", err)
		return 1
	}

	cargoHome := os.Getenv("CARGO_HOME")
	if cargoHome == "" {
		home := os.Getenv("HOME")
		if home == "" {
			// Mirrors bash's `set -u`: an unset $HOME there would have died
			// on expansion rather than let filepath.Join("", ".cargo")
			// silently resolve to a relative ".cargo" under the process's
			// cwd -- the wrong location, with no error.
			fmt.Fprintln(stdout, "driver-exec bind-registry: CARGO_HOME and HOME are both unset, cannot resolve a cargo home")
			return 1
		}
		cargoHome = filepath.Join(home, ".cargo")
	}
	if err := os.MkdirAll(cargoHome, 0o755); err != nil {
		fmt.Fprintln(stdout, "driver-exec bind-registry: create cargo home:", err)
		return 1
	}
	if err := os.WriteFile(filepath.Join(cargoHome, "config.toml"), []byte(bindregistry.CargoConfigTOML(port)), 0o644); err != nil {
		fmt.Fprintln(stdout, "driver-exec bind-registry: write cargo config:", err)
		return 1
	}

	gradleUserHome := os.Getenv("GRADLE_USER_HOME")
	if gradleUserHome == "" {
		home := os.Getenv("HOME")
		if home == "" {
			// Mirrors bash's `set -u`: an unset $HOME there would have died
			// on expansion of `${GRADLE_USER_HOME:-$HOME/.gradle}` rather
			// than let string concatenation silently resolve to the literal
			// "/.gradle" -- an absolute root-level path that MkdirAll/
			// WriteFile below would happily create when running as root,
			// claiming gradle is bound at a path nothing will ever read
			// from. Matches cargo's own both-unset guard above.
			fmt.Fprintln(stdout, "driver-exec bind-registry: GRADLE_USER_HOME and HOME are both unset, cannot resolve a gradle home")
			return 1
		}
		gradleUserHome = filepath.Join(home, ".gradle")
	}
	gradleInitDir := filepath.Join(gradleUserHome, "init.d")
	if err := os.MkdirAll(gradleInitDir, 0o755); err != nil {
		fmt.Fprintln(stdout, "driver-exec bind-registry: create gradle init.d:", err)
		return 1
	}
	gradleInitScript := filepath.Join(gradleInitDir, "spindrift-registry-proxy.init.gradle")
	if err := os.WriteFile(gradleInitScript, []byte(bindregistry.GradleInitScript(port)), 0o644); err != nil {
		fmt.Fprintln(stdout, "driver-exec bind-registry: write gradle init script:", err)
		return 1
	}

	// The Go warning lines and both success lines only print here, after
	// every fallible write above (bindings-env-output, cargo home
	// resolve/mkdir, cargo config.toml, gradle init.d resolve/mkdir/init
	// script) has succeeded -- printing any of them earlier (as this used
	// to, issue #2931) would claim an override or a successful binding even
	// when a later write fails and the whole function returns 1, which the
	// caller (agent/entrypoint.sh's phase_registry_proxy_bindings) treats as
	// "nothing applied, skip sourcing entirely". Order matches the old
	// bash's own inline echoes: "Forwarder up" before "go bound".
	for _, w := range goBindings.Warnings {
		fmt.Fprintln(stdout, w)
	}
	fmt.Fprintln(stdout, "==> registry proxy Forwarder up on 127.0.0.1:"+strconv.Itoa(port)+" — cargo bound to it via "+cargoHome+"/config.toml, npm bound to it via npm_config_registry, pnpm bound to it via pnpm_config_registry, yarn berry bound to it via YARN_NPM_REGISTRY_SERVER, and gradle bound to it via "+gradleInitScript)
	fmt.Fprintln(stdout, "==> go bound to it via GOPROXY=http://127.0.0.1:"+strconv.Itoa(port))

	return 0
}

// applyEachRow calls fn for every row in rows, continuing past a per-row
// error so one row's failure (e.g. an unmerged config file) never blocks
// its siblings, and reports whether any row failed.
func applyEachRow(rows []bindregistry.InTreeBinding, fn func(bindregistry.InTreeBinding) error) bool {
	failed := false
	for _, row := range rows {
		if err := fn(row); err != nil {
			failed = true
		}
	}
	return failed
}

// runBindRegistryIntree is in-tree mode (issue #2932): it ports
// entrypoint.sh's deleted phase_cargo_intree_binding_apply/
// cargo_intree_binding_revert into Go, looping over every
// bindregistry.InTreeBindings() row rather than hardcoding cargo, so a
// future table row needs no change here.
func runBindRegistryIntree(stdout io.Writer, action, workDir, socketPath string, port int, probe bindregistry.ProbeFunc, spawn bindregistry.SpawnFunc, timeout, pollInterval time.Duration) int {
	if action == "revert" {
		failed := applyEachRow(bindregistry.InTreeBindings(), func(row bindregistry.InTreeBinding) error {
			reverted, err := bindregistry.RevertInTreeBinding(workDir, row)
			if err != nil {
				fmt.Fprintln(stdout, "driver-exec bind-registry: revert in-tree "+row.ConfigPath+":", err)
				return err
			}
			if reverted {
				fmt.Fprintln(stdout, "==> in-tree "+row.Ecosystem+" config "+row.ConfigPath+" restored and un-hidden from git")
			}
			return nil
		})
		if failed {
			return 1
		}
		return 0
	}

	// action == "apply" past this point (validated by the caller).
	upstreamHost := os.Getenv("REGISTRY_PROXY_UPSTREAM_HOST")
	if upstreamHost == "" {
		// Mirrors bash's `[ -n "${REGISTRY_PROXY_UPSTREAM_HOST:-}" ] || return 0`.
		return 0
	}

	if !isMountedSocket(socketPath) {
		// Mirrors bash's `[ -S "" ]` short-circuit: entrypoint.sh always
		// passes -registry-proxy-socket, even when the registry proxy is
		// disabled (empty env var) -- that must stay a silent no-op, not a
		// validation error.
		return 0
	}

	// Same socat PATH check runBindRegistryBindings does, and for the same
	// reason: it only gates the *spawn* path (EnsureForwarderReady probes
	// first and only calls spawn if nothing is listening yet), so it must
	// run after probe, not before, or an already-ready Forwarder would
	// wrongly warn and skip the rewrite.
	if !probe(port) {
		if _, err := exec.LookPath("socat"); err != nil {
			fmt.Fprintln(stdout, "==> WARNING: "+socketPath+" is mounted but socat is not on PATH — the in-tree registry rewrite is skipped, ecosystems fall back to the public registry")
			return 0
		}
	}

	ready, err := bindregistry.EnsureForwarderReady(socketPath, port, probe, spawn, timeout, pollInterval)
	if err != nil {
		fmt.Fprintln(stdout, "==> WARNING: registry proxy Forwarder failed to start: "+err.Error()+" — the in-tree registry rewrite is skipped, ecosystems fall back to the public registry")
		return 0
	}
	if !ready {
		// AC5's all-or-nothing gate: a Forwarder that never becomes ready
		// must leave every in-tree config file completely untouched -- no
		// partial rewrite, no skip-worktree bit -- rather than pointing a
		// tracked file at a dead port.
		fmt.Fprintln(stdout, "==> WARNING: registry proxy Forwarder did not start listening on 127.0.0.1:"+strconv.Itoa(port)+" within "+timeout.String()+" — the in-tree registry rewrite is skipped, ecosystems fall back to the public registry")
		return 0
	}

	localURL := "http://127.0.0.1:" + strconv.Itoa(port)
	failed := applyEachRow(bindregistry.InTreeBindings(), func(row bindregistry.InTreeBinding) error {
		applied, untracked, err := bindregistry.ApplyInTreeBinding(workDir, row, upstreamHost, localURL)
		if err != nil {
			fmt.Fprintln(stdout, "driver-exec bind-registry: apply in-tree "+row.ConfigPath+":", err)
			return err
		}
		if untracked {
			fmt.Fprintln(stdout, "==> WARNING: "+row.Ecosystem+" config "+row.ConfigPath+" exists but is not tracked by git — skipping the in-tree registry rewrite for it")
		}
		if applied {
			fmt.Fprintln(stdout, "==> in-tree "+row.Ecosystem+" config "+row.ConfigPath+" rewritten to point at the local registry proxy Forwarder (127.0.0.1:"+strconv.Itoa(port)+") and hidden from git via skip-worktree")
		}
		return nil
	})

	if failed {
		return 1
	}
	return 0
}

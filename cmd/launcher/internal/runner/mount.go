package runner

import (
	"fmt"
	"os"

	"spindrift.dev/launcher/internal/agentpaths"
)

// operatorSkillsDir is the staging path SPINDRIFT_SKILLS_DIR mounts onto
// (issue #2489). Mounting over the Driver's own DRIVER_SKILLS_DIR would erase
// the harness-owned skills baked there, so agent/entrypoint.sh instead copies
// both paths into DRIVER_SKILLS_DIR at box startup, which merges.
const operatorSkillsDir = "/operator-skills"

// RegistryProxySocketTarget is the in-box path the registry proxy's unix
// socket mounts onto (ADR 0044, issue #2849); the Forwarder expects it there,
// so it is not configurable. Exported (issue #3141) so dispatch/box.go can
// name this in-box path in REGISTRY_PROXY_MANIFEST, not the host-side source.
const RegistryProxySocketTarget = "/registry-proxy.sock"

// SignalSocketTarget is the in-box path the Signal socket's unix socket
// mounts onto (issue #3725); the listener expects it there, so it is not
// configurable. Mirrors RegistryProxySocketTarget above.
const SignalSocketTarget = "/signal-socket.sock"

// SocketMount names one launcher-owned unix socket buildMountSpecs mounts
// into a Box (issue #3723): Source is the launcher-side host path, Target the
// fixed in-Box path it always lands at. No ReadOnly or Message field: every
// socket mount goes through candidateSocketMount, which always returns a
// writable, silent spec.
type SocketMount struct {
	Source string
	Target string
}

// MountSpec describes one host-to-box mount. Whether a mount applies is
// decided once by buildMountSpecs, independent of runtime backend; each
// adapter only renders a MountSpec into its own flag syntax.
type MountSpec struct {
	Source   string // host path
	Target   string // in-box path
	ReadOnly bool
	// Message is the operator message to print when this mount applies, or
	// empty when the mount is silent. Includes a trailing newline.
	Message string
}

// MountParams is the subset of Config and Driver-declared paths (ADR 0009)
// that buildMountSpecs needs.
type MountParams struct {
	PromptDir             string
	SkillsDir             string
	DriverSessionCacheDir string

	// HostMediatedRemote reports whether this run's CODE_FORGE has no writable
	// remote to push to in-box (ADR 0033: CODE_FORGE=local). It gates the
	// read-only /repo mount and, with OutboxRelayCapable, the /outbox mount.
	HostMediatedRemote bool
	// AccumulationRepoDir is the host path to the bare Accumulation repo
	// (issue #1726), mounted read-only at /repo under HostMediatedRemote.
	AccumulationRepoDir string
	// OutboxRelayCapable reports whether the active CODE_FORGE backend gets the
	// outbox-relay treatment under BoxForgeAndIssueAccess=="read-only"
	// (issue #1918).
	OutboxRelayCapable bool
	// BoxForgeAndIssueAccess is the BOX_FORGE_AND_ISSUE_ACCESS knob value,
	// "read-write" or "read-only".
	BoxForgeAndIssueAccess string
}

func candidateMount(source, target string, readOnly bool) (MountSpec, bool) {
	if source == "" || target == "" {
		return MountSpec{}, false
	}
	info, err := os.Stat(source)
	if err != nil || !info.IsDir() {
		return MountSpec{}, false
	}
	return MountSpec{Source: source, Target: target, ReadOnly: readOnly}, true
}

// candidateSocketMount requires an already-existing socket at source, not a
// directory or regular file, and always returns a writable spec: connecting
// to a unix socket needs write access.
func candidateSocketMount(source, target string) (MountSpec, bool) {
	if source == "" || target == "" {
		return MountSpec{}, false
	}
	info, err := os.Stat(source)
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		return MountSpec{}, false
	}
	return MountSpec{Source: source, Target: target, ReadOnly: false}, true
}

func buildMountSpecs(p MountParams, box Box) []MountSpec {
	var specs []MountSpec

	if spec, ok := candidateMount(p.PromptDir, agentpaths.PromptsDir, true); ok {
		spec.Message = fmt.Sprintf("==> SPINDRIFT_PROMPT_DIR set; mounting %s over the baked prompt\n", spec.Source)
		specs = append(specs, spec)
	}

	if p.DriverSessionCacheDir != "" {
		if spec, ok := candidateMount(box.DriverCacheDir, p.DriverSessionCacheDir, false); ok {
			specs = append(specs, spec)
		}
	}

	if spec, ok := candidateMount(p.SkillsDir, operatorSkillsDir, true); ok {
		spec.Message = fmt.Sprintf("==> SPINDRIFT_SKILLS_DIR set; mounting %s over %s\n", spec.Source, spec.Target)
		specs = append(specs, spec)
	}

	if p.HostMediatedRemote {
		if spec, ok := candidateMount(p.AccumulationRepoDir, "/repo", true); ok {
			specs = append(specs, spec)
		}
	}
	if p.HostMediatedRemote || (p.OutboxRelayCapable && p.BoxForgeAndIssueAccess == "read-only") {
		if spec, ok := candidateMount(box.OutboxDir, "/outbox", false); ok {
			specs = append(specs, spec)
		}
	}

	for _, s := range box.Sockets {
		if spec, ok := candidateSocketMount(s.Source, s.Target); ok {
			specs = append(specs, spec)
		}
	}

	return specs
}

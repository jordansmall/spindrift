package runner

import (
	"fmt"
	"os"
)

// MountSpec describes a single host-to-box mount: what to mount, where, and
// under what read-only policy. The decision of whether a mount applies —
// gate, existence guard, operator message — is computed once by
// buildMountSpecs, independent of runtime backend; each adapter only
// renders a MountSpec into its own flag syntax.
type MountSpec struct {
	Source   string // host path
	Target   string // in-box path
	ReadOnly bool
	// Message is the operator message to print when this mount applies, or
	// empty when the mount is silent. Includes a trailing newline.
	Message string
}

// MountParams is the subset of Config and Driver-declared paths (ADR 0009)
// that buildMountSpecs needs. Both adapters build one from their own fields.
type MountParams struct {
	PromptDir             string
	SkillsDir             string
	DriverSkillsDir       string
	DriverSessionCacheDir string
}

// candidateMount reports whether source should be mounted at target: both
// must be set and source must be a directory that exists.
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

// buildMountSpecs computes the list of host-to-box mounts that apply for p
// and box, independent of runtime backend.
func buildMountSpecs(p MountParams, box Box) []MountSpec {
	var specs []MountSpec

	if spec, ok := candidateMount(p.PromptDir, "/agent/prompts", true); ok {
		spec.Message = fmt.Sprintf("==> SPINDRIFT_PROMPT_DIR set; mounting %s over the baked prompt\n", spec.Source)
		specs = append(specs, spec)
	}

	if p.DriverSessionCacheDir != "" {
		if spec, ok := candidateMount(box.DriverCacheDir, p.DriverSessionCacheDir, false); ok {
			specs = append(specs, spec)
		}
	}

	if spec, ok := candidateMount(p.SkillsDir, p.DriverSkillsDir, true); ok {
		spec.Message = fmt.Sprintf("==> SPINDRIFT_SKILLS_DIR set; mounting %s over %s\n", spec.Source, spec.Target)
		specs = append(specs, spec)
	}

	return specs
}

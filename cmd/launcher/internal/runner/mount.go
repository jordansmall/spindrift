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

	// CodeForge is the CODE_FORGE knob value; the Accumulation-repo and
	// outbox mounts below apply only when it is "local" (ADR 0033).
	CodeForge string
	// AccumulationRepoDir is the host path to the bare Accumulation repo
	// (.spindrift/accum.git by default, issue #1726) mounted read-only at
	// /repo under CODE_FORGE=local.
	AccumulationRepoDir string

	// IssueTracker and LocalIssuesDir gate the read-only /issues mount
	// (ADR 0032): only ISSUE_TRACKER=local reads its issues from the Box, so
	// only that tracker gets the mount, and only when LocalIssuesDir resolves
	// to a real directory.
	IssueTracker   string
	LocalIssuesDir string
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

	if p.CodeForge == "local" {
		if spec, ok := candidateMount(p.AccumulationRepoDir, "/repo", true); ok {
			specs = append(specs, spec)
		}
		if spec, ok := candidateMount(box.OutboxDir, "/outbox", false); ok {
			specs = append(specs, spec)
		}
	}

	// The local issue tracker has no in-box reachability (ADR 0032): its
	// content plane is host-mediated via a read-only mount of the issues dir
	// at the fixed top-level target /issues, silent like the driver-cache
	// mount (this is the tracker's normal read path, not an operator
	// override). A missing dir or non-local tracker yields no mount.
	if p.IssueTracker == "local" {
		if spec, ok := candidateMount(p.LocalIssuesDir, "/issues", true); ok {
			specs = append(specs, spec)
		}
	}

	return specs
}

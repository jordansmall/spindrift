package runner

import (
	"fmt"
	"os"

	"spindrift.dev/launcher/internal/agentpaths"
)

// operatorSkillsDir is the fixed in-box path SPINDRIFT_SKILLS_DIR mounts
// onto (issue #2489) — NOT the Driver's actual skills dir (DRIVER_SKILLS_DIR)
// directly, since a mount placed there would replace its whole contents and
// erase the harness-owned skill(s) baked alongside it. agent/entrypoint.sh's
// phase_prompt_assembly copies both this staging path and the baked skills
// into the real DRIVER_SKILLS_DIR at box startup instead, which merges
// rather than replaces.
const operatorSkillsDir = "/operator-skills"

// issueSnapshotTarget is the fixed in-box path Box.IssueSnapshotPath mounts
// onto (issue #2547) -- the frozen issue-read snapshot every implement/
// review issue-read fragment reads instead of a live tracker call.
const issueSnapshotTarget = "/issue-snapshot.md"

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
	DriverSessionCacheDir string

	// HostMediatedRemote reports whether this run's CODE_FORGE has no
	// writable remote to push to in-box at all (ADR 0033: CODE_FORGE=local)
	// -- gates the read-only Accumulation-repo mount at /repo, and
	// (alongside OutboxRelayCapable) the writable /outbox mount.
	HostMediatedRemote bool
	// AccumulationRepoDir is the host path to the bare Accumulation repo
	// (.spindrift/accum.git by default, issue #1726) mounted read-only at
	// /repo under HostMediatedRemote.
	AccumulationRepoDir string
	// OutboxRelayCapable reports whether the active CODE_FORGE backend gets
	// the outbox-relay treatment under BoxForgeAndIssueAccess=="read-only"
	// (issue #1918) -- combined with BoxForgeAndIssueAccess to gate the
	// /outbox mount alongside HostMediatedRemote.
	OutboxRelayCapable bool
	// BoxForgeAndIssueAccess is the BOX_FORGE_AND_ISSUE_ACCESS knob value
	// ("read-write" or "read-only") -- see OutboxRelayCapable's doc comment.
	BoxForgeAndIssueAccess string

	// HostMediatedIssueTracker reports whether ISSUE_TRACKER has no in-box
	// reachability at all (ADR 0032: ISSUE_TRACKER=local), gating the
	// read-only /issues mount.
	HostMediatedIssueTracker bool
	LocalIssuesDir           string
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

// candidateFileMount reports whether source should be mounted at target:
// both must be set and source must be a regular file that exists -- the
// single-file counterpart to candidateMount, which requires a directory.
func candidateFileMount(source, target string, readOnly bool) (MountSpec, bool) {
	if source == "" || target == "" {
		return MountSpec{}, false
	}
	info, err := os.Stat(source)
	if err != nil || info.IsDir() {
		return MountSpec{}, false
	}
	return MountSpec{Source: source, Target: target, ReadOnly: readOnly}, true
}

// buildMountSpecs computes the list of host-to-box mounts that apply for p
// and box, independent of runtime backend. Every mount here is optional and
// silent-when-absent except the issue-snapshot mount below: once
// box.IssueSnapshotPath is non-empty, mounting it is required, so a stat
// failure on it returns a descriptive error instead of silently dropping
// the mount (issue #2547 review finding).
func buildMountSpecs(p MountParams, box Box) ([]MountSpec, error) {
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

	// The local issue tracker has no in-box reachability (ADR 0032): its
	// content plane is host-mediated via a read-only mount of the issues dir
	// at the fixed top-level target /issues, silent like the driver-cache
	// mount (this is the tracker's normal read path, not an operator
	// override). A missing dir or non-local tracker yields no mount.
	if p.HostMediatedIssueTracker {
		if spec, ok := candidateMount(p.LocalIssuesDir, "/issues", true); ok {
			specs = append(specs, spec)
		}
	}

	// box.IssueSnapshotPath is the frozen issue-read snapshot file written
	// once at Run's box start (issue #2547), silent like the /issues mount
	// (this is a normal read path, not an operator override). An empty
	// path -- research dispatches, and every pre-#2547 Box construction --
	// yields no mount, same as candidateFileMount's own contract. But once
	// box.Run's writeIssueSnapshot step has set a non-empty path, that file
	// is the box's sole source of issue text for implement/review/fix
	// passes (the issue-read prompt fragments no longer do a live tracker
	// read) -- candidateFileMount's fail-open behavior would otherwise leave
	// the box silently starting with no issue text at all if the source
	// were missing, unreadable, or (mis-)pointed at a directory. So unlike
	// every mount above, a non-empty IssueSnapshotPath is resolved with a
	// single os.Stat here rather than candidateFileMount's own
	// stat-then-maybe-restat shape: a stat failure, or a directory in
	// place of a regular file, is a hard error, not a silently dropped
	// mount.
	if box.IssueSnapshotPath != "" {
		info, err := os.Stat(box.IssueSnapshotPath)
		if err != nil {
			return nil, fmt.Errorf("issue snapshot %q: %w", box.IssueSnapshotPath, err)
		}
		if info.IsDir() {
			return nil, fmt.Errorf("issue snapshot %q: is a directory, want a regular file", box.IssueSnapshotPath)
		}
		specs = append(specs, MountSpec{Source: box.IssueSnapshotPath, Target: issueSnapshotTarget, ReadOnly: true})
	}

	return specs, nil
}

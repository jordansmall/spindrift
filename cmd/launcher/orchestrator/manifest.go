package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/driver/driverkit"
)

// ManifestSlice is one implementor slice a coordinator pass has declared for
// parallel dispatch (issue #2059): its name, the scoped task description
// seedWorkerPrompt (workers.go) hands the dispatched worker as the actual
// work it's delegated -- required, not omitempty like FileLeases/DependsOn
// below, since a slice with no task description leaves the worker with
// nothing to implement (issue #2059 code-review finding) -- the files it
// declares it will touch, and the names of other slices in the same
// manifest it depends on. Both FileLeases and DependsOn are read and
// enforced by scheduleSlices (schedule.go, issue #2060), which partitions a
// manifest's slices into concurrency-safe batches from them: disjoint
// FileLeases (and no DependsOn edge) let slices share a batch and run
// concurrently, an empty FileLeases sequences a slice into its own solo
// batch, and DependsOn puts a floor under a slice's earliest eligible batch
// -- not just carried through inertly for a later pass to use.
type ManifestSlice struct {
	Name       string   `json:"name"`
	Task       string   `json:"task"`
	FileLeases []string `json:"file_leases,omitempty"`
	DependsOn  []string `json:"depends_on,omitempty"`
}

// SliceManifest is what a coordinator pass emits instead of blocking on
// parallel work itself (issue #2059): the coordinator's only parallel job is
// to declare this and end its turn — the Go orchestrator (not this file)
// reads it and drives the actual fan-out/join.
type SliceManifest struct {
	Slices []ManifestSlice `json:"slices"`
}

// ManifestToken is the exact SPINDRIFT_SLICE_MANIFEST marker literal a
// coordinator pass emits, mirroring VerdictApprove/VerdictBlock's role as a
// load-bearing literal scanForManifest greps for.
const ManifestToken = "SPINDRIFT_SLICE_MANIFEST"

// validSliceNameRe is the sole allowed shape for a ManifestSlice.Name: 1-64
// chars of letters, digits, underscore, and hyphen only. A manifest is
// model-driven, untrusted-ish input (issue #2059), and slice.Name flows
// into two places downstream that this charset/length check forecloses in
// one move:
//
//   - launchOneWorker (workers.go) joins slice.Name straight into
//     opts.WorkDir to build each worker's sentinel/result/log/heartbeat
//     paths. Excluding "/" and "." rules out path traversal escaping
//     workDir, and rejecting duplicate names (checked alongside this regex
//     in ParseManifestLine) rules out two workers racing on the same
//     sentinel/result files -- both would otherwise break the join's own
//     "not inferred" completion guarantee.
//   - dispatchManifestIfPresent (dispatch.go) writes slice.Name verbatim
//     into state.WorkerFindings, which run.go's seedPromptFromState later
//     injects into the next coordinator pass's seeded prompt. Excluding
//     newlines/whitespace rules out a name forging extra
//     "- other-slice: done" lines into that findings block.
var validSliceNameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// toolResultEchoRe matches a rendered tool_result echo line -- the
// "[role]   -> <summarizeResult output>" shape RenderTranscriptWithRole
// (transcript_render.go) emits for a "user" tool_result event, three spaces
// and an arrow after the closing bracket, as opposed to the single-space
// "[role] <text>" shape an assistant-authored text/tool_use line has.
// scanForManifest must never honor a SPINDRIFT_SLICE_MANIFEST token found on
// this line shape: a tool_result echoes back whatever the underlying tool
// read or ran verbatim (e.g. a Read of the issue body, a Bash command's
// stdout), and the issue body plus every comment from any GitHub user is
// untrusted input once an issue is labeled for dispatch -- only the label
// gates dispatch, not the content that follows. Excluding this shape is one
// of two checks scanForManifest applies before honoring a line -- see
// renderedRolePrefixRe's doc comment for the other (issue #2059 code-review
// finding).
var toolResultEchoRe = regexp.MustCompile(`^\[[^\]]*\]   -> `)

// isToolResultEchoLine reports whether line (already trimmed) is a rendered
// tool_result echo rather than an assistant-authored line -- see
// toolResultEchoRe's doc comment for why scanForManifest must skip these.
func isToolResultEchoLine(line string) bool {
	return toolResultEchoRe.MatchString(line)
}

// renderedRolePrefixRe extracts the bracketed role name RenderTranscriptWithRole
// (transcript_render.go) leads a rendered line with -- "[role] text" for an
// assistant-authored text/tool_use event, "[role]   -> summary" for a
// tool_result echo. Capture group 1 is the role name.
//
// A line with no match at all is a bare physical-line continuation of a
// prior multi-line rendered entry: RenderTranscriptWithRole appends
// "["+role+"] "+text as ONE entry in its own lines slice, but text itself is
// only strings.TrimSpace-trimmed at its outer ends, so an embedded "\n"
// inside it survives into that entry verbatim. scanForManifest's own
// bufio.Scanner then splits that "\n"-joined rendered transcript back into
// separate physical lines -- so line 2+ of a multi-line assistant text block
// carries no "[role] " prefix at all, and must never be honored (issue
// #2059 code-review finding: a manifest token sitting in the middle of the
// coordinator's own multi-line text, e.g. relayed/quoted issue-comment
// content, must not be mistaken for a dispatch instruction).
var renderedRolePrefixRe = regexp.MustCompile(`^\[([^\]]*)\]`)

// maxManifestSlices bounds how many slices a single manifest may declare.
// LaunchWorkers (workers.go) fans out one concurrent driver-exec/claude
// process plus one `git worktree add` per slice with nothing else capping
// len(manifest.Slices) -- a model-driven, untrusted-ish manifest (see
// validSliceNameRe's doc comment) declaring hundreds of slices would fork
// that many concurrent processes and worktrees (issue #2059 code-review
// finding). 8 is a generous bound on how many implementor seams a single
// coordinator pass can meaningfully carve out of one issue while still
// capping concurrent process/worktree fan-out to something a single Box's
// CPU/memory/disk can sanely host.
const maxManifestSlices = 8

// validSliceName reports whether name is safe to use as a filesystem
// path component and as a single line of plain text (see
// validSliceNameRe's doc comment for why).
func validSliceName(name string) bool {
	return validSliceNameRe.MatchString(name)
}

// Line encodes m as a single marker line: the token followed by its JSON
// form, base64-standard-encoded, ready to print into a pass's own output.
// Returns an error only if m fails to JSON-marshal (its fields are plain
// strings/slices, so this can't practically happen).
func (m SliceManifest) Line() (string, error) {
	data, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return ManifestToken + " " + base64.StdEncoding.EncodeToString(data) + "\n", nil
}

// ParseManifestLine decodes a single marker line produced by Line — the
// token, found anywhere in line (not only as a line prefix, matching
// scanPassLog's own tolerance for RenderTranscript's "[role] " prefix),
// followed by its base64 JSON payload. Returns (SliceManifest{}, false) when
// the token is absent, the payload fails to decode, the JSON fails to
// unmarshal, the decoded manifest has zero slices (an empty manifest is
// never a meaningful dispatch instruction), the manifest declares more than
// maxManifestSlices slices (unbounded fan-out of concurrent worker
// processes/worktrees, issue #2059 code-review finding), any slice name
// fails validSliceName, any slice's Task is empty/whitespace-only (a slice
// with no task description leaves the dispatched worker with nothing to
// implement, issue #2059 code-review finding), or two slices share the same
// name -- a malformed manifest is simply not a valid dispatch instruction,
// fail-closed the same way as the empty-manifest case (issue #2059).
func ParseManifestLine(line string) (SliceManifest, bool) {
	idx := strings.Index(line, ManifestToken)
	if idx == -1 {
		return SliceManifest{}, false
	}
	rest := line[idx+len(ManifestToken):]
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return SliceManifest{}, false
	}

	data, err := base64.StdEncoding.Strict().DecodeString(fields[0])
	if err != nil {
		return SliceManifest{}, false
	}

	var m SliceManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return SliceManifest{}, false
	}
	if len(m.Slices) == 0 {
		return SliceManifest{}, false
	}
	if len(m.Slices) > maxManifestSlices {
		return SliceManifest{}, false
	}

	seen := make(map[string]bool, len(m.Slices))
	for _, s := range m.Slices {
		if !validSliceName(s.Name) {
			return SliceManifest{}, false
		}
		if strings.TrimSpace(s.Task) == "" {
			return SliceManifest{}, false
		}
		if seen[s.Name] {
			return SliceManifest{}, false
		}
		seen[s.Name] = true
	}

	return m, true
}

// scanForManifest scans one coordinator pass's rendered log for the last
// SPINDRIFT_SLICE_MANIFEST line, mirroring scanPassLog's own
// RenderTranscript + substring-anywhere-match approach (run.go:506-530).
// driverName selects the RenderTranscript strategy, same as scanPassLog's
// own parameter.
//
// A manifest token is honored ONLY on a line that (1) carries a "[role] "
// prefix at all (renderedRolePrefixRe; see its doc comment for why a bare
// continuation line never matches), (2) that role is exactly
// driverkit.ImplementorRole -- the top-level coordinator itself, never a
// subagent's own rendered role (e.g. a scout relaying issue/comment text
// verbatim in its own report) -- and (3) the line is not a tool_result echo
// (isToolResultEchoLine; a tool_result echoes back whatever the underlying
// tool read or ran verbatim, and the issue body plus every comment from any
// GitHub user is untrusted input once an issue is labeled for dispatch --
// only the label gates dispatch, not the content that follows). Together
// these three checks mean a manifest is only ever honored when the
// coordinator model itself authored the line, on a single physical line of
// its own (issue #2059 code-review finding). Returns (SliceManifest{},
// false) when no valid manifest line is found, including when
// RenderTranscript itself fails (a scan failure is not a hard error here —
// same convention as scanPassLog).
func scanForManifest(logPath, driverName string) (SliceManifest, bool) {
	d, err := driver.New(driverName)
	if err != nil {
		fmt.Fprintln(os.Stderr, "orchestrator: scan slice manifest:", err)
		return SliceManifest{}, false
	}
	rendered, err := d.RenderTranscript(logPath, driverkit.RenderOptions{TopLevelRole: driverkit.ImplementorRole})
	if err != nil {
		fmt.Fprintln(os.Stderr, "orchestrator: scan slice manifest:", err)
		return SliceManifest{}, false
	}

	var manifest SliceManifest
	found := false
	sc := bufio.NewScanner(strings.NewReader(rendered))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if isToolResultEchoLine(line) {
			continue
		}
		match := renderedRolePrefixRe.FindStringSubmatch(line)
		if match == nil || match[1] != driverkit.ImplementorRole {
			// No "[role] " prefix at all (a bare continuation line of a
			// multi-line rendered entry) or a role other than the top-level
			// coordinator's own -- never honored (issue #2059 code-review
			// finding: trust-boundary gap).
			continue
		}
		if m, ok := ParseManifestLine(line); ok {
			manifest = m
			found = true
		}
	}
	return manifest, found
}

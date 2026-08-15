package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/driver/driverkit"
)

// ManifestSlice is one implementor slice a coordinator pass has declared for
// parallel dispatch (issue #2059): its name, the files it declares it will
// touch (for a future scheduler to avoid overlapping leases — not enforced
// by this slice), and the names of other slices in the same manifest it
// depends on (also not enforced yet — declared, carried through, and
// available for a later scheduling pass to use).
type ManifestSlice struct {
	Name       string   `json:"name"`
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
// unmarshal, or the decoded manifest has zero slices (an empty manifest is
// never a meaningful dispatch instruction).
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
	return m, true
}

// scanForManifest scans one coordinator pass's rendered log for the last
// SPINDRIFT_SLICE_MANIFEST line, mirroring scanPassLog's own
// RenderTranscript + substring-anywhere-match approach (run.go:506-530).
// driverName selects the RenderTranscript strategy, same as scanPassLog's
// own parameter. Returns (SliceManifest{}, false) when no valid manifest
// line is found, including when RenderTranscript itself fails (a scan
// failure is not a hard error here — same convention as scanPassLog).
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
		if m, ok := ParseManifestLine(line); ok {
			manifest = m
			found = true
		}
	}
	return manifest, found
}

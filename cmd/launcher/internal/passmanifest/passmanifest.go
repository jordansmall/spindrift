// Package passmanifest holds the pass-manifest file contract (issue #2983):
// the orchestrator writes it from inside the Go harness running a box, and the
// host-side dispatch package parses it back afterward. It is a separate package
// because orchestrator is package main and cannot be imported.
package passmanifest

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"spindrift.dev/launcher/internal/landdelta"
	"spindrift.dev/launcher/internal/passmachine"
	"spindrift.dev/launcher/internal/usage"
)

// FileName is the manifest's fixed name within the outbox mount, shared by
// every Go call site that must agree on the path (issue #2983).
const FileName = "manifest.json"

// maxManifestBytes bounds how much of a manifest file Read buffers into memory
// (issue #2983 DoS finding). A sandboxed, potentially prompt-injected Box
// writes manifest.json through a 0o777 outbox mount. A file past this cap is
// corrupt or hostile evidence, not a legitimate manifest.
const maxManifestBytes = 4 * 1024 * 1024

// Entry is one pass's summary (issue #2983). It is Box-authored evidence: the
// launcher computes Resolved outcome tier selection independently, and only
// UnreviewedReady's settle/recover backstop consults it. That guards model
// mistakes, not an adversarial Box, which could forge entries.
type Entry struct {
	Pass         int         `json:"pass"`
	Kind         string      `json:"kind"`
	Verdict      string      `json:"verdict,omitempty"`
	OutcomeFound bool        `json:"outcome_found"`
	Usage        usage.Usage `json:"usage"`
	// LandDelta is the terminal land pass's post-approval tree delta (issue
	// #3244), relative to the tree the reviewer APPROVEd. Nil on every
	// non-land entry, always non-nil on a land entry: an unknown delta
	// (Known: false) still counts as a value and is never dropped.
	LandDelta *landdelta.Delta `json:"land_delta,omitempty"`
}

// Write encodes manifest as a JSON array and overwrites path, best-effort: it
// logs a failure to stderr and otherwise ignores it, since the manifest is
// advisory evidence, never a gate on the pass's decision. An empty path means
// no outbox is wired for this run and is a silent no-op. Write renames a temp
// file into place, so a concurrent Read never sees a torn file (issue #2983).
func Write(path string, manifest []Entry) {
	if path == "" {
		return
	}
	b, err := json.Marshal(manifest)
	if err != nil {
		fmt.Fprintln(os.Stderr, "passmanifest: encode pass manifest:", err)
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".pass-manifest-*.json.tmp")
	if err != nil {
		fmt.Fprintln(os.Stderr, "passmanifest: create temp pass manifest:", err)
		return
	}
	defer os.Remove(tmp.Name())

	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		fmt.Fprintln(os.Stderr, "passmanifest: chmod temp pass manifest:", err)
		return
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		fmt.Fprintln(os.Stderr, "passmanifest: write temp pass manifest:", err)
		return
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		fmt.Fprintln(os.Stderr, "passmanifest: sync temp pass manifest:", err)
		return
	}
	if err := tmp.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "passmanifest: close temp pass manifest:", err)
		return
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		fmt.Fprintln(os.Stderr, "passmanifest: rename temp pass manifest:", err)
	}
}

// Read parses the manifest file at path. A missing file returns (nil, nil);
// one that exists but fails to parse or is not a regular file returns an
// error for the caller to log and proceed anyway (issue #2983 AC2).
func Read(path string) ([]Entry, error) {
	// path is Box-authored: Stat the descriptor, not the path, to avoid a
	// TOCTOU swap. O_NOFOLLOW refuses a symlink to a host file; O_NONBLOCK
	// keeps a FIFO from wedging settle.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("pass manifest %s is not a regular file (mode %s)", path, info.Mode())
	}

	b, err := io.ReadAll(io.LimitReader(f, maxManifestBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxManifestBytes {
		return nil, fmt.Errorf("pass manifest exceeds %d bytes", maxManifestBytes)
	}
	var entries []Entry
	if err := json.Unmarshal(b, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// UnreviewedReady reports whether a run that reported status=ready got that
// ready from a review-loop pass other than its land pass (issue #5973). The
// caller has already established the resolved self-report is ready, so a land
// outcome of any status settles it: a blocked land would have made the
// self-report blocked. Let L be the last implement, fix or land entry. No L
// (legacy loop, advise-only kinds, older manifests) fails open; implement or
// fix at L means the loop stopped before land (true). Land at L counts as
// reviewed only if an outcome appears at or after it (the land pass, or a
// legacy nudge resume or host fix pass); otherwise land crashed and the ready
// is stale.
func UnreviewedReady(entries []Entry) bool {
	last := lastWork(entries)
	if last < 0 {
		return false
	}
	if !isLand(entries[last]) {
		return true
	}
	for _, en := range entries[last:] {
		if en.OutcomeFound {
			return false
		}
	}
	return true
}

// StoppedBeforeLand reports whether the last implement, fix or land entry is
// implement or fix rather than land (issue #5984). No such entry fails open.
func StoppedBeforeLand(entries []Entry) bool {
	last := lastWork(entries)
	return last >= 0 && !isLand(entries[last])
}

func isLand(e Entry) bool { return e.Kind == passmachine.KindLand.ManifestKind() }

// lastWork returns the index of the last implement, fix or land entry, or -1.
func lastWork(entries []Entry) int {
	work := map[string]bool{
		passmachine.KindImplement.ManifestKind(): true,
		passmachine.KindFix.ManifestKind():       true,
		passmachine.KindLand.ManifestKind():      true,
	}
	last := -1
	for i, en := range entries {
		if work[en.Kind] {
			last = i
		}
	}
	return last
}

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
	"spindrift.dev/launcher/internal/usage"
)

// FileName is the manifest's fixed name within the outbox mount, shared by
// every Go call site that must agree on the path (issue #2983).
const FileName = "manifest.json"

// maxManifestBytes bounds how much of a manifest file Read buffers into memory
// (issue #2983 DoS finding). A sandboxed, potentially prompt-injected Box
// writes manifest.json through a 0o777 outbox mount, and Read runs on every
// console refresh tick. A file past this cap is corrupt or hostile evidence,
// not a legitimate manifest.
const maxManifestBytes = 4 * 1024 * 1024

// Entry is one pass's advisory summary (issue #2983). It is Box-authored
// evidence only: the launcher computes Resolved outcome tier selection and
// every settle decision independently and never consults it.
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

// Read parses the manifest file at path, distinguishing "no manifest ever
// written" from "a manifest was written but is corrupt" so the caller can log
// the latter (issue #2983 AC2). A missing file returns (nil, nil), same as an
// empty one; a file that exists but fails to parse, including one that is not
// a regular file, returns (nil, err) for the caller to log and proceed anyway.
func Read(path string) ([]Entry, error) {
	// path is Box-authored, so an Lstat before the open leaves a TOCTOU window
	// for the Box to swap the object at path. O_NOFOLLOW fails the open on a
	// symlink rather than redirecting the read to a host file; O_NONBLOCK
	// returns immediately on a FIFO instead of freezing the console or wedging
	// settle. Stat on the descriptor reports the object that was opened.
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

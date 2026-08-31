// Package passmanifest holds the pass-manifest file contract (issue #2983):
// the orchestrator writes it from inside the Go harness running a box, and
// the host-side dispatch package parses it back afterward. It lives in its
// own package, rather than folded into outcome or dispatch, because it's a
// seam genuinely shared by both sides of the box boundary -- orchestrator is
// package main and can't be imported, so the type and its (de)serialization
// live here instead.
package passmanifest

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"spindrift.dev/launcher/internal/usage"
)

// FileName is the manifest's fixed name within the outbox mount -- a single
// well-known name, shared by every Go call site that must agree on the path
// (issue #2983's console and dispatch/retry callers), mirroring
// seambundle.FileName's own idiom for a shared filename constant.
const FileName = "manifest.json"

// maxManifestBytes bounds how much of a manifest file Read will buffer into
// memory (issue #2983 DoS finding). manifest.json is Box-authored: written
// from inside a sandboxed, potentially prompt-injected or runaway Box that
// has shell access and a 0o777 outbox mount, and Read runs on every console
// refresh tick (per running pick) as well as once per dispatch result on the
// host. The manifest is documented as a handful of small JSON entries, so 4
// MiB is generous headroom, not a real-world ceiling -- a file past this
// cap is corrupt/suspicious evidence, not a legitimate manifest.
const maxManifestBytes = 4 * 1024 * 1024

// Entry is one pass's own advisory summary (issue #2983): pass identity the
// host currently has to re-derive by heuristically parsing spindrift_op
// heartbeat lines out of the raw Driver stream. It is Box-authored advisory
// evidence only -- Resolved outcome tier selection and every settle decision
// are computed independently and never consult it (the Box advises, the
// Launcher decides).
type Entry struct {
	Pass         int         `json:"pass"`
	Kind         string      `json:"kind"`
	Verdict      string      `json:"verdict,omitempty"`
	OutcomeFound bool        `json:"outcome_found"`
	Usage        usage.Usage `json:"usage"`
}

// Write overwrites path with manifest JSON-array-encoded, best-effort: a
// write failure is logged to stderr and otherwise ignored -- the manifest is
// optional advisory evidence, never a gate on the pass's own decision,
// mirroring runstate.WriteRunState's own caller's degrade-not-error
// contract. Empty path means no manifest artifact is wired for this run
// (e.g. no outbox mounted) and is a silent no-op -- callers must check this
// themselves before constructing anything expensive, though today building
// one Entry is cheap enough not to matter.
//
// It writes to a temp file in the same directory as path and renames it into
// place, rather than truncating path directly, mirroring
// runstate.WriteRunState's own temp-file-then-rename pattern (issue #2983
// review finding): the console's RunningPassState polls Read on every
// tea.Msg concurrently with the orchestrator calling Write on each pass, and
// an in-place truncate-then-write leaves a window where a concurrent Read
// sees a torn, malformed file and fails with a spurious parse error --
// contradicting pick.go:200's documented convention that a Read failure here
// leaves prior state stale, not cleared. os.Rename on the same filesystem is
// atomic, so a concurrent reader always sees either the old complete file or
// the new one, never a torn intermediate.
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
// written" from "a manifest was written but is corrupt" so the caller can
// log the latter -- but both cases must degrade to the same pass-blind
// functional behavior from the CALLER's perspective (issue #2983 AC2): a
// missing file returns (nil, nil), same as an empty one, while a file that
// exists but fails to parse -- including a path that exists but is not a
// regular file (a FIFO, symlink, device, socket, or directory) -- returns
// (nil, err) for the caller to log before proceeding exactly as if Passes
// were nil.
func Read(path string) ([]Entry, error) {
	// A single OpenFile with O_NOFOLLOW|O_NONBLOCK, then Stat on the
	// resulting descriptor, replaces a separate pre-open Lstat check: path
	// is Box-authored (a sandboxed, potentially prompt-injected or runaway
	// Box with shell access to a 0o777 outbox mount), so a check-then-open
	// with two syscalls leaves a TOCTOU window where the Box swaps the
	// object at path between the check and the open. O_NOFOLLOW makes the
	// open itself fail (ELOOP) on a symlink at the final path component --
	// no window where an approved-then-swapped symlink redirects the read
	// to an arbitrary host file. O_NONBLOCK makes the open itself return
	// immediately on a FIFO instead of blocking forever with no writer on
	// the other end -- freezing the console (refreshPickDecorations runs
	// Read on every tea.Msg) or wedging settle (outcomeResult runs it once
	// per dispatch result, after the Box has already exited). Stat on the
	// open descriptor then reports the mode of the exact object that was
	// opened, not of whatever happened to be at path moments earlier.
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

// Package inputdoc reads and resolves the nix-rendered Launcher input
// document (ADR 0020): loading it, resolving a knob against it with the
// ambient-override provenance warning, and parsing integer/duration knob
// values with their lower bounds. The Launcher and the Daemon each wrap this
// package in their own adapter, since each has its own extra document
// sections and lookup helpers (issue #3619).
package inputdoc

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"
)

// Document mirrors the nix-rendered Launcher input document (ADR 0020):
// Settings holds resolved knob values keyed by env var name, Artifacts the
// nix-computed plumbing (image refs, agent files, driver name). It is
// hand-written, not nix-generated (issue #813), and the mkharness-defaults
// drift gate in nix/checks/equivalence.nix greps hand-picked keys only.
type Document struct {
	Settings  map[string]string `json:"settings"`
	Artifacts map[string]string `json:"artifacts"`
}

// Load reads and parses the Launcher input document at path.
func Load(path string) (*Document, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read input document %s: %w", path, err)
	}
	var doc Document
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse input document %s: %w", path, err)
	}
	return &doc, nil
}

// Lookup resolves one schema knob: ambient env first, then the document's
// settings (keyed by env var name — lib/mkHarness.nix's documentSettings),
// then nothing (found=false). When the document also carries a value and the
// ambient env wins anyway, it prints a provenance warning to stderr —
// otherwise nothing records which value actually drove the run, and a stale
// exported override silently wins (ADR 0020). d may be nil (no --input
// document loaded).
func (d *Document) Lookup(envVar string, stderr io.Writer) (string, bool) {
	if v := os.Getenv(envVar); v != "" {
		if d != nil {
			if docVal, ok := d.Settings[envVar]; ok && docVal != "" {
				fmt.Fprintf(stderr, "%s=%s set in environment — knob env overrides are deprecated; use the --input document's settings.%s\n", envVar, v, envVar)
			}
		}
		return v, true
	}
	if d != nil {
		if v, ok := d.Settings[envVar]; ok && v != "" {
			return v, true
		}
	}
	return "", false
}

// Resolve wraps Lookup for knobs that must have a value: the default lives
// in the schema and travels in the document, so a knob absent from both is a
// configuration error, not a silent default.
func (d *Document) Resolve(envVar string, stderr io.Writer) (string, error) {
	if v, ok := d.Lookup(envVar, stderr); ok {
		return v, nil
	}
	return "", fmt.Errorf("no value for %s (not in environment or --input document settings)", envVar)
}

// ResolveOptional wraps Lookup for knobs whose schema default is itself the
// empty string — absent-from-both is the normal case, not a configuration
// error — while still keeping Lookup's ambient-env-wins provenance warning.
func (d *Document) ResolveOptional(envVar string, stderr io.Writer) string {
	v, _ := d.Lookup(envVar, stderr)
	return v
}

// ParseInt parses raw as a base-10 integer no smaller than min, both
// callers' shared shape: Resolve can hand back an ambient env override of
// anything, so a malformed or out-of-range knob must fail startup here with
// a clear diagnostic rather than reaching the caller's own halt, which is
// meant for a genuine programming error, not an operator's mistyped env var.
// label names the constraint in the error text (e.g. "positive integer"); it
// must read correctly next to both "got %q" (unparsable) and "got %d" (out
// of range).
func ParseInt(name, label, raw string, min int) (int, error) {
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be a %s, got %q", name, label, raw)
	}
	if n < min {
		return 0, fmt.Errorf("%s must be a %s, got %d", name, label, n)
	}
	return n, nil
}

// ParseDuration parses raw with time.ParseDuration, then rejects a result
// below min, mirroring ParseInt's shape (same "got %q"/"got %v" pair, %v
// rather than %d so the out-of-range diagnostic prints a readable duration
// like "500ms" instead of a raw nanosecond count).
func ParseDuration(name, label, raw string, min time.Duration) (time.Duration, error) {
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be a %s, got %q", name, label, raw)
	}
	if d < min {
		return 0, fmt.Errorf("%s must be a %s, got %v", name, label, d)
	}
	return d, nil
}

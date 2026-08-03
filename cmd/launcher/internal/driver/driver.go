// Package driver is the host-side half of the Driver seam (ADR 0009): the
// strategy through which the launcher varies its transient classification,
// heartbeat parsing, and usage extraction by agent CLI. Each Driver's own
// behaviour lives in its sibling subpackage (e.g. driver/claude); this
// package owns only the interface, the shared classification vocabulary, and
// the registry wiring. The in-box half (invocation, agent-config rendering,
// skill wiring, outcome extraction) is nix-generated and lives in
// lib/drivers/; the two registries are kept from drifting by a parity test.
package driver

import (
	"fmt"
	"io"
	"sort"

	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/usage"
)

// Driver is the seam through which the launcher varies its transient
// classification, heartbeat parsing, and usage extraction by agent CLI,
// selected at runtime by the DRIVER value (defaulting to "claude").
type Driver interface {
	// Name returns the Driver identifier, matching a key in the nix
	// lib/drivers/ registry.
	Name() string

	// ClassifyTransient scans the box log at logPath and reports whether a
	// non-zero exit is a retryable infrastructure failure or a genuine task
	// failure, in this Driver's own error taxonomy.
	ClassifyTransient(logPath string) (Classification, error)

	// NewHeartbeatWriter wraps raw (the log file) with a writer that emits
	// coarse status lines to out at natural event boundaries in this
	// Driver's own transcript format, forwarding all bytes to raw unchanged.
	// opts.TopLevelRole is the role attributed to top-level (empty
	// parent_tool_use_id) messages; an empty value means the implementor's
	// own default (issue #2092).
	NewHeartbeatWriter(raw io.Writer, issue string, out io.Writer, opts driverkit.RenderOptions) io.Writer

	// ExtractUsage scans the box log at logPath and returns its aggregate and
	// per-model usage in one report, in this Driver's own log format.
	ExtractUsage(logPath string) (usage.Report, error)

	// RenderTranscript scans the box log at logPath and returns a readable
	// rendering of its assistant turns and tool calls, in this Driver's own
	// transcript format — a Console drill-in's rendered view (#648).
	// opts.TopLevelRole is the role attributed to top-level (empty
	// parent_tool_use_id) messages; an empty value means the implementor's
	// own default (issue #2092).
	RenderTranscript(logPath string, opts driverkit.RenderOptions) (string, error)

	// ResolveExit returns the exit code the launcher should treat as
	// authoritative for a completed run. exitCode is the agent process's own
	// exit code; logPath is the box log. A Driver whose own exit code is
	// already trustworthy (e.g. claude) returns exitCode unchanged; a Driver
	// whose own exit code is not (e.g. opencode, which exits 0 even on a
	// mid-run error) derives a replacement from the log instead (issue
	// #2263).
	ResolveExit(logPath string, exitCode int) (int, error)
}

// registry maps a Driver name to its strategy. Populated by each driver's
// init().
var registry = map[string]Driver{}

// register adds a Driver strategy to the registry. Called from each driver
// file's init(); panics on a duplicate name since that is a programming
// error, never a runtime condition.
func register(d Driver) {
	name := d.Name()
	if _, exists := registry[name]; exists {
		panic("driver: duplicate registration for " + name)
	}
	registry[name] = d
}

// New returns the registered Driver strategy for name. An empty name
// defaults to "claude", matching the nix side's default. Returns an error
// for any name absent from the registry.
func New(name string) (Driver, error) {
	if name == "" {
		name = "claude"
	}
	d, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown DRIVER %q; known drivers: %v", name, Names())
	}
	return d, nil
}

// Names returns the sorted list of registered Driver strategy names, used by
// the nix/Go parity test to assert the two registries never drift.
func Names() []string {
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

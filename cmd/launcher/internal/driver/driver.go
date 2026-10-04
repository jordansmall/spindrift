// Package driver is the host-side half of the Driver seam (ADR 0009), through
// which the launcher varies its transient classification, heartbeat parsing,
// and usage extraction by agent CLI. Each Driver's behaviour lives in its
// sibling subpackage. The in-box half is nix-generated in lib/drivers/, and a
// parity test keeps the two registries from drifting.
package driver

import (
	"fmt"
	"io"
	"sort"

	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/usage"
)

// Driver is the per-agent-CLI strategy, selected at runtime by the DRIVER
// value (defaulting to "claude").
type Driver interface {
	// Name returns the Driver identifier, which must match a key in the nix
	// lib/drivers/ registry.
	Name() string

	// ClassifyTransient reads the box log at logPath and reports whether a
	// non-zero exit is a retryable infrastructure failure or a genuine task
	// failure.
	ClassifyTransient(logPath string) (Classification, error)

	// NewHeartbeatWriter wraps raw with a writer that emits coarse status
	// lines to out, forwarding all bytes to raw unchanged.
	// opts.TopLevelRole is the role attributed to top-level (empty
	// parent_tool_use_id) messages; an empty value means the implementor's
	// own default (issue #2092).
	NewHeartbeatWriter(raw io.Writer, issue string, out io.Writer, opts driverkit.RenderOptions) io.Writer

	// ExtractUsage returns aggregate and per-model usage from the box log.
	ExtractUsage(logPath string) (usage.Report, error)

	// RenderTranscript renders the box log's assistant turns and tool calls
	// for a Console drill-in (#648). opts.TopLevelRole is the role attributed
	// to top-level (empty parent_tool_use_id) messages; an empty value means
	// the implementor's own default (issue #2092).
	RenderTranscript(logPath string, opts driverkit.RenderOptions) (string, error)

	// ResolveExit returns the exit code the launcher should treat as
	// authoritative. A Driver whose own exit code is trustworthy (claude)
	// returns exitCode unchanged; one whose exit code is not (opencode exits
	// 0 even on a mid-run error) derives a replacement from the log
	// (issue #2263).
	ResolveExit(logPath string, exitCode int) (int, error)

	// ResultText returns the Driver's final-message text from the raw stream
	// log at logPath, one value per line, unstripped: the Go twin of the
	// in-box _driver_extract_result_text's jq stage. Feed it to
	// outcome.StripResultText then outcome.ExtractOutcomeLine. A missing log
	// is "" with no error.
	ResultText(logPath string) (string, error)

	// ResultEvent encodes text as one newline-terminated NDJSON event in this
	// Driver's own stream grammar, such that ResultText on a log holding it
	// returns text. The orchestrator appends it as a synthetic final result
	// (issue #4406).
	ResultEvent(text string) ([]byte, error)

	// SessionFlags renders the CLI's session flags for mode: the Go twin of
	// the registry's former _driver_session_flags. "initial" pins the
	// session, "resume" resumes it only when its transcript exists under
	// home, and any other mode renders nothing. A Driver that wires no
	// session resume (opencode) always renders "".
	SessionFlags(mode, repoSlug, issue, home string) string
}

// registry is populated by each driver subpackage's init().
var registry = map[string]Driver{}

// register adds a Driver strategy to the registry. It panics on a duplicate
// name, which is a programming error rather than a runtime condition.
func register(d Driver) {
	name := d.Name()
	if _, exists := registry[name]; exists {
		panic("driver: duplicate registration for " + name)
	}
	registry[name] = d
}

// New returns the registered Driver strategy for name. An empty name defaults
// to "claude", matching the nix side's default.
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

// Names returns the sorted registered Driver names. The nix/Go parity test
// compares them against the nix registry to catch drift.
func Names() []string {
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

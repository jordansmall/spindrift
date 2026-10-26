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

	// NewStreamFilter wraps w with the filter a Pass log's stdout tee goes
	// through; Flush writes out anything it holds. A Driver with no filter
	// forwards every byte unchanged.
	NewStreamFilter(w io.Writer) driverkit.StreamFilter

	// ExtractUsage returns aggregate and per-model usage from the box log.
	ExtractUsage(logPath string) (usage.Report, error)

	// RenderTranscript renders the box log's assistant turns and tool calls
	// for a Console drill-in (#648). opts.TopLevelRole is the role attributed
	// to top-level (empty parent_tool_use_id) messages; an empty value means
	// the implementor's own default (issue #2092).
	RenderTranscript(logPath string, opts driverkit.RenderOptions) (string, error)

	// ResolveExit returns the exit code the launcher should treat as
	// authoritative. A non-zero exitCode is never resolved to 0. claude returns
	// exitCode unchanged; opencode exits 0 even on a mid-run error, so it
	// turns a clean exit into 1 when the log carries an unrecovered error event
	// (issue #2263). Outcome-line absence is not judged here.
	ResolveExit(logPath string, exitCode int) (int, error)

	// ResultText returns the Driver's final-message text from the raw stream
	// log at logPath, one value per line, unstripped. Feed it to
	// outcome.StripResultText then outcome.ExtractOutcomeLine. A missing log is
	// "" with no error.
	ResultText(logPath string) (string, error)

	// ResultEvent encodes text as one newline-terminated NDJSON event in this
	// Driver's own stream grammar, such that ResultText on a log holding it
	// returns text. The orchestrator appends it as a synthetic final result
	// (issue #4406).
	ResultEvent(text string) ([]byte, error)

	// ReadPasses reads the Pass log at logPath one pass at a time for Dispatch
	// Record ingestion (ADR 0061, 0065): one PassTally per pass_start-delimited
	// segment, and visit, when non-nil, sees every non-blank line. trackWrite
	// names a file whose last successful write a tally reports.
	ReadPasses(logPath, trackWrite string, visit func(driverkit.PassLine)) ([]driverkit.PassTally, error)

	// SessionFlags renders the CLI's session flags for mode. "initial" pins the
	// session, "resume" resumes it (rendering nothing when the Driver's session
	// store lacks it and its CLI cannot create one), and any other mode renders
	// nothing. home is the Box's HOME; a Driver whose store path comes from
	// elsewhere ignores it.
	SessionFlags(mode, repoSlug, issue, home string) string
}

// registry is populated by the bundle package's init(); this package imports
// no Driver, so a binary links the Drivers by importing the bundle (ADR 0066
// decision 8).
var registry = map[string]Driver{}

// Register adds the Driver that runs s to the registry, keyed on s.Name. It
// panics on a duplicate name, which is a programming error rather than a
// runtime condition.
func Register(s driverkit.Strategy) {
	if _, exists := registry[s.Name]; exists {
		panic("driver: duplicate registration for " + s.Name)
	}
	registry[s.Name] = driverkit.New(s)
}

var _ Driver = driverkit.Driver{}

// defaultName is the Driver an empty name selects, matching the nix side's
// default.
const defaultName = "claude"

// orDefault resolves an empty Driver name to defaultName.
func orDefault(name string) string {
	if name == "" {
		return defaultName
	}
	return name
}

// New returns the registered Driver strategy for name. An empty name defaults
// to "claude".
func New(name string) (Driver, error) {
	name = orDefault(name)
	d, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown DRIVER %q; known drivers: %v", name, Names())
	}
	return d, nil
}

// descriptor returns the generated Descriptor for an exact Driver name.
func descriptor(name string) (Descriptor, bool) {
	for _, d := range descriptors {
		if d.Name == name {
			return d, true
		}
	}
	return Descriptor{}, false
}

// ProviderNamespacedModels reports whether the named Driver takes
// provider-namespaced model ids, read from its generated Descriptor. It is the
// Go mirror of the Nix fact lib/mkHarness.nix's rosterWithoutBareWorker reads
// (issue #5338); unknown and empty names are false rather than an error.
func ProviderNamespacedModels(name string) bool {
	d, _ := descriptor(name)
	return d.ProviderNamespacedModels
}

// EffortOnModelLevels returns the levels the named Driver reads from a model
// id's `:<level>` suffix, from its generated Descriptor; nil for a Driver whose
// effort does not ride on the model, and for unknown and empty names. It is the
// Go mirror of the Nix effortOnModelLevels fact (issue #5625).
func EffortOnModelLevels(name string) []string {
	d, _ := descriptor(name)
	return d.EffortOnModelLevels
}

// CredentialRules returns the named Driver's credential rules from its
// generated Descriptor. An empty name defaults to "claude", as New does; ok is
// false for an unknown Driver.
func CredentialRules(name string) (rules []CredentialRule, ok bool) {
	d, ok := descriptor(orDefault(name))
	return d.Credentials, ok
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

// Package driver is the host-side half of the Driver seam (ADR 0009), through
// which the launcher varies its transient classification, heartbeat parsing,
// and usage extraction by agent CLI. Each Driver's behaviour lives in its
// sibling subpackage. The in-box half is nix-generated in lib/drivers/, and a
// parity test keeps the two registries from drifting.
package driver

import (
	"fmt"
	"io"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"

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
	// lines to out, forwarding all bytes to raw unchanged. An empty
	// opts.TopLevelRole means the implementor's own default (issue #2092).
	NewHeartbeatWriter(raw io.Writer, issue string, out io.Writer, opts driverkit.RenderOptions) io.Writer

	// NewStreamFilter wraps w with the filter a Pass log's stdout tee goes
	// through; Flush writes out anything it holds. A Driver with no filter
	// forwards every byte unchanged.
	NewStreamFilter(w io.Writer) driverkit.StreamFilter

	// ExtractUsage returns aggregate and per-model usage from the box log.
	ExtractUsage(logPath string) (usage.Report, error)

	// ExtractUsageAfter is ExtractUsage for a log whose sessions may have
	// reported in earlier logs: base seeds each session's previous cumulative
	// result, so a resumed session counts only its increment. It returns the
	// baselines after this log, to seed the next one.
	ExtractUsageAfter(logPath string, base driverkit.SessionBaselines) (usage.Report, driverkit.SessionBaselines, error)

	// RenderTranscript renders the box log's assistant turns and tool calls.
	// opts.TopLevelRole is read as in NewHeartbeatWriter.
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
	// names a file whose last successful write a tally reports. base seeds each
	// session's previous cumulative result, as in ExtractUsageAfter.
	ReadPasses(logPath, trackWrite string, base driverkit.SessionBaselines, visit func(driverkit.PassLine)) ([]driverkit.PassTally, error)

	// SessionBaselinesAfter returns the baselines after the log at logPath,
	// starting from base: what ExtractUsageAfter returns, without the usage
	// scans.
	SessionBaselinesAfter(logPath string, base driverkit.SessionBaselines) (driverkit.SessionBaselines, error)

	// Decode decodes the Pass log at logPath once, for a caller that reads it
	// several ways: each PassLog reader answers as the matching path-based
	// method above does, without decoding the log again. A missing log decodes
	// to an empty PassLog.
	Decode(logPath string) (driverkit.PassLog, error)

	// SessionFlags renders the CLI's session flag and its value for mode.
	// "initial" pins the session, "resume" resumes it (rendering the zero
	// flag when the Driver's session store lacks it and its CLI cannot create
	// one), and any other mode renders the zero flag. home is the Box's HOME;
	// a Driver whose store path comes from elsewhere ignores it. An error
	// means the flag could not be rendered; the caller must not start the
	// session without it.
	SessionFlags(mode, repoSlug, issue, home string) (driverkit.SessionFlag, error)
}

// Set is the Drivers one runtime offers: the native Strategies linked into the
// binary, the Connector Drivers its rows load (lazily, once), the Descriptors
// in force (the generated ones overlaid by rows), and each native Driver's
// shadow row. The zero value is not usable; build one with NewSet. A Set is
// built by one goroutine, then read concurrently.
type Set struct {
	natives     map[string]Driver
	lazy        map[string]func() (Driver, error)
	descriptors []Descriptor
	shadows     map[string]shadowRow
}

// NewSet returns a Set holding natives, keyed on each Strategy's Name, over the
// generated Descriptors. This package imports no Driver, so the binary supplies
// the natives (ADR 0066 decision 8). It panics on a duplicate name, which is a
// programming error rather than a runtime condition.
func NewSet(natives ...driverkit.Strategy) *Set {
	s := &Set{
		natives:     map[string]Driver{},
		lazy:        map[string]func() (Driver, error){},
		descriptors: slices.Clone(descriptors),
		shadows:     map[string]shadowRow{},
	}
	for _, n := range natives {
		if s.Has(n.Name) {
			panic("driver: duplicate registration for " + n.Name)
		}
		s.natives[n.Name] = driverkit.New(n)
	}
	return s
}

// clone returns an independent copy of s.
func (s *Set) clone() *Set {
	return &Set{
		natives:     maps.Clone(s.natives),
		lazy:        maps.Clone(s.lazy),
		descriptors: slices.Clone(s.descriptors),
		shadows:     maps.Clone(s.shadows),
	}
}

// AddConnector adds a Driver that load builds on first use, keyed on name.
// load runs at most once, so a Connector is compiled only when a run selects
// it. It panics on a duplicate name, as NewSet does.
func (s *Set) AddConnector(name string, load func() (driverkit.Strategy, error)) {
	if s.Has(name) {
		panic("driver: duplicate registration for " + name)
	}
	s.lazy[name] = lazyDriver(name, load)
}

// UsePrimary makes the Connector load builds the Driver name, in place of the
// native Strategy linked in under it. The native Strategy stays in the Set, for
// NativeStrategy and a shadow, but New never falls back to it: a load failure
// is the Driver's error. It panics unless a native Strategy holds name and no
// Connector does yet.
func (s *Set) UsePrimary(name string, load func() (driverkit.Strategy, error)) {
	if _, lazy := s.lazy[name]; lazy || !s.HasNative(name) {
		panic("driver: primary Connector needs a native Strategy and no other Connector for " + name)
	}
	s.lazy[name] = lazyDriver(name, load)
}

// lazyDriver wraps load so it runs once and its failure names the Driver.
func lazyDriver(name string, load func() (driverkit.Strategy, error)) func() (Driver, error) {
	return sync.OnceValues(func() (Driver, error) {
		s, err := load()
		if err != nil {
			return nil, fmt.Errorf("DRIVER %q: %w", name, err)
		}
		return driverkit.New(s), nil
	})
}

// HasNative reports whether a Strategy linked into the binary holds name.
func (s *Set) HasNative(name string) bool {
	_, native := s.natives[name]
	return native
}

// Has reports whether a Driver is in the Set under name, linked in or lazily
// loaded, without loading it.
func (s *Set) Has(name string) bool {
	_, native := s.natives[name]
	_, lazy := s.lazy[name]
	return native || lazy
}

// NativeStrategy returns the Strategy linked in under name, which an empty name
// defaults to "claude" for; ok is false for a lazily loaded or unknown Driver.
func (s *Set) NativeStrategy(name string) (driverkit.Strategy, bool) {
	d, ok := s.natives[orDefault(name)].(driverkit.Driver)
	return d.Strategy(), ok
}

// shadowRow is what a Driver row declared as the shadow of a native Driver: the
// Connector module the Box replays each Pass log through to compare, while the
// native Strategy stays the Driver, or why the row was skipped, which the Box
// reports instead of failing the run. Exactly one of the two is set.
type shadowRow struct{ module, skip string }

// UseShadow records module as the Connector shadowing the native Driver name.
func (s *Set) UseShadow(name, module string) { s.shadows[name] = shadowRow{module: module} }

// SkipShadow records why the shadow row for name was not applied.
func (s *Set) SkipShadow(name, reason string) { s.shadows[name] = shadowRow{skip: reason} }

// ShadowSkipped returns why the shadow row for name was skipped, which an empty
// name defaults to "claude" for.
func (s *Set) ShadowSkipped(name string) (reason string, ok bool) {
	row := s.shadows[orDefault(name)]
	return row.skip, row.skip != ""
}

// ShadowModule returns the module shadowing name, which an empty name defaults
// to "claude" for.
func (s *Set) ShadowModule(name string) (module string, ok bool) {
	row := s.shadows[orDefault(name)]
	return row.module, row.module != ""
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

// ResolveName returns name, or the default Driver's name when name is empty.
func ResolveName(name string) string { return orDefault(name) }

// New returns the Driver strategy for name. An empty name defaults to
// "claude". A name with a Descriptor but no Driver is a Connector-only Driver
// (claude) that no Driver row supplied a module for; that is an error, never a
// fallback.
func (s *Set) New(name string) (Driver, error) {
	name = orDefault(name)
	if load, ok := s.lazy[name]; ok {
		return load()
	}
	if d, ok := s.natives[name]; ok {
		return d, nil
	}
	if _, ok := s.DescriptorFor(name); ok {
		return nil, fmt.Errorf("DRIVER %q: no Driver row supplies its Connector module", name)
	}
	return nil, UnknownError(name, s.Names())
}

// UnknownError is the error for a DRIVER name that no known Driver answers to.
func UnknownError(name string, known []string) error {
	return fmt.Errorf("unknown DRIVER %q; known drivers: %v", name, known)
}

// UseDescriptor makes d the Descriptor of the Driver it names, replacing the
// generated one or adding a row the generated set lacks (the launcher input
// document opens the Set at runtime, ADR 0020).
func (s *Set) UseDescriptor(d Descriptor) {
	for i := range s.descriptors {
		if s.descriptors[i].Name == d.Name {
			s.descriptors[i] = d
			return
		}
	}
	s.descriptors = append(s.descriptors, d)
}

// DescriptorFor returns the Descriptor in force for an exact Driver name: the
// generated one, or the row UseDescriptor replaced it with.
func (s *Set) DescriptorFor(name string) (Descriptor, bool) {
	for _, d := range s.descriptors {
		if d.Name == name {
			return d, true
		}
	}
	return Descriptor{}, false
}

// ProviderNamespacedModels reports whether the named Driver takes
// provider-namespaced model ids, read from its Descriptor. It is the Go mirror
// of the Nix fact lib/mkHarness.nix's rosterWithoutBareWorker reads (issue
// #5338); unknown and empty names are false rather than an error.
func (s *Set) ProviderNamespacedModels(name string) bool {
	d, _ := s.DescriptorFor(name)
	return d.ProviderNamespacedModels
}

// EffortOnModelLevels returns the levels the named Driver reads from a model
// id's `:<level>` suffix, from its Descriptor; nil for a Driver whose effort
// does not ride on the model, and for unknown and empty names. It is the Go
// mirror of the Nix effortOnModelLevels fact (issue #5625).
func (s *Set) EffortOnModelLevels(name string) []string {
	d, _ := s.DescriptorFor(name)
	return d.EffortOnModelLevels
}

// CredentialRules returns the named Driver's credential rules from its
// Descriptor. An empty name defaults to "claude", as New does; ok is false for
// an unknown Driver.
func (s *Set) CredentialRules(name string) (rules []CredentialRule, ok bool) {
	d, ok := s.DescriptorFor(orDefault(name))
	return d.Credentials, ok
}

// Names returns the sorted Driver names in the Set. The nix/Go parity test
// compares them against the nix registry to catch drift.
func (s *Set) Names() []string {
	names := make([]string, 0, len(s.natives))
	for name := range s.natives {
		names = append(names, name)
	}
	for name := range s.lazy {
		if !s.HasNative(name) { // a primary Connector shares its native Driver's name
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// SessionArgs renders a Driver's session answer as argv words, refusing any
// answer its row does not allow: a Value with no Flag, a Flag outside the
// row's argvShape.sessionFlags, or a Value starting with "-", which the CLI
// would read as another flag rather than this one's value. The zero answer
// renders nothing. The Box calls it before starting a session, so a Connector
// cannot smuggle an arbitrary CLI flag into the agent's argv through the
// session slot.
func (s *Set) SessionArgs(name string, f driverkit.SessionFlag) ([]string, error) {
	if f == (driverkit.SessionFlag{}) {
		return nil, nil
	}
	if f.Flag == "" {
		return nil, fmt.Errorf("driver %q answered session value %q with no flag", name, f.Value)
	}
	d, ok := s.DescriptorFor(name)
	if !ok {
		return nil, fmt.Errorf("driver %q has no row declaring session flags; refusing session flag %q", name, f.Flag)
	}
	if !slices.Contains(d.SessionFlags, f.Flag) {
		return nil, fmt.Errorf("driver %q answered session flag %q, which its row's argvShape.sessionFlags does not declare", name, f.Flag)
	}
	if strings.HasPrefix(f.Value, "-") {
		return nil, fmt.Errorf("driver %q answered session flag %q with value %q, which the CLI would read as a flag", name, f.Flag, f.Value)
	}
	return f.Args(), nil
}

// Temporary process-wide shims; deleted by #7556's last slice.

// process is the Set the package-level shims read and write.
var process = NewSet()

// Register adds the native Strategy s to the process Set.
func Register(s driverkit.Strategy) {
	if process.Has(s.Name) {
		panic("driver: duplicate registration for " + s.Name)
	}
	process.natives[s.Name] = driverkit.New(s)
}

// ProcessNatives returns the Strategies Register has added to the process Set.
func ProcessNatives() []driverkit.Strategy {
	var out []driverkit.Strategy
	for _, name := range process.Names() {
		if s, ok := process.NativeStrategy(name); ok {
			out = append(out, s)
		}
	}
	return out
}

// Process returns the process Set, for the driverrows shims that apply rows to it.
func Process() *Set { return process }

// Snapshot captures the process Set and returns the function that restores it.
func Snapshot() (restore func()) {
	saved := process.clone()
	return func() { process = saved }
}

func RegisterConnector(name string, load func() (driverkit.Strategy, error)) {
	process.AddConnector(name, load)
}
func RegisterPrimary(name string, load func() (driverkit.Strategy, error)) {
	process.UsePrimary(name, load)
}
func UseShadow(name, module string)                         { process.UseShadow(name, module) }
func SkipShadow(name, reason string)                        { process.SkipShadow(name, reason) }
func UseDescriptor(d Descriptor)                            { process.UseDescriptor(d) }
func HasNative(name string) bool                            { return process.HasNative(name) }
func Has(name string) bool                                  { return process.Has(name) }
func NativeStrategy(name string) (driverkit.Strategy, bool) { return process.NativeStrategy(name) }
func ShadowSkipped(name string) (string, bool)              { return process.ShadowSkipped(name) }
func ShadowModule(name string) (string, bool)               { return process.ShadowModule(name) }
func New(name string) (Driver, error)                       { return process.New(name) }
func DescriptorFor(name string) (Descriptor, bool)          { return process.DescriptorFor(name) }
func ProviderNamespacedModels(name string) bool             { return process.ProviderNamespacedModels(name) }
func EffortOnModelLevels(name string) []string              { return process.EffortOnModelLevels(name) }
func CredentialRules(name string) ([]CredentialRule, bool)  { return process.CredentialRules(name) }
func AuthContentFor(name string) (AuthContent, bool)        { return process.AuthContentFor(name) }
func Names() []string                                       { return process.Names() }
func SessionArgs(name string, f driverkit.SessionFlag) ([]string, error) {
	return process.SessionArgs(name, f)
}

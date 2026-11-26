// Package driverrows opens the Driver set at runtime: it parses the Driver rows
// of the launcher input document (ADR 0020) and builds a driver.Set from them,
// the generated Descriptors overlaid by the rows and each Connector-backed row
// registered for lazy loading. It is a package of its own because it imports
// both driver and connectorhost, and connectorhost must not import driver.
package driverrows

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectorsandbox"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/driver/driverkit"
)

// Row is one Driver row of the input document: the Descriptor's keys, flat,
// plus the compiled Connector that implements the Driver.
type Row struct {
	driver.Descriptor
	// Module is the path of the Connector's compiled wasm. Empty for a Driver
	// whose Strategy is linked in.
	Module string `json:"module,omitempty"`
	// Shadow marks Module as a Connector that shadows the native Driver the row
	// names: the Box replays each Pass log through it and reports where it
	// disagrees, while the native Strategy stays the Driver.
	Shadow bool `json:"shadow,omitempty"`
	// Primary makes Module the Driver the row names, in place of the native
	// Strategy, which stays linked in: the Connector decodes in the Box and on
	// the host alike. A Module that fails to load fails the run with an error
	// naming the Driver; there is no fallback to the native Strategy. It needs a
	// Module and a native Driver, and excludes Shadow.
	Primary bool `json:"primary,omitempty"`
	// FirstParty is rendered only by mkHarness, and only when Module is the wasm
	// the batteries flake itself builds under this name; no Nix row can set it
	// (mkHarness throws), though Go decodes it from any rows JSON. It
	// gates only in-process loads inside the Box; the Launcher has no
	// first-party exception.
	FirstParty bool `json:"firstParty,omitempty"`
}

// ErrNotFirstParty is why the Box refuses to load a Driver Connector: it loads
// only the batteries flake's Connectors in-process, with no fallback for any
// other. The refusal fires when the Driver is loaded, so an unmarked row the
// run never selects does not fail it.
var ErrNotFirstParty = errors.New("Driver Connector is not first-party; the Box loads only the batteries flake's Connectors in-process")

// Rows is a validated set of Driver rows and the native Strategies they were
// validated against; it loads nothing. Hosted and InProcess each build a
// driver.Set from it.
type Rows struct {
	natives []driverkit.Strategy
	rows    []Row
}

// Loader loads a Connector's wasm into a sandbox: the Launcher's shared one.
type Loader func(ctx context.Context, wasm []byte) (*connectorhost.Connector, error)

// Parse decodes raw, a JSON array of Row, and validates it whole against
// natives, the Strategies linked into the binary; empty raw is no rows. Each
// row replaces the Descriptor of the Driver it names when a Set is built. A row
// with a Module registers a lazily loaded Driver unless a native Strategy
// already holds the name; a Primary row registers its Module in place of the
// native Strategy. A row naming a native Driver must carry its Descriptor
// unchanged: native is authoritative for the Descriptor as for the Strategy. A
// Primary row and a Shadow row for one Driver are an error in either order, even
// when the Shadow row would be skipped.
//
// A Shadow row registers no Driver: it records its Module as the native
// Driver's shadow and may sit beside the native Driver's own row. One that
// cannot apply is skipped with a reason driver-exec reports, never an error
// (shadow never fails a run).
func Parse(raw json.RawMessage, natives []driverkit.Strategy) (Rows, error) {
	out := Rows{natives: natives}
	if len(bytes.TrimSpace(raw)) == 0 {
		return out, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var rows []Row
	if err := dec.Decode(&rows); err != nil {
		return Rows{}, fmt.Errorf("driver rows: %w", err)
	}
	base := driver.NewSet(natives...)
	seen := map[string]bool{}
	seenPrimary := map[string]bool{}
	seenShadow := map[string]bool{}
	for _, row := range rows {
		if row.Primary && row.Shadow {
			return Rows{}, fmt.Errorf("driver rows: Driver %q cannot be both shadow and primary", row.Name)
		}
		if row.Primary && seenShadow[row.Name] || row.Shadow && seenPrimary[row.Name] {
			return Rows{}, fmt.Errorf("driver rows: Driver %q has both a primary row and a shadow row", row.Name)
		}
		if row.Shadow {
			seenShadow[row.Name] = true
			continue
		}
		if seen[row.Name] {
			return Rows{}, fmt.Errorf("driver rows: Driver %q has more than one row", row.Name)
		}
		seen[row.Name] = true
		if !base.Has(row.Name) && row.Module == "" {
			return Rows{}, fmt.Errorf("driver rows: Driver %q has no native Strategy and no module", row.Name)
		}
		if row.Primary {
			seenPrimary[row.Name] = true
			if row.Module == "" {
				return Rows{}, fmt.Errorf("driver rows: Driver %q is primary but has no module", row.Name)
			}
			if !base.HasNative(row.Name) {
				return Rows{}, fmt.Errorf("driver rows: Driver %q is primary but has no native Strategy", row.Name)
			}
		}
		if base.HasNative(row.Name) {
			if cur, ok := base.DescriptorFor(row.Name); ok && !sameDescriptor(cur, row.Descriptor) {
				return Rows{}, fmt.Errorf("driver rows: Driver %q is native, so its row must equal its generated Descriptor", row.Name)
			}
		}
	}
	out.rows = rows
	return out, nil
}

// ReadFile parses the JSON file at path, the Box's row file; an empty path is
// no rows.
func ReadFile(path string, natives []driverkit.Strategy) (Rows, error) {
	if path == "" {
		return Rows{natives: natives}, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Rows{}, fmt.Errorf("driver rows: %w", err)
	}
	return Parse(raw, natives)
}

// Hosted builds the Launcher's Set: its natives and every row's Driver, any
// Connector loaded through load. A nil load fails each Connector load with
// connectorsandbox.ErrNoConnectorSandbox.
func (r Rows) Hosted(load Loader) *driver.Set {
	s := driver.NewSet(r.natives...)
	r.apply(s, hostLoads, load)
	return s
}

// InProcess builds the Box's Set: its natives and every row's Driver, loaded
// in-process. A row not marked FirstParty still registers, but loading its
// Driver fails with ErrNotFirstParty, and a shadow row it refuses is skipped.
func (r Rows) InProcess() *driver.Set {
	s := driver.NewSet(r.natives...)
	r.apply(s, boxLoads, nil)
	return s
}

// loadPolicy is who applies the rows, which decides what it may load.
type loadPolicy int

const (
	hostLoads loadPolicy = iota // the Launcher loads any Connector
	boxLoads                    // the Box loads only first-party Connectors
)

// refuses reports whether p must not load row's Connector.
func (p loadPolicy) refuses(row Row) bool {
	return p == boxLoads && row.Module != "" && !row.FirstParty
}

// load loads wasm as p does: the Box in-process, since refuses has already
// kept every Connector but a first-party one from reaching here, and the
// Launcher only through host, its Connector sandbox.
func (p loadPolicy) load(ctx context.Context, host Loader, wasm []byte) (*connectorhost.Connector, error) {
	if p == boxLoads {
		return connectorhost.Load(ctx, wasm)
	}
	if host == nil {
		return nil, connectorsandbox.ErrNoConnectorSandbox
	}
	return host(ctx, wasm)
}

// apply writes r's rows into s under p, loading through host.
func (r Rows) apply(s *driver.Set, p loadPolicy, host Loader) {
	skipped := map[string]string{}
	seenShadow := map[string]bool{}
	for _, row := range r.rows {
		if row.Shadow {
			if reason := shadowSkip(s, row, seenShadow[row.Name], p); reason != "" {
				skipped[row.Name] = "shadow row skipped: " + reason
			}
			seenShadow[row.Name] = true
		}
	}
	for _, row := range r.rows {
		if row.Shadow {
			if reason, ok := skipped[row.Name]; ok {
				s.SkipShadow(row.Name, reason)
			} else {
				s.UseDescriptor(row.Descriptor)
				s.UseShadow(row.Name, row.Module)
			}
			continue
		}
		s.UseDescriptor(row.Descriptor)
		load := loader(row.Name, row.Module, p, host)
		if p.refuses(row) {
			load = refusal(row.Name)
		}
		switch {
		case row.Primary:
			s.UsePrimary(row.Name, load)
		case row.Module != "" && !s.Has(row.Name):
			s.AddConnector(row.Name, load)
		}
	}
}

// Temporary process-wide shims; deleted by #7556's last slice.

// Use applies the rows in raw to the process Set, the Launcher's way: any
// Connector loads through connectorsandbox.Load.
func Use(raw json.RawMessage) error {
	r, err := Parse(raw, driver.ProcessNatives())
	if err != nil {
		return err
	}
	r.apply(driver.Process(), hostLoads, connectorsandbox.Load)
	return nil
}

// UseFile applies the rows in the file at path to the process Set, the Box's
// way: a Connector not marked FirstParty fails with ErrNotFirstParty.
func UseFile(path string) error {
	r, err := ReadFile(path, driver.ProcessNatives())
	if err != nil {
		return err
	}
	r.apply(driver.Process(), boxLoads, nil)
	return nil
}

// notFirstParty is the one text of a refused Driver Connector, shared by the
// load refusal and the shadow skip reason.
func notFirstParty(name string) error {
	return fmt.Errorf("Driver %q: %w", name, ErrNotFirstParty)
}

// refusal is the loader of a Connector the Box will not load. It reads no
// module, so no third-party wasm is ever instantiated.
func refusal(name string) func() (driverkit.Strategy, error) {
	return func() (driverkit.Strategy, error) {
		return driverkit.Strategy{}, fmt.Errorf("driver rows: %w", notFirstParty(name))
	}
}

// loader returns the function that loads the Connector at module as the Driver
// name. The Connector stays open for the life of the process, as the Strategy
// borrows its instance.
func loader(name, module string, p loadPolicy, host Loader) func() (driverkit.Strategy, error) {
	return func() (driverkit.Strategy, error) {
		wasm, err := os.ReadFile(module)
		if err != nil {
			return driverkit.Strategy{}, fmt.Errorf("read module: %w", err)
		}
		c, err := p.load(context.Background(), host, wasm)
		if err != nil {
			return driverkit.Strategy{}, fmt.Errorf("load module %s for driver %q: %w", module, name, err)
		}
		s, err := c.DriverStrategy()
		if err != nil {
			return driverkit.Strategy{}, fmt.Errorf("driver strategy: %w", err)
		}
		if s.Name != name {
			return driverkit.Strategy{}, fmt.Errorf("module %s implements Driver %q", module, s.Name)
		}
		return s, nil
	}
}

// sameDescriptor compares through the wire form, where an absent key and an
// empty one are the same fact.
func sameDescriptor(a, b driver.Descriptor) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

// shadowSkip returns why a shadow row cannot apply, or "" if it can. repeated
// is whether an earlier shadow row names the same Driver; p is who applies it.
func shadowSkip(s *driver.Set, row Row, repeated bool, p loadPolicy) string {
	switch {
	case repeated:
		return fmt.Sprintf("Driver %q has more than one shadow row", row.Name)
	case p.refuses(row):
		return notFirstParty(row.Name).Error()
	case !s.HasNative(row.Name) || row.Module == "":
		return fmt.Sprintf("Driver %q needs a native Strategy and a module", row.Name)
	}
	if cur, ok := s.DescriptorFor(row.Name); ok && !sameDescriptor(cur, row.Descriptor) {
		return fmt.Sprintf("Driver %q is native, so its row must equal its generated Descriptor", row.Name)
	}
	return ""
}

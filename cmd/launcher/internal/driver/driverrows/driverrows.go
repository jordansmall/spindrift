// Package driverrows opens the Driver registry at runtime: it applies the
// Driver rows of the launcher input document (ADR 0020) over the generated
// Descriptors and registers each Connector-backed row for lazy loading. It is
// a package of its own because it imports both the registry and connectorhost,
// and connectorhost must not import the registry.
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

// Use applies the rows in raw, a JSON array of Row; empty raw is a no-op. Each
// row replaces the Descriptor of the Driver it names.
//
// A row with a Module registers a lazily loaded Driver unless a native
// Strategy already holds the name, which wins. A Primary row is the exception:
// it registers its Module as the Driver in place of the native Strategy.
//
// These are errors: a row with neither Module nor native Strategy, a repeated
// name, a Primary row without a Module or a native Driver, a Primary row that
// is also Shadow, a Primary row and a Shadow row for one Driver (in either
// order, even a Shadow row that would be skipped), and a row naming a native
// Driver whose Descriptor differs from the one in force (native is
// authoritative for the Descriptor as it is for the Strategy).
//
// A Shadow row registers no Driver: it records its Module as the native
// Driver's shadow, and may sit beside the native Driver's own row. Without a
// native Strategy or a Module, repeated, or with a Descriptor other than the
// native one, it is skipped, not an error, and driver-exec reports the reason
// (shadow never fails a run). Rows are validated whole before any is applied.
// Use is the Launcher's entry point and loads any Connector; UseFile is the
// Box's, whose registered loader refuses a Connector row not marked FirstParty.
func Use(raw json.RawMessage) error { return use(raw, hostLoads) }

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
// Launcher only into its Connector sandbox.
func (p loadPolicy) load(ctx context.Context, wasm []byte) (*connectorhost.Connector, error) {
	if p == boxLoads {
		return connectorhost.Load(ctx, wasm)
	}
	return connectorsandbox.Load(ctx, wasm)
}

// use applies raw under p: a row p refuses still registers, but loading its
// Driver fails with ErrNotFirstParty, and a shadow row it refuses is skipped.
func use(raw json.RawMessage, p loadPolicy) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var rows []Row
	if err := dec.Decode(&rows); err != nil {
		return fmt.Errorf("driver rows: %w", err)
	}
	seen := map[string]bool{}
	seenPrimary := map[string]bool{}
	seenShadow := map[string]bool{}
	skipped := map[string]string{}
	for _, row := range rows {
		if row.Primary && row.Shadow {
			return fmt.Errorf("driver rows: Driver %q cannot be both shadow and primary", row.Name)
		}
		if row.Primary && seenShadow[row.Name] || row.Shadow && seenPrimary[row.Name] {
			return fmt.Errorf("driver rows: Driver %q has both a primary row and a shadow row", row.Name)
		}
		if row.Shadow {
			if reason := shadowSkip(row, seenShadow[row.Name], p); reason != "" {
				skipped[row.Name] = "shadow row skipped: " + reason
			}
			seenShadow[row.Name] = true
			continue
		}
		if seen[row.Name] {
			return fmt.Errorf("driver rows: Driver %q has more than one row", row.Name)
		}
		seen[row.Name] = true
		if !driver.Has(row.Name) && row.Module == "" {
			return fmt.Errorf("driver rows: Driver %q has no native Strategy and no module", row.Name)
		}
		if row.Primary {
			seenPrimary[row.Name] = true
			if row.Module == "" {
				return fmt.Errorf("driver rows: Driver %q is primary but has no module", row.Name)
			}
			if !driver.HasNative(row.Name) {
				return fmt.Errorf("driver rows: Driver %q is primary but has no native Strategy", row.Name)
			}
		}
		if driver.HasNative(row.Name) {
			if cur, ok := driver.DescriptorFor(row.Name); ok && !sameDescriptor(cur, row.Descriptor) {
				return fmt.Errorf("driver rows: Driver %q is native, so its row must equal its generated Descriptor", row.Name)
			}
		}
	}
	for _, row := range rows {
		if row.Shadow {
			if reason, ok := skipped[row.Name]; ok {
				driver.SkipShadow(row.Name, reason)
			} else {
				driver.UseDescriptor(row.Descriptor)
				driver.UseShadow(row.Name, row.Module)
			}
			continue
		}
		driver.UseDescriptor(row.Descriptor)
		load := loader(row.Name, row.Module, p)
		if p.refuses(row) {
			load = refusal(row.Name)
		}
		switch {
		case row.Primary:
			driver.RegisterPrimary(row.Name, load)
		case row.Module != "" && !driver.Has(row.Name):
			driver.RegisterConnector(row.Name, load)
		}
	}
	return nil
}

// UseFile applies the rows in the JSON file at path; an empty path is a no-op.
// It is the Box's entry point, so loading a Connector not marked FirstParty
// fails with ErrNotFirstParty, where Use loads any.
func UseFile(path string) error {
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("driver rows: %w", err)
	}
	return use(raw, boxLoads)
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
func loader(name, module string, p loadPolicy) func() (driverkit.Strategy, error) {
	return func() (driverkit.Strategy, error) {
		wasm, err := os.ReadFile(module)
		if err != nil {
			return driverkit.Strategy{}, fmt.Errorf("read module: %w", err)
		}
		c, err := p.load(context.Background(), wasm)
		if err != nil {
			return driverkit.Strategy{}, fmt.Errorf("load module %s: %w", module, err)
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
func shadowSkip(row Row, repeated bool, p loadPolicy) string {
	switch {
	case repeated:
		return fmt.Sprintf("Driver %q has more than one shadow row", row.Name)
	case p.refuses(row):
		return notFirstParty(row.Name).Error()
	case !driver.HasNative(row.Name) || row.Module == "":
		return fmt.Sprintf("Driver %q needs a native Strategy and a module", row.Name)
	}
	if cur, ok := driver.DescriptorFor(row.Name); ok && !sameDescriptor(cur, row.Descriptor) {
		return fmt.Sprintf("Driver %q is native, so its row must equal its generated Descriptor", row.Name)
	}
	return ""
}

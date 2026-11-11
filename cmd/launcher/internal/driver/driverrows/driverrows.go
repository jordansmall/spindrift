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
	"fmt"
	"os"

	"spindrift.dev/launcher/internal/connectorhost"
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
}

// Use applies the rows in raw, a JSON array of Row; empty raw is a no-op. Each
// row replaces the Descriptor of the Driver it names. A row with a Module
// registers a lazily loaded Driver unless a native Strategy already holds the
// name, which always wins. A row with neither is an error, as is a repeated
// name, and a row naming a native Driver must carry the Descriptor already in
// force: native is authoritative for the Descriptor as it is for the Strategy.
// Rows are validated whole before any is applied.
func Use(raw json.RawMessage) error {
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
	for _, row := range rows {
		if seen[row.Name] {
			return fmt.Errorf("driver rows: Driver %q has more than one row", row.Name)
		}
		seen[row.Name] = true
		if !driver.Has(row.Name) && row.Module == "" {
			return fmt.Errorf("driver rows: Driver %q has no native Strategy and no module", row.Name)
		}
		if driver.HasNative(row.Name) {
			if cur, ok := driver.DescriptorFor(row.Name); ok && !sameDescriptor(cur, row.Descriptor) {
				return fmt.Errorf("driver rows: Driver %q is native, so its row must equal its generated Descriptor", row.Name)
			}
		}
	}
	for _, row := range rows {
		driver.UseDescriptor(row.Descriptor)
		if row.Module != "" && !driver.Has(row.Name) {
			driver.RegisterConnector(row.Name, loader(row.Name, row.Module))
		}
	}
	return nil
}

// UseFile applies the rows in the JSON file at path; an empty path is a no-op.
func UseFile(path string) error {
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("driver rows: %w", err)
	}
	return Use(raw)
}

// loader returns the function that loads the Connector at module as the Driver
// name. The Connector stays open for the life of the process, as the Strategy
// borrows its instance.
func loader(name, module string) func() (driverkit.Strategy, error) {
	return func() (driverkit.Strategy, error) {
		wasm, err := os.ReadFile(module)
		if err != nil {
			return driverkit.Strategy{}, fmt.Errorf("read module: %w", err)
		}
		c, err := connectorhost.Load(context.Background(), wasm)
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

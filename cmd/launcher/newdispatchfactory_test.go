package main

import (
	"errors"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/forge"
)

// A primary Connector that fails to load fails the run with its named error:
// there is no native fallback, and no Factory is built around a nil Driver.
func TestNewDispatchFactoryPrimaryLoadFailureFailsRun(t *testing.T) {
	t.Cleanup(driver.Snapshot())
	errBoom := errors.New("boom")
	driver.Register(driverkit.Strategy{Name: "primfail"})
	driver.RegisterPrimary("primfail", func() (driverkit.Strategy, error) {
		return driverkit.Strategy{}, errBoom
	})
	f, err := newDispatchFactory(config{driver: "primfail"}, "", nil, nil, nil, nil, forge.Capabilities{})
	if f != nil || err == nil {
		t.Fatalf("newDispatchFactory = %v, %v, want a nil Factory and an error", f, err)
	}
	if !strings.Contains(err.Error(), `DRIVER "primfail"`) || !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, want it to name DRIVER \"primfail\" and wrap boom", err)
	}
}

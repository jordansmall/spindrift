package seamtest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/fixturebuild"
)

// Strategy is the fake Driver: the compiled Connector seamtest-driver.wasm
// loaded through connectorhost, so every core test crosses the real Connector
// seam (the package doc's Fake Driver section gives its log grammar).
//
// The Connector loads once per test binary and is never closed: compiling is
// the expensive part. Each call returns a copy, so a caller may override
// host-only fields (Totals, Heartbeat, ...); copies share the one Connector.
// It panics on a load failure: a TestMain caller has no testing.TB and no
// core test may skip the seam (ADR 0067).
func Strategy() driverkit.Strategy {
	s, err := loadStrategy()
	if err != nil {
		panic(fmt.Sprintf("seamtest: fake Driver Connector: %v", err))
	}
	return s
}

var loadStrategy = sync.OnceValues(func() (driverkit.Strategy, error) {
	dir, err := fixturebuild.ConnectorDir()
	if err != nil {
		return driverkit.Strategy{}, err
	}
	wasm, err := os.ReadFile(filepath.Join(dir, "seamtest-driver.wasm"))
	if err != nil {
		return driverkit.Strategy{}, err
	}
	c, err := connectorhost.LoadCompiled(context.Background(), wasm, fixturebuild.WazeroCacheDir())
	if err != nil {
		return driverkit.Strategy{}, err
	}
	return c.DriverStrategy()
})

// Line is ev as the Strategy's log line: one JSON-encoded Event and a newline.
func Line(ev driverkit.Event) string {
	b, err := json.Marshal(ev)
	if err != nil {
		panic(err)
	}
	return string(b) + "\n"
}

// Millis is the RFC3339 ts as epoch milliseconds, the canonical Event's
// Timestamp unit. It panics on a malformed ts.
func Millis(rfc3339 string) int64 {
	t, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		panic(err)
	}
	return t.UnixMilli()
}

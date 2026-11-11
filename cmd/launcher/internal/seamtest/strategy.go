package seamtest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/driver/driverkit"
)

const (
	// connectorEnvVar names the dir holding the fixture Connector .wasm files.
	connectorEnvVar = "SPINDRIFT_CONNECTOR_FIXTURES_DIR"
	// connectorFlakeAttr is the flake package that builds that dir.
	connectorFlakeAttr = "connector-wasm-fixtures"
)

// fakeDriverName is the name the fake CLI is invoked under, so the Strategy
// must be registered under it for the launcher to find a Driver for it.
const fakeDriverName = "claude"

// Strategy is the fake Driver: the compiled Connector seamtest-driver.wasm
// loaded through connectorhost, so every core test crosses the real Connector
// seam (see the package doc's Fake Driver section for its log grammar).
//
// The Connector loads once per test binary and is never closed: compiling the
// module is the expensive part. Each call returns a copy of the Strategy, so a
// caller may override host-only fields (Totals, Heartbeat, ...) without
// affecting others; every copy still shares the one loaded Connector, its
// mutex and funcs. It panics when the Connector cannot be loaded, because a
// TestMain caller has no testing.TB and no core test may skip the seam (ADR
// 0067).
func Strategy() driverkit.Strategy {
	s, err := loadStrategy()
	if err != nil {
		panic(fmt.Sprintf("seamtest: fake Driver Connector: %v", err))
	}
	return s
}

var loadStrategy = sync.OnceValues(func() (driverkit.Strategy, error) {
	dir := os.Getenv(connectorEnvVar)
	if dir == "" {
		built, skip, err := ResolveBuild(connectorEnvVar, "a directory of fixture Connector .wasm files", func(root string) []string {
			return []string{root + "#" + connectorFlakeAttr}
		})
		if skip != "" {
			return driverkit.Strategy{}, errors.New(skip)
		}
		if err != nil {
			return driverkit.Strategy{}, err
		}
		dir = built
	}
	wasm, err := os.ReadFile(filepath.Join(dir, "seamtest-driver.wasm"))
	if err != nil {
		return driverkit.Strategy{}, err
	}
	// Per-user, not os.TempDir: another user must not plant native code here.
	cacheDir, err := os.UserCacheDir()
	if err == nil {
		cacheDir = filepath.Join(cacheDir, "spindrift", "wazero")
	}
	c, err := connectorhost.LoadCompiled(context.Background(), wasm, cacheDir)
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

var registerMu sync.Mutex

// registerIfAbsent registers the Strategy build returns under name unless a
// Driver of that name is registered. build runs only then, so a linked bundle
// never loads the Connector. The registry panics on a duplicate name and is not
// synchronized: registerMu orders concurrent registrations only, so callers
// register before any parallel test reads the registry.
func registerIfAbsent(name string, build func() driverkit.Strategy) {
	registerMu.Lock()
	defer registerMu.Unlock()
	if _, err := driver.New(name); err == nil {
		return
	}
	driver.Register(build())
}

// RegisterDriver installs Strategy() as the "claude" Driver when none is
// registered, and does nothing when one is (a test binary that links the
// bundle keeps the real Driver). Call it before any parallel test reads the
// registry.
func RegisterDriver(tb testing.TB) {
	tb.Helper()
	registerIfAbsent(fakeDriverName, Strategy)
}

// RegisterStrategy installs s under its own Name when no Driver of that name
// is registered, and does nothing when one is. It takes no testing.TB so a
// TestMain can call it; the same ordering caveat as RegisterDriver applies.
func RegisterStrategy(s driverkit.Strategy) {
	registerIfAbsent(s.Name, func() driverkit.Strategy { return s })
}

package dispatch

import (
	"io"
	"time"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/runner"
)

// Clock is injectable for tests; RealClock() gives production behaviour.
type Clock struct {
	Now   func() time.Time
	Sleep func(time.Duration)
}

// RealClock returns a Clock backed by the real time.Now / time.Sleep.
func RealClock() Clock {
	return Clock{Now: time.Now, Sleep: time.Sleep}
}

// Factory is constructed once per top-level dispatch entry point (run, the
// selective `dispatch <nums>` path, or recover) with the config, working
// dir, runner, driver, and clock it will use for every issue in that
// invocation, plus the driver-cache root all its Dispatch values share.
type Factory struct {
	cfg    Config
	pwd    string
	runner runner.Runner
	driver driver.Driver
	clock  Clock
	cache  *cache
}

// NewFactory constructs a Factory and its driver-cache root. When cfg
// declares no DriverSessionCacheDir, the Factory skips cache creation
// entirely (a nil cache, same as a creation failure) since there is no
// in-box target to mount it over. Otherwise a cache creation failure
// degrades to a nil cache (fix boxes cold-start) rather than failing
// construction; the returned error is diagnostic only and the Factory is
// still usable.
func NewFactory(cfg Config, pwd string, r runner.Runner, drv driver.Driver, clock Clock) (*Factory, error) {
	if cfg.DriverSessionCacheDir == "" {
		return &Factory{cfg: cfg, pwd: pwd, runner: r, driver: drv, clock: clock, cache: nil}, nil
	}
	c, err := newCache()
	return &Factory{cfg: cfg, pwd: pwd, runner: r, driver: drv, clock: clock, cache: c}, err
}

// New constructs a Dispatch for one issue, claiming its per-issue
// driver-cache directory up front.
func (f *Factory) New(number, title string) *Dispatch {
	return &Dispatch{
		number:   number,
		title:    title,
		pwd:      f.pwd,
		runner:   f.runner,
		driver:   f.driver,
		clock:    f.clock,
		cfg:      f.cfg,
		cacheDir: f.cache.dirFor(number),
		cache:    f.cache,
	}
}

// Cleanup removes the whole driver-cache root. Called once, on exit, by
// whichever entry point constructed the Factory.
func (f *Factory) Cleanup() {
	f.cache.cleanup()
}

// Driver returns the Driver strategy this Factory was constructed with — a
// Console drill-in's route to RenderTranscript, so rendering a Dispatch's
// logs needs no second Driver-holding type (#648).
func (f *Factory) Driver() driver.Driver {
	return f.driver
}

// SetHeartbeatOut overrides the human-facing heartbeat sink every Dispatch
// this Factory constructs afterward will use (issue #1583). Must be called
// before any New(), which copies cfg by value into the returned Dispatch.
func (f *Factory) SetHeartbeatOut(w io.Writer) {
	f.cfg.HeartbeatOut = w
}

// HeartbeatOut returns the heartbeat sink this Factory currently carries --
// nil unless SetHeartbeatOut was called. A console-entry-point test seam
// (issue #1583) confirming the wiring reaches the Factory, alongside
// Driver's existing test-spy role.
func (f *Factory) HeartbeatOut() io.Writer {
	return f.cfg.HeartbeatOut
}

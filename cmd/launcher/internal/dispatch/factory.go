package dispatch

import (
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

// NewFactory constructs a Factory and its driver-cache root. A cache
// creation failure degrades to a nil cache (fix boxes cold-start) rather
// than failing construction; the returned error is diagnostic only and the
// Factory is still usable.
func NewFactory(cfg Config, pwd string, r runner.Runner, drv driver.Driver, clock Clock) (*Factory, error) {
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

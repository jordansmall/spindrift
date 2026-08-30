package dispatch

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"sync"
	"sync/atomic"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/retry"
	"spindrift.dev/launcher/internal/runner"
)

// Clock is the injectable time seam, now owned by the retry leaf (issue
// #2154); dispatch keeps this alias so every existing constructor compiles
// unchanged.
type Clock = retry.Clock

// RealClock returns a Clock backed by the real time.Now / time.Sleep.
func RealClock() Clock { return retry.RealClock() }

// Factory is constructed once per top-level dispatch entry point (run, the
// selective `dispatch <nums>` path, or recover) with the config, working
// dir, runner, driver, and clock it will use for every issue in that
// invocation, plus the driver-cache root all its Dispatch values share.
type Factory struct {
	cfg       Config
	pwd       string
	runner    runner.Runner
	driver    driver.Driver
	clock     Clock
	cache     *cache
	newCalled atomic.Bool

	// genMu guards agentGeneration -- unlike cfg (copied by value into every
	// Dispatch at New() and protected only by the coarse newCalled panic
	// guard on SetHeartbeatOut), a bwrap staleness hot-swap (issue #2682)
	// must be able to land at any time, including concurrently with New()
	// and with an already-launched Box's own in-flight Run(). A mutex, not a
	// before-any-New() panic, is what makes that safe.
	genMu           sync.RWMutex
	agentGeneration *runner.AgentGeneration
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
// driver-cache directory up front and minting its per-run nonce (issue
// #1937).
func (f *Factory) New(number, title string) *Dispatch {
	f.newCalled.Store(true)
	return &Dispatch{
		number:          number,
		title:           title,
		pwd:             f.pwd,
		runner:          f.runner,
		driver:          f.driver,
		clock:           f.clock,
		cfg:             f.cfg,
		cacheDir:        f.cache.dirFor(number),
		cache:           f.cache,
		nonce:           newNonce(),
		agentGeneration: f.AgentGeneration(),
	}
}

// newNonce mints a fresh, unpredictable per-run nonce (issue #1937): 16
// bytes read from the OS's cryptographic random source, hex-encoded. Two
// calls never return the same value. The nonce is what lets the host tell a
// control-signal line genuinely produced by this run's own Box from one an
// untrusted issue/comment author -- who wrote their text before this nonce
// was minted -- echoed into the log; a predictable or reused value would
// defeat that. crypto/rand.Read only fails when the OS's entropy source is
// broken, a host condition no caller can recover from, so newNonce panics
// rather than threading an error through Factory.New's unchanged signature
// and every one of its call sites for a failure mode that never happens in
// practice.
func newNonce() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("dispatch: crypto/rand.Read failed: " + err.Error())
	}
	return hex.EncodeToString(b)
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
// before any New(), which copies cfg by value into the returned Dispatch;
// New() may run concurrently from multiple goroutines (waves/continuous.go,
// waves/engine.go), so a call arriving after New() has already started
// handing out cfg snapshots would silently leave earlier Dispatch values on
// the old sink. Runtime-enforced (issue #1594): calling it once any New()
// has run panics instead of racing.
func (f *Factory) SetHeartbeatOut(w io.Writer) {
	if f.newCalled.Load() {
		panic("dispatch: Factory.SetHeartbeatOut called after Factory.New(); must be called before any New()")
	}
	f.cfg.HeartbeatOut = w
}

// HeartbeatOut returns the heartbeat sink this Factory currently carries --
// nil unless SetHeartbeatOut was called. A console-entry-point test seam
// (issue #1583) confirming the wiring reaches the Factory, alongside
// Driver's existing test-spy role.
func (f *Factory) HeartbeatOut() io.Writer {
	return f.cfg.HeartbeatOut
}

// SetAgentGeneration overrides the agent-closure generation every Box
// launched by a Dispatch this Factory constructs afterward will bind (issue
// #2682, the bwrap Box-only staleness hot-swap). Unlike SetHeartbeatOut,
// this carries no before-any-New() panic guard: a hot-swap must be able to
// land well after dispatching has already started -- concurrently with
// New() minting new Dispatch values on other goroutines, and with an
// already-launched Box's own in-flight Run() -- so the mutex here, not a
// runtime-enforced ordering, is what keeps a concurrent set/read race-free.
// A nil gen restores "use the runner adapter's own startup-baked default",
// matching runner.Box.ClosureGeneration's own nil-means-default contract.
func (f *Factory) SetAgentGeneration(gen *runner.AgentGeneration) {
	f.genMu.Lock()
	defer f.genMu.Unlock()
	f.agentGeneration = gen
}

// AgentGeneration returns the agent-closure generation this Factory
// currently carries -- nil until SetAgentGeneration is ever called (every
// non-bwrap runtime, and a bwrap run before its first hot-swap), meaning
// "use the runner adapter's own startup-baked default." New() snapshots this
// value into each Dispatch it constructs, so a swap that lands mid-run only
// affects Boxes launched by a Dispatch minted after the swap, never one
// already in flight.
func (f *Factory) AgentGeneration() *runner.AgentGeneration {
	f.genMu.RLock()
	defer f.genMu.RUnlock()
	return f.agentGeneration
}

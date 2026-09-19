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

// Clock is the injectable time seam, owned by the retry leaf (issue #2154);
// this alias keeps every existing dispatch constructor compiling unchanged.
type Clock = retry.Clock

// RealClock returns a Clock backed by the real time.Now and time.Sleep.
func RealClock() Clock { return retry.RealClock() }

// Factory is constructed once per top-level dispatch entry point (run, the
// selective `dispatch <nums>` path, or recover) and holds what every issue in
// that invocation shares, including the driver-cache root.
type Factory struct {
	cfg       Config
	pwd       string
	runner    runner.Runner
	driver    driver.Driver
	clock     Clock
	cache     *cache
	newCalled atomic.Bool

	// genMu guards agentGeneration: a bwrap staleness hot-swap (issue #2682)
	// can land at any time, including concurrently with New() and with an
	// already-launched Box's own in-flight Run(), so a mutex is required
	// where cfg gets by with the coarse newCalled panic guard.
	genMu           sync.RWMutex
	agentGeneration *runner.AgentGeneration

	// killMu guards killLatches, the per-claim kill latches (issue #3521). A
	// reap by container name cannot match a Box whose container does not exist
	// yet — the New-to-runner.Run window, and the transient-backoff hold — so
	// Kill closes a latch the Dispatch checks before launching and waits on
	// during backoff. New re-arms it, so the latch tracks the current claim.
	killMu      sync.Mutex
	killLatches map[string]chan struct{}
}

// NewFactory constructs a Factory and its driver-cache root. An empty
// DriverSessionCacheDir means there is no in-box target to mount a cache over,
// so the Factory skips creating one. A creation failure also degrades to a nil
// cache (fix boxes cold-start) rather than failing construction; the returned
// error is diagnostic only and the Factory is still usable.
func NewFactory(cfg Config, pwd string, r runner.Runner, drv driver.Driver, clock Clock) (*Factory, error) {
	if cfg.DriverSessionCacheDir == "" {
		return &Factory{cfg: cfg, pwd: pwd, runner: r, driver: drv, clock: clock, cache: nil}, nil
	}
	c, err := newCache()
	return &Factory{cfg: cfg, pwd: pwd, runner: r, driver: drv, clock: clock, cache: c}, err
}

// New constructs a Dispatch for one issue, claiming its per-issue driver-cache
// directory up front and minting its per-run nonce (issue #1937).
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
		killed:          f.armKillLatch(number),
	}
}

// newNonce mints an unpredictable per-run nonce (issue #1937) that lets the
// host tell a control-signal line produced by this run's own Box from one an
// untrusted issue or comment author echoed into the log; a predictable or
// reused value would defeat that. crypto/rand.Read only fails when the OS
// entropy source is broken, so this panics rather than threading an error.
func newNonce() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("dispatch: crypto/rand.Read failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// Cleanup removes the whole driver-cache root. Whichever entry point
// constructed the Factory calls it once, on exit.
func (f *Factory) Cleanup() {
	f.cache.cleanup()
}

// Driver returns the Driver this Factory was constructed with, so a Console
// drill-in can reach RenderTranscript without a second Driver-holding type
// (#648).
func (f *Factory) Driver() driver.Driver {
	return f.driver
}

// SetHeartbeatOut overrides the heartbeat sink for every Dispatch this Factory
// constructs afterward (issue #1583). Call it before any New(), which copies
// cfg by value and may run concurrently from several goroutines; a later call
// would silently leave earlier Dispatch values on the old sink. Calling it once
// any New() has run panics instead of racing (issue #1594).
func (f *Factory) SetHeartbeatOut(w io.Writer) {
	if f.newCalled.Load() {
		panic("dispatch: Factory.SetHeartbeatOut called after Factory.New(); must be called before any New()")
	}
	f.cfg.HeartbeatOut = w
}

// HeartbeatOut returns the heartbeat sink this Factory carries, nil unless
// SetHeartbeatOut was called. A test seam for the console entry point (issue
// #1583).
func (f *Factory) HeartbeatOut() io.Writer {
	return f.cfg.HeartbeatOut
}

// SetAgentGeneration overrides the agent-closure generation every Box launched
// afterward binds (issue #2682, the bwrap Box-only staleness hot-swap). It
// carries no before-any-New() panic guard because a hot-swap must be able to
// land mid-run, so the mutex is what keeps a concurrent set and read safe. A
// nil gen restores the runner adapter's own startup-baked default.
func (f *Factory) SetAgentGeneration(gen *runner.AgentGeneration) {
	f.genMu.Lock()
	defer f.genMu.Unlock()
	f.agentGeneration = gen
}

// AgentGeneration returns the agent-closure generation this Factory carries,
// nil until SetAgentGeneration is called, meaning "use the runner adapter's own
// startup-baked default". New() snapshots it into each Dispatch, so a swap that
// lands mid-run only affects Boxes launched by a Dispatch minted after it,
// never one already in flight.
func (f *Factory) AgentGeneration() *runner.AgentGeneration {
	f.genMu.RLock()
	defer f.genMu.RUnlock()
	return f.agentGeneration
}

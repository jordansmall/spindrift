package dispatch

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/registrymanifest"
	"spindrift.dev/launcher/internal/registryproxy"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/unixsocket"
)

// Dispatch is the per-issue execution object: every Box launched for one
// issue, from claim to verdict, plus its driver-cache entry. Construct one
// via Factory.New.
type Dispatch struct {
	number, title string
	pwd           string
	runner        runner.Runner
	driver        driver.Driver
	clock         Clock
	cfg           Config
	cacheDir      string
	cache         *cache

	// nonce is this Dispatch's per-run nonce (issue #1937), minted by
	// Factory.New, forwarded into every Box as RUN_NONCE, and kept here so
	// successResult and retry.go can check a log line against it.
	nonce string

	// agentGeneration is the agent-closure generation snapshot taken at
	// New()-time (issue #2682), forwarded into every Box as ClosureGeneration;
	// nil means runner.Box's own default. Snapshotted once rather than read
	// live so a Fix() launched minutes later (settle/ready.go, after a Box
	// idles awaiting CI) stays on the generation this Dispatch started on.
	agentGeneration *runner.AgentGeneration

	// killed is this issue's kill latch, minted by Factory.New and closed by
	// Factory.Kill (issue #3521). Nil for a Dispatch built without a Factory,
	// which then behaves exactly as it did before.
	killed <-chan struct{}
}

var _ Dispatcher = (*Dispatch)(nil)

// errKilled reports that Factory.Kill landed before this attempt created its
// container, so the attempt must not start one (issue #3521).
var errKilled = errors.New("dispatch: killed before launch")

// isKilled reports whether this issue's kill latch has closed.
func (d *Dispatch) isKilled() bool {
	if d.killed == nil {
		return false
	}
	select {
	case <-d.killed:
		return true
	default:
		return false
	}
}

// sleepOrKilled waits out dur unless the kill latch closes first, so an abort
// landing inside a retry backoff — or a rate-limit hold that can run an hour —
// exits promptly (issue #3521). The orphaned sleeper goroutine is bounded by
// dur, and the process is on its way out.
func (d *Dispatch) sleepOrKilled(dur time.Duration) {
	if d.killed == nil {
		d.clock.Sleep(dur)
		return
	}
	slept := make(chan struct{})
	go func() {
		d.clock.Sleep(dur)
		close(slept)
	}()
	select {
	case <-slept:
	case <-d.killed:
	}
}

func (d *Dispatch) logPath() string {
	return logPathFor(d.pwd, d.number)
}

func (d *Dispatch) fixLogPath(pass int) string {
	return fixLogPathFor(d.pwd, d.number, pass)
}

func (d *Dispatch) conflictLogPath() string {
	return conflictLogPathFor(d.pwd, d.number)
}

// OutboxDirFor returns the host path of number's per-issue writable outbox
// directory (CODE_FORGE=local, ADR 0033), where the Box's code-out bundle
// lands for the Launcher to relay into the Accumulation repo. Exported so
// settle's bundle relay computes the identical path runOnce mounts without
// holding the Dispatch itself.
func OutboxDirFor(pwd, number string) string {
	return filepath.Join(pwd, ".spindrift", "outbox", number)
}

// HostLogDirFor returns the host-side log directory for a working dir, the
// single source of truth for `<pwd>/.spindrift/logs` so it cannot drift.
func HostLogDirFor(pwd string) string {
	return filepath.Join(pwd, ".spindrift", "logs")
}

// logPathFor, fixLogPathFor, and conflictLogPathFor are the single source of
// truth for a Dispatch's log naming, shared with LogPaths (logs.go) so a
// drill-in's pass discovery cannot drift from the paths a Dispatch writes.
func logPathFor(pwd, number string) string {
	return filepath.Join(HostLogDirFor(pwd), "issue-"+number+".log")
}

func fixLogPathFor(pwd, number string, pass int) string {
	return filepath.Join(HostLogDirFor(pwd), fmt.Sprintf("issue-%s-fix-%d.log", number, pass))
}

func conflictLogPathFor(pwd, number string) string {
	return filepath.Join(HostLogDirFor(pwd), fmt.Sprintf("issue-%s-conflict-resolve.log", number))
}

// Run dispatches the initial box for this issue.
func (d *Dispatch) Run() Result {
	logPath := d.logPath()
	return d.dispatchWithRetry(logPath, func(resumeAfterHold bool) error {
		fmt.Fprint(d.humanOut(), announceLine(d.number, "", d.title))
		if !resumeAfterHold && !d.runner.IsRunning(BoxName(d.number)) {
			// Only on this Run()'s first attempt, and only when no live
			// container owns this issue's log (the same guard
			// quarantinePriorRunLogs makes, so a live run's log dir gets
			// neither a rename nor a marker). Unconditional otherwise: a
			// fresh Run() makes any marker on disk stale.
			if err := quarantinePriorRunLogs(d.pwd, d.number, d.runner); err != nil {
				return quarantineErr{err: fmt.Errorf("quarantine prior-run logs: %w", err)}
			}
			if err := markRunLineage(d.pwd, d.number); err != nil {
				return quarantineErr{err: fmt.Errorf("mark run lineage: %w", err)}
			}
		}
		env, err := buildBoxEnv(d.cfg, d.number, d.title, 0, "", d.nonce)
		if err != nil {
			return err
		}
		if resumeAfterHold {
			env["RESUME_AFTER_HOLD"] = "1"
		}
		return d.runOnce(logPath, env, d.cacheDir)
	})
}

// Fix dispatches a fix box for the given 1-based pass number. resumeAfterHold
// is ignored: FIX_PASS>0 already resumes the session, so a transient-backoff
// re-dispatch mid-fix needs no extra signal.
func (d *Dispatch) Fix(pass int, ciFailureSummary string) Result {
	logPath := d.fixLogPath(pass)
	return d.dispatchWithRetry(logPath, func(_ bool) error {
		fmt.Fprint(d.humanOut(), announceLine(d.number, fmt.Sprintf("fix-pass-%d", pass), d.title))
		env, err := buildBoxEnv(d.cfg, d.number, d.title, pass, ciFailureSummary, d.nonce)
		if err != nil {
			return err
		}
		return d.runOnce(logPath, env, d.cacheDir)
	})
}

// ResolveConflict dispatches a conflict-resolution box against pr.
// CONFLICT_RESOLVE_PR_URL puts the entrypoint in conflict-resolve mode: it
// resolves the rebase conflict, publishes the branch (pushed directly, else
// bundled to the outbox for the launcher to relay, issue #1979), and exits
// without the main agent prompt, so it needs neither retry nor driver cache.
func (d *Dispatch) ResolveConflict(pr string) error {
	fmt.Fprint(d.humanOut(), announceLine(d.number, "conflict-resolve", d.title))
	env, err := buildBoxEnv(d.cfg, d.number, d.title, 0, "", d.nonce)
	if err != nil {
		return err
	}
	env["CONFLICT_RESOLVE_PR_URL"] = pr
	return d.runOnce(d.conflictLogPath(), env, "")
}

// announceLine builds the one line of human-facing output announcing a
// dispatched Box for number, shared by Run, Fix, and ResolveConflict so the
// three call sites cannot drift out of sync with each other. phase is the
// parenthesized suffix ("", "fix-pass-N", or "conflict-resolve") — not a
// daemon.Kind, which the rest of this branch means by "kind"; "" omits the
// parens entirely.
//
// This is the only channel naming the dispatched issue (issue #3538): a
// second reader, internal/daemon's ParseAnnouncedIssue, parses this exact
// shape back out of a child launcher's stdout. TestAnnounceLine_ParsesBack
// (internal/dispatch/announce_test.go) feeds this function's own output
// through that parser, so an edit here that breaks the pairing fails in the
// package that owns the format, not silently in the daemon.
func announceLine(number, phase, title string) string {
	if phase == "" {
		return fmt.Sprintf("    -> #%s: %s\n", number, title)
	}
	return fmt.Sprintf("    -> #%s (%s): %s\n", number, phase, title)
}

// humanOut is the human-facing sink for this Dispatch: the heartbeat writer
// and each dispatch-start announce line write here (issue #1829). The console
// discards it via Factory.SetHeartbeatOut so a console-driven dispatch never
// scribbles over the TUI frame; every other caller gets stdout.
func (d *Dispatch) humanOut() io.Writer {
	if d.cfg.HeartbeatOut == nil {
		return os.Stdout
	}
	return d.cfg.HeartbeatOut
}

// Close evicts this issue's driver-cache entry.
func (d *Dispatch) Close() {
	d.cache.evict(d.number)
}

// runOnce opens logPath fresh, dispatches one box with env, and blocks until
// it exits. A log already at logPath is rotated aside so os.Create cannot
// truncate it away (issue #561). If a container or sandbox named for this
// issue is already live per Runner.IsRunning's contract (issue #3633) — a
// live run (possibly orphaned by a killed launcher) owns that log, so
// runOnce returns ErrAlreadyRunning first (#562).
func (d *Dispatch) runOnce(logPath string, env map[string]string, driverCacheDir string) error {
	if d.isKilled() {
		return errKilled
	}
	name := BoxName(d.number)
	if d.runner.IsRunning(name) {
		return runner.ErrAlreadyRunning
	}

	if err := rotateStaleLog(logPath); err != nil {
		return fmt.Errorf("rotate stale log: %w", err)
	}

	logFile, err := os.Create(logPath)
	if err != nil {
		return fmt.Errorf("create log: %w", err)
	}
	defer logFile.Close()

	// A HostMediatedRemote backend always needs an outbox (ADR 0033,
	// CODE_FORGE=local); an OutboxRelayCapable one needs it only under
	// BOX_FORGE_AND_ISSUE_ACCESS=read-only (issue #1918). Every other
	// combination skips .spindrift/outbox/<num> rather than leaving an
	// empty directory behind on every dispatch.
	var outboxDir string
	if needsOutbox(d.cfg) {
		outboxDir = OutboxDirFor(d.pwd, d.number)
		if err := resetOutboxDir(outboxDir); err != nil {
			return fmt.Errorf("reset outbox dir: %w", err)
		}
	}

	// The per-Box registry-credential proxy (ADR 0044, issue #2849) lives
	// exactly as long as this Run call, never shared across Boxes the way
	// the runner.Runner adapter is. An empty RegistryProxyRoutes leaves the
	// feature off entirely: no directory, no listener, no probe, no socket
	// path on box.
	var registryProxyLocation runner.RegistryProxyLocation
	if len(d.cfg.RegistryProxyRoutes) > 0 {
		// Rewrite rows come from ecosystem.Table, not d.cfg: which response
		// shapes get rewritten is static per-ecosystem knowledge, not
		// something a run resolves per-route the way routes are.
		handler, err := registryproxy.New(d.cfg.RegistryProxyRoutes, ecosystem.ResponseRewriteRows())
		if err != nil {
			return fmt.Errorf("registry proxy: %w", err)
		}
		proxy := &registryproxy.Proxy{Handler: handler}

		// The runner probes the transport live (issue #3111): a unix socket
		// that cannot cross into the guest (a remote-context docker/podman, a
		// VM-backed runtime) needs the loopback-TCP fallback, so the transport
		// can never be inferred from GOOS. The probe returns only the kind and
		// host; this dispatch mints the real path or port below.
		transport, tcpAddHost, err := d.runner.RegistryProxyTransport()
		if err != nil {
			return fmt.Errorf("registry proxy: %w", err)
		}

		// manifestEndpoint is what REGISTRY_PROXY_MANIFEST carries, and is
		// deliberately not always registryProxyLocation.Endpoint: that one is
		// the mount SOURCE (a host path buildMountSpecs reads), while a
		// Box-side reader can only dial the mount TARGET, a host path being
		// meaningless inside the Box. See runner.RegistryProxySocketTarget.
		var manifestEndpoint registrymanifest.Endpoint

		switch {
		case transport.IsUnix():
			proxyDir, err := registryProxySocketDir()
			if err != nil {
				return fmt.Errorf("registry proxy: %w", err)
			}
			defer os.RemoveAll(proxyDir)

			socketPath := filepath.Join(proxyDir, registryProxySocketFile)
			if err := proxy.ListenAndServe(socketPath); err != nil {
				return fmt.Errorf("registry proxy: %w", err)
			}
			registryProxyLocation = runner.RegistryProxyLocation{Endpoint: registrymanifest.NewUnixEndpoint(socketPath)}
			manifestEndpoint = registrymanifest.NewUnixEndpoint(runner.RegistryProxySocketTarget)
		case transport.IsTCP():
			tcpHost := transport.Host()
			secret := newRegistryProxyTCPSecret()
			// The listener binds every interface, not loopback (issue #3111
			// review finding): the Box reaches it only via --add-host
			// <host>:host-gateway, which on a plain Linux docker bridge
			// resolves to the bridge IP (e.g. 172.17.0.1), so a loopback-only
			// bind leaves nothing on the address the Box dials.
			if err := proxy.ListenAndServeTCP("0.0.0.0:0", secret); err != nil {
				return fmt.Errorf("registry proxy: %w", err)
			}
			tcpAddr, ok := proxy.Addr().(*net.TCPAddr)
			if !ok {
				return fmt.Errorf("registry proxy: TCP listener address %v is not a *net.TCPAddr", proxy.Addr())
			}
			registryProxyLocation = runner.RegistryProxyLocation{
				Endpoint:   registrymanifest.NewTCPEndpoint(tcpHost, strconv.Itoa(tcpAddr.Port)),
				TCPSecret:  secret,
				TCPAddHost: tcpAddHost,
			}
			// The Box dials the TCP endpoint directly, no mount involved, so
			// the bound host:port is what a Box-side reader connects to and
			// the two endpoints agree.
			manifestEndpoint = registryProxyLocation.Endpoint

			// The host and port that bind-registry needs travel inside
			// REGISTRY_PROXY_MANIFEST's endpoint field (ADR 0045), but
			// REGISTRY_PROXY_TCP_SECRET stays its own env var: it is a
			// bearer-token-shaped credential, so bwrap.go's offArgvKeys keeps
			// it off argv and ADR 0045 keeps it out of the manifest.
			env["REGISTRY_PROXY_TCP_SECRET"] = secret
		default:
			return fmt.Errorf("registry proxy: transport probe returned neither a unix nor a tcp endpoint")
		}

		manifest := registrymanifest.Manifest{
			Endpoint: manifestEndpoint,
			Routes:   registryManifestRoutes(d.cfg.RegistryProxyRoutes),
		}
		encoded, err := registrymanifest.Encode(manifest)
		if err != nil {
			return fmt.Errorf("registry proxy: %w", err)
		}
		env[registrymanifest.EnvVar] = encoded

		defer proxy.Close()
	}

	box := runner.Box{
		Issue:             d.number,
		Name:              name,
		Env:               env,
		Output:            d.driver.NewHeartbeatWriter(logFile, d.number, d.humanOut(), driverkit.RenderOptions{}),
		DriverCacheDir:    driverCacheDir,
		OutboxDir:         outboxDir,
		RegistryProxy:     registryProxyLocation,
		ClosureGeneration: d.agentGeneration,
	}
	return d.runner.Run(box)
}

// newRegistryProxyTCPSecret mints a fresh per-run secret (issue #3111) gating
// the registry proxy's loopback TCP fallback
// (registrymanifest.TCPSecretHeader): 16 crypto/rand bytes, hex-encoded. It
// must never share a value with newNonce (factory.go, issue #1937), which has
// a different security role. rand.Read fails only on a broken entropy source.
func newRegistryProxyTCPSecret() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("dispatch: crypto/rand.Read failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// registryManifestRoutes projects Config.RegistryProxyRoutes into the ADR-0045
// manifest's Route shape. It carries Prefix verbatim: AssignPrefixes already
// ran over the table. An Upstream that fails to parse keeps its route with an
// empty UpstreamHost. EnforcedPaths clones EnforcedSubtrees so it cannot alias
// route's array (issue #3259); Ecosystems aliases, a map-of-maps clone being shallow.
func registryManifestRoutes(routes []registryproxy.Route) []registrymanifest.Route {
	out := make([]registrymanifest.Route, len(routes))
	for i, route := range routes {
		var upstreamHost string
		if u, err := url.Parse(route.Upstream); err == nil {
			upstreamHost = u.Host
		}
		out[i] = registrymanifest.Route{
			Prefix:        route.Prefix,
			UpstreamHost:  upstreamHost,
			EnforcedPaths: slices.Clone(route.EnforcedSubtrees),
			Ecosystems:    route.Ecosystems,
		}
	}
	return out
}

// registryProxySocketFile is shared by registryProxySocketDir's length check
// and runOnce's bind path so the two joins cannot drift apart.
const registryProxySocketFile = "proxy.sock"

const spindriftRegistryProxyDirPattern = "spindrift-registry-proxy-*"

// registryProxyMkdirTemp and registryProxyRemoveAll are swappable in tests the
// way statCgroupControllerFile is (runner/validate.go, issue #3103): the
// RemoveAll failure branch below is reachable only through an
// EACCES/EROFS/EBUSY-class error no test can provoke deterministically.
var (
	registryProxyMkdirTemp = os.MkdirTemp
	registryProxyRemoveAll = os.RemoveAll
)

// mkProxyDir creates a fresh, unique directory for the registry proxy's
// unix socket under base ("" means os.MkdirTemp's own default, os.TempDir()).
func mkProxyDir(base string) (string, error) {
	dir, err := registryProxyMkdirTemp(base, spindriftRegistryProxyDirPattern)
	if err != nil {
		return "", fmt.Errorf("mktemp registry proxy dir under %q: %w", base, err)
	}
	return dir, nil
}

// registryProxySocketDir returns a fresh directory for the registry proxy's
// unix socket, preferring os.TempDir() but falling back to /tmp when appending
// "proxy.sock" would overflow the platform's AF_UNIX sun_path limit (issue
// #3077), as macOS's $TMPDIR under nix develop's nix-shell.XXXXXX/ prefix
// does. Any other os.MkdirTemp failure is returned as-is, never rerouted.
func registryProxySocketDir() (string, error) {
	dir, err := mkProxyDir("")
	if err != nil {
		return "", err
	}
	if !unixsocket.TooLong(filepath.Join(dir, registryProxySocketFile)) {
		return dir, nil
	}
	if err := registryProxyRemoveAll(dir); err != nil {
		return "", fmt.Errorf("remove over-long registry proxy dir: %w", err)
	}

	// A too-long path from this fallback is ListenAndServe's error to raise:
	// it already names the platform, the cap, and the byte length (issue
	// #3077), so a second message here would only drift out of sync.
	return mkProxyDir("/tmp")
}

// needsOutbox reports whether cfg's dispatch needs a writable per-issue outbox
// directory: HostMediatedRemote unconditionally (ADR 0033, CODE_FORGE=local),
// or OutboxRelayCapable under BOX_FORGE_AND_ISSUE_ACCESS=read-only (issue
// #1918), where the harness bundles the Box's finished branch to seam.bundle
// post-driver (issue #2082) for the launcher's BundleRelay to pick up.
func needsOutbox(cfg Config) bool {
	return cfg.ForgeDescriptor.HostMediatedRemote ||
		(cfg.ForgeDescriptor.OutboxRelayCapable && cfg.BoxForgeAndIssueAccess == "read-only")
}

// resetOutboxDir empties dir and recreates it: the writable outbox mount must
// start empty every dispatch (ADR 0033), and buildMountSpecs only produces the
// mount when the source directory exists. The 0o777 mode lets the Box's
// uid-1000 agent write regardless of rootless uid remapping (issue #1723), and
// the explicit Chmod is needed because MkdirAll's mode passes through the umask.
func resetOutboxDir(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return err
	}
	return os.Chmod(dir, 0o777)
}

// rotateStaleLog renames an existing file at logPath aside to the first
// available logPath.N suffix, so a subsequent os.Create(logPath) starts
// clean without destroying it. A missing logPath is a no-op.
func rotateStaleLog(logPath string) error {
	if _, err := os.Stat(logPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for n := 1; ; n++ {
		candidate := fmt.Sprintf("%s.%d", logPath, n)
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return os.Rename(logPath, candidate)
		}
	}
}

// quarantineErr wraps a quarantinePriorRunLogs failure so dispatchWithRetry
// (retry.go) can tell it apart from other once() failures via errors.As:
// nothing this run produced is necessarily at logPath yet, so the caller must
// not consult settledOutcome or ClassifyTransient against it, since either
// could settle on the prior-run content quarantine failed to move (#2575).
type quarantineErr struct{ err error }

func (e quarantineErr) Error() string { return e.err.Error() }
func (e quarantineErr) Unwrap() error { return e.err }

// quarantinePriorRunLogs renames every attempt log AllAttemptLogPaths finds
// for this issue to "<path>.prior-run.N", a suffix its own "<path>.N" probe
// never matches, so an earlier run's spend cannot fold into this run's usage
// comment and budget gate (issue #2575). Skipped under the same IsRunning
// guard as runOnce — live per Runner.IsRunning's contract (issue #3633) —
// though IsRunning cannot see a run paused between attempts (issue #562).
func quarantinePriorRunLogs(pwd, number string, r runner.Runner) error {
	// A nil runner comes only from a test double that never wants the runner
	// touched; treat it as nothing running so quarantine proceeds instead of
	// panicking on a nil interface call.
	if r != nil && r.IsRunning(BoxName(number)) {
		return nil
	}
	for _, pl := range AllAttemptLogPaths(pwd, number) {
		for n := 1; ; n++ {
			dest := fmt.Sprintf("%s.prior-run.%d", pl.Path, n)
			_, err := os.Stat(dest)
			if err == nil {
				continue
			}
			// A stat failure other than not-found (EACCES on the log dir,
			// ENAMETOOLONG) never yields os.IsNotExist, so treating it as
			// a free slot would spin this n++ loop without a cap (#2575).
			if !os.IsNotExist(err) {
				return fmt.Errorf("stat %s: %w", dest, err)
			}
			if err := os.Rename(pl.Path, dest); err != nil {
				return err
			}
			break
		}
	}
	return nil
}

// runLineageMarkerPath returns the sentinel file Run() drops right after its
// quarantinePriorRunLogs call, so a caller that never went through Run()
// (main.go's recoverByNumber, adopting an open PR) can tell pass logs
// quarantined at the start of this logical run from ones no Run() ever
// quarantined (issue #2575). It never matches AllAttemptLogPaths' pattern.
func runLineageMarkerPath(pwd, number string) string {
	return filepath.Join(HostLogDirFor(pwd), "issue-"+number+".run-lineage")
}

// markRunLineage (re)creates this issue's run-lineage marker, truncating any
// stale marker a prior logical run left behind.
func markRunLineage(pwd, number string) error {
	f, err := os.Create(runLineageMarkerPath(pwd, number))
	if err != nil {
		return err
	}
	return f.Close()
}

// EnsureRunLineage gives a Dispatch that reaches Fix, CumulativeUsage, or
// UsageReport without Run() (main.go's recoverByNumber adopting an open PR)
// the same guarantee Run's quarantinePriorRunLogs call makes (issue #2575).
// The marker is normally already there. A missing one (an orphaned PR, or a
// pre-#2575 log dir) quarantines everything so the count starts from zero.
func (d *Dispatch) EnsureRunLineage() error {
	if fileExists(runLineageMarkerPath(d.pwd, d.number)) {
		return nil
	}
	if err := quarantinePriorRunLogs(d.pwd, d.number, d.runner); err != nil {
		return fmt.Errorf("quarantine prior-run logs: %w", err)
	}
	return markRunLineage(d.pwd, d.number)
}

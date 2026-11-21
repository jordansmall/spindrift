package dispatch

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/hostop"
	"spindrift.dev/launcher/internal/hostpaths"
	"spindrift.dev/launcher/internal/panicguard"
	"spindrift.dev/launcher/internal/passmanifest"
	"spindrift.dev/launcher/internal/registrymanifest"
	"spindrift.dev/launcher/internal/registryproxy"
	"spindrift.dev/launcher/internal/report"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/signalsocket"
	"spindrift.dev/launcher/internal/unixsocket"
)

// Dispatch is the per-issue execution object: every Box launched for one
// issue, from claim to verdict, plus its driver-cache entry. Construct one
// via Factory.New.
type Dispatch struct {
	// number is this Dispatch's log/lock/cache/BoxName key: always
	// subject.key.String(), set once by Factory.newDispatch (issue #3954).
	number   string
	pwd      string
	runner   runner.Runner
	driver   driver.Driver
	clock    Clock
	cfg      Config
	cacheDir string
	cache    *cache

	// warnings accumulates RecordWarnings batches across the initial run and
	// its fix passes.
	warnings []string

	// nonce is this Dispatch's per-run nonce (issue #1937), minted by
	// Factory.New, forwarded into every Box as RUN_NONCE, and kept here so
	// outcomeResult's mid-run signal-line scanners can check a log line
	// against it.
	nonce string

	// agentGeneration is the agent-closure generation snapshot taken at
	// New()-time (issue #2682), forwarded into every Box as ClosureGeneration;
	// nil means runner.Box's own default. Snapshotted once rather than read
	// live so a Fix() launched minutes later (settle/pr_lander.go, after a Box
	// idles awaiting CI) stays on the generation this Dispatch started on.
	agentGeneration *runner.AgentGeneration

	// killed is this issue's kill latch, minted by Factory.New and closed by
	// Factory.Kill (issue #3521); nil for a Dispatch built without a Factory.
	killed <-chan struct{}

	// signalBuffer holds this attempt's Signal socket buffer (ADR 0052, issue
	// #3725), set by runOnce only under BOX_SIGNAL_CARRIER=socket, for
	// outcomeResult to read once the Box exits; nil under the log carrier.
	signalBuffer *signalsocket.Buffer

	// attemptLog is the identity of the log the current attempt created, so
	// reclaimAttemptLog can find it after a mid-run rename (issue #3886).
	attemptLog os.FileInfo

	// subject is what this Dispatch's Box works (ADR 0056, issue #3875): a
	// tracker issue or a Factory.NewChore Chore, and the sole source of the
	// Dispatch's key and title (issue #3954, #3988).
	subject subject

	// releaseClaim drops the claim Run took; only Close calls it, so the
	// claim spans the caller's settle too (issue #4364). Nil until Run
	// claims, and again after Close.
	releaseClaim func()

	// recordID and claimTime identify this Dispatch's Record (issue #4783),
	// stamped into every Pass log and box report so a retry's rotated log, a
	// fix pass and a conflict resolve all land on one Record. Run mints them
	// fresh: the primary log on disk belongs to the Record being replaced,
	// which quarantinePriorRunLogs moves aside. A Dispatch that never Ran
	// (recover adopting an open PR) continues the prior Record from the
	// primary log's stamp (ensureRecordID).
	recordID  string
	claimTime time.Time

	// attemptStampLen is the byte length of the dispatch_start stamp the
	// current attempt wrote at the head of its log, so logIsEmpty can still
	// tell a Box that never produced output from one that ran.
	attemptStampLen int64

	// decodedLogs memoizes the Pass logs the host decodes for its post-attempt
	// reads (see decoded).
	decodedLogs decodedLogs
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
	panicguard.Go(func() {
		d.clock.Sleep(dur)
		close(slept)
	})
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
	return hostpaths.OutboxDir(pwd, number)
}

// ReadPasses reads number's pass manifest from its outbox: the one read both
// a run's result and `spindrift recover` use. A missing manifest is not an
// error (passmanifest.Read).
func ReadPasses(pwd, number string) ([]passmanifest.Entry, error) {
	return passmanifest.Read(filepath.Join(OutboxDirFor(pwd, number), passmanifest.FileName))
}

// HostLogDirFor returns the host-side log directory for a working dir, the
// single source of truth for `<pwd>/.spindrift/logs` so it cannot drift.
func HostLogDirFor(pwd string) string {
	return hostpaths.LogDir(pwd)
}

// LogPathFor returns the host path of number's primary Pass log, the file
// settle appends the dispatch_settled op to.
func LogPathFor(pwd, number string) string {
	return logPathFor(pwd, number)
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

// Run dispatches the initial box for this issue. It claims the issue before
// touching disk (issue #3885): IsRunning cannot see another launcher's
// container still being created, so without the claim a racing Run() would
// quarantine that run's live log. The IsRunning guards still catch a
// container orphaned by a killed launcher, whose claim died with it. The
// claim is held through the caller's settle until Close (issue #4364);
// callers that Run must Close.
func (d *Dispatch) Run() Disposition {
	release, err := ClaimIssue(d.pwd, d.number)
	if err != nil {
		if errors.Is(err, ErrIssueClaimed) {
			return Skipped()
		}
		fmt.Fprintf(os.Stderr, "    ?? #%s: %v\n", d.number, err)
		return Failed(Result{})
	}
	d.releaseClaim = release
	d.mintRecordID()

	logPath := d.logPath()
	return d.dispatchWithRetry(logPath, func(resumeAfterHold bool) error {
		d.announce(report.PhaseInitial, logPath)
		if !resumeAfterHold && !d.runner.IsRunning(BoxName(d.number)) {
			// First attempt only, under the same guard as
			// quarantinePriorRunLogs so a live run's log dir gets neither a
			// rename nor a marker. An existing marker is no reason to skip: a
			// fresh Run() makes any marker on disk stale.
			if err := quarantinePriorRunLogs(d.pwd, d.number, d.runner); err != nil {
				return preDispatchErr{err: fmt.Errorf("quarantine prior-run logs: %w", err)}
			}
			if err := markRunLineage(d.pwd, d.number); err != nil {
				return preDispatchErr{err: fmt.Errorf("mark run lineage: %w", err)}
			}
		}
		env, err := d.boxEnv(0, "")
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
func (d *Dispatch) Fix(pass int, ciFailureSummary string) Disposition {
	d.ensureRecordID()
	logPath := d.fixLogPath(pass)
	return d.dispatchWithRetry(logPath, func(_ bool) error {
		d.announce(report.PhaseFixPass(pass), logPath)
		env, err := d.boxEnv(pass, ciFailureSummary)
		if err != nil {
			return err
		}
		return d.runOnce(logPath, env, d.cacheDir)
	})
}

// ResolveConflict dispatches a conflict-resolution box against pr.
// CONFLICT_RESOLVE_PR_URL puts the Box in conflict-resolve mode: it
// resolves the rebase conflict, publishes the branch (pushed directly, else
// bundled to the outbox for the launcher to relay, issue #1979), and exits
// without the main agent prompt, so it needs neither retry nor driver cache.
func (d *Dispatch) ResolveConflict(pr string) error {
	d.ensureRecordID()
	logPath := d.conflictLogPath()
	d.announce(report.PhaseConflictResolve, logPath)
	env, err := d.boxEnv(0, "")
	if err != nil {
		return err
	}
	env["CONFLICT_RESOLVE_PR_URL"] = pr
	return d.runOnce(logPath, env, "")
}

// boxEnv is buildBoxEnv plus the Dispatch's own facts. It forwards the Record
// ID (issue #4786) so the Box can key the prompt_hashes op it emits to this
// Dispatch's Record; callers run it after the Record ID is set.
func (d *Dispatch) boxEnv(fixPass int, ciFailureSummary string) (map[string]string, error) {
	env, err := buildBoxEnv(d.cfg, d.subject, fixPass, ciFailureSummary, d.nonce)
	if err != nil {
		return nil, err
	}
	if d.recordID != "" {
		env["RECORD_ID"] = d.recordID
	}
	return env, nil
}

// announce prints the human announce line for phase and emits the matching
// report.Box record (issue #3627), so the two cannot drift apart. phase is
// report's vocabulary (report.PhaseInitial and friends).
//
// The record carries logPath relative to the checkout. It is the un-suffixed
// path even when a stale log is rotated aside: the name stays fixed per issue
// (see docs/reference.md).
func (d *Dispatch) announce(phase, logPath string) {
	fmt.Fprint(os.Stdout, announceLine(d.number, humanPhase(phase), d.subject.title))
	rel, err := filepath.Rel(d.pwd, logPath)
	if err != nil {
		// rel is "": the record then names no Pass log.
		fmt.Fprintf(os.Stderr, "    ?? #%s: pass log path: %v\n", d.number, err)
	}
	report.Box(d.subject.key, phase, rel, d.recordID)
}

// mintRecordID mints a fresh Record ID unless the Dispatch already has one,
// stamping the claim time from the clock unless NewChore preset it.
func (d *Dispatch) mintRecordID() {
	if d.recordID != "" {
		return
	}
	if d.claimTime.IsZero() {
		d.claimTime = d.clock.Now()
	}
	d.recordID = dispatchrecord.RecordID(d.cfg.kindName(), d.number, d.claimTime)
}

// ensureRecordID gives Fix and ResolveConflict a Record ID, continuing the
// primary log's stamp when there is one. An unreadable log warns: minting
// fresh there would split the Record the stamp names.
func (d *Dispatch) ensureRecordID() {
	if d.recordID != "" {
		return
	}
	rec, _, err := dispatchrecord.ParseLog(d.logPath())
	if err == nil && rec.Attribution == dispatchrecord.AttributionStamped {
		d.recordID, d.claimTime = rec.ID, rec.ClaimTime
		return
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, dispatchrecord.ErrEmptyLog) && !errors.Is(err, dispatchrecord.ErrUnstamped) {
		fmt.Fprintf(os.Stderr, "    ?? #%s: primary log %s unreadable, minting a fresh Record ID: %v\n", d.number, d.logPath(), err)
	}
	d.mintRecordID()
}

// RecordID returns the Dispatch's Record ID, "" until Run, Fix, or
// ResolveConflict gives it one.
func (d *Dispatch) RecordID() string {
	return d.recordID
}

// LogPath returns the Dispatch's primary Pass log.
func (d *Dispatch) LogPath() string {
	return d.logPath()
}

// dispatchStart builds the stamp heading a fresh Pass log: the launcher's
// deployment facts from Config.Stamp plus this Dispatch's identity.
func (d *Dispatch) dispatchStart() hostop.DispatchStart {
	s := d.cfg.Stamp
	s.RecordID = d.recordID
	s.Kind = d.cfg.kindName()
	s.DispatchKey = d.number
	s.ClaimTime = d.claimTime
	s.Started = d.clock.Now()
	s.Driver = d.driver.Name()
	// Host-only: the Box can write its log, so a dispatch_settled op counts
	// only when it echoes this token (issue #4812).
	s.HostToken = newNonce()
	return s
}

// humanPhase maps report's phase vocabulary onto announceLine's
// parenthesized-suffix vocabulary: the initial Box names no phase in the
// human line, and every other phase passes through unchanged.
func humanPhase(phase string) string {
	if phase == report.PhaseInitial {
		return ""
	}
	return phase
}

// announceLine builds the human announce line for a dispatched Box. phase is
// the parenthesized suffix ("", "fix-pass-N", or "conflict-resolve"); ""
// omits the parens. The line is operator-visible text, not a wire format: the
// machine channel is the report record announce emits alongside it (issue
// #3627).
func announceLine(number, phase, title string) string {
	if phase == "" {
		return fmt.Sprintf("    -> #%s: %s\n", number, title)
	}
	return fmt.Sprintf("    -> #%s (%s): %s\n", number, phase, title)
}

// Close evicts this issue's driver-cache entry and releases the claim Run
// took, if any. Safe to call more than once.
func (d *Dispatch) Close() {
	d.cache.evict(d.number)
	if d.releaseClaim != nil {
		d.releaseClaim()
		d.releaseClaim = nil
	}
}

// runOnce opens logPath fresh, dispatches one box with env, and blocks until
// it exits. A log already at logPath is rotated aside so os.Create cannot
// truncate it away (issue #561). A Box already live for this issue per
// Runner.IsRunning (issue #3633) owns that log, so runOnce returns
// ErrAlreadyRunning first (#562).
func (d *Dispatch) runOnce(logPath string, env map[string]string, driverCacheDir string) error {
	// Reset before any listener starts: a killed or retried attempt's
	// buffer must never leak into a later attempt's Result (issue #3725).
	d.signalBuffer = nil

	if d.isKilled() {
		return errKilled
	}
	name := BoxName(d.number)
	if d.runner.IsRunning(name) {
		return runner.ErrAlreadyRunning
	}

	if err := rotateStaleLog(logPath); err != nil {
		return preDispatchErr{err: fmt.Errorf("rotate stale log: %w", err)}
	}

	logFile, err := os.Create(logPath)
	if err != nil {
		return fmt.Errorf("create log: %w", err)
	}
	defer logFile.Close()
	// The stamp is the log's very first line, ahead of any Box output, and
	// every fresh log carries one: a retry rotates the previous log aside, so
	// a claim-time write alone would be lost.
	start := d.dispatchStart()
	stamp := hostop.EncodeSpindriftOp(hostop.SpindriftOp{Op: hostop.OpDispatchStart, Start: &start})
	if _, err := logFile.WriteString(stamp); err != nil {
		return fmt.Errorf("write dispatch_start: %w", err)
	}
	d.attemptStampLen = int64(len(stamp))
	if info, statErr := logFile.Stat(); statErr == nil {
		d.attemptLog = info
	}

	// Unconditional: the stream view is byte-transparent under the log carrier.
	boxLog := newBoxLog(logFile)
	defer func() { _ = boxLog.flush() }()

	// A HostMediatedRemote or RelayCapable backend needs an outbox
	// (backend.RelaysBundle; ADR 0033, issue #1918). Any other forge skips
	// .spindrift/outbox/<num> rather than leaving an empty directory behind
	// on every dispatch.
	var outboxDir string
	if needsOutbox(d.cfg) {
		outboxDir = OutboxDirFor(d.pwd, d.number)
		if err := resetOutboxDir(outboxDir); err != nil {
			return fmt.Errorf("reset outbox dir: %w", err)
		}
	}

	var mirrorDir string
	if d.cfg.SeedMirror != nil {
		dir, release, err := d.cfg.SeedMirror()
		if err != nil {
			return fmt.Errorf("seed mirror: %w", err)
		}
		defer release()
		mirrorDir = dir
	}

	// The per-Box registry-credential proxy (ADR 0044, issue #2849) lives
	// exactly as long as this Run call, never shared across Boxes. An empty
	// RegistryProxyRoutes leaves it off entirely: no directory, listener,
	// probe, or socket.
	var registryProxyLocation runner.RegistryProxyLocation
	// Unix-verdict socket mounts for both the registry proxy and the Signal
	// socket (ADR 0052, issue #3725), gated on the one transport probe below.
	var boxSockets []runner.SocketMount
	// TCP verdict only; under unix the socket travels through boxSockets.
	var signalSocketLocation runner.SignalSocketLocation

	needRegistryProxy := len(d.cfg.RegistryProxyRoutes) > 0
	needSignalSocket := d.cfg.signalCarrierSocket()

	// Routes are validated before the probe: a malformed-routes value is a
	// static configuration mistake, so it must not wait on -- or lose its
	// error to -- a container-runtime probe subprocess.
	var proxy *registryproxy.Proxy
	if needRegistryProxy {
		handler, err := registryproxy.New(d.cfg.RegistryProxyRoutes, d.cfg.RegistryProxyRewriteRows)
		if err != nil {
			return fmt.Errorf("registry proxy: %w", err)
		}
		proxy = &registryproxy.Proxy{Handler: handler}
	}

	// The runner probes the transport live at most once per Dispatch (issue
	// #3111, ADR 0052): a unix socket that cannot cross into the guest (a
	// remote-context docker/podman, a VM-backed runtime) needs the TCP
	// fallback, so the transport can never be inferred from GOOS.
	var transport registrymanifest.Endpoint
	var tcpAddHost bool
	if needRegistryProxy || needSignalSocket {
		t, addHost, err := d.runner.RegistryProxyTransport()
		if err != nil {
			// The registry-proxy wrapping is pinned by existing tests; a
			// probe taken for the Signal socket alone reports as its own.
			if needRegistryProxy {
				return fmt.Errorf("registry proxy: %w", err)
			}
			return fmt.Errorf("signal socket: %w", err)
		}
		transport, tcpAddHost = t, addHost
	}

	if needRegistryProxy {
		// manifestEndpoint is what REGISTRY_PROXY_MANIFEST carries, and is
		// deliberately not always registryProxyLocation.Endpoint (the host-side
		// address): under unix a Box-side reader can only dial the mount
		// TARGET, a host path being meaningless inside the Box.
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
			boxSockets = append(boxSockets, runner.SocketMount{Source: socketPath, Target: runner.RegistryProxySocketTarget})
		case transport.IsTCP():
			// Backstop for a TCP verdict the probe never returns, nor its
			// cache replays (#3775), under a denying mode: on docker
			// no-host-loopback renders as plain bridge, so the route would
			// otherwise silently work against the operator's mode (ADR 0044).
			if d.cfg.NetworkMode.DeniesHostLoopback() {
				return fmt.Errorf("registry proxy: unsupported under NETWORK_MODE=%s -- "+tcpFallbackBlockedReason+"; use a different NETWORK_MODE or drop REGISTRY_PROXY_ROUTES", d.cfg.NetworkMode, "registry proxy")
			}
			tcpHost := transport.Host()
			secret := newRegistryProxyTCPSecret()
			// Binds every interface, not loopback (issue #3111): on a plain
			// Linux docker bridge the host name the Box dials resolves to the
			// bridge IP (e.g. 172.17.0.1). registrymanifest.TCPSecretHeader is
			// the sole access control; ADR 0044's issue #3772 amendment
			// records why and what bounds it.
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
			manifestEndpoint = registryProxyLocation.Endpoint

			// The secret stays its own env var, out of the manifest (ADR
			// 0045): it is a bearer credential, which offargv.go's
			// OffArgvKeys keeps off argv.
			env["REGISTRY_PROXY_TCP_SECRET"] = secret
		default:
			return fmt.Errorf("registry proxy: transport probe returned neither a unix nor a tcp endpoint")
		}

		manifest := registrymanifest.Manifest{
			Endpoint: manifestEndpoint,
			Routes:   registryproxy.ManifestRoutes(d.cfg.RegistryProxyRoutes),
		}
		encoded, err := registrymanifest.Encode(manifest)
		if err != nil {
			return fmt.Errorf("registry proxy: %w", err)
		}
		env[registrymanifest.EnvVar] = encoded

		defer proxy.Close()
	}

	if needSignalSocket {
		mount, loc, closeSignalSocket, err := d.startSignalSocket(transport, tcpAddHost, env, boxLog.mirror())
		if err != nil {
			return err
		}
		if mount != nil {
			boxSockets = append(boxSockets, *mount)
		}
		signalSocketLocation = loc
		if closeSignalSocket != nil {
			defer closeSignalSocket()
		}
	}

	renderOpts := driverkit.RenderOptions{
		OnModel: func(model, role string) { report.Model(d.subject.key, model, role) },
	}
	box := runner.Box{
		Issue:             d.number,
		Name:              name,
		Env:               env,
		Output:            d.driver.NewHeartbeatWriter(boxLog.stream(), d.number, os.Stdout, renderOpts),
		DriverCacheDir:    driverCacheDir,
		OutboxDir:         outboxDir,
		MirrorDir:         mirrorDir,
		RegistryProxy:     registryProxyLocation,
		Sockets:           boxSockets,
		SignalSocket:      signalSocketLocation,
		ClosureGeneration: d.agentGeneration,
	}
	return d.runner.Run(box)
}

// newRegistryProxyTCPSecret mints a fresh per-run secret (issue #3111) gating
// the registry proxy's every-interface TCP fallback. It must never share a
// value with newNonce (issue #1937), which has a different security role.
func newRegistryProxyTCPSecret() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("dispatch: crypto/rand.Read failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// registryProxySocketFile is shared by registryProxySocketDir's length check
// and runOnce's bind path so the two joins cannot drift apart.
const registryProxySocketFile = "proxy.sock"

const spindriftRegistryProxyDirPattern = "spindrift-registry-proxy-*"

const signalSocketFile = "signal.sock"
const spindriftSignalSocketDirPattern = "spindrift-signal-socket-*"

// socketDirMkdirTemp and socketDirRemoveAll are swapped in tests: the
// RemoveAll failure branch in socketDirWithFallback is reachable only through
// an EACCES/EROFS/EBUSY-class error no test can provoke deterministically.
// socketDirRemoveAll covers only that branch; every other removal of these
// directories calls os.RemoveAll directly.
var (
	socketDirMkdirTemp = os.MkdirTemp
	socketDirRemoveAll = os.RemoveAll
)

// mkSocketDir creates a fresh directory for a launcher-owned unix socket under
// base ("" means os.TempDir()); label names the caller in error text.
func mkSocketDir(base, pattern, label string) (string, error) {
	dir, err := socketDirMkdirTemp(base, pattern)
	if err != nil {
		return "", fmt.Errorf("mktemp %s dir under %q: %w", label, base, err)
	}
	return dir, nil
}

// socketDirWithFallback returns a fresh directory for socketFile, preferring
// os.TempDir() but falling back to /tmp when the path would overflow the
// AF_UNIX sun_path limit (issue #3077), as macOS's $TMPDIR under nix
// develop's nix-shell.XXXXXX/ prefix does. Any other os.MkdirTemp failure is
// returned as-is, never rerouted.
func socketDirWithFallback(pattern, socketFile, label string) (string, error) {
	dir, err := mkSocketDir("", pattern, label)
	if err != nil {
		return "", err
	}
	if !unixsocket.TooLong(filepath.Join(dir, socketFile)) {
		return dir, nil
	}
	if err := socketDirRemoveAll(dir); err != nil {
		return "", fmt.Errorf("remove over-long %s dir: %w", label, err)
	}

	// A too-long path from this fallback is ListenAndServe's error to raise:
	// it already names the platform, the cap, and the byte length (issue
	// #3077), so a second message here would only drift out of sync.
	return mkSocketDir("/tmp", pattern, label)
}

// registryProxySocketDir returns a fresh directory for the registry proxy's
// unix socket (issue #3077/#3723).
func registryProxySocketDir() (string, error) {
	return socketDirWithFallback(spindriftRegistryProxyDirPattern, registryProxySocketFile, "registry proxy")
}

// signalSocketDir returns a fresh directory for the Signal socket's unix
// socket (ADR 0052, issue #3725).
func signalSocketDir() (string, error) {
	return socketDirWithFallback(spindriftSignalSocketDirPattern, signalSocketFile, "signal socket")
}

// needsOutbox reports whether cfg's dispatch needs a writable per-issue outbox
// directory: the harness bundles the Box's finished branch to seam.bundle
// post-driver (issue #2082) for the launcher's BundleRelay to pick up, whenever
// the forge relays a bundle (backend.RelaysBundle; issues #1918, #6898).
func needsOutbox(cfg Config) bool {
	return backend.RelaysBundle(cfg.ForgeDescriptor)
}

// resetOutboxDir empties dir and recreates it: the writable outbox mount must
// start empty every dispatch (ADR 0033), and BuildMountSpecs only produces the
// mount when the source directory exists. The 0o777 mode lets the Box's
// uid-1000 agent write regardless of rootless uid remapping (issue #1723).
func resetOutboxDir(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return mkdirBoxWritable(dir)
}

// mkdirBoxWritable creates dir 0o777 so the Box's uid-1000 agent can write it
// under rootless uid remapping (issues #1723, #5387). The explicit Chmod is
// needed because MkdirAll's mode passes through the umask.
func mkdirBoxWritable(dir string) error {
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return err
	}
	return os.Chmod(dir, 0o777)
}

// rotateStaleLog renames an existing file at logPath aside to the first
// available logPath.N suffix, so a subsequent os.Create(logPath) starts
// clean without destroying it. A missing logPath is a no-op; a stat failure
// on a candidate slot is returned as an error.
func rotateStaleLog(logPath string) error {
	if _, err := os.Stat(logPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat %s: %w", logPath, err)
	}
	return renameToFirstFreeSuffix(logPath, "")
}

// renameToFirstFreeSuffix renames path to the first destination
// "<path><stem>.<n>" that does not exist yet. A stat failure other
// than not-found (EACCES on the log dir, ENAMETOOLONG, ELOOP) never yields
// os.IsNotExist, so treating it as a free slot would spin this uncapped loop
// (issues #2575, #5444). Which of these dispatchWithRetry fails at once is
// isPermanentPreDispatchErr's call.
func renameToFirstFreeSuffix(path, stem string) error {
	for n := 1; ; n++ {
		dest := fmt.Sprintf("%s%s.%d", path, stem, n)
		_, err := os.Stat(dest)
		if err == nil {
			continue
		}
		if !os.IsNotExist(err) {
			return fmt.Errorf("stat %s: %w", dest, err)
		}
		return os.Rename(path, dest)
	}
}

// preDispatchErr marks an attempt that failed before writing a log of its own
// (Run()'s quarantine and run-lineage steps, issue #2575; runOnce's stale-log
// rotation, issue #5444): logPath may still hold earlier content, so
// dispatchWithRetry must not consult settledOutcome or ClassifyTransient
// against it.
type preDispatchErr struct{ err error }

func (e preDispatchErr) Error() string { return e.err.Error() }
func (e preDispatchErr) Unwrap() error { return e.err }

// quarantinePriorRunLogs renames every attempt log AllAttemptLogPaths finds
// for this issue to "<path>.prior-run.N", a suffix its own "<path>.N" probe
// never matches, so an earlier run's spend cannot fold into this run's usage
// comment and budget gate (issue #2575). Skipped under the same IsRunning
// guard as runOnce, though IsRunning cannot see a run paused between attempts
// (issue #562).
func quarantinePriorRunLogs(pwd, number string, r runner.Runner) error {
	// A nil runner comes only from a test double; treat it as nothing running.
	if r != nil && r.IsRunning(BoxName(number)) {
		return nil
	}
	for _, pl := range AllAttemptLogPaths(pwd, number) {
		if err := renameToFirstFreeSuffix(pl.Path, ".prior-run"); err != nil {
			return err
		}
	}
	return nil
}

// runLineageMarkerPath returns the sentinel file Run() drops right after its
// quarantinePriorRunLogs call, so a caller that never went through Run()
// (main.go's recoverIssue, adopting an open PR or settling a relayed branch)
// can tell pass logs quarantined at the start of this logical run from ones no
// Run() ever quarantined (issue #2575). It never matches AllAttemptLogPaths'
// pattern.
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

// warningsPath returns the per-issue sidecar holding the settle warnings of
// the initial run plus each fix pass (issue #3744). It is a sibling of the
// attempt logs, never a suffix of one: appending to issue-<n>.log would feed
// the Box log's outcome-line rescan.
func warningsPath(pwd, number string) string {
	return filepath.Join(HostLogDirFor(pwd), "issue-"+number+".warnings")
}

// RecordWarnings appends warnings to this Dispatch's accumulated set and
// rewrites the sidecar, one per line, so a fix pass adds to the initial run's
// batch while the first call replaces an earlier run's file. While the set is
// empty the sidecar is removed so a stale one never lingers. Works with no
// report pipe (non-daemon runs).
func (d *Dispatch) RecordWarnings(warnings []string) {
	d.warnings = append(d.warnings, warnings...)
	path := warningsPath(d.pwd, d.number)
	var err error
	if len(d.warnings) == 0 {
		if err = os.Remove(path); os.IsNotExist(err) {
			err = nil
		}
	} else if err = os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
		err = os.WriteFile(path, []byte(strings.Join(d.warnings, "\n")+"\n"), 0o644)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: warnings sidecar: %v\n", d.number, err)
	}
}

// EnsureRunLineage gives a Dispatch that reaches Fix, CumulativeUsage, or
// UsageReport without Run() (recover adopting an open PR or settling a
// relayed branch) the same guarantee Run's quarantinePriorRunLogs call makes
// (issue #2575). A missing marker (an orphaned PR) quarantines everything so
// the count starts from zero.
func (d *Dispatch) EnsureRunLineage() error {
	if fileExists(runLineageMarkerPath(d.pwd, d.number)) {
		return nil
	}
	if err := quarantinePriorRunLogs(d.pwd, d.number, d.runner); err != nil {
		return fmt.Errorf("quarantine prior-run logs: %w", err)
	}
	return markRunLineage(d.pwd, d.number)
}

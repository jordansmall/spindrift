package dispatch

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/hostpaths"
	"spindrift.dev/launcher/internal/panicguard"
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

	// signalBuffer holds this attempt's Signal socket buffer (ADR 0052, issue
	// #3725), set by runOnce only under BOX_SIGNAL_CARRIER=socket, for
	// outcomeResult to read once the Box exits. runOnce resets it to nil before
	// every attempt starts a listener, so a killed or retried attempt's buffer
	// can never leak into a later attempt's Result; nil also covers the log
	// carrier, where no buffer is ever minted.
	signalBuffer *signalsocket.Buffer

	// attemptLog is the identity of the log the current attempt created, so
	// reclaimAttemptLog can find it after a mid-run rename (issue #3886).
	attemptLog os.FileInfo

	// subject is what this Dispatch's Box works (ADR 0056, issue #3875): a
	// tracker issue, or a Factory.NewChore Dispatch's Chore, and the sole
	// source of the Dispatch's key and title (issue #3954, #3988).
	// buildBoxEnv reads subject.key.IsChore() to forward CHORE_*/BASE_BRANCH
	// in place of the issue-keyed ISSUE_NUMBER/ISSUE_TITLE/ISSUE_TEXT trio.
	subject subject

	// releaseClaim drops the claim Run took; only Close calls it, so the
	// claim spans the caller's settle too (issue #4364). Nil until Run
	// claims, and again after Close.
	releaseClaim func()

	// recordID and claimTime identify this Dispatch's Record (issue #4783):
	// minted once by ensureRecordID, then stamped into every Pass log and
	// every box report so a retry's rotated log, a fix pass and a conflict
	// resolve all land on one Record.
	recordID  string
	claimTime time.Time

	// attemptStampLen is the byte length of the dispatch_start stamp the
	// current attempt wrote at the head of its log, so logIsEmpty can still
	// tell a Box that never produced output from one that ran.
	attemptStampLen int64
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

// HostLogDirFor returns the host-side log directory for a working dir, the
// single source of truth for `<pwd>/.spindrift/logs` so it cannot drift.
func HostLogDirFor(pwd string) string {
	return hostpaths.LogDir(pwd)
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
// quarantine that run's live log. The IsRunning guards still catch a container
// orphaned by a killed launcher, whose claim died with it. The claim is held
// past Run's return, through the caller's settle, until Close (issue #4364);
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
	d.ensureRecordID()

	logPath := d.logPath()
	return d.dispatchWithRetry(logPath, func(resumeAfterHold bool) error {
		d.announce(report.PhaseInitial, logPath)
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
// CONFLICT_RESOLVE_PR_URL puts the entrypoint in conflict-resolve mode: it
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
// Dispatch's Record; callers run it after ensureRecordID.
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
// the report vocabulary spelled once in report.PhaseInitial,
// report.PhaseConflictResolve, and report.PhaseFixPass; humanPhase maps it
// onto announceLine's own vocabulary here rather than at the two call sites,
// so a phase name only needs to be spelled once per caller.
//
// logPath is the Pass log the phase writes, absolute under d.pwd; the record
// carries it relative to the checkout. It is the un-suffixed path even when a
// stale log is rotated aside, since runOnce moves the old file to logPath.N.
// A later Dispatch of the same issue (or Chore) reuses the path once
// quarantinePriorRunLogs moves the earlier run's log to logPath.prior-run.N;
// the name stays fixed per issue (see docs/reference.md).
func (d *Dispatch) announce(phase, logPath string) {
	fmt.Fprint(d.humanOut(), announceLine(d.number, humanPhase(phase), d.subject.title))
	rel, err := filepath.Rel(d.pwd, logPath)
	if err != nil {
		// rel is "": the record then names no Pass log.
		fmt.Fprintf(os.Stderr, "    ?? #%s: pass log path: %v\n", d.number, err)
	}
	report.Box(d.subject.key, phase, rel, d.recordID)
}

// ensureRecordID mints the Record ID on first use. Run calls it right after
// the claim; Fix and ResolveConflict mint lazily for a Dispatch that never ran
// Run (recover adopting an open PR).
func (d *Dispatch) ensureRecordID() {
	if d.recordID != "" {
		return
	}
	d.claimTime = d.clock.Now()
	d.recordID = dispatchrecord.RecordID(d.cfg.kindName(), d.number, d.claimTime)
}

// dispatchStart builds the stamp heading a fresh Pass log: the launcher's
// deployment facts from Config.Stamp plus this Dispatch's identity.
func (d *Dispatch) dispatchStart() claude.DispatchStart {
	s := d.cfg.Stamp
	s.RecordID = d.recordID
	s.Kind = d.cfg.kindName()
	if _, ok := s.Knobs["BOX_FORGE_AND_ISSUE_ACCESS"]; ok {
		// A ReadOnlyBox kind always runs read-only whatever the raw knob says.
		// Knobs is shared across Dispatches, so clone before overriding.
		s.Knobs = maps.Clone(s.Knobs)
		s.Knobs["BOX_FORGE_AND_ISSUE_ACCESS"] = d.cfg.boxAccessForKind()
	}
	s.DispatchKey = d.number
	s.ClaimTime = d.claimTime
	s.Started = d.clock.Now()
	s.Driver = d.driver.Name()
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

// announceLine builds the one line of human-facing output announcing a
// dispatched Box for number, shared by Run, Fix, and ResolveConflict (via
// announce) so the three call sites cannot drift out of sync with each other.
// phase is the parenthesized suffix ("", "fix-pass-N", or "conflict-resolve")
// — not a daemon.Kind, which the rest of this branch means by "kind"; ""
// omits the parens entirely.
//
// This line is human output, not the machine channel: the daemon now relays
// child stdout unchanged rather than parsing it (issue #3627), so this exact
// wording is operator-visible text, not a wire format, and internal/daemon no
// longer has a ParseAnnouncedIssue reader over it. The machine channel is the
// report record announce emits alongside this line.
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
// truncate it away (issue #561). If a container or sandbox named for this
// issue is already live per Runner.IsRunning's contract (issue #3633) — a
// live run (possibly orphaned by a killed launcher) owns that log, so
// runOnce returns ErrAlreadyRunning first (#562).
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
		return fmt.Errorf("rotate stale log: %w", err)
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
	stamp := claude.EncodeSpindriftOp(claude.SpindriftOp{Op: claude.OpDispatchStart, Start: &start})
	if _, err := logFile.WriteString(stamp); err != nil {
		return fmt.Errorf("write dispatch_start: %w", err)
	}
	d.attemptStampLen = int64(len(stamp))
	if info, statErr := logFile.Stat(); statErr == nil {
		d.attemptLog = info
	}

	// Unconditional: the stream view is byte-transparent, so a log-carrier
	// run writes exactly what it wrote before.
	boxLog := newBoxLog(logFile)
	defer func() { _ = boxLog.flush() }()

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
	// boxSockets collects every Box-facing socket mount this Dispatch mints,
	// under whichever transport verdict RegistryProxyTransport returns once
	// below: the registry proxy's unix verdict appends here, and so does the
	// Signal socket's (ADR 0052, issue #3725), both gated on the same probe.
	var boxSockets []runner.SocketMount
	// signalSocketLocation is the Signal socket's TCP-only counterpart to
	// registryProxyLocation; the zero value covers the log carrier and the
	// unix verdict alike, where the socket travels through boxSockets instead.
	var signalSocketLocation runner.SignalSocketLocation

	needRegistryProxy := len(d.cfg.RegistryProxyRoutes) > 0
	needSignalSocket := d.cfg.signalCarrierSocket()

	// The routes are validated before the probe below, the order this had
	// before the one-probe hoist: a malformed-routes value is a static
	// configuration mistake, so it must not wait on -- or lose its error to
	// -- a container-runtime probe subprocess that has nothing to say about
	// it.
	var proxy *registryproxy.Proxy
	if needRegistryProxy {
		// Rewrite rows come from ecosystem.Table, not d.cfg: which response
		// shapes get rewritten is static per-ecosystem knowledge, not
		// something a run resolves per-route the way routes are.
		handler, err := registryproxy.New(d.cfg.RegistryProxyRoutes, ecosystem.ResponseRewriteRows())
		if err != nil {
			return fmt.Errorf("registry proxy: %w", err)
		}
		proxy = &registryproxy.Proxy{Handler: handler}
	}

	// The runner probes the transport live at most once per Dispatch (issue
	// #3111; one-probe rule for the Signal socket too, ADR 0052): a unix
	// socket that cannot cross into the guest (a remote-context
	// docker/podman, a VM-backed runtime) needs the TCP fallback, so the
	// transport can never be inferred from GOOS. The registry proxy and
	// the Signal socket share this one verdict rather than probing twice.
	// Neither feature configured takes no probe at all, exactly as before
	// this knob existed.
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
			// socketPath is the mount SOURCE on the host; only the unix
			// verdict has one to mount at all, TCP dials directly.
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
			// The listener binds every interface, not loopback (issue #3111
			// review finding): the Box dials the host by name, resolved
			// either by the runtime itself or by --add-host
			// <host>:host-gateway (probeRegistryTCPReachable decides which),
			// and on a plain Linux docker bridge that name resolves to the
			// bridge IP (e.g. 172.17.0.1), so a loopback-only bind would
			// leave nothing on the address the Box dials.
			//
			// Every-interface exposure is accepted, with
			// registrymanifest.TCPSecretHeader as the sole access control;
			// ADR 0044's issue #3772 amendment records why and what bounds it.
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
		Output:            d.driver.NewHeartbeatWriter(boxLog.stream(), d.number, d.humanOut(), renderOpts),
		DriverCacheDir:    driverCacheDir,
		OutboxDir:         outboxDir,
		RegistryProxy:     registryProxyLocation,
		Sockets:           boxSockets,
		SignalSocket:      signalSocketLocation,
		ClosureGeneration: d.agentGeneration,
	}
	return d.runner.Run(box)
}

// newRegistryProxyTCPSecret mints a fresh per-run secret (issue #3111) gating
// the registry proxy's every-interface TCP fallback
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

// signalSocketFile and spindriftSignalSocketDirPattern are the Signal
// socket's counterparts (ADR 0052, issue #3725): the two mounted unix
// sockets share the same over-long-sun_path fallback mechanism (issue #3077)
// through mkSocketDir/socketDirWithFallback below, rather than a second
// copy-pasted variant.
const signalSocketFile = "signal.sock"
const spindriftSignalSocketDirPattern = "spindrift-signal-socket-*"

// registryProxyMkdirTemp and registryProxyRemoveAll are swappable in tests the
// way statCgroupControllerFile is (runner/validate.go, issue #3103): the
// RemoveAll failure branch below is reachable only through an
// EACCES/EROFS/EBUSY-class error no test can provoke deterministically. Both
// the registry proxy's and the Signal socket's directories are created
// through registryProxyMkdirTemp (via mkSocketDir below), but only the
// registry proxy's removal goes through registryProxyRemoveAll -- the Signal
// socket's cleanup calls os.RemoveAll directly (signal_socket.go).
var (
	registryProxyMkdirTemp = os.MkdirTemp
	registryProxyRemoveAll = os.RemoveAll
)

// mkSocketDir creates a fresh, unique directory for one of this Dispatch's
// launcher-owned unix sockets under base ("" means os.MkdirTemp's own
// default, os.TempDir()). label names the caller in error text ("registry
// proxy" or "signal socket").
func mkSocketDir(base, pattern, label string) (string, error) {
	dir, err := registryProxyMkdirTemp(base, pattern)
	if err != nil {
		return "", fmt.Errorf("mktemp %s dir under %q: %w", label, base, err)
	}
	return dir, nil
}

// socketDirWithFallback returns a fresh directory for label's unix socket
// file named socketFile, preferring os.TempDir() but falling back to /tmp
// when appending socketFile would overflow the platform's AF_UNIX sun_path
// limit (issue #3077), as macOS's $TMPDIR under nix develop's
// nix-shell.XXXXXX/ prefix does. Any other os.MkdirTemp failure is returned
// as-is, never rerouted. registryProxySocketDir and signalSocketDir are both
// thin callers of this one mechanism (ADR 0052).
func socketDirWithFallback(pattern, socketFile, label string) (string, error) {
	dir, err := mkSocketDir("", pattern, label)
	if err != nil {
		return "", err
	}
	if !unixsocket.TooLong(filepath.Join(dir, socketFile)) {
		return dir, nil
	}
	if err := registryProxyRemoveAll(dir); err != nil {
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
// socket, mirroring registryProxySocketDir (ADR 0052, issue #3725).
func signalSocketDir() (string, error) {
	return socketDirWithFallback(spindriftSignalSocketDirPattern, signalSocketFile, "signal socket")
}

// needsOutbox reports whether cfg's dispatch needs a writable per-issue outbox
// directory: HostMediatedRemote unconditionally (ADR 0033, CODE_FORGE=local),
// or OutboxRelayCapable under BOX_FORGE_AND_ISSUE_ACCESS=read-only (issue
// #1918), where the harness bundles the Box's finished branch to seam.bundle
// post-driver (issue #2082) for the launcher's BundleRelay to pick up.
func needsOutbox(cfg Config) bool {
	return cfg.ForgeDescriptor.HostMediatedRemote ||
		(cfg.ForgeDescriptor.OutboxRelayCapable && cfg.boxAccessForKind() == "read-only")
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

// warningsPath returns the per-issue sidecar holding the settle warnings
// (scan and rejection) of the initial run plus each fix pass (issue #3744).
// It is a sibling of the attempt logs, never a
// suffix of one: appending to issue-<n>.log would feed the Box log's
// outcome-line rescan, and AllAttemptLogPaths probes exact names.
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

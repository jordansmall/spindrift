// Command daemon drives daemon.Loop against a real operator checkout: it
// resolves each iteration's revision with git and runs each child with nix
// (issue #3538). It is a separate binary, not a launcher subcommand, because
// it cannot exec the launcher store path it was built against — that path is
// precisely the stale one a rebuild exists to replace — so it always shells
// out to `nix run` with the app attribute baked in (see runner.go).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"spindrift.dev/launcher/internal/daemon"
)

// inputDocument mirrors the nix-rendered Launcher input document (ADR 0020).
// cmd/launcher/inputdoc.go is the source of truth for the shape; it cannot be
// imported here (a different package main), so this is a minimal local copy
// of the two fields this binary reads.
type inputDocument struct {
	Settings  map[string]string `json:"settings"`
	Artifacts map[string]string `json:"artifacts"`
}

// parsedArgs is the result of parsing argv: `--input <path>` plus an
// optional positional Dispatch kind (dispatch|research, default dispatch).
type parsedArgs struct {
	InputPath string
	Kind      daemon.Kind
}

// parseArgs parses `daemon --input <path> [dispatch|research]`.
func parseArgs(args []string) (parsedArgs, error) {
	var inputPath string
	var havePath bool
	var positional []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--input" {
			if i+1 >= len(args) {
				return parsedArgs{}, fmt.Errorf("flag --input requires a value")
			}
			inputPath = args[i+1]
			havePath = true
			i++
			continue
		}
		positional = append(positional, args[i])
	}
	if !havePath {
		return parsedArgs{}, fmt.Errorf("flag --input is required")
	}
	if len(positional) > 1 {
		return parsedArgs{}, fmt.Errorf("unexpected extra arguments: %v", positional[1:])
	}
	var kindArg string
	if len(positional) == 1 {
		kindArg = positional[0]
	}
	kind, err := daemon.ParseKind(kindArg)
	if err != nil {
		return parsedArgs{}, err
	}
	return parsedArgs{InputPath: inputPath, Kind: kind}, nil
}

// loadInputDocument reads and parses the Launcher input document at path.
func loadInputDocument(path string) (*inputDocument, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read input document %s: %w", path, err)
	}
	var doc inputDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse input document %s: %w", path, err)
	}
	return &doc, nil
}

// lookupKnob resolves one schema knob: ambient env first, then the
// document's settings (keyed by env var name — lib/mkHarness.nix's
// documentSettings), then nothing (found=false). When the document also
// carries a value and the ambient env wins anyway, it prints a provenance
// warning to stderr (mirroring cmd/launcher/inputdoc.go's
// warnAmbientKnobEnv) — otherwise nothing records which value actually drove
// the run, and a stale exported override silently wins (ADR 0020).
func lookupKnob(doc *inputDocument, envVar string, stderr io.Writer) (string, bool) {
	if v := os.Getenv(envVar); v != "" {
		if doc != nil {
			if docVal, ok := doc.Settings[envVar]; ok && docVal != "" {
				fmt.Fprintf(stderr, "%s=%s set in environment — knob env overrides are deprecated; use the --input document's settings.%s\n", envVar, v, envVar)
			}
		}
		return v, true
	}
	if doc != nil {
		if v, ok := doc.Settings[envVar]; ok && v != "" {
			return v, true
		}
	}
	return "", false
}

// resolveKnob wraps lookupKnob for knobs that must have a value: the
// default lives in the schema and travels in the document, so a knob absent
// from both is a configuration error, not a silent default.
func resolveKnob(doc *inputDocument, envVar string, stderr io.Writer) (string, error) {
	if v, ok := lookupKnob(doc, envVar, stderr); ok {
		return v, nil
	}
	return "", fmt.Errorf("daemon: no value for %s (not in environment or --input document settings)", envVar)
}

// resolveKnobOptional wraps lookupKnob for knobs whose schema default is
// itself the empty string — absent-from-both is the normal case, not a
// configuration error — while still keeping lookupKnob's ambient-env-wins
// provenance warning.
func resolveKnobOptional(doc *inputDocument, envVar string, stderr io.Writer) string {
	v, _ := lookupKnob(doc, envVar, stderr)
	return v
}

// parseSlots turns MAX_PARALLEL's resolved string value into the daemon's
// pool size. lib/env-schema.nix declares MAX_PARALLEL with
// intKind = "positive", so the document always carries a valid value, but
// resolveKnob can still hand back an ambient env override of anything — a
// malformed or non-positive value fails startup here with a clear
// diagnostic rather than reaching daemon.Loop's own "reject non-positive
// Slots" halt, which is meant for a genuine programming error, not an
// operator's mistyped env var.
func parseSlots(raw string) (int, error) {
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("MAX_PARALLEL must be a positive integer, got %q", raw)
	}
	if n <= 0 {
		return 0, fmt.Errorf("MAX_PARALLEL must be a positive integer, got %d", n)
	}
	return n, nil
}

// repoRoot resolves the git checkout root containing dir via `git rev-parse
// --show-toplevel`, run in dir. Failing fast here — rather than letting a
// subdirectory-relative repoPath reach the first child — turns a
// misconfigured working directory into a startup error instead of a
// git+file:// flakeref pointing at a non-root that only breaks the first
// child invocation.
func repoRoot(dir string) (string, error) {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		// git's own stderr, not just the bare error: the likeliest cause is
		// a `safe.directory` refusal, and without this the fail-fast
		// diagnostic reduces to "exit status 128" (same shape as
		// hostRunner.ResolveRevision's fetch error).
		return "", fmt.Errorf("not a git checkout: %s: %w: %s", dir, err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

// fail prints one daemon diagnostic and yields mainRun's error exit code, so
// each startup step below stays a single guarded line.
func fail(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "daemon: %s\n", err)
	return 1
}

// daemonIdleFloor and daemonIdleCap are the shipped defaults for the
// pool-wide idle backoff; the backoff's mechanics live in backoff.go and
// the operator-facing writeup is in docs/reference.md's daemon exit-code
// section. These values are a defensible first cut, not a tuned final
// answer (final values were out of scope for the issue that added them);
// expect to revisit them against a real unattended run — retuning them
// also means updating TestIdleBackoffCapsAtShippedDefaults
// (cmd/launcher/internal/daemon/backoff_test.go), which spells this pair
// out by hand since it can't import them from package main. Making either
// configurable (per-kind backoff) is a later ticket (loop.go's Config
// doc).
const (
	daemonIdleFloor = 5 * time.Minute
	daemonIdleCap   = 30 * time.Minute
)

// daemonFailureBackoff, daemonBreakerThreshold and daemonBreakerWindow are
// a defensible first cut, not a tuned final answer (final values are out
// of scope for the issue that added them) — expect to revisit them against
// a real unattended run. A systemic fault (an expired token, a forge
// outage) fails every slot's child immediately, so the pool crosses
// daemonBreakerThreshold within about one backoff at the default 3 slots,
// and after (daemonBreakerThreshold-1) backoffs — about four minutes — at
// MAX_PARALLEL=1, where the pool-wide count is one slot's own retries. A
// lone slot reaching the threshold by itself is deliberate rather than a
// gap: a failed child's issue has already left the ready queue (dispatch
// claims it by label swap), so consecutive unclassified failures read as
// systemic rather than as one bad issue retried, and a 1-slot pool has no
// sibling still doing useful work for a spared breaker to protect (see
// TestBreakerDefaults_TripReachableAtOneSlot).
const (
	daemonFailureBackoff   = 1 * time.Minute
	daemonBreakerThreshold = 5
	daemonBreakerWindow    = 15 * time.Minute
)

// hostClock is the production daemon.Clock: Now is time.Now, Sleep waits d
// or returns early on ctx cancellation, so an operator stop during the idle
// wait is honoured immediately rather than after the full interval.
type hostClock struct{}

func (hostClock) Now() time.Time { return time.Now() }

func (hostClock) Sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

// isOperatorStop reports whether reason (daemon.Loop's return value) reflects
// an operator-requested stop rather than a real failure: either the loop's
// own ctx was cancelled (a signal arrived between iterations, no child
// running) or the running child drained and reported exit 7 / "signalled-stop"
// (a forwarded SIGTERM reached it mid-run — see runner.go's forwardStop).
// Anything else — a resolve/run-child error or a Halt-mapped exit like
// host-tainted or config-invalid — is a real failure.
func isOperatorStop(reason string) bool {
	return strings.HasPrefix(reason, "context-cancelled") || reason == "outcome: signalled-stop"
}

// handleStopSignal waits for a single signal on sig, then cancels ctx and
// calls forward once. Loop only checks ctx.Err() between iterations and
// always waits out a started child (daemon.Loop's doc), so cancelling here
// already gets "stop starting new work, let anything running drain".
// Forwarding SIGTERM on top makes the running child itself start draining
// right away instead of only being noticed once it exits on its own — the
// same gesture as dogfood.sh's request_stop and cmd/launcher/main.go's
// notifyStopSignal. It is never a kill: the child chooses to drain.
func handleStopSignal(sig <-chan os.Signal, cancel context.CancelFunc, forward func()) {
	<-sig
	cancel()
	forward()
}

// mainRun holds everything main() does: argv parse, input document load,
// knob resolution, repo root, signal wiring, and daemon.Loop, returning the
// exit code rather than calling os.Exit so tests can drive it repeatedly
// with different argv (mirrors cmd/launcher/driver-exec/main.go's mainRun).
func mainRun(argv []string, stdout, stderr io.Writer) int {
	args, err := parseArgs(argv)
	if err != nil {
		return fail(stderr, err)
	}

	doc, err := loadInputDocument(args.InputPath)
	if err != nil {
		return fail(stderr, err)
	}

	appAttr, err := resolveKnob(doc, "DAEMON_APP", stderr)
	if err != nil {
		return fail(stderr, err)
	}
	baseBranch, err := resolveKnob(doc, "BASE_BRANCH", stderr)
	if err != nil {
		return fail(stderr, err)
	}
	maxParallelRaw, err := resolveKnob(doc, "MAX_PARALLEL", stderr)
	if err != nil {
		return fail(stderr, err)
	}
	slots, err := parseSlots(maxParallelRaw)
	if err != nil {
		return fail(stderr, err)
	}

	// The schema validates DAEMON_AWAKE_WINDOW at Nix eval time, but an
	// ambient env override (lookupKnob above) bypasses that entirely, so
	// this runtime parse is the actual guarantee.
	awakeRaw := resolveKnobOptional(doc, "DAEMON_AWAKE_WINDOW", stderr)
	awake, err := daemon.ParseWindow(awakeRaw)
	if err != nil {
		return fail(stderr, err)
	}

	wd, err := os.Getwd()
	if err != nil {
		return fail(stderr, err)
	}
	repoPath, err := repoRoot(wd)
	if err != nil {
		return fail(stderr, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r := newHostRunner(repoPath, appAttr, baseBranch)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go handleStopSignal(sig, cancel, r.forwardStop)

	clk := hostClock{}
	em := daemon.NewEmitter(stdout, clk.Now)
	cfg := daemon.Config{
		Kind:             args.Kind,
		IdleFloor:        daemonIdleFloor,
		IdleCap:          daemonIdleCap,
		Slots:            slots,
		FailureBackoff:   daemonFailureBackoff,
		BreakerThreshold: daemonBreakerThreshold,
		BreakerWindow:    daemonBreakerWindow,
		Awake:            awake,
	}

	reason := daemon.Loop(ctx, cfg, r, em, clk)

	if isOperatorStop(reason) {
		return 0
	}
	// A plain non-zero exit: the event stream already carries the specific
	// halt reason as structured JSON, so stderr/exit code need only say
	// "this was not a clean stop" for a process supervisor to act on.
	return 1
}

func main() {
	os.Exit(mainRun(os.Args[1:], os.Stdout, os.Stderr))
}

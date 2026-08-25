package doctor

import (
	"errors"
	"fmt"
	"io"
	"strings"
)

// ErrDegraded, wrapped into a Required-tier Check's Probe error (e.g. via
// fmt.Errorf("...: %w", ErrDegraded)), tells FirstRequiredError and
// RunChecksFailFast to treat that one failure as non-blocking even though
// the Check's own Tier is Required -- for a probe that could not determine
// the condition it exists to check (e.g. a permission error), as distinct
// from one that affirmatively detected the condition it guards against.
// ReportResults still reports the failure (MISSING line) and its Remedy
// line exactly as any other failure; only the fail-fast/required-error
// gating changes.
var ErrDegraded = errors.New("check degraded: probe could not determine result")

// Tier classifies a Check as either blocking (Required) or non-blocking
// (Advisory). FirstRequiredError uses this to decide which failures fail
// fast a caller like validate() and which are surfaced for information
// only. Exception: a Required-tier failure whose Err wraps ErrDegraded is
// treated as non-blocking, the same as an Advisory failure, because the
// probe could not determine the condition it checks rather than
// affirmatively detecting it.
type Tier int

const (
	// Required checks must pass; FirstRequiredError surfaces their
	// failures.
	Required Tier = iota
	// Advisory checks are informational only; their failures never make
	// FirstRequiredError return non-nil.
	Advisory
)

// Check is one probe in the doctor check registry.
type Check struct {
	// Name identifies the check for reporting.
	Name string
	// Tier determines whether a failure is blocking (Required) or
	// informational (Advisory).
	Tier Tier
	// Probe runs the check, returning a non-nil error on failure.
	Probe func() error
	// Remedy is a short human-readable hint printed alongside a failure,
	// e.g. "set GIT_USER_NAME, or configure git user.name on the host".
	Remedy string
	// SuccessMsg, if set, overrides the "ok: <Name>" line ReportResults
	// prints on success, letting a Check report a dynamic, probe-derived
	// detail in its own success line -- e.g. a live-fetched repo slug or a
	// count -- the same way Remedy lets it report a fatal detail. When nil,
	// ReportResults falls back to printing "ok: <Name>" as before.
	SuccessMsg func() string
}

// Result is the outcome of running one Check's Probe.
type Result struct {
	Check Check
	// Err is nil on success.
	Err error
}

// runOne runs a single Check's Probe, treating a nil Probe as a failure.
func runOne(c Check) Result {
	if c.Probe == nil {
		return Result{Check: c, Err: fmt.Errorf("check %q has no Probe", c.Name)}
	}
	return Result{Check: c, Err: c.Probe()}
}

// blocking reports whether r should stop a fail-fast chain (RunChecksFailFast)
// or count as the failure a fail-fast gate surfaces (FirstRequiredError): a
// Required-tier Result with a non-nil Err that does not wrap ErrDegraded. A
// Required-tier Err wrapping ErrDegraded, or any Advisory-tier Err, is never
// blocking.
func blocking(r Result) bool {
	return r.Check.Tier == Required && r.Err != nil && !errors.Is(r.Err, ErrDegraded)
}

// RunChecks runs every check's Probe in order, always running all of them
// (no short-circuiting on failure), and returns one Result per check.
func RunChecks(checks []Check) []Result {
	results := make([]Result, len(checks))
	for i, c := range checks {
		results[i] = runOne(c)
	}
	return results
}

// RunChecksFailFast runs checks in order, calling each one's Probe (nil
// Probes are treated as failures, same as RunChecks), but -- unlike
// RunChecks, which always runs every check regardless of outcome -- stops
// immediately after the first Result whose Check.Tier is Required and whose
// Err is non-nil, never running any later check in the slice. An
// Advisory-tier failure does not stop iteration -- and neither does a
// Required-tier failure whose Err wraps ErrDegraded, which is treated as
// non-blocking even though its Check.Tier is Required. The returned slice
// holds only the Results for checks that actually ran, so its length can be
// shorter than len(checks). This lets a fail-fast doctor probe chain avoid
// running a later check -- e.g. an extra live network call -- once an
// earlier required one has already failed.
func RunChecksFailFast(checks []Check) []Result {
	results := make([]Result, 0, len(checks))
	for _, c := range checks {
		r := runOne(c)
		results = append(results, r)
		if blocking(r) {
			break
		}
	}
	return results
}

// FirstRequiredError returns the Err of the first Result in slice order
// whose Check.Tier is Required and whose Err is non-nil, returning nil if
// there is no such failure. A Required-tier Result whose Err wraps
// ErrDegraded is skipped, the same as an Advisory-tier failure, and scanning
// continues into later results looking for a genuine Required failure. It
// returns the error verbatim — never wrapped or reformatted — so it's a
// drop-in fail-fast replacement for callers like validate() that assert on
// exact error messages.
func FirstRequiredError(results []Result) error {
	for _, r := range results {
		if blocking(r) {
			return r.Err
		}
	}
	return nil
}

// RunRequiredFailFast runs checks via RunChecksFailFast and returns the
// first Required-tier failure via FirstRequiredError, or nil if none — the
// combined shape a fail-fast gate like validate() (cmd/launcher/main.go)
// wants when it only cares about the first blocking error, not the full
// Result slice ReportResults needs.
func RunRequiredFailFast(checks []Check) error {
	return FirstRequiredError(RunChecksFailFast(checks))
}

// ReportResults writes one line per Result to w: "ok: <name>" on success,
// "MISSING: <name>: <err>" plus a "  remedy: <remedy>" line on a genuine
// failure, or "advisory: <name>: <err>" plus remedy for a Result whose Err
// wraps ErrDegraded (the probe couldn't determine the answer, so it reads
// like every other advisory line rather than a hard failure) -- so a Check
// row's Name and Remedy alone drive its own doctor-visible status line, with
// no bespoke fmt.Fprintf needed per Probe. ErrDegraded's own sentinel text
// is trimmed off the degraded line, since it's an internal marker, not a
// detail an operator needs. The remedy line is skipped when Remedy is
// identical to the error text, so a row whose Remedy simply repeats its own
// error message doesn't print the same sentence twice.
func ReportResults(w io.Writer, results []Result) {
	for _, r := range results {
		if r.Err == nil {
			if r.Check.SuccessMsg != nil {
				fmt.Fprintf(w, "ok: %s\n", r.Check.SuccessMsg())
				continue
			}
			fmt.Fprintf(w, "ok: %s\n", r.Check.Name)
			continue
		}
		if errors.Is(r.Err, ErrDegraded) {
			msg := strings.TrimSuffix(r.Err.Error(), ": "+ErrDegraded.Error())
			fmt.Fprintf(w, "advisory: %s: %s\n", r.Check.Name, msg)
			if r.Check.Remedy != "" && r.Check.Remedy != msg {
				fmt.Fprintf(w, "  remedy: %s\n", r.Check.Remedy)
			}
			continue
		}
		fmt.Fprintf(w, "MISSING: %s: %s\n", r.Check.Name, r.Err)
		if r.Check.Remedy != "" && r.Check.Remedy != r.Err.Error() {
			fmt.Fprintf(w, "  remedy: %s\n", r.Check.Remedy)
		}
	}
}

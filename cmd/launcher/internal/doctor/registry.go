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
// gating changes. A second, deliberate use: an Advisory-tier Check whose
// Probe genuinely (not indeterminately) detects the absent condition can
// still wrap ErrDegraded purely to pick up ReportResults' "advisory:"
// rendering over "MISSING:" -- see bwrap-cgroup-delegation in
// cmd/launcher/bwrap_doctor_checks.go.
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
	// Probe runs the check, returning a non-nil error on failure. On
	// success, its first return value is passed to SuccessMsg as that
	// call's own output parameter, rather than a Probe closure having to
	// stash the value in a variable captured by a sibling SuccessMsg
	// closure.
	Probe func() (any, error)
	// Remedy is a short human-readable hint printed alongside a failure,
	// e.g. "set GIT_USER_NAME, or configure git user.name on the host".
	Remedy string
	// SuccessMsg, if set, overrides the "ok: <Name>" line ReportResults
	// prints on success, letting a Check report a dynamic, probe-derived
	// detail in its own success line -- e.g. a live-fetched repo slug or a
	// count -- the same way Remedy lets it report a fatal detail. output is
	// the value Probe returned alongside its nil error (Result.Output).
	// When SuccessMsg is nil, ReportResults falls back to printing "ok:
	// <Name>" as before.
	SuccessMsg func(output any) string
}

// Result is the outcome of running one Check's Probe.
type Result struct {
	Check Check
	// Output is the value Probe returned alongside a nil Err -- passed to
	// Check.SuccessMsg by ReportResults. Meaningless when Err is non-nil.
	Output any
	// Err is nil on success.
	Err error
}

// runOne runs a single Check's Probe, treating a nil Probe as a failure.
func runOne(c Check) Result {
	if c.Probe == nil {
		return Result{Check: c, Err: fmt.Errorf("check %q has no Probe", c.Name)}
	}
	output, err := c.Probe()
	return Result{Check: c, Output: output, Err: err}
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

// firstBlockingResult returns a pointer to the first Result in results for
// which blocking reports true, or nil if none does -- factored out so
// FirstRequiredError and RunRequiredFailFast, which both need "the first
// blocking Result in slice order", can't drift apart on what counts as
// blocking.
func firstBlockingResult(results []Result) *Result {
	for i := range results {
		if blocking(results[i]) {
			return &results[i]
		}
	}
	return nil
}

// FirstRequiredError returns the Err of the first Result in slice order
// whose Check.Tier is Required and whose Err is non-nil, returning nil if
// there is no such failure. A Required-tier Result whose Err wraps
// ErrDegraded is skipped, the same as an Advisory-tier failure, and scanning
// continues into later results looking for a genuine Required failure. It
// returns the error verbatim -- never wrapped or reformatted -- so a caller
// that needs the Probe's own error value, like Run (doctor.go), gets it
// unchanged.
func FirstRequiredError(results []Result) error {
	if r := firstBlockingResult(results); r != nil {
		return r.Err
	}
	return nil
}

// RemedyError wraps a blocking Check's Probe error together with the Check
// itself, so Error() can append the Remedy hint ReportResults prints for a
// human onto a fail-fast caller's returned error text, and leaves the Check
// available via errors.As for a caller that ever wants the failing row's
// Name or Remedy as data, instead of re-parsing Error()'s string. Error()
// shares Remedy's text and its suppression rule with ReportResults, not its
// line formatting: ReportResults prints an indented "  remedy: ..." row,
// Error() an unindented "\nremedy: ...".
type RemedyError struct {
	// Err is the failing Check's Probe error, unmodified.
	Err error
	// Check is the Check whose Probe produced Err.
	Check Check
}

// Error returns Err's text, followed by a "\nremedy: <remedy>" line when
// Check.Remedy is set and says something Err's own text doesn't already say
// -- a Remedy identical to the error is dropped rather than printed twice.
func (e *RemedyError) Error() string {
	msg := e.Err.Error()
	if suffix := remedySuffix(e.Check.Remedy, msg); suffix != "" {
		return msg + "\nremedy: " + suffix
	}
	return msg
}

// Unwrap returns Err, so errors.Is and errors.As see through RemedyError to
// the underlying Probe error.
func (e *RemedyError) Unwrap() error {
	return e.Err
}

// remedySuffix returns remedy, unless it is empty or identical to msg (the
// error text it would otherwise be paired with), in which case it returns ""
// so the caller skips printing a remedy line that would just repeat the
// error -- one copy of the rule for every reporter that pairs a Remedy with
// an error text, so they can't drift apart on it.
func remedySuffix(remedy, msg string) string {
	if remedy == "" || remedy == msg {
		return ""
	}
	return remedy
}

// WithRemedy returns r.Err carrying r.Check's Remedy hint -- the same hint
// ReportResults prints for a human -- so a caller that surfaces only an error
// value still tells an operator how to fix the failure. It returns r.Err
// verbatim when the Remedy is empty or only repeats the error text, leaving
// nothing worth adding, and a literal nil, never a nil *RemedyError, when
// r.Err is nil.
func WithRemedy(r Result) error {
	if r.Err == nil {
		return nil
	}
	if remedySuffix(r.Check.Remedy, r.Err.Error()) == "" {
		return r.Err
	}
	return &RemedyError{Err: r.Err, Check: r.Check}
}

// RunRequiredFailFast runs checks via RunChecksFailFast and returns the first
// Required-tier failure through WithRemedy, or nil if none -- the combined
// shape a fail-fast gate like validate() (cmd/launcher/main.go) wants when it
// only cares about the first blocking error, not the full Result slice
// ReportResults needs. The remedy wrapping lives here rather than in
// FirstRequiredError because that function's contract is to return the error
// verbatim; nothing depends on this one's return value being any particular
// error's identity.
func RunRequiredFailFast(checks []Check) error {
	r := firstBlockingResult(RunChecksFailFast(checks))
	if r == nil {
		return nil
	}
	return WithRemedy(*r)
}

// rowPrefix returns the status-line prefix a Tier renders as: "MISSING" for
// Required (blocking) and "advisory" for Advisory (non-blocking) -- shared by
// ReportResults and doctor.go's label rows so those two can't drift apart on
// wording. Reporters outside this package (e.g. the launch-gate rows in
// cmd/launcher/launchgates.go) still spell their own prefixes.
func rowPrefix(t Tier) string {
	if t == Advisory {
		return "advisory"
	}
	return "MISSING"
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
				fmt.Fprintf(w, "ok: %s\n", r.Check.SuccessMsg(r.Output))
				continue
			}
			fmt.Fprintf(w, "ok: %s\n", r.Check.Name)
			continue
		}
		if errors.Is(r.Err, ErrDegraded) {
			msg := strings.TrimSuffix(r.Err.Error(), ": "+ErrDegraded.Error())
			fmt.Fprintf(w, "%s: %s: %s\n", rowPrefix(Advisory), r.Check.Name, msg)
			if suffix := remedySuffix(r.Check.Remedy, msg); suffix != "" {
				fmt.Fprintf(w, "  remedy: %s\n", suffix)
			}
			continue
		}
		fmt.Fprintf(w, "%s: %s: %s\n", rowPrefix(Required), r.Check.Name, r.Err)
		if suffix := remedySuffix(r.Check.Remedy, r.Err.Error()); suffix != "" {
			fmt.Fprintf(w, "  remedy: %s\n", suffix)
		}
	}
}

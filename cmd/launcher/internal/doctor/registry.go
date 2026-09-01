package doctor

import (
	"errors"
	"fmt"
	"io"
	"strings"
)

// ErrDegraded, wrapped into a Required-tier Check's Probe error, makes that
// one failure non-blocking: it marks a probe that could not *determine* the
// condition it checks (e.g. a permission error), as distinct from one that
// affirmatively detected it. Reporting is unchanged; only the fail-fast/
// required-error gating. A second, deliberate use: an Advisory-tier Check
// may wrap it purely to pick up ReportResults' "advisory:" rendering over
// "MISSING:" -- see bwrap-cgroup-delegation in
// cmd/launcher/bwrap_doctor_checks.go.
var ErrDegraded = errors.New("check degraded: probe could not determine result")

// Tier classifies a Check as blocking (Required) or non-blocking
// (Advisory), deciding which failures fail a caller like validate() fast.
type Tier int

const (
	Required Tier = iota
	Advisory
)

// Check is one probe in the doctor check registry.
type Check struct {
	Name string
	Tier Tier
	// Probe runs the check, returning a non-nil error on failure. On
	// success its first return value is handed to SuccessMsg, so a Probe
	// need not stash it in a variable captured by a sibling closure.
	Probe func() (any, error)
	// Remedy is a short human-readable hint printed alongside a failure,
	// e.g. "set GIT_USER_NAME, or configure git user.name on the host".
	Remedy string
	// SuccessMsg, if set, overrides the "ok: <Name>" success line so a Check
	// can report a probe-derived detail (a live-fetched slug, a count).
	// output is the value Probe returned alongside its nil error.
	SuccessMsg func(output any) string
}

// Result is the outcome of running one Check's Probe. Err is nil on success;
// Output is meaningful only then.
type Result struct {
	Check  Check
	Output any
	Err    error
}

// runOne runs a single Check's Probe, treating a nil Probe as a failure.
func runOne(c Check) Result {
	if c.Probe == nil {
		return Result{Check: c, Err: fmt.Errorf("check %q has no Probe", c.Name)}
	}
	output, err := c.Probe()
	return Result{Check: c, Output: output, Err: err}
}

// blocking reports whether r should stop a fail-fast chain or count as the
// failure a fail-fast gate surfaces: a Required-tier Result with a non-nil
// Err that does not wrap ErrDegraded.
func blocking(r Result) bool {
	return r.Check.Tier == Required && r.Err != nil && !errors.Is(r.Err, ErrDegraded)
}

// RunChecks runs every check's Probe in order with no short-circuiting, and
// returns one Result per check.
func RunChecks(checks []Check) []Result {
	results := make([]Result, len(checks))
	for i, c := range checks {
		results[i] = runOne(c)
	}
	return results
}

// RunChecksFailFast runs checks in order, stopping after the first blocking
// failure so a later check -- e.g. an extra live network call -- is skipped
// once an earlier required one failed. The returned slice holds only the
// Results for checks that actually ran, so it can be shorter than checks.
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

// FirstRequiredError returns the first blocking failure's Err in slice
// order, or nil if there is none. The error comes back verbatim — never
// wrapped or reformatted — so callers like validate() that assert on exact
// error messages keep working.
func FirstRequiredError(results []Result) error {
	for _, r := range results {
		if blocking(r) {
			return r.Err
		}
	}
	return nil
}

// RunRequiredFailFast is the combined shape a fail-fast gate like validate()
// wants: the first blocking failure, or nil, without the full Result slice.
func RunRequiredFailFast(checks []Check) error {
	return FirstRequiredError(RunChecksFailFast(checks))
}

// ReportResults writes one line per Result to w: "ok: <name>", "MISSING:
// <name>: <err>" plus a "  remedy:" line, or "advisory: <name>: <err>" plus
// remedy when Err wraps ErrDegraded. ErrDegraded's own sentinel text is
// trimmed off — it's an internal marker, not an operator-facing detail — and
// the remedy line is skipped when Remedy merely repeats the error text.
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

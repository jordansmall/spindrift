package doctor

import (
	"errors"
	"fmt"
	"io"
	"strings"
)

// ErrDegraded marks a Probe error as indeterminate: the probe could not
// determine the condition it checks, as distinct from affirmatively detecting
// it. Wrapping it makes FirstRequiredError and RunChecksFailFast treat the
// failure as non-blocking even at Required tier. ReportResults still prints
// the failure and its Remedy, as an advisory line.
var ErrDegraded = errors.New("check degraded: probe could not determine result")

// Tier classifies a Check as blocking (Required) or non-blocking (Advisory).
// A Required-tier failure wrapping ErrDegraded is the exception.
type Tier int

const (
	// Required checks must pass; FirstRequiredError reports their failures.
	Required Tier = iota
	// Advisory failures never make FirstRequiredError return non-nil.
	Advisory
)

// Check is one probe in the doctor check registry.
type Check struct {
	Name string
	Tier Tier
	// Probe runs the check, returning a non-nil error on failure. On success
	// ReportResults passes its first return value to SuccessMsg, so a Probe
	// need not stash that value in a variable a sibling closure captures.
	Probe func() (any, error)
	// Remedy is a short hint printed alongside a failure, e.g. "set
	// GIT_USER_NAME, or configure git user.name on the host".
	Remedy string
	// SuccessMsg overrides the "ok: <Name>" line on success, so a check can
	// report a probe-derived detail such as a live-fetched repo slug. output
	// is Result.Output; nil keeps "ok: <Name>".
	SuccessMsg func(output any) string
}

// Result is the outcome of running one Check's Probe.
type Result struct {
	Check Check
	// Output is what Probe returned alongside a nil Err, which ReportResults
	// passes to Check.SuccessMsg. Meaningless when Err is non-nil.
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

// blocking reports whether r stops a fail-fast chain: a Required-tier Result
// with a non-nil Err that does not wrap ErrDegraded.
func blocking(r Result) bool {
	return r.Check.Tier == Required && r.Err != nil && !errors.Is(r.Err, ErrDegraded)
}

// RunChecks runs every check's Probe in order, never short-circuiting on a
// failure, and returns one Result per check.
func RunChecks(checks []Check) []Result {
	results := make([]Result, len(checks))
	for i, c := range checks {
		results[i] = runOne(c)
	}
	return results
}

// RunChecksFailFast runs checks in order and stops after the first blocking
// Result, so the returned slice can be shorter than checks. That lets a probe
// chain skip an expensive check, such as an extra live network call, once an
// earlier required one has failed.
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

// firstBlockingResult returns the first Result for which blocking reports
// true, or nil. FirstRequiredError and RunRequiredFailFast share it so the two
// cannot drift apart on what counts as blocking.
func firstBlockingResult(results []Result) *Result {
	for i := range results {
		if blocking(results[i]) {
			return &results[i]
		}
	}
	return nil
}

// FirstRequiredError returns the Err of the first blocking Result, or nil.
// The error comes back verbatim, never wrapped or reformatted, because a
// caller like Run (doctor.go) needs the Probe's own error value.
func FirstRequiredError(results []Result) error {
	if r := firstBlockingResult(results); r != nil {
		return r.Err
	}
	return nil
}

// RemedyError pairs a blocking Check's Probe error with the Check, so Error()
// can append the Remedy hint to a fail-fast caller's error text. Error() shares
// Remedy's text and suppression rule with ReportResults, but prints
// "\nremedy: ..." where ReportResults indents.
type RemedyError struct {
	// Err is the failing Check's Probe error, unmodified.
	Err error
	// Check is the failing row, reachable through errors.As for its Name and
	// Remedy.
	Check Check
}

// Error returns Err's text, followed by a "\nremedy: <remedy>" line unless
// Check.Remedy is empty or identical to that text.
func (e *RemedyError) Error() string {
	msg := e.Err.Error()
	if suffix := remedySuffix(e.Check.Remedy, msg); suffix != "" {
		return msg + "\nremedy: " + suffix
	}
	return msg
}

// Unwrap returns Err so errors.Is and errors.As see the underlying Probe
// error.
func (e *RemedyError) Unwrap() error {
	return e.Err
}

// remedySuffix returns remedy, or "" when it is empty or identical to msg so
// the caller skips a remedy line that would just repeat the error. Every
// reporter pairing a Remedy with an error text shares this one copy of the rule.
func remedySuffix(remedy, msg string) string {
	if remedy == "" || remedy == msg {
		return ""
	}
	return remedy
}

// WithRemedy returns r.Err carrying r.Check's Remedy hint, so a caller that
// returns only an error value still tells an operator how to fix the failure.
// It returns r.Err verbatim when the Remedy is empty or only repeats the error
// text, and a literal nil, never a nil *RemedyError, when r.Err is nil.
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
// Required-tier failure through WithRemedy, or nil. The remedy wrapping lives
// here rather than in FirstRequiredError because that function must return the
// error verbatim; nothing depends on this return value's error identity.
func RunRequiredFailFast(checks []Check) error {
	r := firstBlockingResult(RunChecksFailFast(checks))
	if r == nil {
		return nil
	}
	return WithRemedy(*r)
}

// rowPrefix returns the status-line prefix for a Tier: "MISSING" for Required,
// "advisory" for Advisory. ReportResults and doctor.go's label rows share it;
// reporters outside this package spell their own prefixes.
func rowPrefix(t Tier) string {
	if t == Advisory {
		return "advisory"
	}
	return "MISSING"
}

// ReportResults writes one line per Result to w: "ok: <name>" on success, or a
// status line keyed on r.Check.Tier plus a "  remedy: <remedy>" line on
// failure. An Err wrapping ErrDegraded prints as "advisory:" whatever the
// Check's own Tier, with the sentinel's text trimmed off.
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
		// An indeterminate probe demotes the row's tier for rendering
		// only; the Check's own Tier is left alone.
		tier := r.Check.Tier
		msg := r.Err.Error()
		if errors.Is(r.Err, ErrDegraded) {
			tier = Advisory
			// Strips the sentinel only where every call site puts it:
			// wrapped last in the chain.
			msg = strings.TrimSuffix(msg, ": "+ErrDegraded.Error())
		}
		fmt.Fprintf(w, "%s: %s: %s\n", rowPrefix(tier), r.Check.Name, msg)
		if suffix := remedySuffix(r.Check.Remedy, msg); suffix != "" {
			fmt.Fprintf(w, "  remedy: %s\n", suffix)
		}
	}
}

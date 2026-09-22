package doctor

import (
	"errors"
	"fmt"
	"io"
	"strings"
)

// Reporter owns every line doctor writes to its report stream and
// classifies each one at its call site: a success row, a finding keyed on
// Tier, or a passthrough line that fits neither shape. verbose is the one
// stored flag driving the quiet/verbose split (issue #3777); same-package
// code (the quiet-only recap in doctor.go) also reads it directly.
type Reporter struct {
	w       io.Writer
	verbose bool
}

// NewReporter returns a Reporter that writes to w. verbose true reproduces
// today's full report; false suppresses Success rows and Advisory-tier
// Finding rows (issue #3777).
func NewReporter(w io.Writer, verbose bool) *Reporter {
	return &Reporter{w: w, verbose: verbose}
}

// NewDiscardReporter returns a Reporter that drops every row it is handed.
// It takes no verbose bool: the quiet/verbose split is moot when the
// destination is io.Discard regardless.
func NewDiscardReporter() *Reporter {
	return NewReporter(io.Discard, false)
}

// AdvisoryWriter returns the writer for a sub-component (e.g. a launch
// gate's Check) that writes operator-facing prose straight to a raw
// io.Writer, bypassing Success/Finding. Reporter picks that writer — r.w
// when verbose, io.Discard otherwise — so the sub-component's raw prose
// obeys the same quiet gate as an advisory: row (issue #3777).
func (r *Reporter) AdvisoryWriter() io.Writer {
	if r.verbose {
		return r.w
	}
	return io.Discard
}

// visible reports whether a row at Tier t should print: a Required row
// always does (that's the whole point of a blocking failure); an Advisory
// one only under --verbose. Finding and Results both route through this so
// the quiet/verbose line never drifts between the two.
func (r *Reporter) visible(t Tier) bool {
	return t == Required || r.verbose
}

// Success writes an "ok: " row, followed by a newline. Suppressed unless
// verbose (issue #3777): a passing check has nothing for a quiet run to act
// on.
func (r *Reporter) Success(format string, a ...any) {
	if !r.verbose {
		return
	}
	line := fmt.Sprintf(format, a...)
	fmt.Fprint(r.w, "ok: "+line+"\n")
}

// Finding writes a "MISSING: " (Required) or "advisory: " (Advisory) row,
// followed by a newline. The prefix is keyed on t, not on any Check's own
// Tier — a caller demoting a row (e.g. for ErrDegraded) passes the demoted
// tier here. Advisory is suppressed unless verbose (issue #3777); Required
// always writes since a quiet run must still surface what's blocking it.
func (r *Reporter) Finding(t Tier, format string, a ...any) {
	if !r.visible(t) {
		return
	}
	line := fmt.Sprintf(format, a...)
	fmt.Fprint(r.w, rowPrefix(t)+": "+line+"\n")
}

// remedyLine writes a finding's indented "  remedy: <remedy>" line, or
// nothing when remedySuffix reports the remedy repeats the error text. It
// writes unconditionally, ignoring verbose: its standalone call site
// (doctor.go's connectivity fail-fast path) deliberately drops the row above
// it and still needs the remedy to reach the operator. Results, which pairs
// it with a row, is the one that gates it on that row's own visibility.
func (r *Reporter) remedyLine(remedy, msg string) {
	if suffix := remedySuffix(remedy, msg); suffix != "" {
		fmt.Fprint(r.w, "  remedy: "+suffix+"\n")
	}
}

// Passthrough writes text verbatim, with no prefix and no added newline —
// for lines that are neither a success row nor a finding (the interactive
// create-label prompt, "created:" lines, the still-missing-after-creation
// lines).
func (r *Reporter) Passthrough(format string, a ...any) {
	line := fmt.Sprintf(format, a...)
	fmt.Fprint(r.w, line)
}

// rowPrefix returns the status-line prefix for a Tier: "MISSING" for Required,
// "advisory" for Advisory. Finding is its only caller; reporters outside this
// package spell their own prefixes.
func rowPrefix(t Tier) string {
	if t == Advisory {
		return "advisory"
	}
	return "MISSING"
}

// Results reports one row per Result: an "ok: <name>" success row, or a
// status line keyed on r.Check.Tier plus a "  remedy: <remedy>" line on
// failure. An Err wrapping ErrDegraded prints as "advisory:" whatever the
// Check's own Tier, with the sentinel's text trimmed off.
func (r *Reporter) Results(results []Result) {
	for _, res := range results {
		if res.Err == nil {
			if res.Check.SuccessMsg != nil {
				r.Success("%s", res.Check.SuccessMsg(res.Output))
				continue
			}
			r.Success("%s", res.Check.Name)
			continue
		}
		// An indeterminate probe demotes the row's tier for rendering
		// only; the Check's own Tier is left alone.
		tier := res.Check.Tier
		msg := res.Err.Error()
		if errors.Is(res.Err, ErrDegraded) {
			tier = Advisory
			// Strips the sentinel only where every call site puts it:
			// wrapped last in the chain.
			msg = strings.TrimSuffix(msg, ": "+ErrDegraded.Error())
		}
		// visible gates both lines together: a suppressed row must not
		// leave its remedy printed underneath nothing (issue #3777).
		if !r.visible(tier) {
			continue
		}
		r.Finding(tier, "%s: %s", res.Check.Name, msg)
		r.remedyLine(res.Check.Remedy, msg)
	}
}

package doctor

import (
	"errors"
	"fmt"
	"io"
	"strings"
)

// Reporter owns every line doctor writes to its report stream and
// classifies each one at its call site: a success row, a finding keyed on
// Tier, or a passthrough line that fits neither shape.
type Reporter struct {
	w io.Writer
}

// NewReporter returns a Reporter that writes to w.
func NewReporter(w io.Writer) *Reporter {
	return &Reporter{w: w}
}

// Success writes an "ok: " row, followed by a newline.
func (r *Reporter) Success(format string, a ...any) {
	line := fmt.Sprintf(format, a...)
	fmt.Fprint(r.w, "ok: "+line+"\n")
}

// Finding writes a "MISSING: " (Required) or "advisory: " (Advisory) row,
// followed by a newline. The prefix is keyed on t, not on any Check's own
// Tier — a caller demoting a row (e.g. for ErrDegraded) passes the demoted
// tier here.
func (r *Reporter) Finding(t Tier, format string, a ...any) {
	line := fmt.Sprintf(format, a...)
	fmt.Fprint(r.w, rowPrefix(t)+": "+line+"\n")
}

// remedyLine writes a finding's indented "  remedy: <remedy>" line, or
// nothing when remedySuffix reports the remedy repeats the error text.
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
		r.Finding(tier, "%s: %s", res.Check.Name, msg)
		r.remedyLine(res.Check.Remedy, msg)
	}
}

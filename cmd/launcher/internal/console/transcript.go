package console

import (
	"fmt"
	"os"
	"strings"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/driver"
)

// DrillIn loads and renders every pass log dispatch.LogPaths finds for
// number under pwd, concatenated in chronological order with a boundary
// line between passes — both in drv's rendered form and in byte-exact raw
// form, loaded together so the raw toggle needs no further I/O. Wraps the
// result into a Msg Update can apply directly, matching Refresh and
// PickIssue's adapter shape.
func DrillIn(drv driver.Driver, pwd, number string) Msg {
	passes := dispatch.LogPaths(pwd, number)
	if len(passes) == 0 {
		return DrillInMsg{Number: number, Err: fmt.Errorf("no logs found for issue #%s", number)}
	}

	var rendered, raw strings.Builder
	for _, p := range passes {
		boundary := fmt.Sprintf("=== pass: %s ===\n", p.Label)
		rendered.WriteString(boundary)
		raw.WriteString(boundary)

		text, err := drv.RenderTranscript(p.Path)
		if err != nil {
			return DrillInMsg{Number: number, Err: err}
		}
		rendered.WriteString(text)

		bytes, err := os.ReadFile(p.Path)
		if err != nil {
			return DrillInMsg{Number: number, Err: err}
		}
		raw.Write(bytes)
	}
	return DrillInMsg{Number: number, Rendered: rendered.String(), Raw: raw.String()}
}

package console

import (
	"fmt"
	"strings"
)

// View renders m as the text the run loop writes to the terminal: an
// optional dogfood-competition notice, the visible backlog (one line per
// issue: number, title, labels), and any refresh error.
func View(m Model) string {
	var b strings.Builder
	if m.DogfoodLive {
		b.WriteString("notice: a live dogfood loop (.dogfood.pid) is competing for the same queue\n")
	}
	for _, iss := range m.Visible() {
		fmt.Fprintf(&b, "#%s  %s  [%s]\n", iss.Number, iss.Title, strings.Join(iss.Labels, ", "))
	}
	if m.Err != nil {
		fmt.Fprintf(&b, "refresh failed: %s\n", m.Err)
	}
	return b.String()
}

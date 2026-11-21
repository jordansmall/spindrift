package forge

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/testutil"
)

func TestWarnPageMayTruncateBacklog_AtLimitWarns(t *testing.T) {
	for _, source := range []string{"gh issue list", "jira search"} {
		t.Run(source, func(t *testing.T) {
			out := testutil.CaptureStderr(t, func() {
				WarnPageMayTruncateBacklog(source, ResultPageLimit)
			})
			if !strings.Contains(out, source) || !strings.Contains(out, "backlog may be larger") {
				t.Errorf("WarnPageMayTruncateBacklog(%q, limit) output = %q, want it to mention the source and the backlog warning", source, out)
			}
		})
	}
}

func TestWarnPageMayTruncateBacklog_UnderLimitSilent(t *testing.T) {
	out := testutil.CaptureStderr(t, func() {
		WarnPageMayTruncateBacklog("gh issue list", ResultPageLimit-1)
	})
	if out != "" {
		t.Errorf("WarnPageMayTruncateBacklog under the limit printed %q, want silence", out)
	}
}

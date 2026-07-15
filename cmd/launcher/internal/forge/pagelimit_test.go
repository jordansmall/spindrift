package forge

import (
	"os"
	"strings"
	"testing"
)

// captureStderr runs fn with os.Stderr redirected to a pipe and returns
// everything written to it.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w

	fn()

	w.Close()
	os.Stderr = orig

	var buf strings.Builder
	tmp := make([]byte, 4096)
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			buf.Write(tmp[:n])
		}
		if err != nil {
			break
		}
	}
	return buf.String()
}

// TestWarnPageMayTruncateBacklog_AtLimitWarns verifies the shared page-limit
// warning fires once count reaches ResultPageLimit, for both the github and
// jira sources that consume it.
func TestWarnPageMayTruncateBacklog_AtLimitWarns(t *testing.T) {
	for _, source := range []string{"gh issue list", "jira search"} {
		t.Run(source, func(t *testing.T) {
			out := captureStderr(t, func() {
				WarnPageMayTruncateBacklog(source, ResultPageLimit)
			})
			if !strings.Contains(out, source) || !strings.Contains(out, "backlog may be larger") {
				t.Errorf("WarnPageMayTruncateBacklog(%q, limit) output = %q, want it to mention the source and the backlog warning", source, out)
			}
		})
	}
}

// TestWarnPageMayTruncateBacklog_UnderLimitSilent verifies the warning is
// silent when a page comes back under ResultPageLimit.
func TestWarnPageMayTruncateBacklog_UnderLimitSilent(t *testing.T) {
	out := captureStderr(t, func() {
		WarnPageMayTruncateBacklog("gh issue list", ResultPageLimit-1)
	})
	if out != "" {
		t.Errorf("WarnPageMayTruncateBacklog under the limit printed %q, want silence", out)
	}
}

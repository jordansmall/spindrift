package sandbox

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/runner"
)

func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

func TestReapOrphans(t *testing.T) {
	dead := sandboxName(deadPID(t), time.Now())
	live := sandboxName(os.Getpid(), time.Now())
	tests := []struct {
		name    string
		running []string
		listErr error
		killErr error
		want    []string
		out     string
	}{
		{name: "dead owner is reaped", running: []string{dead}, want: []string{dead}, out: "reaped orphaned Connector sandbox"},
		{name: "live owner is left", running: []string{live}},
		{name: "unparsable name is left", running: []string{runner.ConnectorSandboxPrefix + "x-y", runner.ConnectorSandboxPrefix}},
		{name: "non-sandbox names are left", running: []string{"agent-issue-7016", "spindrift-connector-sandboxes", strings.TrimPrefix(dead, "spindrift-")}},
		{name: "only the dead one of several", running: []string{live, "agent-issue-1", dead}, want: []string{dead}, out: "reaped"},
		{name: "list error warns, kills nothing", listErr: errors.New("boom"), out: "boom"},
		{name: "kill error warns", running: []string{dead}, killErr: errors.New("nope"), want: []string{dead}, out: "nope"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &runner.Fake{RunningNames: tc.running, ListRunningErr: tc.listErr, KillErr: tc.killErr}
			var w bytes.Buffer
			ReapOrphans(f, &w)
			if strings.Join(f.KillCalls, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("killed %v, want %v", f.KillCalls, tc.want)
			}
			if !strings.Contains(w.String(), tc.out) || (tc.out == "" && w.Len() != 0) {
				t.Fatalf("output %q, want containing %q", w.String(), tc.out)
			}
		})
	}
}

func TestNameRoundTrip(t *testing.T) {
	pid, ok := ownerPID(sandboxName(4242, time.Now()))
	if !ok || pid != 4242 {
		t.Fatalf("ownerPID = %d, %v", pid, ok)
	}
}

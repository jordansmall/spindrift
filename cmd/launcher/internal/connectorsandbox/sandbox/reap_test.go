package sandbox

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/runner"
)

const mine = "0123456789ab"

func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

func setOwner(t *testing.T, id string, err error) {
	t.Helper()
	orig := ownerID
	ownerID = func() (string, error) { return id, err }
	t.Cleanup(func() { ownerID = orig })
}

func setAlive(t *testing.T, f func(int) bool) {
	t.Helper()
	orig := pidAlive
	pidAlive = f
	t.Cleanup(func() { pidAlive = orig })
}

func TestPidAliveRealProcesses(t *testing.T) {
	if !pidAlive(os.Getpid()) {
		t.Fatal("own pid reported dead")
	}
	if pidAlive(deadPID(t)) {
		t.Fatal("exited pid reported alive")
	}
}

func TestReapOrphans(t *testing.T) {
	const deadP, liveP = 1111, 2222
	setOwner(t, mine, nil)
	setAlive(t, func(pid int) bool { return pid == liveP })
	dead := sandboxName(mine, deadP, time.Now())
	live := sandboxName(mine, liveP, time.Now())
	foreign := sandboxName("ba9876543210", deadP, time.Now())
	unownedDead := sandboxName(unowned, deadP, time.Now())
	legacy := runner.ConnectorSandboxPrefix + "1234567-" + "1700000000000000000"
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
		{name: "foreign owner is left", running: []string{foreign}},
		{name: "legacy name is noted, not reaped", running: []string{legacy}, out: "no owner identity (remove it by hand if orphaned): " + legacy},
		{name: "unowned name is noted, not reaped", running: []string{unownedDead}, out: "no owner identity (remove it by hand if orphaned): " + unownedDead},
		{name: "unparsable name is left", running: []string{runner.ConnectorSandboxPrefix + "x-y", runner.ConnectorSandboxPrefix, runner.ConnectorSandboxPrefix + mine + "-x-1"}},
		{name: "non-sandbox names are left", running: []string{"agent-issue-7016", "spindrift-connector-sandboxes", strings.TrimPrefix(dead, "spindrift-")}},
		{name: "only the dead one of several", running: []string{live, "agent-issue-1", foreign, dead}, want: []string{dead}, out: "reaped"},
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

func TestReapOrphansUnreadableOwnerReapsNothing(t *testing.T) {
	setOwner(t, "", errors.New("no proc"))
	dead := sandboxName(mine, 1111, time.Now())
	f := &runner.Fake{RunningNames: []string{dead}, ListRunningErr: errors.New("must not list")}
	var w bytes.Buffer
	ReapOrphans(f, &w)
	if len(f.KillCalls) != 0 {
		t.Fatalf("killed %v, want none", f.KillCalls)
	}
	if !strings.Contains(w.String(), "owner identity") || strings.Contains(w.String(), "must not list") {
		t.Fatalf("output %q", w.String())
	}
}

func TestNameRoundTrip(t *testing.T) {
	if len(mine) != ownerLen {
		t.Fatalf("mine = %q, want %d chars", mine, ownerLen)
	}
	owner, pid, ok := parseName(sandboxName(mine, 4242, time.Now()))
	if !ok || owner != mine || pid != 4242 {
		t.Fatalf("parseName = %q, %d, %v", owner, pid, ok)
	}
	pre := runner.ConnectorSandboxPrefix
	for name, want := range map[string]struct {
		owner string
		pid   int
		ok    bool
	}{
		pre + "4242-1700000000000000000":      {"", 4242, true},
		pre + "0-1700000000000000000":         {},
		pre + mine + "-0-1700000000000000000": {},
		pre + mine + "-4242-x":                {},
		pre + "4242":                          {},
		"other-" + mine + "-4242-1":           {},
	} {
		owner, pid, ok := parseName(name)
		if owner != want.owner || pid != want.pid || ok != want.ok {
			t.Errorf("parseName(%q) = %q, %d, %v; want %v", name, owner, pid, ok, want)
		}
	}
}

func TestRealOwnerAndName(t *testing.T) {
	id, err := ownerID()
	if err != nil {
		t.Skipf("owner identity unreadable here: %v", err)
	}
	if !regexp.MustCompile(fmt.Sprintf(`^[0-9a-f]{%d}$`, ownerLen)).MatchString(id) {
		t.Fatalf("ownerID = %q, want %d hex chars", id, ownerLen)
	}
	if n := Name(os.Getpid(), time.Now()); !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`).MatchString(n) {
		t.Fatalf("Name = %q is not a legal container name", n)
	}
}

func TestNameUnownedWhenIdentityUnreadable(t *testing.T) {
	setOwner(t, "", errors.New("no proc"))
	owner, _, ok := parseName(Name(1, time.Now()))
	if !ok || owner != unowned {
		t.Fatalf("owner = %q, %v", owner, ok)
	}
}

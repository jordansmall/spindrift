package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

func hostname(t *testing.T) string {
	t.Helper()
	h, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// get serves content as the status file through a real handler and returns
// the response body; a nil content leaves the file absent.
func get(t *testing.T, content *string, path string) (int, string) {
	t.Helper()
	statusPath := filepath.Join(t.TempDir(), statusFileName)
	if content != nil {
		if err := os.WriteFile(statusPath, []byte(*content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	srv := newServer(statusPath)
	srv.now = func() time.Time { return testNow }
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	body, _ := io.ReadAll(rec.Result().Body)
	return rec.Code, string(body)
}

func statusJSON(t *testing.T, state, reason string, slots string) string {
	t.Helper()
	r := ""
	if reason != "" {
		r = `"reason":"` + reason + `",`
	}
	return `{"pid":` + itoa(os.Getpid()) + `,"host":"` + hostname(t) + `",` +
		`"started":"2026-10-07T10:00:00Z","time":"2026-10-07T11:59:58Z",` +
		`"kinds":["work","research"],"state":"` + state + `",` + r +
		`"slots":` + slots + `,"checks":[],"trackers":[]}`
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestPoolStates(t *testing.T) {
	idle := `[{"slot":0,"phase":"idle","busy":false,"since":"2026-10-07T11:00:00Z"}]`
	for _, state := range []string{"working", "waiting", "jammed", "asleep", "checking"} {
		t.Run(state, func(t *testing.T) {
			c := statusJSON(t, state, "", idle)
			code, body := get(t, &c, "/")
			if code != 200 {
				t.Fatalf("code = %d", code)
			}
			if !strings.Contains(body, `state-`+state) {
				t.Errorf("no state-%s class in body", state)
			}
			if strings.Contains(body, "no Daemon running") {
				t.Errorf("live daemon rendered as absent")
			}
		})
	}
}

func TestHaltedShowsReason(t *testing.T) {
	c := statusJSON(t, "halted", "preflight refused: label missing", `[]`)
	_, body := get(t, &c, "/")
	for _, want := range []string{"state-halted", "preflight refused: label missing"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
}

func TestHeaderFacts(t *testing.T) {
	slots := `[
	  {"slot":0,"phase":"running","busy":true,"since":"2026-10-07T11:47:57Z","kind":"work","revision":"0123456789abcdef","issues":["4712","4713"]},
	  {"slot":1,"phase":"running","busy":true,"since":"2026-10-07T11:59:00Z","kind":"butler","chore":"dead-code"},
	  {"slot":2,"phase":"idle","busy":false,"since":"2026-10-07T11:00:00Z"},
	  {"slot":3,"phase":"backing_off","busy":false,"since":"2026-10-07T11:30:00Z"}]`
	c := statusJSON(t, "working", "", slots)
	_, body := get(t, &c, "/")
	for _, want := range []string{
		hostname(t),
		"2/4 busy",
		"2h 0m 0s",       // uptime: now - started
		"12m 3s",         // slot 0 time in phase, from since
		"#4712", "#4713", // issues
		"0123456789ab", // short revision
		"dead-code",    // chore
		"phase-running", "phase-idle", "phase-backing_off",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
	if strings.Contains(body, "0123456789abcdef") {
		t.Errorf("revision not shortened")
	}
}

func TestIdleSlotRendersAsIdleCard(t *testing.T) {
	c := statusJSON(t, "waiting", "", `[{"slot":0,"phase":"idle","busy":false,"since":"2026-10-07T11:00:00Z"}]`)
	_, body := get(t, &c, "/")
	if !strings.Contains(body, `class="card phase-idle"`) {
		t.Errorf("no idle card:\n%s", body)
	}
	if !strings.Contains(body, "1h 0m 0s") {
		t.Errorf("idle time in phase missing")
	}
	if !strings.Contains(body, "0/1 busy") {
		t.Errorf("busy count missing")
	}
}

func TestKindAndTrackerRows(t *testing.T) {
	c := `{"pid":` + itoa(os.Getpid()) + `,"host":"` + hostname(t) + `","started":"2026-10-07T10:00:00Z",
	"time":"2026-10-07T11:59:58Z","kinds":["work","butler"],"state":"jammed","slots":[],
	"checks":[
	 {"kind":"work","nextCheck":"2026-10-07T12:05:00Z","jammed":true,"ready":3,"probed_at":"2026-10-07T11:55:00Z",
	  "next_probe":"2026-10-07T12:10:00Z","jam_until":"2026-10-07T12:30:00Z","ready_at_jam":2},
	 {"kind":"butler","next_due":"on_tip_move"},
	 {"kind":"research","next_due":"2026-10-07T13:00:00Z","next_due_on_tip_move":true}],
	"trackers":[{"tracker":"github","rate_limited_until":"2026-10-07T12:20:00Z"}]}`
	_, body := get(t, &c, "/")
	for _, want := range []string{
		"2026-10-07T12:05:00Z", "jammed", "2026-10-07T12:30:00Z",
		"2026-10-07T11:55:00Z", "2026-10-07T12:10:00Z",
		"on tip move", "2026-10-07T13:00:00Z",
		"github", "2026-10-07T12:20:00Z",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
}

func TestNoDaemon(t *testing.T) {
	code, body := get(t, nil, "/")
	if code != 200 || !strings.Contains(body, "no Daemon running") {
		t.Errorf("code=%d body=%s", code, body)
	}
}

func goneCommandPid(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

func TestStalePid(t *testing.T) {
	c := `{"pid":` + itoa(goneCommandPid(t)) + `,"host":"` + hostname(t) + `","started":"2026-10-07T10:00:00Z",
	"time":"2026-10-07T11:59:58Z","kinds":["work"],"state":"working","slots":[],"checks":[]}`
	code, body := get(t, &c, "/")
	if code != 200 || !strings.Contains(body, "no Daemon running") {
		t.Errorf("code=%d body=%s", code, body)
	}
	if strings.Contains(body, "state-working") {
		t.Errorf("stale status rendered as live")
	}
}

func TestDeadPidShowsNonHaltedReason(t *testing.T) {
	c := `{"pid":` + itoa(goneCommandPid(t)) + `,"host":"` + hostname(t) + `","started":"2026-10-07T10:00:00Z",
	"time":"2026-10-07T11:59:58Z","kinds":["work"],"state":"waiting","reason":"waiting on demand",
	"slots":[],"checks":[]}`
	_, body := get(t, &c, "/")
	if !strings.Contains(body, "waiting on demand") || strings.Contains(body, "Pool halted") {
		t.Errorf("non-halted reason missing or shown as a halt:\n%s", body)
	}
}

func TestHaltedDeadPidShowsReason(t *testing.T) {
	c := `{"pid":` + itoa(goneCommandPid(t)) + `,"host":"` + hostname(t) + `","started":"2026-10-07T10:00:00Z",
	"time":"2026-10-07T11:59:58Z","kinds":["work"],"state":"halted","reason":"breaker tripped: 3 red runs",
	"slots":[{"slot":0,"phase":"idle","busy":false,"since":"2026-10-07T11:00:00Z"}],"checks":[]}`
	_, body := get(t, &c, "/")
	for _, want := range []string{"no Daemon running", "breaker tripped: 3 red runs", "Pool halted", "2026-10-07T11:59:58Z"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
	for _, bad := range []string{"state-halted", "stale leftover", `class="card`} {
		if strings.Contains(body, bad) {
			t.Errorf("dead daemon body contains %q", bad)
		}
	}
}

func TestRemoteHostTreatedLive(t *testing.T) {
	c := `{"pid":1,"host":"some-other-host.invalid","started":"2026-10-07T10:00:00Z",
	"time":"2026-10-07T11:59:58Z","kinds":["work"],"state":"waiting","slots":[],"checks":[]}`
	_, body := get(t, &c, "/")
	if strings.Contains(body, "no Daemon running") || !strings.Contains(body, "some-other-host.invalid") {
		t.Errorf("remote daemon not reported:\n%s", body)
	}
}

func TestPidNotPositiveIsNotLive(t *testing.T) {
	c := `{"pid":0,"host":"` + hostname(t) + `","state":"working","slots":[],"checks":[]}`
	_, body := get(t, &c, "/")
	if !strings.Contains(body, "no Daemon running") {
		t.Errorf("pid 0 read as live")
	}
}

func TestUnparseableFile(t *testing.T) {
	c := `{not json <script>alert(1)</script>`
	code, body := get(t, &c, "/")
	if code != 200 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(body, "class=\"banner error\"") ||
		!strings.Contains(body, "{not json &lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Errorf("error banner / escaped raw content missing:\n%s", body)
	}
}

func TestUnknownPath404(t *testing.T) {
	c := statusJSON(t, "waiting", "", `[]`)
	if code, _ := get(t, &c, "/nope"); code != 404 {
		t.Errorf("code = %d", code)
	}
}

func TestAssetsServed(t *testing.T) {
	for _, p := range []string{"/static/style.css"} {
		if code, body := get(t, nil, p); code != 200 || body == "" {
			t.Errorf("%s: code=%d len=%d", p, code, len(body))
		}
	}
}

func TestFormatDuration(t *testing.T) {
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{0, "0s"}, {5 * time.Second, "5s"}, {723 * time.Second, "12m 3s"},
		{2 * time.Hour, "2h 0m 0s"}, {50*time.Hour + time.Minute, "2d 2h 1m"},
		{-time.Second, "0s"},
	} {
		if got := formatDuration(tc.d); got != tc.want {
			t.Errorf("formatDuration(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

package main

import (
	"fmt"
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
	srv := newServer("", statusPath)
	srv.now = func() time.Time { return testNow }
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	body, _ := io.ReadAll(rec.Result().Body)
	return rec.Code, string(body)
}

func statusJSON(t *testing.T, state, reason string, slots string) string {
	t.Helper()
	return statusJSONRepo(t, state, reason, slots, "")
}

// statusJSONRepo is statusJSON with the Daemon's repo_url published, or
// omitted when repoURL is empty as for a tracker with no web repo.
func statusJSONRepo(t *testing.T, state, reason, slots, repoURL string) string {
	t.Helper()
	repo := ""
	if repoURL != "" {
		repo = `"repo_url":"` + repoURL + `",`
	}
	r := ""
	if reason != "" {
		r = `"reason":"` + reason + `",`
	}
	return `{"schema":1,"pid":` + itoa(os.Getpid()) + `,"host":"` + hostname(t) + `",` +
		`"started":"2026-10-07T10:00:00Z","time":"2026-10-07T11:59:58Z",` +
		`"kinds":["work","research"],"state":"` + state + `",` + r + repo +
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

func TestRunningSlotShowsPass(t *testing.T) {
	running := func(pass string) string {
		return statusJSON(t, "working", "", `[{"slot":0,"phase":"running","busy":true,"since":"2026-10-07T11:00:00Z","kind":"work","issues":["7"]`+pass+`}]`)
	}
	with := running(`,"pass":"fix-pass-2"`)
	_, body := get(t, &with, "/")
	if !strings.Contains(body, `<div class="pass">pass fix-pass-2</div>`) {
		t.Errorf("pass missing from running slot card:\n%s", body)
	}
	without := running("")
	_, body = get(t, &without, "/")
	if strings.Contains(body, `class="pass"`) {
		t.Errorf("pass element rendered with no pass published:\n%s", body)
	}
}

func TestRunningSlotShowsModel(t *testing.T) {
	running := func(model string) string {
		return statusJSON(t, "working", "", `[{"slot":0,"phase":"running","busy":true,"since":"2026-10-07T11:00:00Z","kind":"work","issues":["7"]`+model+`}]`)
	}
	cases := []struct {
		name, fields, want string
	}{
		{"role and model", `,"model":"claude-sonnet-5-5","model_role":"worker"`, `<div class="model">worker · claude-sonnet-5-5</div>`},
		{"model alone", `,"model":"claude-sonnet-5-5"`, `<div class="model">claude-sonnet-5-5</div>`},
	}
	for _, c := range cases {
		st := running(c.fields)
		_, body := get(t, &st, "/")
		if !strings.Contains(body, c.want) {
			t.Errorf("%s: want %q in card:\n%s", c.name, c.want, body)
		}
	}
	none := running("")
	_, body := get(t, &none, "/")
	if strings.Contains(body, `class="model"`) {
		t.Errorf("model element rendered with no model published:\n%s", body)
	}
}

func TestRunningSlotShowsCIWait(t *testing.T) {
	running := func(fields string) string {
		return statusJSON(t, "working", "", `[{"slot":0,"phase":"running","busy":true,"since":"2026-10-07T11:00:00Z","kind":"work","issues":["7"]`+fields+`}]`)
	}
	waiting := running(`,"ci_wait":true,"pr_url":"https://github.com/o/r/pull/12"`)
	_, body := get(t, &waiting, "/")
	want := `<a class="pill ci-wait" href="https://github.com/o/r/pull/12" target="_blank" rel="noopener noreferrer">waiting on CI</a>`
	if !strings.Contains(body, want) {
		t.Errorf("want %q in card:\n%s", want, body)
	}
	for name, fields := range map[string]string{
		"not waiting": "",
		"pr only":     `,"pr_url":"https://github.com/o/r/pull/12"`,
	} {
		st := running(fields)
		_, body := get(t, &st, "/")
		if strings.Contains(body, "waiting on CI") {
			t.Errorf("%s: CI wait pill rendered:\n%s", name, body)
		}
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
		`<time datetime="2026-10-07T11:59:58Z">2026-10-07T11:59:58Z</time>`,
		`<time datetime="2026-10-07T12:05:00Z">`, `<time datetime="2026-10-07T12:30:00Z">`,
		`<time datetime="2026-10-07T11:55:00Z">`, `<time datetime="2026-10-07T12:10:00Z">`,
		`<time datetime="2026-10-07T13:00:00Z">`, `<time datetime="2026-10-07T12:20:00Z">`,
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

var skewBanner = fmt.Sprintf("Daemon schema 999, Dashboard understands %d: restart the Dashboard", statusSchema)

func TestUnknownSchemaShowsBannerAndRawJSON(t *testing.T) {
	c := strings.Replace(statusJSON(t, "working", "",
		`[{"slot":0,"phase":"zz_phase","busy":true,"since":"2026-10-07T11:00:00Z","issues":["4713"]}]`),
		`"schema":1`, `"schema":999`, 1)
	code, body := get(t, &c, "/")
	if code != 200 {
		t.Fatalf("code = %d", code)
	}
	for _, want := range []string{skewBanner, `<pre class="raw">`, "&#34;schema&#34;:999", "zz_phase"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q:\n%s", want, body)
		}
	}
	for _, bad := range []string{"state-", `class="card`, "phase-zz_phase", "#4713", "Slots</h2>"} {
		if strings.Contains(body, bad) {
			t.Errorf("body renders the views despite schema skew (%q):\n%s", bad, body)
		}
	}
}

func TestUnknownSchemaWithChangedFieldTypesStillShowsSkew(t *testing.T) {
	c := `{"schema":999,"pid":"abc","slots":{}}`
	_, body := get(t, &c, "/")
	if !strings.Contains(body, skewBanner) {
		t.Errorf("skew banner missing:\n%s", body)
	}
	if strings.Contains(body, "could not be read") {
		t.Errorf("skew reported as a parse error:\n%s", body)
	}
}

func TestKnownSchemaRendersLive(t *testing.T) {
	known := statusJSON(t, "working", "", `[]`)
	for name, c := range map[string]string{
		"explicit": known,
		"missing":  strings.Replace(known, `"schema":1,`, "", 1),
	} {
		t.Run(name, func(t *testing.T) {
			_, body := get(t, &c, "/")
			if !strings.Contains(body, "state-working") || strings.Contains(body, "restart the Dashboard") {
				t.Errorf("schema-1 file not rendered live:\n%s", body)
			}
		})
	}
}

func TestUnknownPath404(t *testing.T) {
	c := statusJSON(t, "waiting", "", `[]`)
	if code, _ := get(t, &c, "/nope"); code != 404 {
		t.Errorf("code = %d", code)
	}
}

func TestAssetsServed(t *testing.T) {
	for _, p := range []string{"/static/style.css", "/static/time.js"} {
		if code, body := get(t, nil, p); code != 200 || body == "" {
			t.Errorf("%s: code=%d len=%d", p, code, len(body))
		}
	}
}

func TestPageLoadsLiveScript(t *testing.T) {
	c := statusJSON(t, "waiting", "", `[]`)
	if _, body := get(t, &c, "/"); !strings.Contains(body, `<script src="/static/live.js"`) {
		t.Error("page does not load /static/live.js")
	}
	if code, body := get(t, nil, "/static/live.js"); code != 200 || !strings.Contains(body, `EventSource("/events")`) {
		t.Errorf("live.js: code=%d, no EventSource", code)
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

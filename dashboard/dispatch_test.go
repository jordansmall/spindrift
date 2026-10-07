package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const dispatchEvents = `{"time":"2026-10-07T09:00:00Z","event":"child_start","kind":"work","slot":0,"child_log":"logs/first.log"}
{"time":"2026-10-07T09:00:05Z","event":"box","slot":0,"phase":"initial","issue":"11"}
{"time":"2026-10-07T09:10:00Z","event":"settled","slot":0,"issue":"11","state":"failed","note":"first went wrong"}
{"time":"2026-10-07T09:10:01Z","event":"child_finish","slot":0,"issue":"11","exit":3,"revision":"0123456789abcdef0123"}
{"time":"2026-10-07T09:30:00Z","event":"child_start","kind":"research","slot":1,"child_log":"logs/other.log"}
{"time":"2026-10-07T10:00:00Z","event":"child_start","kind":"work","slot":0,"child_log":"logs/a b&c.log"}
{"time":"2026-10-07T10:00:05Z","event":"box","slot":0,"phase":"initial","issue":"22"}
{"time":"2026-10-07T10:00:06Z","event":"box","slot":0,"phase":"fix-pass-1","issue":"22"}
{"time":"2026-10-07T10:05:00Z","event":"settled","slot":0,"issue":"22","state":"complete","note":"second landed"}
`

func getDispatch(t *testing.T, events, query string) (int, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, eventsFileName), []byte(events), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := newServer(dir, filepath.Join(dir, statusFileName))
	srv.now = func() time.Time { return testNow }
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dispatch"+query, nil))
	return rec.Code, rec.Body.String()
}

func TestDispatchResolvesBySlotAndTime(t *testing.T) {
	for _, tc := range []struct {
		name, query string
		want, not   []string
	}{
		{"row inside the first dispatch", "?slot=0&at=2026-10-07T09:00:00Z",
			[]string{"#11", "first went wrong", "exit", "0123456789ab", "logs%2ffirst.log"}, []string{"second landed", "#22"}},
		{"child_start resolves to itself", "?slot=0&at=2026-10-07T10:00:00Z",
			[]string{"#22", "second landed"}, []string{"first went wrong"}},
		{"at absent is the latest", "?slot=0",
			[]string{"#22", "second landed", "fix-pass-1"}, []string{"first went wrong"}},
		{"other slot", "?slot=1&at=2026-10-07T09:30:00Z",
			[]string{"research", "other.log"}, []string{"#22", "#11"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, body := getDispatch(t, dispatchEvents, tc.query)
			if code != 200 {
				t.Fatalf("status %d", code)
			}
			low := strings.ToLower(body)
			for _, w := range tc.want {
				if !strings.Contains(low, strings.ToLower(w)) {
					t.Errorf("missing %q in %s", w, body)
				}
			}
			for _, n := range tc.not {
				if strings.Contains(body, n) {
					t.Errorf("unexpected %q", n)
				}
			}
		})
	}
}

func TestDispatchNotFound(t *testing.T) {
	for _, q := range []string{
		"", "?slot=x", "?slot=-1", "?slot=7", "?slot=0&at=2026-10-07T08:00:00Z",
		"?slot=0&at=garbage", "?at=2026-10-07T10:00:00Z",
	} {
		if code, _ := getDispatch(t, dispatchEvents, q); code != 404 {
			t.Errorf("%q: status %d, want 404", q, code)
		}
	}
	if code, _ := getDispatch(t, "", "?slot=0"); code != 404 {
		t.Errorf("no events: status %d, want 404", code)
	}
}

func TestDispatchLogHook(t *testing.T) {
	_, body := getDispatch(t, dispatchEvents, "?slot=0")
	for _, w := range []string{
		`<pre class="log" data-src="/log?path=logs%2fa%20b%26c.log" data-stream-filter`,
		`class="show-all"`, `class="follow-state"`, `src="/static/dispatch.js"`, `href="/"`,
		`pill outcome-complete`,
	} {
		if !strings.Contains(strings.ToLower(body), strings.ToLower(w)) {
			t.Errorf("missing %q in\n%s", w, body)
		}
	}
}

func TestDispatchWithoutChildLog(t *testing.T) {
	ev := `{"time":"2026-10-07T09:00:00Z","event":"child_start","kind":"work","slot":0}` + "\n"
	code, body := getDispatch(t, ev, "?slot=0")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	if !strings.Contains(body, "names no Child log") || strings.Contains(body, `class="log"`) {
		t.Errorf("want the no-log note and no log element:\n%s", body)
	}
}

func TestDispatchSpansGenerations(t *testing.T) {
	dir := t.TempDir()
	events := filepath.Join(dir, eventsFileName)
	os.WriteFile(events+rotatedSuffix, []byte(`{"time":"2026-10-06T09:00:00Z","event":"child_start","kind":"work","slot":0,"child_log":"old.log"}`+"\n"), 0o644)
	os.WriteFile(events, []byte(`{"time":"2026-10-07T09:00:00Z","event":"settled","slot":0,"state":"failed"}`+"\n"), 0o644)
	srv := newServer(dir, filepath.Join(dir, statusFileName))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dispatch?slot=0", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "outcome-failed") {
		t.Errorf("status %d body %s", rec.Code, rec.Body)
	}
}

func TestIndexLinksToDispatch(t *testing.T) {
	slots := `[{"slot":0,"phase":"running","busy":true,"kind":"work","issues":["5"],"since":"2026-10-07T11:00:00Z"},{"slot":1,"phase":"idle"}]`
	c := statusJSON(t, "working", "", slots)
	body := getHistory(t, &c, map[string]string{eventsFileName: dispatchEvents})
	for _, w := range []string{
		`href="/dispatch?slot=0"`,
		`href="/dispatch?slot=0&at=2026-10-07T10%3a00%3a00Z"`,
	} {
		if !strings.Contains(strings.ToLower(body), strings.ToLower(w)) {
			t.Errorf("missing %q in %s", w, body)
		}
	}
	if strings.Contains(body, `href="/dispatch?slot=1"`) {
		t.Error("idle slot must not link")
	}
}

// A Dispatch's last row and the next Dispatch's child_start can share a
// second on one slot; every row must still link to its own Dispatch.
const sameSecondEvents = `{"time":"2026-10-07T09:00:00Z","event":"child_start","kind":"work","slot":0,"child_log":"logs/a.log"}
{"time":"2026-10-07T09:10:01Z","event":"settled","slot":0,"issue":"11","state":"failed","note":"a went wrong"}
{"time":"2026-10-07T09:10:01Z","event":"child_finish","slot":0,"issue":"11","exit":3}
{"time":"2026-10-07T09:10:01Z","event":"child_start","kind":"work","slot":0,"child_log":"logs/b.log"}
{"time":"2026-10-07T09:10:01Z","event":"child_start","kind":"work","slot":0,"child_log":"logs/c.log"}
{"time":"2026-10-07T09:10:05Z","event":"box","slot":0,"phase":"initial","issue":"33"}
`

func TestEntriesLinkToOwningDispatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, eventsFileName)
	if err := os.WriteFile(path, []byte(sameSecondEvents), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err := readHistory(path)
	if err != nil {
		t.Fatal(err)
	}
	type key struct {
		event, started string
		n              int
	}
	const a, second = "2026-10-07T09:00:00Z", "2026-10-07T09:10:01Z"
	want := []key{ // newest first
		{"box", second, 1},
		{"child_start", second, 1},
		{"child_start", second, 0},
		{"settled", a, 0},
		{"child_start", a, 0},
	}
	if len(entries) != len(want) {
		t.Fatalf("got %d entries, want %d", len(entries), len(want))
	}
	for i, e := range entries {
		if got := (key{e.Event, e.Started, e.StartN}); got != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, got, want[i])
		}
	}
}

func TestFindDispatchSameSecondChildStarts(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, eventsFileName), []byte(sameSecondEvents), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := newServer(dir, filepath.Join(dir, statusFileName))
	const second = "2026-10-07T09:10:01Z"
	for n, wantLog := range []string{"logs/b.log", "logs/c.log"} {
		d, ok := srv.findDispatch(0, second, n)
		if !ok || d.ChildLog != wantLog {
			t.Errorf("n=%d: got %+v, want %s", n, d, wantLog)
		}
	}
	first, ok := srv.findDispatch(0, second, 0)
	if !ok {
		t.Fatal("first same-second Dispatch not found")
	}
	if len(first.Subject) != 0 || first.Exit != nil {
		t.Errorf("first same-second Dispatch absorbed a later one's events: %+v", first)
	}
	next, ok := srv.findDispatch(0, second, 1)
	if !ok {
		t.Fatal("second same-second Dispatch not found")
	}
	if len(next.Subject) != 1 || next.Subject[0] != "#33" {
		t.Errorf("second same-second Dispatch lacks its box event: %+v", next)
	}
}

func TestFindDispatchMatchesExactly(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, eventsFileName), []byte(sameSecondEvents), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := newServer(dir, filepath.Join(dir, statusFileName))
	for _, tc := range []struct {
		at string
		n  int
	}{
		{"2026-10-07T09:10:00Z", 0}, // between two child_starts: no at-or-before fallback
		{"2026-10-07T09:10:01Z", 2}, // n past the same-second starts
		{"2026-10-07T09:00:00Z", 1},
		{"2026-10-07T10:00:00Z", 0}, // after the last child_start
	} {
		if d, ok := srv.findDispatch(0, tc.at, tc.n); ok {
			t.Errorf("at=%s n=%d: found %+v, want none", tc.at, tc.n, d)
		}
	}
	d, ok := srv.findDispatch(0, "", 0)
	if !ok || d.ChildLog != "logs/c.log" {
		t.Errorf("latest = %+v, want logs/c.log", d)
	}
}

func TestRenderedRowsLinkToOwningDispatch(t *testing.T) {
	c := statusJSON(t, "working", "", `[]`)
	body := strings.ToLower(getHistory(t, &c, map[string]string{eventsFileName: sameSecondEvents}))
	for _, w := range []string{
		`<a class="entry ev-settled" href="/dispatch?slot=0&at=2026-10-07t09%3a00%3a00z">`,
		`<a class="entry ev-box" href="/dispatch?slot=0&at=2026-10-07t09%3a10%3a01z&n=1">`,
	} {
		if !strings.Contains(body, w) {
			t.Errorf("missing %q in %s", w, body)
		}
	}
}

func TestDispatchBadN(t *testing.T) {
	for _, q := range []string{"&n=x", "&n=-1"} {
		if code, _ := getDispatch(t, dispatchEvents, "?slot=0&at=2026-10-07T10:00:00Z"+q); code != 404 {
			t.Errorf("%q: status %d, want 404", q, code)
		}
	}
}

func TestDispatchTimeNamingNoChildStartIs404(t *testing.T) {
	// Inside the first Dispatch but not its child_start's own time.
	if code, _ := getDispatch(t, dispatchEvents, "?slot=0&at=2026-10-07T09:10:00Z"); code != 404 {
		t.Errorf("status %d, want 404", code)
	}
}

const passLogEvents = `{"time":"2026-10-07T09:00:00Z","event":"child_start","kind":"work","slot":0,"child_log":"logs/old.log"}
{"time":"2026-10-07T09:00:05Z","event":"box","slot":0,"phase":"initial","issue":"1","pass_log":"logs/old-pass.log"}
{"time":"2026-10-07T10:00:00Z","event":"child_start","kind":"work","slot":0,"child_log":"logs/child.log"}
{"time":"2026-10-07T10:00:05Z","event":"box","slot":0,"phase":"initial","issue":"42","pass_log":".spindrift/logs/issue-42.log"}
{"time":"2026-10-07T10:00:06Z","event":"box","slot":0,"phase":"initial","issue":"42","pass_log":".spindrift/logs/issue-42.log"}
{"time":"2026-10-07T10:00:07Z","event":"box","slot":0,"phase":"recover","issue":"42"}
{"time":"2026-10-07T10:01:00Z","event":"box","slot":0,"phase":"fix-pass-1","issue":"42","pass_log":".spindrift/logs/issue-42-fix-1.log"}
{"time":"2026-10-07T10:02:00Z","event":"box","slot":0,"phase":"conflict-resolve","issue":"42","pass_log":".spindrift/logs/issue-42-conflict-resolve.log"}
`

func tabOrder(body string) []string {
	var out []string
	for _, part := range strings.Split(body, `data-tab="`)[1:] {
		out = append(out, part[:strings.Index(part, `"`)])
	}
	return out
}

func TestDispatchListsChildLogThenPassLogsInEventOrder(t *testing.T) {
	code, body := getDispatch(t, passLogEvents, "?slot=0")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	if got, want := strings.Join(tabOrder(body), ","), "child-log,pass-0,pass-1,pass-2"; got != want {
		t.Errorf("tabs = %s, want %s", got, want)
	}
	low := strings.ToLower(body)
	last := 0
	for _, w := range []string{
		`data-src="/log?path=logs%2fchild.log" data-stream-filter`,
		`id="tab-pass-0" hidden`, `data-src="/log?path=.spindrift%2flogs%2fissue-42.log"`,
		`id="tab-pass-1" hidden`, `data-src="/log?path=.spindrift%2flogs%2fissue-42-fix-1.log"`,
		`id="tab-pass-2" hidden`, `data-src="/log?path=.spindrift%2flogs%2fissue-42-conflict-resolve.log"`,
	} {
		i := strings.Index(low, strings.ToLower(w))
		if i < last {
			t.Fatalf("%q missing or out of order in\n%s", w, body)
		}
		last = i
	}
	for _, w := range []string{">fix-pass-1</button>", ">conflict-resolve</button>"} {
		if !strings.Contains(body, w) {
			t.Errorf("missing tab label %q", w)
		}
	}
	if strings.Contains(body, "old-pass") {
		t.Error("an earlier Dispatch's Pass log leaked in")
	}
	if n := strings.Count(body, "issue-42.log"); n != 1 {
		t.Errorf("repeated phase announced its Pass log %d times, want 1", n)
	}
}

func TestDispatchChoreKeyedRendersPassLogTabs(t *testing.T) {
	ev := `{"time":"2026-10-07T09:00:00Z","event":"child_start","kind":"butler","slot":2,"child_log":"logs/butler.log"}
{"time":"2026-10-07T09:00:05Z","event":"box","slot":2,"kind":"butler","phase":"initial","chore":"docs-drift","pass_log":".spindrift/logs/issue-butler-docs-drift.log"}
`
	code, body := getDispatch(t, ev, "?slot=2")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	if got, want := strings.Join(tabOrder(body), ","), "child-log,pass-0"; got != want {
		t.Errorf("tabs = %s, want %s", got, want)
	}
	for _, w := range []string{"docs-drift", ">initial</button>", `data-src="/log?path=.spindrift%2flogs%2fissue-butler-docs-drift.log"`} {
		if !strings.Contains(body, w) {
			t.Errorf("missing %q in\n%s", w, body)
		}
	}
}

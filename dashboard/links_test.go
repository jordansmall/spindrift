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

const (
	testRepoURL = "https://github.com/acme/widgets"
	issueA      = `<a class="chip extlink" href="https://github.com/acme/widgets/issues/22" target="_blank" rel="noopener noreferrer">#22</a>`
	testPRURL   = "https://github.com/acme/widgets/pull/57"
	prAnchor    = `href="https://github.com/acme/widgets/pull/57" target="_blank" rel="noopener noreferrer"`
)

const linkSlots = `[{"slot":0,"phase":"running","busy":true,"kind":"work","issues":["22"],"since":"2026-10-07T11:00:00Z"},` +
	`{"slot":1,"phase":"running","busy":true,"kind":"butler","chore":"dead-code","since":"2026-10-07T11:00:00Z"}]`

const linkEvents = `{"time":"2026-10-07T10:00:00Z","event":"child_start","kind":"work","slot":0,"child_log":"logs/a.log"}
{"time":"2026-10-07T10:00:05Z","event":"box","slot":0,"phase":"initial","issue":"22"}
{"time":"2026-10-07T10:05:00Z","event":"settled","slot":0,"issue":"22","state":"complete","pr_url":"` + testPRURL + `"}
{"time":"2026-10-07T10:30:00Z","event":"child_start","kind":"work","slot":0,"child_log":"logs/b.log"}
{"time":"2026-10-07T10:30:05Z","event":"box","slot":0,"phase":"initial","issue":"23"}
{"time":"2026-10-07T10:35:00Z","event":"settled","slot":0,"issue":"23","state":"failed"}
{"time":"2026-10-07T10:40:00Z","event":"settled","slot":1,"chore":"dead-code","state":"complete"}
`

func TestSlotCardLinksIssue(t *testing.T) {
	c := statusJSONRepo(t, "working", "", linkSlots, testRepoURL)
	_, body := get(t, &c, "/")
	if !strings.Contains(body, issueA) {
		t.Errorf("card lacks the issue link %s in %s", issueA, body)
	}
	if strings.Contains(body, "/pull/") || strings.Contains(body, `prlink`) {
		t.Error("a busy card must not link a PR")
	}
	if !strings.Contains(body, `<span class="chip">dead-code</span>`) {
		t.Errorf("Chore subject must stay an unlinked chip: %s", body)
	}
}

func TestSlotCardNoLinkWithoutRepoURL(t *testing.T) {
	c := statusJSON(t, "working", "", linkSlots)
	_, body := get(t, &c, "/")
	if !strings.Contains(body, `<span class="chip">#22</span>`) || strings.Contains(body, "/issues/") {
		t.Errorf("without repo_url the issue must stay a plain chip: %s", body)
	}
}

func TestSlotCardNeverLinksNonNumericKey(t *testing.T) {
	slots := `[{"slot":0,"phase":"running","busy":true,"kind":"work","issues":["PROJ-7"],"since":"2026-10-07T11:00:00Z"}]`
	c := statusJSONRepo(t, "working", "", slots, testRepoURL)
	_, body := get(t, &c, "/")
	if strings.Contains(body, "/issues/") {
		t.Errorf("a non-numeric key must not be linked: %s", body)
	}
}

func TestTimelineLinksIssueAndPR(t *testing.T) {
	c := statusJSONRepo(t, "working", "", `[]`, testRepoURL)
	body := getHistory(t, &c, map[string]string{eventsFileName: linkEvents})
	rows := strings.Split(body, `<div class="entry `)
	var settled22, settled23, chore string
	for _, r := range rows {
		switch {
		case strings.HasPrefix(r, "ev-settled") && strings.Contains(r, "#22"):
			settled22 = r
		case strings.HasPrefix(r, "ev-settled") && strings.Contains(r, "#23"):
			settled23 = r
		case strings.HasPrefix(r, "ev-settled") && strings.Contains(r, "dead-code"):
			chore = r
		}
	}
	if !strings.Contains(settled22, issueA) || !strings.Contains(settled22, prAnchor) {
		t.Errorf("settled row with a PR lacks issue or PR link: %s", settled22)
	}
	if !strings.Contains(settled23, "/issues/23") || strings.Contains(settled23, "prlink") {
		t.Errorf("settled row without a PR must link the issue only: %s", settled23)
	}
	if chore == "" || strings.Contains(chore, "extlink") || strings.Contains(chore, "prlink") {
		t.Errorf("Chore row must have an unlinked chip: %s", chore)
	}
}

func TestTimelineNoLinksWithoutRepoURLButPRStays(t *testing.T) {
	c := statusJSON(t, "working", "", `[]`)
	body := getHistory(t, &c, map[string]string{eventsFileName: linkEvents})
	if strings.Contains(body, "/issues/") {
		t.Error("issue linked without repo_url")
	}
	if !strings.Contains(body, prAnchor) {
		t.Error("the PR link does not depend on repo_url")
	}
}

func getDispatchWithStatus(t *testing.T, status, events, query string) (int, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, eventsFileName), []byte(events), 0o644); err != nil {
		t.Fatal(err)
	}
	if status != "" {
		if err := os.WriteFile(filepath.Join(dir, statusFileName), []byte(status), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	srv := newServer(dir, filepath.Join(dir, statusFileName))
	srv.now = func() time.Time { return testNow }
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dispatch"+query, nil))
	return rec.Code, rec.Body.String()
}

func TestDrillInLinksIssueAndPR(t *testing.T) {
	c := statusJSONRepo(t, "working", "", `[]`, testRepoURL)
	code, body := getDispatchWithStatus(t, c, linkEvents, "?slot=0&at=2026-10-07T10:00:00Z")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	if !strings.Contains(body, issueA) || !strings.Contains(body, prAnchor) {
		t.Errorf("drill-in lacks issue or PR link: %s", body)
	}
}

func TestDrillInWithoutPRShowsNoPRLink(t *testing.T) {
	c := statusJSONRepo(t, "working", "", `[]`, testRepoURL)
	_, body := getDispatchWithStatus(t, c, linkEvents, "?slot=0&at=2026-10-07T10:30:00Z")
	if !strings.Contains(body, "/issues/23") || strings.Contains(body, "prlink") || strings.Contains(body, "/pull/") {
		t.Errorf("drill-in without a PR must link the issue only: %s", body)
	}
}

func TestDrillInWithoutStatusStillRendersUnlinked(t *testing.T) {
	for name, status := range map[string]string{
		"absent":  "",
		"skewed":  `{"schema":99}`,
		"no repo": statusJSON(t, "working", "", `[]`),
	} {
		t.Run(name, func(t *testing.T) {
			code, body := getDispatchWithStatus(t, status, linkEvents, "?slot=0&at=2026-10-07T10:00:00Z")
			if code != 200 || strings.Contains(body, "/issues/") || !strings.Contains(body, `<span class="chip">#22</span>`) {
				t.Errorf("status %d, body %s", code, body)
			}
		})
	}
}

func TestLiveEntryFrameLinks(t *testing.T) {
	st := openStream(t, statusJSONRepo(t, "working", "", noSlots, testRepoURL), "")
	st.expect(t, "status")
	st.expect(t, "history")
	appendTo(t, st.events(), `{"time":"2026-10-07T11:02:00Z","event":"settled","slot":0,"issue":"22","state":"complete","pr_url":"`+testPRURL+`"}`+"\n")
	st.expect(t, "entry", issueA, prAnchor)
}

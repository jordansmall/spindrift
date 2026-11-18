package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"spindrift.dev/sdk/connector"
	"spindrift.dev/sdk/tracker"
)

func TestTrackerProbeReturnsStatusWithoutFailingOnNon2xx(t *testing.T) {
	for _, status := range []int{200, 401, 404} {
		got := stub(t, map[string]connector.Response{
			"https://api.github.com/repos/o/r/issues?state=open&per_page=1": {Status: status, Body: []byte("{}")},
		})
		s, err := trackerProbe(tgt)
		if err != nil || s != status {
			t.Fatalf("probe = %d, %v; want %d, nil", s, err, status)
		}
		if len(*got) != 1 || (*got)[0].Method != "GET" {
			t.Fatalf("requests = %#v, want one GET", *got)
		}
	}
}

func TestTrackerProbeErrorsOnTransportFailure(t *testing.T) {
	old := send
	send = func(connector.Request) (connector.Response, error) { return connector.Response{}, errors.New("dial") }
	t.Cleanup(func() { send = old })
	if _, err := trackerProbe(tgt); err == nil {
		t.Fatal("want transport error")
	}
}

func TestCountSizesTheListIssuesSet(t *testing.T) {
	// Page 1 is 40 PRs then 60 issues, page 2 a full page of issues, page 3 a
	// short one: only issues count, and the walk goes on while a page is full.
	p1 := items(1, 40, true)[:len(items(1, 40, true))-1] + "," + items(41, 60, false)[1:]
	resetCountCache(t)
	stub(t, map[string]connector.Response{
		countBase + "&labels=ready&page=1": ok(p1),
		countBase + "&labels=ready&page=2": ok(items(101, 100, false)),
		countBase + "&labels=ready&page=3": ok(items(201, 5, false)),
		listBase + "&labels=ready&page=1":  ok(p1),
		listBase + "&labels=ready&page=2":  ok(items(101, 100, false)),
		listBase + "&labels=ready&page=3":  ok(items(201, 5, false)),
	})
	q := tracker.Query{Label: "ready"}
	n, err := count(tgt, q, 0, false)
	if err != nil || n != 165 {
		t.Fatalf("count = %d, %v; want 165, nil", n, err)
	}
	list, err := listIssues(tgt, q, 1000)
	if err != nil || len(list) != n {
		t.Fatalf("listIssues returned %d, %v; count must size exactly that set (%d)", len(list), err, n)
	}
	// Core asks both with limit 100; the capped walks must still agree.
	resetCountCache(t)
	n, err = count(tgt, q, 100, false)
	if err != nil || n != 100 {
		t.Fatalf("count(limit 100) = %d, %v; want 100, nil", n, err)
	}
	if list, err = listIssues(tgt, q, 100); err != nil || len(list) != n {
		t.Fatalf("listIssues(100) returned %d, %v; want %d", len(list), err, n)
	}
}

func TestCountWithoutLabelCountsEveryOpenIssue(t *testing.T) {
	resetCountCache(t)
	stub(t, map[string]connector.Response{countBase + "&page=1": ok(items(1, 3, false))})
	if n, err := count(tgt, tracker.Query{}, 0, false); err != nil || n != 3 {
		t.Fatalf("count = %d, %v; want 3, nil", n, err)
	}
}

func TestCountFailsOnNon2xx(t *testing.T) {
	resetCountCache(t)
	stub(t, map[string]connector.Response{countBase + "&page=1": {Status: 500}})
	if _, err := count(tgt, tracker.Query{}, 0, false); err == nil {
		t.Fatal("want error")
	}
}

func TestCountIntervalIsInfrequent(t *testing.T) {
	if countInterval < time.Second {
		t.Fatalf("countInterval = %v, want at least a second", countInterval)
	}
}

const countBase = "https://api.github.com/repos/o/r/issues?state=open&sort=updated&direction=desc&per_page=100"

// resetCountCache empties the guest-global count cache around a test.
func resetCountCache(t *testing.T) {
	t.Helper()
	countCache = map[string]countEntry{}
	t.Cleanup(func() { countCache = map[string]countEntry{} })
}

// stubETag replaces responseHeader with a host whose latest response carried etag.
func stubETag(t *testing.T, etag string) {
	t.Helper()
	old := responseHeader
	responseHeader = func(name string) (string, bool) { return etag, etag != "" && name == "ETag" }
	t.Cleanup(func() { responseHeader = old })
}

func TestCountUsesUpdatedDescendingSort(t *testing.T) {
	resetCountCache(t)
	got := stub(t, map[string]connector.Response{countBase + "&labels=ready&page=1": ok(items(1, 2, false))})
	if _, err := count(tgt, tracker.Query{Label: "ready"}, 0, false); err != nil {
		t.Fatal(err)
	}
	u := (*got)[0].URL
	if !strings.Contains(u, "sort=updated&direction=desc") || strings.Contains(u, "sort=created") {
		t.Fatalf("count requested %s, want the update-ordered query", u)
	}
}

func TestCountSendsCachedETagAndAnswers304FromCache(t *testing.T) {
	resetCountCache(t)
	stubETag(t, `"abc"`)
	url1 := countBase + "&labels=ready&page=1"
	got := stub(t, map[string]connector.Response{url1: ok(items(1, 3, false))})
	q := tracker.Query{Label: "ready"}
	if n, err := count(tgt, q, 100, false); err != nil || n != 3 {
		t.Fatalf("first count = %d, %v; want 3, nil", n, err)
	}
	if h := (*got)[0].Headers["If-None-Match"]; h != "" {
		t.Fatalf("first request sent If-None-Match %q with nothing cached", h)
	}
	got = stub(t, map[string]connector.Response{url1: {Status: 304}})
	if n, err := count(tgt, q, 100, false); err != nil || n != 3 {
		t.Fatalf("second count = %d, %v; want cached 3, nil", n, err)
	}
	if len(*got) != 1 || (*got)[0].Headers["If-None-Match"] != `"abc"` {
		t.Fatalf("second poll requests = %#v, want one page-1 request carrying the cached ETag", *got)
	}
}

func TestCountFreshOmitsIfNoneMatch(t *testing.T) {
	resetCountCache(t)
	stubETag(t, `"abc"`)
	url1 := countBase + "&page=1"
	stub(t, map[string]connector.Response{url1: ok(items(1, 3, false))})
	if _, err := count(tgt, tracker.Query{}, 100, false); err != nil {
		t.Fatal(err)
	}
	got := stub(t, map[string]connector.Response{url1: ok(items(1, 4, false))})
	n, err := count(tgt, tracker.Query{}, 100, true)
	if err != nil || n != 4 {
		t.Fatalf("fresh count = %d, %v; want 4, nil", n, err)
	}
	if h, sent := (*got)[0].Headers["If-None-Match"]; sent {
		t.Fatalf("fresh request sent If-None-Match %q", h)
	}
}

func TestCountWithoutETagCachesNothing(t *testing.T) {
	resetCountCache(t)
	stubETag(t, "")
	url1 := countBase + "&page=1"
	got := stub(t, map[string]connector.Response{url1: ok(items(1, 3, false))})
	for range 2 {
		if _, err := count(tgt, tracker.Query{}, 100, false); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range *got {
		if _, sent := r.Headers["If-None-Match"]; sent {
			t.Fatalf("sent If-None-Match with no ETag to replay: %#v", r.Headers)
		}
	}
}

func TestCountDropsCachedETagWhenA200CarriesNone(t *testing.T) {
	// Native overwrites its cache on every 200; a stale ETag kept past an
	// ETag-less 200 would answer a later 304 with the older count.
	resetCountCache(t)
	stubETag(t, `"abc"`)
	url1 := countBase + "&page=1"
	stub(t, map[string]connector.Response{url1: ok(items(1, 3, false))})
	if _, err := count(tgt, tracker.Query{}, 100, false); err != nil {
		t.Fatal(err)
	}
	stubETag(t, "")
	stub(t, map[string]connector.Response{url1: ok(items(1, 4, false))})
	if _, err := count(tgt, tracker.Query{}, 100, true); err != nil {
		t.Fatal(err)
	}
	got := stub(t, map[string]connector.Response{url1: ok(items(1, 4, false))})
	if _, err := count(tgt, tracker.Query{}, 100, false); err != nil {
		t.Fatal(err)
	}
	if h, sent := (*got)[0].Headers["If-None-Match"]; sent {
		t.Fatalf("replayed dropped ETag %q", h)
	}
}

func TestCountDeeperPage304IsAnError(t *testing.T) {
	resetCountCache(t)
	stubETag(t, `"abc"`)
	stub(t, map[string]connector.Response{
		countBase + "&page=1": ok(items(1, 100, false)),
		countBase + "&page=2": {Status: 304},
	})
	if _, err := count(tgt, tracker.Query{}, 0, false); err == nil {
		t.Fatal("want error on a 304 to an unconditional page")
	}
}

func TestCountStopsAtLimit(t *testing.T) {
	resetCountCache(t)
	resp := map[string]connector.Response{}
	for p := 1; p <= 7; p++ {
		resp[fmt.Sprintf("%s&page=%d", countBase, p)] = ok(items(p*100, 100, false))
	}
	resp[countBase+"&page=8"] = ok(items(800, 3, false))
	got := stub(t, resp)
	n, err := count(tgt, tracker.Query{}, 100, false)
	if err != nil || n != 100 || len(*got) != 1 {
		t.Fatalf("count = %d, %v after %d requests; want 100, nil after 1", n, err, len(*got))
	}
}

func TestCountStopsAtFivePagesWhenPRsCrowdThePages(t *testing.T) {
	resetCountCache(t)
	resp := map[string]connector.Response{}
	for p := 1; p <= 60; p++ {
		resp[fmt.Sprintf("%s&page=%d", countBase, p)] = ok(items(p*100, 100, true))
	}
	got := stub(t, resp)
	n, err := count(tgt, tracker.Query{}, 100, false)
	if err != nil || n != 0 || len(*got) != countMaxPages {
		t.Fatalf("count = %d, %v after %d requests; want 0, nil after %d", n, err, len(*got), countMaxPages)
	}
}

func TestCountWithoutLimitStopsAtFivePagesPastFifty(t *testing.T) {
	resetCountCache(t)
	resp := map[string]connector.Response{}
	for p := 1; p <= 60; p++ {
		resp[fmt.Sprintf("%s&page=%d", countBase, p)] = ok(items(p*100, 100, false))
	}
	got := stub(t, resp)
	n, err := count(tgt, tracker.Query{}, 0, false)
	if err != nil || n != countMaxPages*pageSize || len(*got) != countMaxPages {
		t.Fatalf("count = %d, %v after %d requests; want %d, nil after %d", n, err, len(*got), countMaxPages*pageSize, countMaxPages)
	}
}

package main

import (
	"errors"
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
	stub(t, map[string]connector.Response{
		listBase + "&labels=ready&page=1": ok(p1),
		listBase + "&labels=ready&page=2": ok(items(101, 100, false)),
		listBase + "&labels=ready&page=3": ok(items(201, 5, false)),
	})
	q := tracker.Query{Label: "ready"}
	n, err := count(tgt, q)
	if err != nil || n != 165 {
		t.Fatalf("count = %d, %v; want 165, nil", n, err)
	}
	list, err := listIssues(tgt, q, 1000)
	if err != nil || len(list) != n {
		t.Fatalf("listIssues returned %d, %v; count must size exactly that set (%d)", len(list), err, n)
	}
}

func TestCountWithoutLabelCountsEveryOpenIssue(t *testing.T) {
	stub(t, map[string]connector.Response{listBase + "&page=1": ok(items(1, 3, false))})
	if n, err := count(tgt, tracker.Query{}); err != nil || n != 3 {
		t.Fatalf("count = %d, %v; want 3, nil", n, err)
	}
}

func TestCountFailsOnNon2xx(t *testing.T) {
	stub(t, map[string]connector.Response{listBase + "&page=1": {Status: 500}})
	if _, err := count(tgt, tracker.Query{}); err == nil {
		t.Fatal("want error")
	}
}

func TestCountIntervalIsInfrequent(t *testing.T) {
	if countInterval < time.Second {
		t.Fatalf("countInterval = %v, want at least a second", countInterval)
	}
}

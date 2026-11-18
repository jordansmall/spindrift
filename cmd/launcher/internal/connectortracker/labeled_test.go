package connectortracker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/forge"
)

func TestLabeledLayerScansEachLabelAndMapsClosedFields(t *testing.T) {
	call, calls := stubGuest(t, map[string]string{CapLabeled: `{"issues":[
		{"number":"9","title":"newer","body":"b","state":"closed","labels":["agent-review-finding"],"closedAt":"2026-01-02T03:04:05Z","closedNotPlanned":true},
		{"number":"4","title":"older","state":"closed","labels":["agent-review-finding"]}]}`})
	tr := mustTracker(t, call, newNative(), testLifecycle, testTarget, []string{CapLabeled}, true)
	l, ok := forge.As[forge.LabeledBacklogLister](tr)
	if !ok {
		t.Fatal("a Connector advertising tracker.labeled resolved no LabeledBacklogLister")
	}
	got, err := l.ListIssuesWithLabels(forge.IssueClosed, []string{"agent-review-finding", "other"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Number != "9" || got[1].Number != "4" {
		t.Fatalf("issues = %+v; want 9 then 4, the duplicate of the second label merged away", got)
	}
	if !got[0].ClosedNotPlanned || !got[0].ClosedAt.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) || got[0].Body != "b" {
		t.Errorf("issue 9 = %+v; want ClosedAt, ClosedNotPlanned and Body carried", got[0])
	}
	if len(*calls) != 2 || (*calls)[0].method != CapLabeled || (*calls)[0].params["label"] != "agent-review-finding" ||
		(*calls)[0].params["state"] != "closed" || (*calls)[0].params["limit"] != float64(forge.DedupScanLimit) || (*calls)[1].params["label"] != "other" {
		t.Errorf("calls = %v; want one tracker.labeled read per label at the dedup scan limit", *calls)
	}
}

func TestLabeledLayerRefusesAStateTheScanDoesNotCover(t *testing.T) {
	call, calls := stubGuest(t, nil)
	tr := mustTracker(t, call, newNative(), testLifecycle, testTarget, []string{CapLabeled}, true)
	l, _ := forge.As[forge.LabeledBacklogLister](tr)
	if _, err := l.ListIssuesWithLabels(forge.IssueMerged, []string{"x"}); err == nil || len(*calls) != 0 {
		t.Errorf("err = %v with %d calls; want a refusal before any call", err, len(*calls))
	}
}

func TestLabeledLayerFailsClosedOnABadIssueNumber(t *testing.T) {
	call, _ := stubGuest(t, map[string]string{CapLabeled: `{"issues":[{"number":"../x","state":"open"}]}`})
	tr := mustTracker(t, call, newNative(), testLifecycle, testTarget, []string{CapLabeled}, true)
	l, _ := forge.As[forge.LabeledBacklogLister](tr)
	if _, err := l.ListIssuesWithLabels(forge.IssueOpen, []string{"x"}); !errors.Is(err, ErrAnswer) {
		t.Errorf("err = %v; want ErrAnswer", err)
	}
}

// pagedGuest answers tracker.labeled from the request's cursor: page returns
// the reply JSON for it.
func pagedGuest(t *testing.T, page func(cursor string) string) (func(context.Context, string, any, any) error, *[]recordedCall) {
	t.Helper()
	var calls []recordedCall
	return func(_ context.Context, method string, params, result any) error {
		raw, err := json.Marshal(params)
		if err != nil {
			t.Fatal(err)
		}
		var p map[string]any
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, recordedCall{method, p})
		cursor, _ := p["cursor"].(string)
		return json.Unmarshal([]byte(page(cursor)), result)
	}, &calls
}

// labeledReply renders open issues first..last (descending, newest first) as a
// tracker.labeled reply; an empty next omits the field.
func labeledReply(first, last int, next string) string {
	var b strings.Builder
	b.WriteString(`{"issues":[`)
	for i := first; i >= last; i-- {
		if i != first {
			b.WriteString(",")
		}
		b.WriteString(`{"number":"` + strconv.Itoa(i) + `","state":"open"}`)
	}
	b.WriteString(`]`)
	if next != "" {
		b.WriteString(`,"next":"` + next + `"`)
	}
	b.WriteString(`}`)
	return b.String()
}

// captureStderr runs f and returns what it wrote to os.Stderr.
func captureStderr(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = orig }()
	// Read while f runs: output past the pipe buffer would block it otherwise.
	done := make(chan string)
	go func() {
		out, _ := io.ReadAll(r)
		done <- string(out)
	}()
	f()
	_ = w.Close()
	return <-done
}

func labeledLister(t *testing.T, call func(context.Context, string, any, any) error) forge.LabeledBacklogLister {
	t.Helper()
	tr := mustTracker(t, call, newNative(), testLifecycle, testTarget, []string{CapLabeled}, true)
	l, ok := forge.As[forge.LabeledBacklogLister](tr)
	if !ok {
		t.Fatal("no LabeledBacklogLister")
	}
	return l
}

func cursorsSent(calls []recordedCall) []string {
	var out []string
	for _, c := range calls {
		s, _ := c.params["cursor"].(string)
		out = append(out, s)
	}
	return out
}

func TestLabeledLayerWalksEveryCursorPage(t *testing.T) {
	// 3 x 200 plus a final page of 5, the oldest (issue 1) closed not planned.
	pages := map[string]string{
		"":   labeledReply(605, 406, "c1"),
		"c1": labeledReply(405, 206, "c2"),
		"c2": labeledReply(205, 6, "c3"),
		"c3": `{"issues":[` +
			`{"number":"5","state":"open"},{"number":"4","state":"open"},{"number":"3","state":"open"},{"number":"2","state":"open"},` +
			`{"number":"1","state":"closed","closedNotPlanned":true}]}`,
	}
	call, calls := pagedGuest(t, func(c string) string { return pages[c] })
	got, err := labeledLister(t, call).ListIssuesWithLabels(forge.IssueOpen, []string{"x"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 605 {
		t.Fatalf("got %d issues; want all 605", len(got))
	}
	if first, last := got[0], got[len(got)-1]; first.Number != "605" || last.Number != "1" || !last.ClosedNotPlanned {
		t.Errorf("first = %q, last = %+v; want 605 newest and issue 1 closed not planned oldest", first.Number, last)
	}
	if want := []string{"", "c1", "c2", "c3"}; !slices.Equal(cursorsSent(*calls), want) {
		t.Errorf("cursors sent = %q; want %q", cursorsSent(*calls), want)
	}
	if (*calls)[0].params["limit"] != float64(forge.DedupScanLimit) {
		t.Errorf("limit = %v; want every page asked at the dedup scan limit", (*calls)[0].params["limit"])
	}
}

func TestLabeledLayerOneCallForAGuestThatNeverPages(t *testing.T) {
	// A pre-paging Connector ignores cursor and sets no next. A full page is
	// the one case where the scan may have lost older findings, so it warns.
	for _, tc := range []struct {
		n    int
		warn bool
	}{{forge.DedupScanLimit - 1, false}, {forge.DedupScanLimit, true}, {forge.DedupScanLimit + 3, true}} {
		call, calls := pagedGuest(t, func(string) string { return labeledReply(tc.n, 1, "") })
		var got []forge.Issue
		var err error
		out := captureStderr(t, func() { got, err = labeledLister(t, call).ListIssuesWithLabels(forge.IssueOpen, []string{"x"}) })
		if err != nil || len(got) != tc.n || len(*calls) != 1 {
			t.Errorf("n=%d: got %d issues, %d calls, %v; want every row kept from one call", tc.n, len(got), len(*calls), err)
		}
		if warned := strings.Contains(out, "dedup scan"); warned != tc.warn {
			t.Errorf("n=%d: stderr = %q; warned = %v, want %v", tc.n, out, warned, tc.warn)
		}
	}
}

func TestLabeledLayerFailsClosedOnARepeatedCursor(t *testing.T) {
	for name, next := range map[string]func(cursor string) string{
		"echoing the cursor it was given": func(string) string { return "a" },
		"returning to an earlier cursor": func(c string) string {
			if c == "b" {
				return "a"
			}
			return map[string]string{"": "a", "a": "b"}[c]
		},
	} {
		// Fresh numbers on every page, so only the cursor guard can refuse.
		n := 0
		call, calls := pagedGuest(t, func(c string) string { n++; return labeledReply(n, n, next(c)) })
		_, err := labeledLister(t, call).ListIssuesWithLabels(forge.IssueOpen, []string{"x"})
		if !errors.Is(err, ErrAnswer) || !strings.Contains(err.Error(), "a cursor it had returned already") {
			t.Errorf("%s: err = %v; want the repeated-cursor refusal wrapping ErrAnswer", name, err)
		}
		if len(*calls) > 3 {
			t.Errorf("%s: %d calls; want the loop stopped at the repeat", name, len(*calls))
		}
	}
}

func TestLabeledLayerStopsAtThePageBoundWithoutError(t *testing.T) {
	old := labeledMaxPages
	labeledMaxPages = 5
	t.Cleanup(func() { labeledMaxPages = old })
	call, calls := pagedGuest(t, func(c string) string {
		n := len(c)
		return labeledReply(n+1, n+1, c+"x")
	})
	var got []forge.Issue
	var err error
	out := captureStderr(t, func() { got, err = labeledLister(t, call).ListIssuesWithLabels(forge.IssueOpen, []string{"x"}) })
	if err != nil || len(*calls) != labeledMaxPages || len(got) != labeledMaxPages {
		t.Errorf("%d issues, %d calls, %v; want the %d pages read kept and no error", len(got), len(*calls), err, labeledMaxPages)
	}
	if !strings.Contains(out, "dedup scan") {
		t.Errorf("stderr = %q; want a truncation warning", out)
	}
}

func TestLabeledLayerFailsClosedOnARepeatedPage(t *testing.T) {
	// The same page under a fresh cursor each call would otherwise run to the
	// page bound and return it over and over.
	call, calls := pagedGuest(t, func(c string) string { return labeledReply(3, 1, c+"x") })
	got, err := labeledLister(t, call).ListIssuesWithLabels(forge.IssueOpen, []string{"x"})
	if !errors.Is(err, ErrAnswer) || !strings.Contains(err.Error(), "refusing to loop") {
		t.Errorf("err = %v with %d issues; want a refusal to loop wrapping ErrAnswer", err, len(got))
	}
	if len(*calls) != 2 {
		t.Errorf("%d calls; want the walk stopped at the repeated page", len(*calls))
	}
}

func TestLabeledLayerKeepsAPageThatOnlyPartlyOverlaps(t *testing.T) {
	// New issues made mid-walk shift an offset-paged tracker by a row.
	pages := map[string]string{"": labeledReply(5, 3, "a"), "a": labeledReply(3, 1, "")}
	call, _ := pagedGuest(t, func(c string) string { return pages[c] })
	if _, err := labeledLister(t, call).ListIssuesWithLabels(forge.IssueOpen, []string{"x"}); err != nil {
		t.Errorf("err = %v; want a partly overlapping page kept", err)
	}
}

func TestLabeledLayerStopsAtTheByteBudgetWithoutError(t *testing.T) {
	old := labeledMaxBytes
	labeledMaxBytes = 1000
	t.Cleanup(func() { labeledMaxBytes = old })
	call, calls := pagedGuest(t, func(c string) string {
		n := len(c) * 5
		return labeledReply(n+5, n+1, c+"x")
	})
	var got []forge.Issue
	var err error
	out := captureStderr(t, func() { got, err = labeledLister(t, call).ListIssuesWithLabels(forge.IssueOpen, []string{"x"}) })
	if err != nil || len(*calls) < 2 || len(*calls) >= labeledMaxPages || len(got) != 5*len(*calls) {
		t.Errorf("%d issues, %d calls, %v; want the pages read before the budget kept and no error", len(got), len(*calls), err)
	}
	if !strings.Contains(out, "dedup scan") || !strings.Contains(out, "byte") {
		t.Errorf("stderr = %q; want a truncation warning naming the byte bound", out)
	}
}

func TestLabeledLayerGuestErrorFailsWhenEveryLabelFails(t *testing.T) {
	boom := errors.New("boom")
	call := func(_ context.Context, _ string, _, _ any) error { return boom }
	tr := mustTracker(t, call, newNative(), testLifecycle, testTarget, []string{CapLabeled}, true)
	l, _ := forge.As[forge.LabeledBacklogLister](tr)
	if _, err := l.ListIssuesWithLabels(forge.IssueOpen, []string{"x"}); !errors.Is(err, boom) {
		t.Errorf("err = %v; want boom", err)
	}
}

func TestAbsentLabeledStaysAbsent(t *testing.T) {
	call, _ := stubGuest(t, nil)
	tr := mustTracker(t, call, newNative(), testLifecycle, testTarget, nil, true)
	if _, ok := forge.As[forge.LabeledBacklogLister](tr); ok {
		t.Error("LabeledBacklogLister resolved without tracker.labeled")
	}
}

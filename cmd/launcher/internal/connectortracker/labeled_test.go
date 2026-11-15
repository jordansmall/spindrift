package connectortracker

import (
	"context"
	"errors"
	"strconv"
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

func TestLabeledLayerCapsAGuestThatIgnoresTheLimit(t *testing.T) {
	issues := ``
	for i := 1; i <= forge.DedupScanLimit+3; i++ {
		if i > 1 {
			issues += ","
		}
		issues += `{"number":"` + strconv.Itoa(i) + `","state":"open"}`
	}
	call, _ := stubGuest(t, map[string]string{CapLabeled: `{"issues":[` + issues + `]}`})
	tr := mustTracker(t, call, newNative(), testLifecycle, testTarget, []string{CapLabeled}, true)
	l, _ := forge.As[forge.LabeledBacklogLister](tr)
	got, err := l.ListIssuesWithLabels(forge.IssueOpen, []string{"x"})
	if err != nil || len(got) != forge.DedupScanLimit {
		t.Errorf("got %d issues, %v; want %d", len(got), err, forge.DedupScanLimit)
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

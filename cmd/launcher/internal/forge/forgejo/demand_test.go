package forgejo_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgejo"
)

// demandFake serves a labels registry and a one-item issue page, counting each
// request kind.
type demandFake struct {
	labelsBody  string
	issuesBody  string
	totalHeader string      // "" omits X-Total-Count
	labelsFail  atomic.Bool // the labels registry answers 403
	labelReqs   atomic.Int32
	issueReqs   atomic.Int32
	lastQuery   atomic.Value // url.Values of the last issue-list request
}

func (f *demandFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if serveNoOrgLabels(w, r) {
		return
	}
	switch r.URL.Path {
	case "/api/v1/repos/owner/repo/labels":
		if page := r.URL.Query().Get("page"); page == "1" || page == "" {
			f.labelReqs.Add(1)
		}
		if f.labelsFail.Load() {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		serveLabels(w, r, f.labelsBody)
	case "/api/v1/repos/owner/repo/issues":
		f.issueReqs.Add(1)
		f.lastQuery.Store(r.URL.Query())
		if f.totalHeader != "" {
			w.Header().Set("X-Total-Count", f.totalHeader)
		}
		w.Write([]byte(f.issuesBody))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func newDemandCounter(t *testing.T, f *demandFake) forge.DemandCounter {
	t.Helper()
	return newDemandCounterAt(t, f, nil)
}

// newDemandCounterAt is newDemandCounter with an injected clock; nil means the
// real one.
func newDemandCounterAt(t *testing.T, f *demandFake, now func() time.Time) forge.DemandCounter {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	tr := forgejo.NewForgejoClient(forgejo.ForgejoConfig{
		BaseURL: srv.URL,
		Repo:    "owner/repo",
		Token:   "tok",
		Labels:  forge.DispatchLabels{Dispatchable: "ready"},
	})
	if now != nil {
		forgejo.SetNow(tr, now)
	}
	dc, ok := tr.(forge.DemandCounter)
	if !ok {
		t.Fatal("forgejo client does not implement forge.DemandCounter")
	}
	return dc
}

const readyItem = `[{"number":3,"title":"t","state":"open","labels":[{"name":"ready"}]}]`

func TestForgejoDemand_ReadsTotalCountFromOneItemPage(t *testing.T) {
	f := &demandFake{labelsBody: `[{"id":1,"name":"ready"}]`, issuesBody: readyItem, totalHeader: "7"}
	dc := newDemandCounter(t, f)

	n, err := dc.CountReady(false)
	if err != nil {
		t.Fatalf("CountReady: %v", err)
	}
	if n != 7 {
		t.Fatalf("CountReady = %d, want 7 (the X-Total-Count, not the page length)", n)
	}
	q := f.lastQuery.Load().(url.Values)
	if q["limit"][0] != "1" || q["labels"][0] != "ready" || q["state"][0] != "open" || q["type"][0] != "issues" {
		t.Fatalf("issue query = %v, want limit=1 labels=ready state=open type=issues", q)
	}
	if dc.ProbeInterval() != 3*time.Minute {
		t.Fatalf("ProbeInterval = %v, want 3m", dc.ProbeInterval())
	}
}

func TestForgejoDemand_UndefinedLabelCountsZeroWithoutIssueQuery(t *testing.T) {
	f := &demandFake{labelsBody: `[{"id":1,"name":"other"}]`, issuesBody: readyItem, totalHeader: "7"}
	dc := newDemandCounter(t, f)

	n, err := dc.CountReady(false)
	if err != nil {
		t.Fatalf("CountReady: %v", err)
	}
	if n != 0 {
		t.Fatalf("CountReady = %d, want 0 for an undefined label", n)
	}
	if got := f.issueReqs.Load(); got != 0 {
		t.Fatalf("issue list requests = %d, want 0", got)
	}
}

func TestForgejoDemand_UndefinedLabelIsRecheckedAfterTTL(t *testing.T) {
	f := &demandFake{labelsBody: `[]`, issuesBody: readyItem, totalHeader: "2"}
	clock := time.Unix(1_000_000, 0)
	dc := newDemandCounterAt(t, f, func() time.Time { return clock })
	if n, _ := dc.CountReady(false); n != 0 {
		t.Fatalf("first CountReady = %d, want 0", n)
	}
	if got := f.labelReqs.Load(); got != 1 {
		t.Fatalf("label walks after first probe = %d, want 1", got)
	}

	f.labelsBody = `[{"id":1,"name":"ready"}]`
	clock = clock.Add(9 * time.Minute)
	if n, err := dc.CountReady(false); err != nil || n != 0 {
		t.Fatalf("CountReady inside the window = %d, %v; want 0, nil", n, err)
	}
	if got := f.labelReqs.Load(); got != 1 {
		t.Fatalf("label walks inside the window = %d, want 1 (negative verdict cached)", got)
	}

	// Exactly at expiry the verdict is stale: the window is half-open.
	clock = clock.Add(1 * time.Minute)
	n, err := dc.CountReady(false)
	if err != nil || n != 2 {
		t.Fatalf("CountReady at the window's end = %d, %v; want 2, nil", n, err)
	}
	if got := f.labelReqs.Load(); got != 2 {
		t.Fatalf("label walks at the window's end = %d, want 2", got)
	}
}

func TestForgejoDemand_LabelCheckFailureIsNotCachedAsUndefined(t *testing.T) {
	f := &demandFake{issuesBody: readyItem, totalHeader: "2"}
	f.labelsFail.Store(true)
	clock := time.Unix(1_000_000, 0)
	dc := newDemandCounterAt(t, f, func() time.Time { return clock })
	for i := 0; i < 2; i++ {
		if n, err := dc.CountReady(false); err != nil || n != 2 {
			t.Fatalf("CountReady #%d on a failing registry = %d, %v; want 2, nil", i, n, err)
		}
	}
	if got := f.labelReqs.Load(); got != 2 {
		t.Fatalf("label walks = %d, want 2 (an error is never cached)", got)
	}
}

func TestForgejoDemand_LabelPreCheckCachedAcrossProbes(t *testing.T) {
	f := &demandFake{labelsBody: `[{"id":1,"name":"ready"}]`, issuesBody: readyItem, totalHeader: "1"}
	dc := newDemandCounter(t, f)

	for i := 0; i < 4; i++ {
		if _, err := dc.CountReady(false); err != nil {
			t.Fatalf("CountReady #%d: %v", i, err)
		}
	}
	if got := f.labelReqs.Load(); got != 1 {
		t.Fatalf("label list walks = %d across 4 probes, want 1", got)
	}
	if got := f.issueReqs.Load(); got != 4 {
		t.Fatalf("issue list requests = %d, want 4", got)
	}
}

func TestForgejoDemand_MissingTotalCountErrors(t *testing.T) {
	f := &demandFake{labelsBody: `[{"id":1,"name":"ready"}]`, issuesBody: readyItem}
	dc := newDemandCounter(t, f)

	_, err := dc.CountReady(false)
	if err == nil || !strings.Contains(err.Error(), "forgejo") {
		t.Fatalf("CountReady error = %v, want one naming forgejo", err)
	}

	f.totalHeader = "many"
	if _, err := dc.CountReady(false); err == nil {
		t.Fatal("CountReady with an unparseable X-Total-Count returned nil error")
	}
}

func TestForgejoDemand_DroppedFilterCountsZeroAndRechecksLabels(t *testing.T) {
	f := &demandFake{labelsBody: `[{"id":1,"name":"ready"}]`, issuesBody: readyItem, totalHeader: "1"}
	dc := newDemandCounter(t, f)
	if n, _ := dc.CountReady(false); n != 1 {
		t.Fatalf("warm-up CountReady = %d, want 1", n)
	}

	// The label is deleted: Forgejo drops the filter and serves an unlabelled issue.
	f.labelsBody = `[]`
	f.issuesBody = `[{"number":9,"title":"x","state":"open","labels":[]}]`
	f.totalHeader = "40"
	n, err := dc.CountReady(false)
	if err != nil || n != 0 {
		t.Fatalf("CountReady with dropped filter = %d, %v; want 0, nil", n, err)
	}

	before := f.labelReqs.Load()
	if n, _ := dc.CountReady(false); n != 0 {
		t.Fatalf("next CountReady = %d, want 0", n)
	}
	if f.labelReqs.Load() == before {
		t.Fatal("next probe did not re-check labels after the backstop invalidated the cache")
	}
	if got := f.issueReqs.Load(); got != 2 {
		t.Fatalf("issue list requests = %d, want 2 (undefined label sends none)", got)
	}
}

func TestForgejoDemand_LabelCheckFailureWarnsOncePerOutage(t *testing.T) {
	f := &demandFake{labelsBody: `[]`, issuesBody: readyItem, totalHeader: "1"}
	clock := time.Unix(1_000_000, 0)
	dc := newDemandCounterAt(t, f, func() time.Time { return clock })
	const warning = "ListLabels failed"

	warnings := func() int {
		out := captureStderr(t, func() {
			if _, err := dc.CountReady(false); err != nil {
				t.Errorf("CountReady: %v", err)
			}
		})
		return strings.Count(out, warning)
	}

	f.labelsFail.Store(true)
	if got := warnings(); got != 1 {
		t.Fatalf("first failing probe warned %d times, want 1", got)
	}
	if got := warnings(); got != 0 {
		t.Fatalf("second consecutive failing probe warned %d times, want 0", got)
	}

	// A successful check, even one that finds the label undefined, ends the outage.
	f.labelsFail.Store(false)
	if got := warnings(); got != 0 {
		t.Fatalf("recovered probe warned %d times, want 0", got)
	}
	// The undefined verdict just cached would hide the outage until it expires.
	clock = clock.Add(11 * time.Minute)
	f.labelsFail.Store(true)
	if got := warnings(); got != 1 {
		t.Fatalf("first failing probe after recovery warned %d times, want 1", got)
	}
}

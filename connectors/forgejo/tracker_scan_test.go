package main

import (
	"reflect"
	"strconv"
	"strings"
	"testing"

	"spindrift.dev/sdk/connector"
	"spindrift.dev/sdk/tracker"
)

// withHeaders serves the latest response's headers.
func withHeaders(t *testing.T, h map[string]string) {
	t.Helper()
	old := responseHeader
	responseHeader = func(name string) (string, bool) { v, ok := h[name]; return v, ok }
	t.Cleanup(func() { responseHeader = old })
}

const countURL = repoAPI + "/issues?labels=ready&limit=1&page=1&state=open&type=issues"

func TestTrackerProbeReportsStatus(t *testing.T) {
	for _, code := range []int{200, 401, 404} {
		got := stub(t, map[string]connector.Response{repoAPI: {Status: code}})
		st, err := trackerProbe(ttgt)
		if err != nil || st != code {
			t.Errorf("probe = %d, %v, want %d", st, err, code)
		}
		if r := (*got)[0]; r.Method != "GET" || r.URL != repoAPI {
			t.Errorf("request = %#v", r)
		}
	}
}

func TestCount(t *testing.T) {
	page := ok(`[{"number":3,"labels":[{"name":"ready"}]}]`)
	for _, tc := range []struct {
		name   string
		labels string
		page   connector.Response
		total  string
		limit  int
		want   int
	}{
		{"defined label", `[{"id":1,"name":"ready"}]`, page, "12", 0, 12},
		{"clamped by limit", `[{"id":1,"name":"ready"}]`, page, "12", 5, 5},
		{"limit above total", `[{"id":1,"name":"ready"}]`, page, "2", 5, 2},
		{"no open issue", `[{"id":1,"name":"ready"}]`, ok(`[]`), "0", 0, 0},
		{"filter dropped by Forgejo", `[{"id":1,"name":"ready"}]`, ok(`[{"number":3,"labels":[{"name":"other"}]}]`), "40", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			labelStub(t, tc.labels, status(404), map[string]connector.Response{countURL: tc.page})
			withHeaders(t, map[string]string{"X-Total-Count": tc.total})
			got, err := count(ttgt, tracker.Query{Label: "ready"}, tc.limit, false)
			if err != nil || got != tc.want {
				t.Fatalf("count = %d, %v, want %d", got, err, tc.want)
			}
		})
	}
}

func TestCountUndefinedLabelAsksNoIssues(t *testing.T) {
	got := labelStub(t, `[{"id":1,"name":"other"}]`, ok(`[]`), nil)
	withHeaders(t, map[string]string{"X-Total-Count": "40"})
	n, err := count(ttgt, tracker.Query{Label: "ready"}, 0, false)
	if err != nil || n != 0 {
		t.Fatalf("count = %d, %v", n, err)
	}
	for _, r := range *got {
		if r.URL == countURL {
			t.Fatalf("issue query sent for an undefined label: %v", r.URL)
		}
	}
}

func TestCountOrgLabelIsDefined(t *testing.T) {
	labelStub(t, `[]`, ok(`[{"id":9,"name":"ready"}]`), map[string]connector.Response{
		countURL: ok(`[{"number":3,"labels":[{"name":"ready"}]}]`),
	})
	withHeaders(t, map[string]string{"X-Total-Count": "4"})
	if n, err := count(ttgt, tracker.Query{Label: "ready"}, 0, false); err != nil || n != 4 {
		t.Fatalf("count = %d, %v", n, err)
	}
}

func TestCountBadHeaderIsAnError(t *testing.T) {
	for _, h := range []map[string]string{{}, {"X-Total-Count": "x"}, {"X-Total-Count": "-1"}} {
		labelStub(t, `[{"id":1,"name":"ready"}]`, status(404), map[string]connector.Response{countURL: ok(`[]`)})
		withHeaders(t, h)
		if _, err := count(ttgt, tracker.Query{Label: "ready"}, 0, false); err == nil {
			t.Errorf("headers %v accepted", h)
		}
	}
}

func TestCountStatusIsClassed(t *testing.T) {
	labelStub(t, `[{"id":1,"name":"ready"}]`, status(404), map[string]connector.Response{countURL: status(500)})
	if _, err := count(ttgt, tracker.Query{Label: "ready"}, 0, false); err == nil {
		t.Fatal("a 500 must be an error")
	}
}

func labeledURL(state string, limit, page int) string {
	return repoAPI + "/issues?labels=finding&limit=" + strconv.Itoa(limit) + "&page=" + strconv.Itoa(page) + "&state=" + state + "&type=issues"
}

func TestLabeledPageClosedFieldsAndRecheck(t *testing.T) {
	labelStub(t, `[{"id":1,"name":"finding"}]`, status(404), map[string]connector.Response{
		labeledURL("closed", 20, 1): ok(`[
			{"number":9,"title":"nine","state":"closed","closed_at":"2026-01-02T03:04:05Z","labels":[{"name":"finding"}]},
			{"number":8,"title":"dropped","state":"closed","labels":[{"name":"other"}]}]`),
	})
	withHeaders(t, nil)
	got, next, err := labeledPage(ttgt, "finding", nil, "closed", "", 20)
	want := []tracker.Issue{{Number: "9", Title: "nine", State: "closed", Labels: []string{"finding"}, ClosedAt: "2026-01-02T03:04:05Z"}}
	if err != nil || next != "" || !reflect.DeepEqual(got, want) {
		t.Fatalf("labeledPage = %#v, %q, %v", got, next, err)
	}
}

func TestLabeledPagePagesByLinkHeader(t *testing.T) {
	labelStub(t, `[{"id":1,"name":"finding"}]`, status(404), map[string]connector.Response{
		labeledURL("open", 20, 2): ok(`[{"number":4,"state":"open","labels":[{"name":"finding"}]}]`),
	})
	withHeaders(t, map[string]string{"Link": `<https://forge.test/x?page=3>; rel="next", <https://forge.test/x?page=9>; rel="last"`})
	got, next, err := labeledPage(ttgt, "finding", nil, "open", "2", 20)
	if err != nil || next != "3" || len(got) != 1 || got[0].ClosedAt != "" {
		t.Fatalf("labeledPage = %#v, %q, %v", got, next, err)
	}
	withHeaders(t, map[string]string{"Link": `<https://forge.test/x?page=1>; rel="first"`})
	if _, next, err = labeledPage(ttgt, "finding", nil, "open", "2", 20); err != nil || next != "" {
		t.Fatalf("last page: next = %q, %v", next, err)
	}
}

func TestLabeledPageFullPageFallback(t *testing.T) {
	labelStub(t, `[{"id":1,"name":"finding"}]`, status(404), map[string]connector.Response{
		labeledURL("open", 1, 1): ok(`[{"number":4,"state":"open","labels":[{"name":"finding"}]}]`),
	})
	withHeaders(t, nil)
	if _, next, err := labeledPage(ttgt, "finding", nil, "open", "", 1); err != nil || next != "2" {
		t.Fatalf("next = %q, %v", next, err)
	}
}

func TestLabeledPageUndefinedLabelIsEmpty(t *testing.T) {
	got := labelStub(t, `[]`, ok(`[]`), nil)
	out, next, err := labeledPage(ttgt, "finding", nil, "open", "", 20)
	if err != nil || next != "" || out == nil || len(out) != 0 {
		t.Fatalf("labeledPage = %#v, %q, %v", out, next, err)
	}
	for _, r := range *got {
		if r.URL == labeledURL("open", 20, 1) {
			t.Fatal("issue query sent for an undefined label")
		}
	}
}

func TestLabeledPageRefusesBadInput(t *testing.T) {
	stub(t, nil)
	for _, c := range []struct {
		state, cursor string
		limit         int
	}{{"all", "", 5}, {"open", "x", 5}, {"open", "0", 5}, {"open", "", 0}} {
		if _, _, err := labeledPage(ttgt, "finding", nil, c.state, c.cursor, c.limit); err == nil {
			t.Errorf("%+v accepted", c)
		}
	}
}

func searchURL(extra, state string) string {
	return repoAPI + "/issues?" + extra + "limit=10&q=dup+me&state=" + state + "&type=issues"
}

func TestSearchWithoutLabel(t *testing.T) {
	stub(t, map[string]connector.Response{
		searchURL("", "closed"): ok(`[{"number":2,"title":"dup me","state":"closed"},{"number":1,"state":"closed"}]`),
	})
	got, err := search(ttgt, "dup me", "closed", "", 10)
	if err != nil || len(got) != 2 || got[0].Number != "2" {
		t.Fatalf("search = %#v, %v", got, err)
	}
}

func TestSearchDefinedLabelIsRechecked(t *testing.T) {
	labelStub(t, `[{"id":1,"name":"finding"}]`, status(404), map[string]connector.Response{
		searchURL("labels=finding&", "open"): ok(`[{"number":2,"labels":[{"name":"finding"}]},{"number":3,"labels":[{"name":"other"}]}]`),
	})
	got, err := search(ttgt, "dup me", "open", "finding", 10)
	if err != nil || len(got) != 1 || got[0].Number != "2" {
		t.Fatalf("search = %#v, %v", got, err)
	}
}

func TestSearchUndefinedLabelIsNone(t *testing.T) {
	got := labelStub(t, `[]`, ok(`[]`), nil)
	out, err := search(ttgt, "dup me", "open", "finding", 10)
	if err != nil || out == nil || len(out) != 0 {
		t.Fatalf("search = %#v, %v", out, err)
	}
	for _, r := range *got {
		if r.URL == searchURL("labels=finding&", "open") {
			t.Fatal("issue query sent for an undefined label")
		}
	}
}

func TestSearchRefusesBadInput(t *testing.T) {
	stub(t, nil)
	if _, err := search(ttgt, "x", "all", "", 5); err == nil {
		t.Error("state all accepted")
	}
	if _, err := search(ttgt, "x", "open", "", 0); err == nil {
		t.Error("limit 0 accepted")
	}
}

// An unreadable label registry counts the label as defined; the client-side
// re-check then decides.
func TestUnreadableLabelRegistryQueriesAnyway(t *testing.T) {
	t.Run("count", func(t *testing.T) {
		labelStub(t, `[]`, status(500), map[string]connector.Response{
			countURL: ok(`[{"number":3,"labels":[{"name":"ready"}]}]`),
		})
		withHeaders(t, map[string]string{"X-Total-Count": "4"})
		if n, err := count(ttgt, tracker.Query{Label: "ready"}, 0, false); err != nil || n != 4 {
			t.Fatalf("count = %d, %v", n, err)
		}
	})
	t.Run("count rechecks", func(t *testing.T) {
		labelStub(t, `[]`, status(500), map[string]connector.Response{
			countURL: ok(`[{"number":3,"labels":[{"name":"other"}]}]`),
		})
		withHeaders(t, map[string]string{"X-Total-Count": "40"})
		if n, err := count(ttgt, tracker.Query{Label: "ready"}, 0, false); err != nil || n != 0 {
			t.Fatalf("count = %d, %v", n, err)
		}
	})
	t.Run("labeledPage", func(t *testing.T) {
		labelStub(t, `[]`, status(500), map[string]connector.Response{
			labeledURL("open", 20, 1): ok(`[{"number":2,"labels":[{"name":"finding"}]},{"number":3,"labels":[{"name":"other"}]}]`),
		})
		out, _, err := labeledPage(ttgt, "finding", nil, "open", "", 20)
		if err != nil || len(out) != 1 || out[0].Number != "2" {
			t.Fatalf("labeledPage = %#v, %v", out, err)
		}
	})
	t.Run("search", func(t *testing.T) {
		labelStub(t, `[]`, status(500), map[string]connector.Response{
			searchURL("labels=finding&", "open"): ok(`[{"number":2,"labels":[{"name":"finding"}]},{"number":3,"labels":[{"name":"other"}]}]`),
		})
		out, err := search(ttgt, "dup me", "open", "finding", 10)
		if err != nil || len(out) != 1 || out[0].Number != "2" {
			t.Fatalf("search = %#v, %v", out, err)
		}
	})
	t.Run("repo list unreadable", func(t *testing.T) {
		stub(t, map[string]connector.Response{
			labelsURL + "1": status(500),
			countURL:        ok(`[{"number":3,"labels":[{"name":"ready"}]}]`),
		})
		withHeaders(t, map[string]string{"X-Total-Count": "4"})
		if n, err := count(ttgt, tracker.Query{Label: "ready"}, 0, false); err != nil || n != 4 {
			t.Fatalf("count = %d, %v", n, err)
		}
	})
}

func TestCountRouteIsAndedAndRechecked(t *testing.T) {
	const routed = repoAPI + "/issues?labels=ready%2Corders-api&limit=100&page=1&state=open&type=issues"
	both := `{"number":3,"labels":[{"name":"ready"},{"name":"orders-api"}]}`
	for _, tc := range []struct {
		name string
		page string
		want int
	}{
		{"carries both", `[` + both + `]`, 12},
		{"every issue on the page carries both", `[` + both + `,` + both + `]`, 12},
		{"a later issue lacks a label", `[` + both + `,{"number":4,"labels":[{"name":"ready"}]}]`, 0},
		{"filter dropped by Forgejo", `[{"number":3,"labels":[{"name":"ready"}]}]`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			labelStub(t, `[{"id":1,"name":"ready"},{"id":2,"name":"orders-api"}]`, status(404), map[string]connector.Response{routed: ok(tc.page)})
			withHeaders(t, map[string]string{"X-Total-Count": "12"})
			got, err := count(ttgt, tracker.Query{Label: "ready", Route: orders}, 0, false)
			if err != nil || got != tc.want {
				t.Fatalf("count = %d, %v, want %d", got, err, tc.want)
			}
		})
	}
}

// With the org registry unreadable, a route label absent from the repo's is
// still queried, and Forgejo may drop it from the filter (#7409): every route
// label must be re-checked on every issue, not just the first or the first label.
func TestCountMultiLabelRouteUnreadableOrgRechecksEveryLabel(t *testing.T) {
	const routed = repoAPI + "/issues?labels=ready%2Corders-api%2Cbilling&limit=100&page=1&state=open&type=issues"
	route := []tracker.Selector{{Kind: "label", Value: "orders-api"}, {Kind: "label", Value: "billing"}}
	all := `{"number":3,"labels":[{"name":"ready"},{"name":"orders-api"},{"name":"billing"}]}`
	for _, tc := range []struct {
		name string
		page string
		want int
	}{
		{"every issue carries every label", `[` + all + `,` + strings.Replace(all, `"number":3`, `"number":4`, 1) + `]`, 12},
		{"a later issue lacks the second route label", `[` + all + `,{"number":4,"labels":[{"name":"ready"},{"name":"orders-api"}]}]`, 0},
		{"a later issue lacks the first route label", `[` + all + `,{"number":4,"labels":[{"name":"ready"},{"name":"billing"}]}]`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// billing is org-only, and the org lookup is refused for lack of auth.
			labelStub(t, `[{"id":1,"name":"ready"},{"id":2,"name":"orders-api"}]`, status(403), map[string]connector.Response{routed: ok(tc.page)})
			withHeaders(t, map[string]string{"X-Total-Count": "12"})
			got, err := count(ttgt, tracker.Query{Label: "ready", Route: route}, 0, false)
			if err != nil || got != tc.want {
				t.Fatalf("count = %d, %v, want %d", got, err, tc.want)
			}
		})
	}
}

func TestCountUndefinedRouteLabelAsksNoIssues(t *testing.T) {
	got := labelStub(t, `[{"id":1,"name":"ready"}]`, ok(`[]`), nil)
	withHeaders(t, map[string]string{"X-Total-Count": "40"})
	n, err := count(ttgt, tracker.Query{Label: "ready", Route: orders}, 0, false)
	if err != nil || n != 0 {
		t.Fatalf("count = %d, %v; want 0, not the whole scope", n, err)
	}
	for _, r := range *got {
		if strings.Contains(r.URL, "/issues?") {
			t.Fatalf("issue query sent for an undefined route label: %v", r.URL)
		}
	}
}

func TestCountRefusesANonLabelRoute(t *testing.T) {
	got := stub(t, nil)
	if _, err := count(ttgt, tracker.Query{Label: "ready", Route: []tracker.Selector{{Kind: "project", Value: "x"}}}, 0, false); err == nil {
		t.Fatal("want an error")
	}
	if len(*got) != 0 {
		t.Fatalf("requests = %v, want none", *got)
	}
}

func TestLabeledPageRouteIsAndedAndRechecked(t *testing.T) {
	const routed = repoAPI + "/issues?labels=finding%2Corders-api&limit=20&page=1&state=open&type=issues"
	labelStub(t, `[{"id":1,"name":"finding"},{"id":2,"name":"orders-api"}]`, status(404), map[string]connector.Response{routed: ok(`[
		{"number":4,"state":"open","labels":[{"name":"finding"},{"name":"orders-api"}]},
		{"number":5,"state":"open","labels":[{"name":"finding"}]}]`)})
	got, _, err := labeledPage(ttgt, "finding", orders, "open", "", 20)
	if err != nil || len(got) != 1 || got[0].Number != "4" {
		t.Fatalf("labeledPage = %#v, %v; want only issue 4", got, err)
	}
}

func TestLabeledPageUndefinedRouteLabelIsEmpty(t *testing.T) {
	got := labelStub(t, `[{"id":1,"name":"finding"}]`, ok(`[]`), nil)
	issues, next, err := labeledPage(ttgt, "finding", orders, "open", "", 20)
	if err != nil || len(issues) != 0 || next != "" {
		t.Fatalf("labeledPage = %#v, %q, %v; want empty", issues, next, err)
	}
	for _, r := range *got {
		if strings.Contains(r.URL, "/issues?") {
			t.Fatalf("issue query sent for an undefined route label: %v", r.URL)
		}
	}
}

func TestLabeledPageRefusesANonLabelRoute(t *testing.T) {
	if _, _, err := labeledPage(ttgt, "finding", []tracker.Selector{{Kind: "project", Value: "x"}}, "open", "", 20); err == nil {
		t.Fatal("want an error")
	}
}

func TestLabeledPageNoLabelAndNoRouteAsksNothing(t *testing.T) {
	got := stub(t, nil)
	issues, next, err := labeledPage(ttgt, "", nil, "open", "", 20)
	if err != nil || issues == nil || len(issues) != 0 || next != "" {
		t.Fatalf("labeledPage = %#v, %q, %v; want empty", issues, next, err)
	}
	if len(*got) != 0 {
		t.Fatalf("requests = %v, want none", *got)
	}
}

func TestRoutedQueriesReadTheLabelRegistryOnce(t *testing.T) {
	registry := `[{"id":1,"name":"ready"},{"id":2,"name":"orders-api"}]`
	reads := func(got *[]connector.Request) (n int) {
		for _, r := range *got {
			if r.URL == labelsURL+"1" {
				n++
			}
		}
		return n
	}
	t.Run("count", func(t *testing.T) {
		got := labelStub(t, registry, status(404), nil)
		withHeaders(t, map[string]string{"X-Total-Count": "0"})
		_, _ = count(ttgt, tracker.Query{Label: "ready", Route: orders}, 0, false)
		if n := reads(got); n != 1 {
			t.Fatalf("registry read %d times, want 1", n)
		}
	})
	t.Run("labeledPage", func(t *testing.T) {
		got := labelStub(t, registry, status(404), nil)
		_, _, _ = labeledPage(ttgt, "ready", orders, "open", "", 20)
		if n := reads(got); n != 1 {
			t.Fatalf("registry read %d times, want 1", n)
		}
	})
}

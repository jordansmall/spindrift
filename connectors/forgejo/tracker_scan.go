package main

import (
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"spindrift.dev/sdk/connector"
	"spindrift.dev/sdk/tracker"
)

// responseHeader reads a header of the latest response; tests swap it because
// connector.ResponseHeader has no host outside wasip1.
var responseHeader = connector.ResponseHeader

// countInterval is how often core re-asks count. A count is one small request,
// but Forgejo sends no ETag or Last-Modified, so no conditional request makes
// it free.
const countInterval = 3 * time.Minute

// trackerProbe is one authenticated read of the repository. A non-2xx is
// reported as its status, not an error, so core decides what an auth failure
// means.
func trackerProbe(t tracker.Target) (int, error) {
	resp, err := request("GET", t.API, issuePath(t, ""), nil)
	if err != nil {
		return 0, err
	}
	return resp.Status, nil
}

// labelDefined reports whether label exists on the repository or its owner.
// Forgejo resolves a labels= filter by name and silently drops it when none
// resolve, answering every issue (#3944, #3952), so the issue queries below
// skip an undefined label. When the registry cannot rule the label out (an
// unreadable repo or org lookup, or an org-only label the token cannot see)
// it counts as defined and the client-side re-check decides.
func labelDefined(t tracker.Target, label string) bool {
	labels, orgUnreadable, err := labelUnion(t)
	if err != nil {
		return true
	}
	if slices.ContainsFunc(labels, func(l rawLabel) bool { return l.Name == label }) {
		return true
	}
	return orgUnreadable
}

func hasLabel(i tracker.Issue, label string) bool { return slices.Contains(i.Labels, label) }

func allDefined(t tracker.Target, labels []string) bool {
	return !slices.ContainsFunc(labels, func(l string) bool { return !labelDefined(t, l) })
}

// hasAll re-checks every required label: Forgejo silently drops an unresolved
// labels= filter, so a listed issue may not carry them.
func hasAll(i tracker.Issue, labels []string) bool {
	return !slices.ContainsFunc(labels, func(l string) bool { return !hasLabel(i, l) })
}

// count reads the open issues carrying q.Label from the X-Total-Count of a
// one-item page rather than walking them, clamped to limit when limit > 0.
func count(t tracker.Target, q tracker.Query, limit int, _ bool) (int, error) {
	v := url.Values{"state": {"open"}, "type": {"issues"}, "limit": {"1"}, "page": {"1"}}
	required, err := tracker.RouteLabels(q.Label, q.Route)
	if err != nil {
		return 0, err
	}
	if len(required) > 0 {
		if !allDefined(t, required) {
			return 0, nil
		}
		v.Set("labels", strings.Join(required, ","))
	}
	path := issuePath(t, "/issues") + "?" + v.Encode()
	resp, err := request("GET", t.API, path, nil)
	if err != nil {
		return 0, err
	}
	if !is2xx(resp.Status) {
		return 0, statusError("GET", path, resp.Status)
	}
	// Read the header before anything else sends: it belongs to the latest
	// response.
	raw, _ := responseHeader("X-Total-Count")
	total, err := strconv.Atoi(raw)
	if err != nil || total < 0 {
		return 0, fmt.Errorf("count issues: missing or invalid X-Total-Count header %q", raw)
	}
	if len(required) > 0 {
		var page []rawIssue
		if err := decode("GET", path, resp.Body, &page); err != nil {
			return 0, err
		}
		// Forgejo dropped a label from the filter, so total counts unrelated
		// issues; usually labelDefined answered true for a label it could not
		// rule out.
		if len(page) > 0 && !hasAll(page[0].issue(), required) {
			return 0, nil
		}
	}
	if limit > 0 {
		total = min(total, limit)
	}
	return total, nil
}

// labeledPage returns one page of the issues in state carrying label, newest
// first, with the close date of a closed one. Forgejo records no close reason,
// so ClosedNotPlanned stays false. The cursor is the page number ("" is page
// 1); next follows Forgejo's Link header, since an instance may cap a page
// below limit, and falls back to a full page when the header is absent.
func labeledPage(t tracker.Target, label, state, cursor string, limit int, route []tracker.Selector) ([]tracker.Issue, string, error) {
	if limit < 1 {
		return nil, "", fmt.Errorf("list labeled issues: limit %d < 1", limit)
	}
	if state != "open" && state != "closed" {
		return nil, "", fmt.Errorf("list labeled issues: unsupported issue state %q", state)
	}
	page := 1
	if cursor != "" {
		n, err := strconv.Atoi(cursor)
		if err != nil || n < 1 {
			return nil, "", fmt.Errorf("list labeled issues: malformed cursor %q", cursor)
		}
		page = n
	}
	required, err := tracker.RouteLabels(label, route)
	if err != nil {
		return nil, "", err
	}
	if !allDefined(t, required) {
		return []tracker.Issue{}, "", nil
	}
	perPage := min(limit, pageSize)
	v := url.Values{
		"state": {state}, "type": {"issues"}, "labels": {strings.Join(required, ",")},
		"limit": {strconv.Itoa(perPage)}, "page": {strconv.Itoa(page)},
	}
	path := issuePath(t, "/issues") + "?" + v.Encode()
	resp, err := request("GET", t.API, path, nil)
	if err != nil {
		return nil, "", err
	}
	if !is2xx(resp.Status) {
		return nil, "", statusError("GET", path, resp.Status)
	}
	link, hasLink := responseHeader("Link")
	var raw []rawIssue
	if err := decode("GET", path, resp.Body, &raw); err != nil {
		return nil, "", err
	}
	out := []tracker.Issue{}
	for _, r := range raw {
		i := r.issue()
		// An unresolved labels= filter is dropped by Forgejo, so re-check.
		if !hasAll(i, required) {
			continue
		}
		if r.State == "closed" {
			i.ClosedAt = r.ClosedAt
		}
		out = append(out, i)
	}
	more := len(raw) >= perPage
	if hasLink {
		more = strings.Contains(link, `rel="next"`)
	}
	next := ""
	if more && len(raw) > 0 {
		next = strconv.Itoa(page + 1)
	}
	return out, next, nil
}

// search returns up to limit issues matching text in state, in one request:
// the server caps the page at its own MAX_RESPONSE_ITEMS, the intended bound
// for a dedup lookup. text travels only as the url-encoded q value.
func search(t tracker.Target, text, state, label string, limit int) ([]tracker.Issue, error) {
	if limit < 1 {
		return nil, fmt.Errorf("search issues: limit %d < 1", limit)
	}
	if state != "open" && state != "closed" {
		return nil, fmt.Errorf("search issues: unsupported issue state %q", state)
	}
	v := url.Values{"q": {text}, "type": {"issues"}, "state": {state}, "limit": {strconv.Itoa(limit)}}
	if label != "" {
		if !labelDefined(t, label) {
			return []tracker.Issue{}, nil
		}
		v.Set("labels", label)
	}
	var raw []rawIssue
	if err := call("GET", t.API, issuePath(t, "/issues")+"?"+v.Encode(), nil, &raw); err != nil {
		return nil, err
	}
	out := []tracker.Issue{}
	for _, r := range raw {
		if i := r.issue(); label == "" || hasLabel(i, label) {
			out = append(out, i)
		}
	}
	return out[:min(limit, len(out))], nil
}

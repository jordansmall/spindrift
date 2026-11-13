package main

import (
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"spindrift.dev/sdk/connector"
	"spindrift.dev/sdk/tracker"
)

func TestProbeReturnsStatusWithoutFailingOnNon2xx(t *testing.T) {
	for _, status := range []int{200, 401} {
		got := script(t, func(connector.Request, *url.URL) (int, string) { return status, "{}" })
		s, err := probe(cloud)
		if err != nil || s != status {
			t.Fatalf("probe = %d, %v; want %d, nil", s, err, status)
		}
		r := (*got)[0]
		if r.Method != "GET" || r.URL != site+"/rest/api/2/myself" {
			t.Fatalf("request = %#v", r)
		}
	}
}

func TestProbeErrorsOnTransportFailure(t *testing.T) {
	old := send
	send = func(connector.Request) (connector.Response, error) { return connector.Response{}, errors.New("dial") }
	t.Cleanup(func() { send = old })
	if _, err := probe(cloud); err == nil {
		t.Fatal("want transport error")
	}
}

func TestStatusesCollectsNamesAcrossIssueTypes(t *testing.T) {
	got := serve(t, "/jira/rest/api/2/project/ENG/statuses", `[
		{"name":"Bug","statuses":[{"name":"To Do"},{"name":"Done"}]},
		{"name":"Task","statuses":[{"name":"In Review"}]}]`)
	names, err := statuses(cloud)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"To Do", "Done", "In Review"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("statuses = %v", names)
	}
	if r := (*got)[0]; r.Method != "GET" {
		t.Fatalf("request = %#v", r)
	}
}

func createMetaServer(t *testing.T, fn func(u *url.URL) string) *[]connector.Request {
	return script(t, func(_ connector.Request, u *url.URL) (int, string) {
		return 200, fn(u)
	})
}

func TestCreateMetaCloudShape(t *testing.T) {
	got := createMetaServer(t, func(u *url.URL) string {
		switch u.Path {
		case "/jira/rest/api/2/issue/createmeta/ENG/issuetypes":
			return `{"isLast":true,"issueTypes":[{"id":"1","name":"Bug"},{"id":"10","name":"Task"}]}`
		case "/jira/rest/api/2/issue/createmeta/ENG/issuetypes/10":
			return `{"isLast":true,"fields":[{"fieldId":"summary","name":"Summary","required":true,"hasDefaultValue":false},
				{"fieldId":"priority","name":"Priority","required":true,"hasDefaultValue":true}]}`
		}
		t.Fatalf("unexpected path %s", u.Path)
		return ""
	})
	cm, err := createMeta(cloud, "Task")
	if err != nil {
		t.Fatal(err)
	}
	want := tracker.CreateMeta{
		IssueTypes: []string{"Bug", "Task"},
		Fields: []tracker.CreateField{
			{ID: "summary", Name: "Summary", Required: true},
			{ID: "priority", Name: "Priority", Required: true, HasDefaultValue: true},
		},
	}
	if !reflect.DeepEqual(cm, want) {
		t.Fatalf("createMeta = %#v", cm)
	}
	if q := (*got)[0].URL; !strings.Contains(q, "startAt=0&maxResults=50") {
		t.Fatalf("first request %s lacks paging", q)
	}
}

func TestCreateMetaServerValuesShapeAndPaging(t *testing.T) {
	got := createMetaServer(t, func(u *url.URL) string {
		start := u.Query().Get("startAt")
		switch {
		case u.Path == "/jira/rest/api/2/issue/createmeta/ENG/issuetypes" && start == "0":
			return `{"total":2,"values":[{"id":"1","name":"Bug"}]}`
		case u.Path == "/jira/rest/api/2/issue/createmeta/ENG/issuetypes" && start == "1":
			return `{"total":2,"values":[{"id":"10","name":"Task"}]}`
		case u.Path == "/jira/rest/api/2/issue/createmeta/ENG/issuetypes/10":
			return `{"isLast":true,"values":[{"fieldId":"summary","name":"Summary","required":true}]}`
		}
		t.Fatalf("unexpected request %s", u)
		return ""
	})
	cm, err := createMeta(server, "Task")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cm.IssueTypes, []string{"Bug", "Task"}) ||
		!reflect.DeepEqual(cm.Fields, []tracker.CreateField{{ID: "summary", Name: "Summary", Required: true}}) {
		t.Fatalf("createMeta = %#v", cm)
	}
	if len(*got) != 3 {
		t.Fatalf("requests = %d, want 3", len(*got))
	}
}

func TestCreateMetaTypeNotOfferedSkipsFieldWalk(t *testing.T) {
	got := serve(t, "/jira/rest/api/2/issue/createmeta/ENG/issuetypes", `{"isLast":true,"issueTypes":[{"id":"1","name":"Bug"}]}`)
	cm, err := createMeta(cloud, "Task")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cm.IssueTypes, []string{"Bug"}) || len(cm.Fields) != 0 {
		t.Fatalf("createMeta = %#v", cm)
	}
	if len(*got) != 1 {
		t.Fatalf("requests = %d, want 1", len(*got))
	}
}

func TestCountCloudPostsTheUnorderedListIssuesJQL(t *testing.T) {
	q := tracker.Query{Label: "agent-trigger"}
	got := script(t, func(connector.Request, *url.URL) (int, string) { return 200, `{"count":7}` })
	n, err := count(cloud, q)
	if err != nil || n != 7 {
		t.Fatalf("count = %d, %v", n, err)
	}
	r := (*got)[0]
	var body struct {
		JQL string `json:"jql"`
	}
	if err := json.Unmarshal([]byte(r.Body), &body); err != nil {
		t.Fatal(err)
	}
	if r.Method != "POST" || r.URL != site+"/rest/api/3/search/approximate-count" ||
		body.JQL != openJQL("ENG", q) || strings.Contains(body.JQL, "order by") || r.Headers["Content-Type"] != "application/json" {
		t.Fatalf("request = %#v", r)
	}
}

func TestCountServerReadsSearchTotalWithZeroRows(t *testing.T) {
	q := tracker.Query{Status: "Ready"}
	got := script(t, func(connector.Request, *url.URL) (int, string) { return 200, `{"total":4,"issues":[]}` })
	n, err := count(server, q)
	if err != nil || n != 4 {
		t.Fatalf("count = %d, %v", n, err)
	}
	r := (*got)[0]
	u, _ := url.Parse(r.URL)
	if r.Method != "GET" || u.Path != "/jira/rest/api/2/search" ||
		u.Query().Get("jql") != openJQL("ENG", q) || strings.Contains(u.Query().Get("jql"), "order by") || u.Query().Get("maxResults") != "0" {
		t.Fatalf("request = %#v", r)
	}
}

func TestCountRejectsUnknownDialect(t *testing.T) {
	if _, err := count(tracker.Target{API: site, Repo: "ENG", Dialect: "x"}, tracker.Query{}); err == nil {
		t.Fatal("want error")
	}
}

func TestCountIntervalIsInfrequent(t *testing.T) {
	if countInterval < time.Minute {
		t.Fatalf("countInterval = %s", countInterval)
	}
}

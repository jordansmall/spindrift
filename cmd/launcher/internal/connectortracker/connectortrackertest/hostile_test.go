package connectortrackertest_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/connectorforge/connectorforgetest"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectortracker"
	"spindrift.dev/launcher/internal/connectortracker/connectortrackertest"
	"spindrift.dev/launcher/internal/forge"
)

const (
	repoPath   = "/api/v1/repos/" + connectortrackertest.Repo
	issuesPath = repoPath + "/issues"
)

// hostileConnector is the fixture tracker that relays whatever the scripted
// backend answers for filing, comments and labels, so each case is a hostile
// Connector as far as core can tell.
func hostileConnector(t *testing.T, script ...connectorforgetest.Scripted) connectorforgetest.Connector {
	t.Helper()
	c := connectorforgetest.Connector{
		Wasm:   connectorforgetest.FixtureWasm(t, "tracker-hostile.wasm"),
		Script: script,
	}
	for _, m := range []string{http.MethodGet, http.MethodPost} {
		c.Routes = append(c.Routes, connectorhost.Route{Method: m, Host: "tracker.example", Path: "/api/v1/repos/{repo}"})
	}
	return c
}

func answer(method, path, body string) connectorforgetest.Scripted {
	return connectorforgetest.Scripted{Method: method, Path: path, Status: http.StatusOK, Body: body}
}

// requester is the harness's count of requests the backend answered.
type requester interface {
	Requests(method, path string) int
}

func TestHostileConnector_FiledURL(t *testing.T) {
	for _, tc := range []struct {
		name, url string
		unusable  bool
	}{
		{"same host over https", "https://{host}/o/r/issues/5", false},
		{"another host", "https://evil.example/o/r/issues/5", true},
		{"http scheme", "http://{host}/o/r/issues/5", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := connectortrackertest.New(t, hostileConnector(t, answer(http.MethodPost, issuesPath, `{"html_url":"`+tc.url+`"}`)))
			filer, ok := forge.As[forge.HostPostedIssueFiler](h.Tracker())
			if !ok {
				t.Fatal("no HostPostedIssueFiler")
			}
			got, err := filer.PostIssue("t", "b", []string{"l"})
			if tc.unusable {
				if !errors.Is(err, forge.ErrFiledUnusableURL) || !errors.Is(err, connectortracker.ErrAnswer) || got != "" {
					t.Errorf("PostIssue = %q, %v; want ErrFiledUnusableURL and no url", got, err)
				}
			} else if err != nil || !strings.HasPrefix(got, "https://") || !strings.HasSuffix(got, "/o/r/issues/5") {
				t.Errorf("PostIssue = %q, %v; want the https issue url", got, err)
			}
			if n := h.(requester).Requests(http.MethodPost, issuesPath); n != 1 {
				t.Errorf("Connector filed %d times, want exactly one", n)
			}
		})
	}
}

func TestHostileConnector_Comments(t *testing.T) {
	const commentsPath = issuesPath + "/1/comments"
	comment := func(created, assoc string) string {
		return `[{"user":{"login":"a"},"created_at":"` + created + `","body":"x","author_association":"` + assoc + `"}]`
	}
	read := func(t *testing.T, body string) ([]forge.Comment, error) {
		t.Helper()
		h := connectortrackertest.New(t, hostileConnector(t, answer(http.MethodGet, commentsPath, body)))
		cl, ok := forge.As[forge.CommentLister](h.Tracker())
		if !ok {
			t.Fatal("no CommentLister")
		}
		return cl.Comments("1")
	}

	for _, tc := range []struct {
		name, assoc, want string
		trusted           bool
	}{
		{"member keeps standing", "MEMBER", "MEMBER", true},
		{"none keeps its value but no standing", "NONE", "NONE", false},
		{"unknown association is blanked", "SUPERUSER", "", false},
		{"wrong case is blanked", "member", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := read(t, comment("2024-01-02T03:04:05Z", tc.assoc))
			if err != nil || len(got) != 1 {
				t.Fatalf("Comments = %+v, %v", got, err)
			}
			if got[0].Association != tc.want || forge.CommentTrusted(got[0]) != tc.trusted {
				t.Errorf("comment = %+v, want association %q trusted %v", got[0], tc.want, tc.trusted)
			}
		})
	}

	t.Run("unparseable createdAt is refused", func(t *testing.T) {
		got, err := read(t, comment("yesterday", "MEMBER"))
		if !errors.Is(err, connectortracker.ErrAnswer) || !strings.Contains(err.Error(), "createdAt") {
			t.Errorf("Comments = %+v, %v; want ErrAnswer naming createdAt", got, err)
		}
	})
}

func TestHostileConnector_Labels(t *testing.T) {
	const issuePath = issuesPath + "/1"
	issue := func(label string) string {
		return `{"number":1,"title":"t","state":"open","labels":[{"name":"ok"},{"name":` + label + `}]}`
	}
	for _, tc := range []struct {
		name, label string
		hostile     bool
	}{
		{"valid", `"bug"`, false},
		{"empty", `""`, true},
		{"control character", `"a\nb"`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := connectortrackertest.New(t, hostileConnector(t,
				answer(http.MethodGet, issuePath, issue(tc.label)),
				answer(http.MethodGet, repoPath+"/labels", `[{"name":"ok"},{"name":`+tc.label+`}]`)))
			tr := h.Tracker()
			iss, issErr := tr.Issue("1")
			names, listErr := tr.ListLabels()
			if !tc.hostile {
				if issErr != nil || listErr != nil || len(iss.Labels) != 2 || len(names) != 2 {
					t.Errorf("Issue = %+v, %v; ListLabels = %v, %v; want both labels back", iss, issErr, names, listErr)
				}
				return
			}
			for name, err := range map[string]error{"Issue": issErr, "ListLabels": listErr} {
				if !errors.Is(err, connectortracker.ErrAnswer) || !strings.Contains(err.Error(), "label") {
					t.Errorf("%s err = %v, want ErrAnswer naming label", name, err)
				}
			}
		})
	}
}

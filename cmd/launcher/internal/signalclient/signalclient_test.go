package signalclient_test

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/signalclient"
	"spindrift.dev/launcher/internal/signalwire"
)

const secret = "s3cret"

func statusHandler(reply func(w http.ResponseWriter)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" {
			http.NotFound(w, r)
			return
		}
		reply(w)
	})
}

func okStatus(w http.ResponseWriter) {
	_ = json.NewEncoder(w).Encode(signalwire.Status{
		Comment: &signalwire.Receipt{Kind: "comment", Bytes: 3, Hash: "h", Sequence: 1},
	})
}

func TestStatus_HTTP(t *testing.T) {
	var gotSecret string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSecret = r.Header.Get(signalwire.SecretHeader)
		statusHandler(okStatus).ServeHTTP(w, r)
	}))
	defer srv.Close()
	t.Setenv(signalclient.EndpointEnv, srv.URL+"/")
	t.Setenv(signalclient.SecretEnv, secret)

	st, err := signalclient.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.Comment == nil || st.Comment.Sequence != 1 {
		t.Errorf("Status.Comment = %+v, want the accepted receipt", st.Comment)
	}
	if gotSecret != secret {
		t.Errorf("secret header = %q, want %q", gotSecret, secret)
	}
}

func TestStatus_Unix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(statusHandler(okStatus))
	srv.Listener = l
	srv.Start()
	defer srv.Close()
	t.Setenv(signalclient.EndpointEnv, "unix://"+path)
	t.Setenv(signalclient.SecretEnv, "")

	st, err := signalclient.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.Comment == nil {
		t.Errorf("Status.Comment = nil, want the accepted receipt")
	}
}

func TestStatus_RejectBecomesError(t *testing.T) {
	srv := httptest.NewServer(statusHandler(func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(signalwire.Reject{Status: "unauthorized", Reason: "bad secret"})
	}))
	defer srv.Close()
	t.Setenv(signalclient.EndpointEnv, srv.URL)
	t.Setenv(signalclient.SecretEnv, secret)

	_, err := signalclient.Status()
	if err == nil || !strings.Contains(err.Error(), "rejected (unauthorized): bad secret") {
		t.Errorf("Status err = %v, want a rejected (unauthorized): bad secret error", err)
	}
}

func TestTarget_Errors(t *testing.T) {
	cases := []struct{ name, endpoint, secret, want string }{
		{"unset", "", "", signalclient.EndpointEnv + " is required"},
		{"bad scheme", "ftp://x", "", "must be unix://<path> or http://<host:port>"},
		{"empty unix path", "unix://", "", "names no socket path"},
		{"tcp without secret", "http://127.0.0.1:1", "", signalclient.SecretEnv + " is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(signalclient.EndpointEnv, tc.endpoint)
			t.Setenv(signalclient.SecretEnv, tc.secret)
			_, err := signalclient.Status()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Status err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestDecodeReject_NonSocketReply(t *testing.T) {
	got := signalclient.DecodeReject(502, []byte("bad gateway\nmore"))
	want := signalwire.Reject{Status: "http_502", Reason: "bad gateway"}
	if got != want {
		t.Errorf("DecodeReject = %+v, want %+v", got, want)
	}
	if got := signalclient.DecodeReject(500, nil); got.Reason == "" {
		t.Error("DecodeReject(empty reply) has no reason")
	}
}

func issueServer(t *testing.T, gotRef *string, reply func(w http.ResponseWriter)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/issue" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		*gotRef = r.URL.Query().Get("ref")
		reply(w)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchIssue_EncodesTheRefAndDecodesTheSnapshot(t *testing.T) {
	const ref = "owner/repo#5&x=1 y"
	var gotRef string
	srv := issueServer(t, &gotRef, func(w http.ResponseWriter) {
		_ = json.NewEncoder(w).Encode(signalwire.IssueSnapshot{Ref: ref, Title: "t", State: "open", Truncated: true})
	})

	snap, rej, err := signalclient.FetchIssue(srv.Client(), srv.URL, secret, ref)
	if err != nil || rej != nil {
		t.Fatalf("FetchIssue = (%v, %v), want success", rej, err)
	}
	if gotRef != ref {
		t.Errorf("ref on the wire = %q, want %q intact", gotRef, ref)
	}
	if snap.Ref != ref || snap.Title != "t" || !snap.Truncated {
		t.Errorf("snapshot = %+v, want the decoded reply", snap)
	}
}

func TestFetchIssue_RejectComesBackAsAReject(t *testing.T) {
	var gotRef string
	srv := issueServer(t, &gotRef, func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(signalwire.Reject{Status: "foreign_ref", Reason: "nope"})
	})

	_, rej, err := signalclient.FetchIssue(srv.Client(), srv.URL, secret, "1")
	if err != nil {
		t.Fatalf("FetchIssue: %v", err)
	}
	if rej == nil || rej.Status != "foreign_ref" {
		t.Fatalf("reject = %+v, want foreign_ref", rej)
	}
}

func TestFetchIssue_OversizedReplyIsAnErrorNotATruncatedDecode(t *testing.T) {
	var gotRef string
	srv := issueServer(t, &gotRef, func(w http.ResponseWriter) {
		// A prefix that is valid JSON up to the limit would decode if the
		// client cut the reply silently; the padding makes the cut visible.
		_, _ = w.Write([]byte(`{"ref":"1","title":"` + strings.Repeat("x", 2<<20) + `"}`))
	})

	_, rej, err := signalclient.FetchIssue(srv.Client(), srv.URL, secret, "1")
	if err == nil || rej != nil {
		t.Fatalf("FetchIssue = (%v, %v), want an error", rej, err)
	}
	if !strings.Contains(err.Error(), "limit") {
		t.Errorf("error = %v, want it to name the reply limit", err)
	}
}

func TestFetchIssueSearch_EncodesTheQueryAndDecodesTheReply(t *testing.T) {
	const text = "flaky repo:x/y & more"
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/issue-search" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		got = r.URL.Query()
		_ = json.NewEncoder(w).Encode(signalwire.IssueSearchReply{
			Issues:    []signalwire.IssueSearchResult{{Number: "7", Title: "t", State: "closed", Labels: []string{"a"}}},
			Truncated: true,
		})
	}))
	t.Cleanup(srv.Close)

	reply, rej, err := signalclient.FetchIssueSearch(srv.Client(), srv.URL, secret, text, "closed", "agent-review-finding")
	if err != nil || rej != nil {
		t.Fatalf("FetchIssueSearch = (%v, %v), want success", rej, err)
	}
	if got.Get("text") != text || got.Get("state") != "closed" || got.Get("label") != "agent-review-finding" || len(got) != 3 {
		t.Errorf("query on the wire = %v, want text, state and label intact", got)
	}
	if len(reply.Issues) != 1 || reply.Issues[0].Number != "7" || !reply.Truncated {
		t.Errorf("reply = %+v, want the decoded reply", reply)
	}

	if _, _, err := signalclient.FetchIssueSearch(srv.Client(), srv.URL, secret, "x", "open", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["label"]; ok {
		t.Errorf("query = %v, want the label omitted when empty", got)
	}
}

func TestFetchIssueSearch_RejectComesBackAsAReject(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(signalwire.Reject{Status: "invalid_query", Reason: "nope"})
	}))
	t.Cleanup(srv.Close)

	_, rej, err := signalclient.FetchIssueSearch(srv.Client(), srv.URL, secret, "x", "open", "")
	if err != nil {
		t.Fatalf("FetchIssueSearch: %v", err)
	}
	if rej == nil || rej.Status != "invalid_query" {
		t.Fatalf("reject = %+v, want invalid_query", rej)
	}
}

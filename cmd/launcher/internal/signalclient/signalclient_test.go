package signalclient_test

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
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

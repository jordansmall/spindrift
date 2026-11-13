package github

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// probeBackend answers the Connector's repository-identity query (POST
// /graphql) with a scripted status, recording the credential it was sent.
type probeBackend struct {
	*httptest.Server
	mu       sync.Mutex
	status   int
	seenAuth []string
}

func newProbeBackend(t *testing.T, status int) *probeBackend {
	t.Helper()
	b := &probeBackend{status: status}
	b.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.seenAuth = append(b.seenAuth, r.Header.Get("Authorization"))
		if r.Method != http.MethodPost || r.URL.Path != "/graphql" {
			http.NotFound(w, r)
			return
		}
		if b.status != http.StatusOK {
			w.WriteHeader(b.status)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"repository":{"url":"https://github.com/` + connectorRepo + `","nameWithOwner":"` + connectorRepo + `","autoMergeAllowed":false}}}`))
	}))
	t.Cleanup(b.Close)
	return b
}

// useConnectorClient points the probe's Connector at b's TLS certificate, as
// a real host reaches api.github.com.
func useConnectorClient(t *testing.T, b *probeBackend) {
	t.Helper()
	old := connectorClient
	connectorClient = b.Client()
	t.Cleanup(func() { connectorClient = old })
}

// With a github row the production Probe asks the Connector, with the host's
// own credential, and gives the answers the native gh Probe gives.
func TestExecClientProbe_ConfiguredRowGoesThroughTheConnector(t *testing.T) {
	for _, tc := range []struct {
		status           int
		wantAuth, wantNF bool
		wantRate         bool
	}{
		{status: 200},
		{status: 401, wantAuth: true},
		{status: 404, wantNF: true},
		{status: 429, wantRate: true},
	} {
		t.Run(strconv.Itoa(tc.status), func(t *testing.T) {
			b := newProbeBackend(t, tc.status)
			useGithubRowAt(t, b.Listener.Addr().String())
			useConnectorClient(t, b)
			t.Setenv(tokenCredentialEnv, "ghp_host")
			calls := countGH(t)

			slug, err := NewExecClient(connectorRepo, testLabels).Probe()

			if n := calls(); n != 0 {
				t.Errorf("Probe ran gh %d times", n)
			}
			if tc.status == 200 && (err != nil || slug != connectorRepo) {
				t.Errorf("Probe = (%q, %v), want (%q, nil)", slug, err, connectorRepo)
			}
			if got := errors.Is(err, forge.ErrAuthFailure); got != tc.wantAuth {
				t.Errorf("ErrAuthFailure = %v, want %v (err %v)", got, tc.wantAuth, err)
			}
			if got := errors.Is(err, forge.ErrRepoNotFound); got != tc.wantNF {
				t.Errorf("ErrRepoNotFound = %v, want %v (err %v)", got, tc.wantNF, err)
			}
			if got := errors.Is(err, forge.ErrRateLimit); got != tc.wantRate {
				t.Errorf("ErrRateLimit = %v, want %v (err %v)", got, tc.wantRate, err)
			}
			if len(b.seenAuth) == 0 || b.seenAuth[0] != "Bearer ghp_host" {
				t.Errorf("backend saw Authorization %v, want the host's credential", b.seenAuth)
			}
		})
	}
}

// The Connector's verdicts match the native Probe over the same failures.
func TestExecClientProbe_ConnectorMatchesNative(t *testing.T) {
	for _, status := range []int{200, 401, 404} {
		b := newProbeBackend(t, status)
		useGithubRowAt(t, b.Listener.Addr().String())
		useConnectorClient(t, b)
		t.Setenv(tokenCredentialEnv, "ghp_host")
		gotSlug, gotErr := NewExecClient(connectorRepo, testLabels).Probe()

		noGithubRow(t)
		ghProbeStub(t, status)
		wantSlug, wantErr := NewExecClient(connectorRepo, testLabels).Probe()

		if gotSlug != wantSlug && status == 200 || errClass(gotErr) != errClass(wantErr) {
			t.Errorf("status %d: Connector Probe = (%q, %v), native (%q, %v)", status, gotSlug, gotErr, wantSlug, wantErr)
		}
	}
}

// Without a github row the Probe is the native gh path.
func TestExecClientProbe_NoRowUsesGh(t *testing.T) {
	noGithubRow(t)
	dir := prependFakeGH(t, "printf 'owner/repo\\n'")

	slug, err := NewExecClient(connectorRepo, testLabels).Probe()

	if err != nil || slug != "owner/repo" {
		t.Fatalf("Probe = (%q, %v), want (owner/repo, nil)", slug, err)
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "call-*.txt")); len(m) != 2 {
		t.Errorf("Probe ran gh %d times, want 2 (auth status, repo view)", len(m))
	}
}

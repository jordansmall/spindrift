package github

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/connectorhost"
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
	old := connectorOptions
	connectorOptions = backendrows.Options{Client: b.Client(), PublicForTest: connectorhost.LoopbackForTest}
	t.Cleanup(func() { connectorOptions = old })
}

// With a github row the production Probe asks the Connector, with the host's
// own credential, and reports auth and not-found from the HTTP status.
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
			withGithubRowAt(t, b.Listener.Addr().String())
			useConnectorClient(t, b)
			t.Setenv(tokenCredentialEnv, "ghp_host")
			calls := countGH(t)

			slug, err := newClient(t, connectorRepo, testLabels).Probe()

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

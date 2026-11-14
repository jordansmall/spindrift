package nuget

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"spindrift.dev/launcher/internal/registryproxy"
	"spindrift.dev/launcher/internal/registryvocab"
)

// Lives here, not in registryproxy, because the core's tests stay on the
// synthetic Strategy and only this package supplies the real NuGet rows.
//
// The route credential is attached by the Forwarder only to requests that
// reach it, so a request that escapes the rewrite (the client following an
// absolute upstream URL straight to the feed) carries none and the upstream
// refuses it. Failing visibly is the property; succeeding with a credential
// would be a leak.
func TestEscapingRequestCarriesNoCredential(t *testing.T) {
	const (
		feed  = "/api/nuget/v3/repo"
		token = "s3kr1t"
	)
	type seen struct{ path, auth string }
	var (
		mu       sync.Mutex
		requests []seen
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, seen{r.URL.Path, r.Header.Get("Authorization")})
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		regBase := "http://" + r.Host + feed + "/registration-semver2/"
		if r.URL.Path == feed+"/registration-semver2/pkg/index.json" {
			// "registration" is a match-host link the registration row
			// leaves pointing upstream.
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{{
				"@id":          regBase + "pkg/index.json",
				"registration": regBase + "pkg/index.json",
			}}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"resources": []map[string]string{
			{"@id": regBase, "@type": "RegistrationsBaseUrl/3.6.0"},
		}})
	}))
	t.Cleanup(upstream.Close)
	upstreamURL, _ := url.Parse(upstream.URL)
	requestsTo := func(path string) (n int) {
		mu.Lock()
		defer mu.Unlock()
		for _, s := range requests {
			if s.path == path {
				n++
			}
		}
		return n
	}

	routes := registryproxy.AssignPrefixes([]registryproxy.Route{{
		MatchHost:        upstreamURL.Host,
		Upstream:         upstream.URL,
		Credential:       token,
		EnforcedPaths:    []string{feed},
		EnforcedSubtrees: []registryvocab.Subtree{{Ecosystem: nameNuGet, Path: feed}},
	}})
	handler, err := registryproxy.New(routes, Strategy.RewriteRows)
	if err != nil {
		t.Fatalf("registryproxy.New: %v", err)
	}
	proxy := httptest.NewServer(handler)
	t.Cleanup(proxy.Close)
	proxyURL, _ := url.Parse(proxy.URL)

	get := func(rawURL string) (int, string) {
		t.Helper()
		resp, err := http.Get(rawURL)
		if err != nil {
			t.Fatalf("GET %s: %v", rawURL, err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}

	// Through the proxy: the credential reaches the upstream and the resource
	// @id comes back pointing at the proxy, not the upstream.
	status, body := get(proxy.URL + "/" + routes[0].Prefix + feed + "/index.json")
	if status != http.StatusOK {
		t.Fatalf("index via proxy: status %d, body %q", status, body)
	}
	mu.Lock()
	first := requests[0]
	mu.Unlock()
	if first.auth != "Bearer "+token {
		t.Errorf("proxied request carried Authorization %q, want the route credential", first.auth)
	}
	if strings.Contains(body, upstreamURL.Host) || !strings.Contains(body, proxyURL.Host) {
		t.Errorf("index body not repointed at the proxy: %s", body)
	}

	// Escaping the rewrite: follow the proxied service index to a registration
	// document, take the link the row leaves pointing upstream, and request it
	// as the client would. It carries no credential and the upstream refuses it.
	var index struct {
		Resources []struct {
			ID string `json:"@id"`
		} `json:"resources"`
	}
	if err := json.Unmarshal([]byte(body), &index); err != nil || len(index.Resources) != 1 {
		t.Fatalf("decode proxied index: %v, body %s", err, body)
	}
	status, body = get(index.Resources[0].ID + "pkg/index.json")
	if status != http.StatusOK {
		t.Fatalf("registration via proxy: status %d, body %q", status, body)
	}
	var reg struct {
		Items []struct {
			ID           string `json:"@id"`
			Registration string `json:"registration"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(body), &reg); err != nil || len(reg.Items) != 1 {
		t.Fatalf("decode proxied registration: %v, body %s", err, body)
	}
	if !strings.Contains(reg.Items[0].ID, proxyURL.Host) {
		t.Errorf("registration @id not repointed at the proxy: %s", reg.Items[0].ID)
	}
	escaping := reg.Items[0].Registration
	if !strings.Contains(escaping, upstreamURL.Host) {
		t.Fatalf("registration link %q did not escape the rewrite, want a URL on %s", escaping, upstreamURL.Host)
	}
	status, _ = get(escaping)
	if status != http.StatusUnauthorized {
		t.Errorf("escaping request: status %d, want %d", status, http.StatusUnauthorized)
	}
	mu.Lock()
	last := requests[len(requests)-1]
	mu.Unlock()
	if last.path != feed+"/registration-semver2/pkg/index.json" || last.auth != "" {
		t.Errorf("escaping request reached upstream as %+v, want that path with no Authorization", last)
	}

	// Same host, outside the enforced set: the proxy refuses before dialling,
	// so the credential never leaves.
	status, _ = get(proxy.URL + "/" + routes[0].Prefix + "/elsewhere/secret")
	if status != http.StatusForbidden {
		t.Errorf("out-of-set request: status %d, want %d", status, http.StatusForbidden)
	}
	if n := requestsTo("/elsewhere/secret"); n != 0 {
		t.Errorf("upstream saw %d requests for the out-of-set path, want 0", n)
	}
}

package profileprobe

import (
	"net/http"
	"testing"

	"spindrift.dev/launcher/internal/hostprofile"
	"spindrift.dev/launcher/internal/registrydiscover"
)

func stub(res registrydiscover.ProbeResult) registrydiscover.Probe {
	return func(string) registrydiscover.ProbeResult { return res }
}

func TestFill(t *testing.T) {
	row, ok := hostprofile.Match("https://x.invalid/artifactory/api/npm/", nil)
	if !ok || row.AuthScheme == "" {
		t.Fatalf("artifactory profile row = %+v, ok=%v; want one with an auth scheme", row, ok)
	}

	tests := []struct {
		name        string
		url         string
		res         registrydiscover.ProbeResult
		wantScheme  string
		wantProfile string
	}{
		{
			name:        "declared path prefix on an unrelated hostname",
			url:         "https://repo.corp.example/artifactory/api/npm/npm-local/",
			wantScheme:  row.AuthScheme,
			wantProfile: row.Name,
		},
		{
			name:        "jfrog.io host",
			url:         "https://acme.jfrog.io/npm/",
			wantScheme:  row.AuthScheme,
			wantProfile: row.Name,
		},
		{
			name:        "probe header only",
			url:         "https://repo.corp.example/npm/",
			res:         registrydiscover.ProbeResult{Header: http.Header{"X-Artifactory-Id": {"abc"}}},
			wantScheme:  row.AuthScheme,
			wantProfile: row.Name,
		},
		{
			name:       "real probe scheme wins over the profile",
			url:        "https://acme.jfrog.io/npm/",
			res:        registrydiscover.ProbeResult{Scheme: "basic"},
			wantScheme: "basic",
		},
		{
			name:       "real bearer probe scheme on an artifactory host",
			url:        "https://acme.jfrog.io/npm/",
			res:        registrydiscover.ProbeResult{Scheme: "bearer"},
			wantScheme: "bearer",
		},
		{
			name: "unprofiled host stays silent",
			url:  "https://repo.corp.example/npm/",
		},
		{
			name: "public profile has no scheme to fill",
			url:  "https://registry.npmjs.org/",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Fill(stub(tt.res))(tt.url)
			if got.Scheme != tt.wantScheme {
				t.Errorf("Scheme = %q, want %q", got.Scheme, tt.wantScheme)
			}
			if got.Profile != tt.wantProfile {
				t.Errorf("Profile = %q, want %q", got.Profile, tt.wantProfile)
			}
		})
	}
}

package profileprobe

import (
	"net/http"
	"reflect"
	"testing"

	"spindrift.dev/launcher/internal/hostprofile"
	"spindrift.dev/launcher/internal/registrydiscover"
)

func stub(res registrydiscover.ProbeResult) registrydiscover.Probe {
	return func(string) registrydiscover.ProbeResult { return res }
}

// adviceOf copies row's fields into a ProfileAdvice by name, so the tests do
// not repeat Fill's hand mapping.
func adviceOf(t *testing.T, row hostprofile.Row) registrydiscover.ProfileAdvice {
	t.Helper()
	var advice registrydiscover.ProfileAdvice
	dst, src := reflect.ValueOf(&advice).Elem(), reflect.ValueOf(row)
	for i := range dst.NumField() {
		name := dst.Type().Field(i).Name
		f := src.FieldByName(name)
		if !f.IsValid() || f.Type() != dst.Field(i).Type() {
			t.Fatalf("ProfileAdvice.%s has no same-typed field on hostprofile.Row", name)
		}
		dst.Field(i).Set(f)
	}
	return advice
}

// TestFillCopiesEveryProfileAdviceField fails when ProfileAdvice gains a field
// Fill leaves unset. The artifactory row populates every advice field, so a
// zero field in Fill's output is a field Fill dropped.
func TestFillCopiesEveryProfileAdviceField(t *testing.T) {
	const url = "https://acme.jfrog.io/npm/"
	row, ok := hostprofile.Match(url, nil)
	if !ok {
		t.Fatal("no artifactory profile row")
	}
	want := adviceOf(t, row)
	got := Fill(stub(registrydiscover.ProbeResult{}))(url).Profile
	gv, wv := reflect.ValueOf(got), reflect.ValueOf(want)
	for i := range gv.NumField() {
		name := gv.Type().Field(i).Name
		if wv.Field(i).IsZero() {
			t.Errorf("artifactory row leaves ProfileAdvice.%s empty; the field is untested", name)
		}
		if !reflect.DeepEqual(gv.Field(i).Interface(), wv.Field(i).Interface()) {
			t.Errorf("Fill ProfileAdvice.%s = %v, want %v", name, gv.Field(i), wv.Field(i))
		}
	}
}

func TestFill(t *testing.T) {
	row, ok := hostprofile.Match("https://x.invalid/artifactory/api/npm/", nil)
	if !ok || row.AuthScheme == "" {
		t.Fatalf("artifactory profile row = %+v, ok=%v; want one with an auth scheme", row, ok)
	}
	public, ok := hostprofile.Match("https://registry.npmjs.org/", nil)
	if !ok || public.AuthScheme != "" {
		t.Fatalf("npmjs profile row = %+v, ok=%v; want a public one", public, ok)
	}

	tests := []struct {
		name            string
		url             string
		res             registrydiscover.ProbeResult
		wantScheme      string
		wantAdvice      registrydiscover.ProfileAdvice
		wantFromProfile bool
	}{
		{
			name:            "declared path prefix on an unrelated hostname",
			url:             "https://repo.corp.example/artifactory/api/npm/npm-local/",
			wantScheme:      row.AuthScheme,
			wantAdvice:      adviceOf(t, row),
			wantFromProfile: true,
		},
		{
			name:            "jfrog.io host",
			url:             "https://acme.jfrog.io/npm/",
			wantScheme:      row.AuthScheme,
			wantAdvice:      adviceOf(t, row),
			wantFromProfile: true,
		},
		{
			name:            "probe header only",
			url:             "https://repo.corp.example/npm/",
			res:             registrydiscover.ProbeResult{Header: http.Header{"X-Artifactory-Id": {"abc"}}},
			wantScheme:      row.AuthScheme,
			wantAdvice:      adviceOf(t, row),
			wantFromProfile: true,
		},
		{
			name:       "real probe scheme wins over the profile but advice still arrives",
			url:        "https://acme.jfrog.io/npm/",
			res:        registrydiscover.ProbeResult{Scheme: "basic"},
			wantScheme: "basic",
			wantAdvice: adviceOf(t, row),
		},
		{
			name:       "real bearer probe scheme on an artifactory host",
			url:        "https://acme.jfrog.io/npm/",
			res:        registrydiscover.ProbeResult{Scheme: "bearer"},
			wantScheme: "bearer",
			wantAdvice: adviceOf(t, row),
		},
		{
			name: "unprofiled host stays silent",
			url:  "https://repo.corp.example/npm/",
		},
		{
			name:       "public profile fills advice but no scheme",
			url:        "https://registry.npmjs.org/",
			wantAdvice: adviceOf(t, public),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Fill(stub(tt.res))(tt.url)
			if got.Scheme != tt.wantScheme {
				t.Errorf("Scheme = %q, want %q", got.Scheme, tt.wantScheme)
			}
			if !reflect.DeepEqual(got.Profile, tt.wantAdvice) {
				t.Errorf("Profile = %+v, want %+v", got.Profile, tt.wantAdvice)
			}
			if got.SchemeFromProfile != tt.wantFromProfile {
				t.Errorf("SchemeFromProfile = %v, want %v", got.SchemeFromProfile, tt.wantFromProfile)
			}
		})
	}
}

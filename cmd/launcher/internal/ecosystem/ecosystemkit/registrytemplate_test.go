package ecosystemkit

import (
	"net/url"
	"testing"

	"spindrift.dev/launcher/internal/registryvocab"
)

func TestSpliceTemplateRawPath(t *testing.T) {
	rc := registryvocab.RewriteContext{
		MatchHost: "registry.example.com",
		Forwarder: &url.URL{Scheme: "http", Host: "127.0.0.1:9999"},
		Prefix:    "r0",
	}
	markers := []string{"{"}

	cases := []struct {
		name        string
		value       string
		wantTo      string
		wantLearned string
	}{
		{
			name:        "braces kept literal and learned path narrowed above the first brace",
			value:       "https://registry.example.com/v3-flatcontainer/{lower_id}/{lower_version}/readme",
			wantTo:      "http://127.0.0.1:9999/r0/v3-flatcontainer/{lower_id}/{lower_version}/readme",
			wantLearned: "/v3-flatcontainer",
		},
		{
			name:        "query and fragment and bare bytes verbatim, other escapes unnormalised",
			value:       "https://registry.example.com/a%2Fb/{id}/x y?q={id}&r=%7e#f{g}",
			wantTo:      "http://127.0.0.1:9999/r0/a%2Fb/{id}/x y?q={id}&r=%7e#f{g}",
			wantLearned: "/a/b",
		},
		{
			name:        "template at the root learns slash",
			value:       "https://registry.example.com/{id}",
			wantTo:      "http://127.0.0.1:9999/r0/{id}",
			wantLearned: "/",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			edit, ok := RepointRegistryURL(tc.value, rc)
			if !ok {
				t.Fatalf("RepointRegistryURL declined %q", tc.value)
			}
			got := SpliceTemplateRawPath(tc.value, edit, rc, markers)
			if got.To != tc.wantTo {
				t.Errorf("To = %q, want %q", got.To, tc.wantTo)
			}
			if got.LearnedPath != tc.wantLearned {
				t.Errorf("LearnedPath = %q, want %q", got.LearnedPath, tc.wantLearned)
			}
			if got.From != tc.value {
				t.Errorf("From = %q, want %q", got.From, tc.value)
			}
		})
	}

	t.Run("no marker in the path leaves the edit unchanged", func(t *testing.T) {
		v := "https://registry.example.com/v3/index.json?x={y}"
		edit, _ := RepointRegistryURL(v, rc)
		if got := SpliceTemplateRawPath(v, edit, rc, markers); got != edit {
			t.Fatalf("edit changed: %+v -> %+v", edit, got)
		}
	})

	t.Run("declined foreign host stays declined", func(t *testing.T) {
		v := "https://other.example.com/{id}"
		edit, ok := RepointRegistryURL(v, rc)
		if !ok || edit.To != "" {
			t.Fatalf("setup: want declined edit, got %+v ok=%v", edit, ok)
		}
		if got := SpliceTemplateRawPath(v, edit, rc, markers); got != edit {
			t.Fatalf("declined edit changed: %+v", got)
		}
	})
}

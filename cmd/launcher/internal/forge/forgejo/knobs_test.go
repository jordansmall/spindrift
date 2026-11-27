package forgejo_test

import (
	"testing"

	"spindrift.dev/launcher/internal/forge/forgejo"
)

func lookupFrom(m map[string]string) forgejo.Lookup {
	return func(env string) (string, bool) {
		v, ok := m[env]
		return v, ok
	}
}

func TestReadKnobs_UnsetYieldsDefaults(t *testing.T) {
	t.Parallel()
	k, err := forgejo.ReadKnobs(lookupFrom(nil))
	if err != nil {
		t.Fatalf("ReadKnobs: %v", err)
	}
	if k.ForgejoBaseURL != "https://codeberg.org" {
		t.Errorf("ForgejoBaseURL = %q, want the row default", k.ForgejoBaseURL)
	}
	if k.ForgejoToken != "" {
		t.Errorf("token = %q, want empty", k.ForgejoToken)
	}
}

func TestReadKnobs_SetYieldsValues(t *testing.T) {
	t.Parallel()
	k, err := forgejo.ReadKnobs(lookupFrom(map[string]string{
		"FORGEJO_BASE_URL": "https://git.example.test",
		"FORGEJO_TOKEN":    "tok",
	}))
	if err != nil {
		t.Fatalf("ReadKnobs: %v", err)
	}
	want := forgejo.Knobs{
		ForgejoBaseURL: "https://git.example.test",
		ForgejoToken:   "tok",
	}
	if k != want {
		t.Errorf("ReadKnobs = %+v, want %+v", k, want)
	}
}

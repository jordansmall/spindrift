package promptassembly

import "testing"

// The version-skew fallbacks derive from the backend rows; this pins the
// values the hard-coded switches produced, for every shipped backend.
func TestSkewFallbacksMatchShippedBackends(t *testing.T) {
	trackers := []struct {
		name               string
		read, write, filer string
	}{
		{"", "GITHUB", "GITHUB", "GH"},
		{"github", "GITHUB", "GITHUB", "GH"},
		{"jira", "GITHUB", "GITHUB", "GH"},
		{"local", "LOCAL", "", "GH"},
		{"forgejo", "FORGEJO", "FORGEJO", "FORGEJO"},
		{"unknown", "GITHUB", "GITHUB", "GH"},
	}
	for _, c := range trackers {
		r, w, f := issueTrackerAxisFallback(c.name)
		if r != c.read || w != c.write || f != c.filer {
			t.Errorf("issueTrackerAxisFallback(%q) = %q,%q,%q; want %q,%q,%q", c.name, r, w, f, c.read, c.write, c.filer)
		}
	}
	forges := map[string]string{"": "GH", "github": "GH", "git": "GH", "local": "GH", "forgejo": "FORGEJO"}
	for forge, want := range forges {
		if got := forgeSignal(Env{CodeForge: forge}); got != want {
			t.Errorf("forgeSignal(CodeForge=%q) = %q; want %q", forge, got, want)
		}
	}
	if got := forgeSignal(Env{CodeForge: "forgejo", ForgeBackend: "GH"}); got != "GH" {
		t.Errorf("pre-resolved ForgeBackend must win; got %q", got)
	}
}

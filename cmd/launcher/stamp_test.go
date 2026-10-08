package main

import (
	"maps"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/inputdoc"
)

func TestDispatchStamp_KnobsCarryEffectiveValuesAndNeverSecrets(t *testing.T) {
	t.Cleanup(func() { loadedDoc = nil })
	loadedDoc = nil
	t.Setenv("BASE_BRANCH", "release")
	for _, s := range secretKnobs {
		t.Setenv(s.env, "s3cret-value")
	}

	st := dispatchStamp("")

	if got := st.Knobs["BASE_BRANCH"]; got != "release" {
		t.Errorf("Knobs[BASE_BRANCH] = %q, want release", got)
	}
	if len(st.Knobs) != len(schemaFlags) {
		t.Errorf("Knobs has %d entries, want one per schemaFlags (%d)", len(st.Knobs), len(schemaFlags))
	}
	for _, s := range secretKnobs {
		if v, ok := st.Knobs[s.env]; ok {
			t.Errorf("secret knob %s leaked into Knobs (%q)", s.env, v)
		}
	}
	for k, v := range st.Knobs {
		if v == "s3cret-value" {
			t.Errorf("Knobs[%s] carries a secret value", k)
		}
	}
}

func TestDispatchStamp_RevisionAndDriverVersion(t *testing.T) {
	t.Cleanup(func() { loadedDoc = nil })
	old := revision
	t.Cleanup(func() { revision = old })
	revision = "abc123"
	loadedDoc = &inputdoc.Document{Artifacts: map[string]string{"DRIVER_VERSION": "2.0.1"}}

	st := dispatchStamp("")
	if st.Revision != "abc123" || st.DriverVersion != "2.0.1" {
		t.Errorf("Revision=%q DriverVersion=%q, want abc123 / 2.0.1", st.Revision, st.DriverVersion)
	}
}

func TestStampRoleModels(t *testing.T) {
	baked := `{"reviewer":"opus","worker":"sonnet"}`
	tests := []struct {
		name   string
		doc    map[string]string
		model  string
		review string
		want   map[string]string
	}{
		{"absent artifact, MODEL only", nil, "m1", "", map[string]string{"main": "m1"}},
		{"malformed artifact falls back to empty roster", map[string]string{"ROLE_MODELS": "{nope"}, "m1", "", map[string]string{"main": "m1"}},
		{"baked roster plus main", map[string]string{"ROLE_MODELS": baked}, "m1", "", map[string]string{"main": "m1", "reviewer": "opus", "worker": "sonnet"}},
		{"REVIEW_MODEL overrides reviewer", map[string]string{"ROLE_MODELS": baked}, "m1", "haiku", map[string]string{"main": "m1", "reviewer": "haiku", "worker": "sonnet"}},
		{"MODEL wins over a baked main", map[string]string{"ROLE_MODELS": `{"main":"old"}`}, "m1", "", map[string]string{"main": "m1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Cleanup(func() { loadedDoc = nil })
			loadedDoc = &inputdoc.Document{Artifacts: tt.doc, Settings: map[string]string{}}
			t.Setenv("MODEL", tt.model)
			t.Setenv("REVIEW_MODEL", tt.review)
			got := stampRoleModels(tt.review)
			if !maps.Equal(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDispatchStamp_RedactsURLUserinfo(t *testing.T) {
	t.Cleanup(func() { loadedDoc = nil })
	loadedDoc = nil
	t.Setenv("CODE_FORGE_REMOTE_URL", "https://x-access-token:SECRET@github.com/o/r.git")
	t.Setenv("FORGEJO_BASE_URL", "https://TOKENONLY@example.com/o/r")
	t.Setenv("BASE_BRANCH", "release")

	st := dispatchStamp("")

	if got := st.Knobs["CODE_FORGE_REMOTE_URL"]; got != "https://github.com/o/r.git" {
		t.Errorf("CODE_FORGE_REMOTE_URL = %q, want userinfo stripped", got)
	}
	if got := st.Knobs["FORGEJO_BASE_URL"]; got != "https://example.com/o/r" {
		t.Errorf("FORGEJO_BASE_URL = %q, want a bare-username token stripped", got)
	}
	if got := st.Knobs["BASE_BRANCH"]; got != "release" {
		t.Errorf("plain value changed: BASE_BRANCH = %q", got)
	}
	for k, v := range st.Knobs {
		if strings.Contains(v, "SECRET") || strings.Contains(v, "TOKENONLY") {
			t.Errorf("Knobs[%s] = %q leaks userinfo", k, v)
		}
	}
}

func TestDispatchConfig_StampExcludesSecretsAndTracksReviewModel(t *testing.T) {
	t.Cleanup(func() { loadedDoc = nil })
	loadedDoc = nil
	for _, s := range secretKnobs {
		t.Setenv(s.env, "s3cret-value")
	}
	t.Setenv("BASE_BRANCH", "release")
	t.Setenv("REVIEW_MODEL", "haiku")

	cfg := dispatchConfig(config{}, nil, nil, nil, forge.Capabilities{})

	if got := cfg.Stamp.Knobs["BASE_BRANCH"]; got != "release" {
		t.Errorf("Stamp.Knobs[BASE_BRANCH] = %q, want release", got)
	}
	for _, s := range secretKnobs {
		if _, ok := cfg.Stamp.Knobs[s.env]; ok {
			t.Errorf("secret knob %s present in Stamp.Knobs", s.env)
		}
	}
	if cfg.ReviewModelOverride != "haiku" || cfg.Stamp.RoleModels["reviewer"] != cfg.ReviewModelOverride {
		t.Errorf("reviewer = %q, override = %q; want both haiku", cfg.Stamp.RoleModels["reviewer"], cfg.ReviewModelOverride)
	}
}

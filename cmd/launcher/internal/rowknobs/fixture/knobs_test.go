package fixture_test

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/rowknobs/fixture"
)

func lookupFrom(m map[string]string) fixture.Lookup {
	return func(env string) (string, bool) {
		v, ok := m[env]
		return v, ok
	}
}

func TestReadKnobs_UnsetYieldsDefaults(t *testing.T) {
	k, err := fixture.ReadKnobs(lookupFrom(nil))
	if err != nil {
		t.Fatalf("ReadKnobs: %v", err)
	}
	want := fixture.Knobs{
		FixtureName:    "fallback \"quoted\"",
		FixtureNote:    "",
		FixtureCount:   3,
		FixtureEnabled: true,
		FixtureLimit:   "10",
		FixtureBudget:  5,
		FixtureTier:    "",
		FixtureToken:   0,
		FixtureMode:    "safe",
	}
	if k != want {
		t.Errorf("ReadKnobs = %+v, want %+v", k, want)
	}
}

func TestReadKnobs_SetYieldsTypedValues(t *testing.T) {
	for _, enabled := range []string{"1", "true"} {
		k, err := fixture.ReadKnobs(lookupFrom(map[string]string{
			"FIXTURE_NAME":    "n",
			"FIXTURE_NOTE":    "note",
			"FIXTURE_COUNT":   "42",
			"FIXTURE_ENABLED": enabled,
			"FIXTURE_LIMIT":   "5",
			"FIXTURE_BUDGET":  "7",
			"FIXTURE_TIER":    "gold",
			"FIXTURE_TOKEN":   "9",
			"FIXTURE_MODE":    "fast",
		}))
		if err != nil {
			t.Fatalf("ReadKnobs(enabled=%q): %v", enabled, err)
		}
		want := fixture.Knobs{
			FixtureName:    "n",
			FixtureNote:    "note",
			FixtureCount:   42,
			FixtureEnabled: true,
			FixtureLimit:   "5",
			FixtureBudget:  7,
			FixtureTier:    "gold",
			FixtureToken:   9,
			FixtureMode:    "fast",
		}
		if k != want {
			t.Errorf("ReadKnobs(enabled=%q) = %+v, want %+v", enabled, k, want)
		}
	}
}

func TestReadKnobs_BoolFalse(t *testing.T) {
	// "" is set-but-empty: a bool's explicit off, as parseFlags encodes it.
	for _, v := range []string{"0", "false", ""} {
		k, err := fixture.ReadKnobs(lookupFrom(map[string]string{"FIXTURE_ENABLED": v}))
		if err != nil {
			t.Fatalf("ReadKnobs(%q): %v", v, err)
		}
		if k.FixtureEnabled {
			t.Errorf("FIXTURE_ENABLED=%q read as true", v)
		}
	}
}

func TestReadKnobs_MalformedValueNamesEnvVar(t *testing.T) {
	for _, tc := range []struct{ env, value string }{
		{"FIXTURE_COUNT", "three"},
		{"FIXTURE_ENABLED", "maybe"},
		{"FIXTURE_MODE", "slow"},
		{"FIXTURE_BUDGET", "lots"},
		{"FIXTURE_TIER", "bronze"},
		{"FIXTURE_TOKEN", "x"},
	} {
		t.Run(tc.env, func(t *testing.T) {
			_, err := fixture.ReadKnobs(lookupFrom(map[string]string{tc.env: tc.value}))
			if err == nil {
				t.Fatalf("ReadKnobs(%s=%q) succeeded, want an error", tc.env, tc.value)
			}
			if !strings.Contains(err.Error(), tc.env) {
				t.Errorf("error %q does not name %s", err, tc.env)
			}
		})
	}
}

func TestReadKnobs_EmptyValueFollowsEmptyIsSetting(t *testing.T) {
	k, err := fixture.ReadKnobs(lookupFrom(map[string]string{
		"FIXTURE_LIMIT":  "",
		"FIXTURE_BUDGET": "",
		"FIXTURE_TIER":   "",
		"FIXTURE_NAME":   "",
	}))
	if err != nil {
		t.Fatalf("ReadKnobs: %v", err)
	}
	if k.FixtureLimit != "" {
		t.Errorf("set-but-empty emptyDisables FixtureLimit = %q, want empty", k.FixtureLimit)
	}
	if k.FixtureBudget != 0 {
		t.Errorf("set-but-empty emptyDisables FixtureBudget = %d, want 0", k.FixtureBudget)
	}
	if k.FixtureTier != "" {
		t.Errorf("set-but-empty emptyDisables FixtureTier = %q, want empty", k.FixtureTier)
	}
	if want := "fallback \"quoted\""; k.FixtureName != want {
		t.Errorf("set-but-empty FixtureName = %q, want the default %q", k.FixtureName, want)
	}
}

func TestReadKnobs_SecretErrorOmitsValue(t *testing.T) {
	const secret = "hunter2-not-a-number"
	_, err := fixture.ReadKnobs(lookupFrom(map[string]string{"FIXTURE_TOKEN": secret}))
	if err == nil {
		t.Fatal("ReadKnobs with a malformed secret int succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "FIXTURE_TOKEN") {
		t.Errorf("error %q does not name FIXTURE_TOKEN", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error %q leaks the secret value", err)
	}
	if want := "FIXTURE_TOKEN is not an integer"; err.Error() != want {
		t.Errorf("error = %q, want %q", err, want)
	}
}

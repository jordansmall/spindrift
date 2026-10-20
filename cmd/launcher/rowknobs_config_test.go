package main

import (
	"reflect"
	"strings"
	"testing"
)

// The launcher's config carries no field specific to one row (ADR 0068
// decision 3): row-owned knobs travel in rowKnobs, keyed by env name, and the
// row packages read them through their generated ReadKnobs. The row-owned set
// is the rowKnobs keys loadSchemaConfig fills, so a knob a row gains later is
// checked without editing this test.
func TestSchemaConfigHasNoRowKnobFields(t *testing.T) {
	rowOwned := loadSchemaConfig().rowKnobs
	if len(rowOwned) == 0 {
		t.Fatal("loadSchemaConfig fills no rowKnobs; the guard has nothing to check")
	}
	typ := reflect.TypeOf(schemaConfig{})
	if _, ok := typ.FieldByName("rowKnobs"); !ok {
		t.Error("schemaConfig has no rowKnobs map")
	}
	// Field names are camelCase, so fold case and underscores to match
	// initialisms (FORGEJO_BASE_URL against forgejoBaseURL).
	fold := func(s string) string { return strings.ToLower(strings.ReplaceAll(s, "_", "")) }
	owned := make(map[string]string, len(rowOwned))
	for env := range rowOwned {
		owned[fold(env)] = env
	}
	for i := 0; i < typ.NumField(); i++ {
		if env, ok := owned[fold(typ.Field(i).Name)]; ok {
			t.Errorf("schemaConfig has field %s, which the row knob %s owns", typ.Field(i).Name, env)
		}
	}
	if _, ok := owned[fold("forgejoBaseURL")]; !ok {
		t.Error("derived row-knob set does not cover FORGEJO_BASE_URL, so a forgejoBaseURL field would go unnoticed")
	}
}

func TestLoadSchemaConfigFillsRowKnobsWithDefaults(t *testing.T) {
	for _, env := range []string{"FORGEJO_BASE_URL", "FORGEJO_TOKEN", "JIRA_INCLUDE_COMMENTS", "LOCAL_ISSUES_DIR"} {
		t.Setenv(env, "")
	}
	t.Setenv("JIRA_TOKEN", "jt")
	sc := loadSchemaConfig()
	if got := sc.rowKnobs["FORGEJO_BASE_URL"]; got != "https://codeberg.org" {
		t.Errorf("FORGEJO_BASE_URL = %q, want the schema default", got)
	}
	if got := sc.rowKnobs["JIRA_TOKEN"]; got != "jt" {
		t.Errorf("JIRA_TOKEN = %q, want it read from the environment", got)
	}
	if got := sc.rowKnobs["JIRA_INCLUDE_COMMENTS"]; got != "" {
		t.Errorf("JIRA_INCLUDE_COMMENTS = %q, want empty", got)
	}
	if v, ok := (config{schemaConfig: sc}).knobLookup("LOCAL_ISSUES_DIR"); !ok || v == "" {
		t.Errorf("LOCAL_ISSUES_DIR = %q, %v, want the schema default, set", v, ok)
	}
	if _, ok := (config{schemaConfig: sc}).knobLookup("NOT_A_KNOB"); ok {
		t.Error("knobLookup reported an unknown env as set")
	}
}

// boolKnob renders a bool for a presence-style row knob: any non-empty value
// is on, "" is off.
func boolKnob(on bool) string {
	if on {
		return "1"
	}
	return ""
}

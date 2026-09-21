package inputdoc

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoad_ParsesSettingsAndArtifacts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "input.json")
	body := `{"settings":{"BASE_BRANCH":"develop"},"artifacts":{"IMAGE_TAG":"spindrift:abc"}}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	doc, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if doc.Settings["BASE_BRANCH"] != "develop" {
		t.Errorf("Settings[BASE_BRANCH] = %q, want develop", doc.Settings["BASE_BRANCH"])
	}
	if doc.Artifacts["IMAGE_TAG"] != "spindrift:abc" {
		t.Errorf("Artifacts[IMAGE_TAG] = %q, want spindrift:abc", doc.Artifacts["IMAGE_TAG"])
	}
}

func TestLoad_MissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope.json")
	_, err := Load(path)
	if err == nil {
		t.Fatal("want error for missing input document, got nil")
	}
	want := "read input document " + path + ": "
	if !strings.HasPrefix(err.Error(), want) {
		t.Errorf("error = %q, want prefix %q", err.Error(), want)
	}
}

func TestLoad_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "input.json")
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("want error for invalid JSON, got nil")
	}
	want := "parse input document " + path + ": "
	if !strings.HasPrefix(err.Error(), want) {
		t.Errorf("error = %q, want prefix %q", err.Error(), want)
	}
}

// The Lookup/Resolve tests below key on SPINDRIFT_TEST_KNOB rather than a
// real schema knob, and blank it with t.Setenv even where they want no
// ambient value: these helpers read the process environment, so a knob name
// the surrounding harness might also export would make the result depend on
// who ran the test.
func TestLookup_AmbientEnvWinsWithWarningWhenDocumentAlsoHasValue(t *testing.T) {
	t.Setenv("SPINDRIFT_TEST_KNOB", "from-env")
	doc := &Document{Settings: map[string]string{"SPINDRIFT_TEST_KNOB": "from-doc"}}

	var buf bytes.Buffer
	v, ok := doc.Lookup("SPINDRIFT_TEST_KNOB", &buf)
	if !ok || v != "from-env" {
		t.Errorf("Lookup = (%q, %v), want (from-env, true)", v, ok)
	}
	want := "SPINDRIFT_TEST_KNOB=from-env set in environment — knob env overrides are deprecated; use the --input document's settings.SPINDRIFT_TEST_KNOB\n"
	if buf.String() != want {
		t.Errorf("warning = %q, want %q", buf.String(), want)
	}
}

func TestLookup_AmbientEnvWinsNoWarningWhenDocumentHasNoValue(t *testing.T) {
	t.Setenv("SPINDRIFT_TEST_KNOB", "from-env")
	doc := &Document{Settings: map[string]string{}}

	var buf bytes.Buffer
	v, ok := doc.Lookup("SPINDRIFT_TEST_KNOB", &buf)
	if !ok || v != "from-env" {
		t.Errorf("Lookup = (%q, %v), want (from-env, true)", v, ok)
	}
	if buf.String() != "" {
		t.Errorf("warning = %q, want empty when document carries no value", buf.String())
	}
}

func TestLookup_DocumentWinsWhenEnvUnset(t *testing.T) {
	t.Setenv("SPINDRIFT_TEST_KNOB", "")
	doc := &Document{Settings: map[string]string{"SPINDRIFT_TEST_KNOB": "from-doc"}}

	var buf bytes.Buffer
	v, ok := doc.Lookup("SPINDRIFT_TEST_KNOB", &buf)
	if !ok || v != "from-doc" {
		t.Errorf("Lookup = (%q, %v), want (from-doc, true)", v, ok)
	}
	if buf.String() != "" {
		t.Errorf("warning = %q, want empty", buf.String())
	}
}

func TestLookup_NilReceiver(t *testing.T) {
	t.Setenv("SPINDRIFT_TEST_KNOB", "")
	var doc *Document

	var buf bytes.Buffer
	v, ok := doc.Lookup("SPINDRIFT_TEST_KNOB", &buf)
	if ok || v != "" {
		t.Errorf("Lookup = (%q, %v), want (\"\", false) for nil receiver with no env", v, ok)
	}

	t.Setenv("SPINDRIFT_TEST_KNOB", "from-env")
	v, ok = doc.Lookup("SPINDRIFT_TEST_KNOB", &buf)
	if !ok || v != "from-env" {
		t.Errorf("Lookup = (%q, %v), want (from-env, true) for nil receiver with env set", v, ok)
	}
}

func TestResolve_AbsentFromBoth(t *testing.T) {
	t.Setenv("SPINDRIFT_TEST_KNOB", "")
	doc := &Document{}
	var buf bytes.Buffer
	_, err := doc.Resolve("SPINDRIFT_TEST_KNOB", &buf)
	if err == nil {
		t.Fatal("want error when absent from both, got nil")
	}
	want := "no value for SPINDRIFT_TEST_KNOB (not in environment or --input document settings)"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestResolve_Found(t *testing.T) {
	t.Setenv("SPINDRIFT_TEST_KNOB", "")
	doc := &Document{Settings: map[string]string{"SPINDRIFT_TEST_KNOB": "from-doc"}}
	var buf bytes.Buffer
	v, err := doc.Resolve("SPINDRIFT_TEST_KNOB", &buf)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if v != "from-doc" {
		t.Errorf("Resolve = %q, want from-doc", v)
	}
}

func TestResolveOptional_AbsentFromBoth(t *testing.T) {
	t.Setenv("SPINDRIFT_TEST_KNOB", "")
	doc := &Document{}
	var buf bytes.Buffer
	v := doc.ResolveOptional("SPINDRIFT_TEST_KNOB", &buf)
	if v != "" {
		t.Errorf("ResolveOptional = %q, want empty", v)
	}
}

func TestResolveOptional_Found(t *testing.T) {
	t.Setenv("SPINDRIFT_TEST_KNOB", "")
	doc := &Document{Settings: map[string]string{"SPINDRIFT_TEST_KNOB": "from-doc"}}
	var buf bytes.Buffer
	v := doc.ResolveOptional("SPINDRIFT_TEST_KNOB", &buf)
	if v != "from-doc" {
		t.Errorf("ResolveOptional = %q, want from-doc", v)
	}
}

func TestParseInt(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		min     int
		want    int
		wantErr string
	}{
		{name: "happy path", raw: "5", min: 1, want: 5},
		{name: "unparsable", raw: "abc", min: 1, wantErr: `KNOB must be a positive integer, got "abc"`},
		{name: "below min", raw: "0", min: 1, wantErr: `KNOB must be a positive integer, got 0`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseInt("KNOB", "positive integer", tt.raw, tt.min)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Errorf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseInt: %v", err)
			}
			if got != tt.want {
				t.Errorf("ParseInt = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestParseDuration(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		min     time.Duration
		want    time.Duration
		wantErr string
	}{
		{name: "happy path", raw: "5s", min: time.Second, want: 5 * time.Second},
		{name: "unparsable", raw: "abc", min: time.Second, wantErr: `KNOB must be a positive duration, got "abc"`},
		{name: "below min", raw: "500ms", min: time.Second, wantErr: `KNOB must be a positive duration, got 500ms`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseDuration("KNOB", "positive duration", tt.raw, tt.min)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Errorf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseDuration: %v", err)
			}
			if got != tt.want {
				t.Errorf("ParseDuration = %v, want %v", got, tt.want)
			}
		})
	}
}

package credresolver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGradlePropertiesValue_ResolvesConfiguredKey(t *testing.T) {
	content := []byte("myRepoPassword=s3kr3t\n")

	got, err := gradlePropertiesValue(content, "/some/gradle.properties", "myRepoPassword")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "s3kr3t" {
		t.Errorf("got %q, want %q", got, "s3kr3t")
	}
}

// The error names both the path and the key so a reader can tell a missing
// key from a missing file, which reports a "reading ... file" error instead.
func TestGradlePropertiesValue_MissingKeyIsError(t *testing.T) {
	content := []byte("otherKey=s3kr3t\n")
	const path = "/some/gradle.properties"
	const key = "missingKey"

	_, err := gradlePropertiesValue(content, path, key)
	if err == nil {
		t.Fatal("expected error for missing key, got nil")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("expected error to mention the path %q, got: %v", path, err)
	}
	if !strings.Contains(err.Error(), key) {
		t.Errorf("expected error to mention the key %q, got: %v", key, err)
	}
}

// Like every other file adapter, the "gradle-properties" dispatch must check
// that the file exists before it parses or runs the missing-key guard, so a
// missing file and a missing key stay distinguishable.
func TestNew_GradlePropertiesFormatMissingFileReportsReadingError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.properties")

	r := New(Config{FromFile: path, FileFormat: "gradle-properties", PropertyKey: "myRepoPassword"})
	_, err := r.Peek()
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
	if !strings.Contains(err.Error(), "reading") {
		t.Errorf("expected a \"reading ... file\" error, got: %v", err)
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("expected error to mention the path %q, got: %v", path, err)
	}
}

// Only the routes file can reach this format, so the error has to name the
// unset key and must not blame a scalar REGISTRY_PROXY_* knob.
func TestNew_GradlePropertiesFormatEmptyPropertyKeyIsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gradle.properties")
	if err := os.WriteFile(path, []byte("myRepoPassword=s3kr3t\n"), 0o600); err != nil {
		t.Fatalf("writing test gradle.properties file: %v", err)
	}

	r := New(Config{FromFile: path, FileFormat: "gradle-properties", PropertyKey: ""})
	_, err := r.Peek()
	if err == nil {
		t.Fatal("expected error for empty property key, got nil")
	}
	if !strings.Contains(err.Error(), "key") {
		t.Errorf("expected error to mention the missing key, got: %v", err)
	}
	if strings.Contains(err.Error(), "REGISTRY_PROXY") {
		t.Errorf("expected error not to name a scalar REGISTRY_PROXY_* knob, got: %v", err)
	}
}

// java.util.Properties skips "#" and "!" comment lines and blank lines, and
// accepts ":" as a separator alongside "=", so the parser must match it.
func TestGradlePropertiesValue_CommentsBlankLinesAndSeparatorsAreTolerated(t *testing.T) {
	content := []byte(
		"# a comment\n" +
			"! another comment\n" +
			"\n" +
			"myRepoPassword : s3kr3t\n",
	)

	got, err := gradlePropertiesValue(content, "/some/gradle.properties", "myRepoPassword")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "s3kr3t" {
		t.Errorf("got %q, want %q", got, "s3kr3t")
	}
}

// A matching key with an empty value must fail closed rather than resolve to
// an empty credential.
func TestGradlePropertiesValue_EmptyValueIsError(t *testing.T) {
	content := []byte("myRepoPassword=\n")
	const path = "/some/gradle.properties"
	const key = "myRepoPassword"

	_, err := gradlePropertiesValue(content, path, key)
	if err == nil {
		t.Fatal("expected error for empty value, got nil")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("expected error to mention the path %q, got: %v", path, err)
	}
	if !strings.Contains(err.Error(), key) {
		t.Errorf("expected error to mention the key %q, got: %v", key, err)
	}
}

// strings.TrimSpace strips only a leading or trailing "\r", so a "\r" in the
// middle of a value survives into the token and reaches the HTTP proxy's
// header-write path. The error must name the file, never the value.
func TestGradlePropertiesValue_EmbeddedCRIsError(t *testing.T) {
	content := []byte("myRepoPassword=s3kr3t\rX-Injected: evil\n")
	const path = "/some/gradle.properties"
	const key = "myRepoPassword"

	_, err := gradlePropertiesValue(content, path, key)
	if err == nil {
		t.Fatal("expected error for a value with an embedded CR, got nil")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("expected error to mention the path %q, got: %v", path, err)
	}
	if strings.Contains(err.Error(), "s3kr3t") {
		t.Errorf("expected error not to print the credential value, got: %v", err)
	}
}

func TestGradlePropertiesValue_FirstMatchWins(t *testing.T) {
	content := []byte(
		"myRepoPassword=first\n" +
			"myRepoPassword=second\n",
	)

	got, err := gradlePropertiesValue(content, "/some/gradle.properties", "myRepoPassword")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "first" {
		t.Errorf("got %q, want %q", got, "first")
	}
}

// java.util.Properties also accepts a bare "key value" line with no "=" or
// ":" at all, so the parser must resolve that form.
func TestGradlePropertiesValue_WhitespaceSeparatorIsTolerated(t *testing.T) {
	content := []byte("myRepoPassword s3kr3t\n")

	got, err := gradlePropertiesValue(content, "/some/gradle.properties", "myRepoPassword")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "s3kr3t" {
		t.Errorf("got %q, want %q", got, "s3kr3t")
	}
}

// java.util.Properties splits at the earliest of "=", ":", or whitespace, not
// at the first "=" or ":" anywhere in the line. A whitespace-separated line
// whose value contains ":" would otherwise split in the wrong place.
func TestSplitGradleProperty_ColonInsideWhitespaceSeparatedValueIsNotTheSplit(t *testing.T) {
	k, v, ok := splitGradleProperty("myRepoPassword abc:def")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if k != "myRepoPassword" {
		t.Errorf("got key %q, want %q", k, "myRepoPassword")
	}
	if v != "abc:def" {
		t.Errorf("got value %q, want %q", v, "abc:def")
	}
}

// These cases cover the remaining java.util.Properties split shapes. The last
// one pins that when "=" and ":" both appear with no whitespace before either,
// the earliest one splits the key.
func TestSplitGradleProperty_SeparatorForms(t *testing.T) {
	tests := []struct {
		name      string
		line      string
		wantKey   string
		wantValue string
	}{
		{name: "equals with surrounding whitespace", line: "key = value", wantKey: "key", wantValue: "value"},
		{name: "colon with surrounding whitespace", line: "key : value", wantKey: "key", wantValue: "value"},
		{name: "whitespace only", line: "key value", wantKey: "key", wantValue: "value"},
		{name: "equals inside a whitespace-separated value", line: "key x=y", wantKey: "key", wantValue: "x=y"},
		{name: "earliest separator wins", line: "a=b:c", wantKey: "a", wantValue: "b:c"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k, v, ok := splitGradleProperty(tt.line)
			if !ok {
				t.Fatal("expected ok=true")
			}
			if k != tt.wantKey {
				t.Errorf("got key %q, want %q", k, tt.wantKey)
			}
			// splitGradleProperty returns the raw value; gradlePropertiesValue
			// trims it, so trim here to assert what a caller actually sees.
			if got := strings.TrimSpace(v); got != tt.wantValue {
				t.Errorf("got value %q, want %q", got, tt.wantValue)
			}
		})
	}
}

// A line with no separator of any kind must not match as a bare key.
// splitGradleProperty reports ok=false, so the parser skips the line and
// reports the requested key missing.
func TestGradlePropertiesValue_NoSeparatorLineIsSkipped(t *testing.T) {
	content := []byte("myRepoPassword\n")
	const path = "/some/gradle.properties"
	const key = "myRepoPassword"

	_, err := gradlePropertiesValue(content, path, key)
	if err == nil {
		t.Fatal("expected error for a separator-less line, got nil")
	}
	if !strings.Contains(err.Error(), "has no property") {
		t.Errorf("expected a \"has no property\" error (the line skipped, never matched as key %q), got: %v", key, err)
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("expected error to mention the path %q, got: %v", path, err)
	}
	if !strings.Contains(err.Error(), key) {
		t.Errorf("expected error to mention the key %q, got: %v", key, err)
	}
}

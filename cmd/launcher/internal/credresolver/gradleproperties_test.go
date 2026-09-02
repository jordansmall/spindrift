package credresolver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGradlePropertiesValue_ResolvesConfiguredKey verifies that a
// gradle.properties file with a "key=value" line for the requested key
// resolves the value.
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

// TestGradlePropertiesValue_MissingKeyIsError verifies that a properties
// file with no line for the requested key fails closed with an error naming
// both the file path and the key that was looked for -- distinguishable
// from a missing file (a "reading ... file" error).
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

// TestNew_GradlePropertiesFormatMissingFileReportsReadingError verifies that
// New's "gradle-properties" dispatch reports a "reading ... file" error for
// a missing file -- like every other file adapter, the file-existence check
// must run before any format-specific parsing or the missing-key guard,
// distinguishing a missing file from a missing key.
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

// TestNew_GradlePropertiesFormatEmptyPropertyKeyIsError verifies that New's
// "gradle-properties" dispatch fails closed, naming the route-flavored
// reason ("key" is unset), when the credential's key is unset -- this
// format is only reachable from the routes file, so the error must not
// mention a scalar REGISTRY_PROXY_* knob name.
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

// TestGradlePropertiesValue_CommentsBlankLinesAndSeparatorsAreTolerated
// verifies that "#" and "!"-prefixed comment lines and blank lines are
// skipped, and that a ":" separator with surrounding whitespace resolves
// the same as "=" -- java.util.Properties accepts both.
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

// TestGradlePropertiesValue_EmptyValueIsError verifies that a matching key
// whose value is empty fails closed with an error naming the file and key,
// rather than resolving to an empty credential.
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

// TestGradlePropertiesValue_EmbeddedCRIsError verifies that a resolved value
// containing a mid-line "\r" fails closed -- strings.TrimSpace only strips a
// leading/trailing "\r", so a "\r" embedded earlier in the value survives
// into the returned token and would reach the HTTP proxy's header-write
// path. The error must name the file, never the value itself.
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

// TestGradlePropertiesValue_FirstMatchWins verifies that when a key appears
// more than once, the value from the first matching line is returned.
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

// TestGradlePropertiesValue_WhitespaceSeparatorIsTolerated verifies that a
// line with no "=" or ":" separator at all, just "key value" divided by
// whitespace, still resolves -- java.util.Properties accepts this form too.
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

// TestSplitGradleProperty_ColonInsideWhitespaceSeparatedValueIsNotTheSplit
// verifies that when a line's key and value are divided by whitespace (no
// "=" or ":" immediately after the key), a "=" or ":" appearing later,
// inside the value itself, is not mistaken for the key/value separator --
// java.util.Properties splits at the *earliest* of "=", ":", or whitespace,
// not whichever of "=" or ":" occurs first in the whole line.
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

// TestSplitGradleProperty_SeparatorForms verifies the remaining
// java.util.Properties key/value split shapes: "=" and ":" with surrounding
// whitespace, whitespace alone, a "="/":"-in-value case parallel to the
// colon-in-value bug this file also covers, and that when "=" and ":" both
// appear (with no whitespace before either), the earliest one splits the
// key -- not whichever of the two happens to be "=" or ":".
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
			// splitGradleProperty's value is raw (not-yet-trimmed) --
			// gradlePropertiesValue trims it, so trim here too to assert
			// the effective value a caller sees.
			if got := strings.TrimSpace(v); got != tt.wantValue {
				t.Errorf("got value %q, want %q", got, tt.wantValue)
			}
		})
	}
}

// TestGradlePropertiesValue_NoSeparatorLineIsSkipped verifies that a
// non-comment, non-blank line with no separator of any kind (neither "="/":"
// nor whitespace between a key and a value) is not treated as a key=value
// entry -- splitGradleProperty reports ok=false for it, so the line is
// skipped and the requested key is reported missing rather than matching
// the whole line as a bare key.
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

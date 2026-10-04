package promptassembly_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCompareOrUpdateTextDiffersFails(t *testing.T) {
	golden := writeTemp(t, "golden.txt", "golden content\n")

	err := compareOrUpdateText(golden, []byte("produced content\n"), false)

	if err == nil || !strings.Contains(err.Error(), "produced content") {
		t.Fatalf("err = %v, want a diff naming the produced content", err)
	}
	if got := readFile(t, golden); got != "golden content\n" {
		t.Errorf("golden changed to %q in compare mode", got)
	}
}

func TestCompareOrUpdateTextMatchPasses(t *testing.T) {
	golden := writeTemp(t, "golden.txt", "same content\n")
	if err := compareOrUpdateText(golden, []byte("same content\n"), false); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

func TestCompareOrUpdateTextTrailingNewlineIsBytePinned(t *testing.T) {
	golden := writeTemp(t, "golden.txt", "content\n")
	if err := compareOrUpdateText(golden, []byte("content"), false); err == nil {
		t.Fatal("err = nil, want a diff: a missing trailing newline is a byte difference")
	}
}

func TestCompareOrUpdateTextUpdateOverwrites(t *testing.T) {
	golden := writeTemp(t, "golden.txt", "stale golden content\n")

	if err := compareOrUpdateText(golden, []byte("fresh produced content\n"), true); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got := readFile(t, golden); got != "fresh produced content\n" {
		t.Errorf("golden = %q, want the produced content", got)
	}
}

func TestCompareOrUpdateTextMissingGoldenFails(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.txt")
	if err := compareOrUpdateText(missing, []byte("x"), false); err == nil {
		t.Fatal("err = nil, want an error for a missing golden")
	}
}

func TestCompareOrUpdateJSONDiffersFails(t *testing.T) {
	golden := writeTemp(t, "golden.json", `{"b": 1, "a": 2}`)

	err := compareOrUpdateJSON(golden, []byte(`{"b": 1, "a": 3}`), false)

	if err == nil {
		t.Fatal("err = nil, want a diff")
	}
	if got := readFile(t, golden); got != `{"b": 1, "a": 2}` {
		t.Errorf("golden changed to %q in compare mode", got)
	}
}

func TestCompareOrUpdateJSONIgnoresKeyOrderAndLayout(t *testing.T) {
	golden := writeTemp(t, "golden.json", "{\n  \"a\": 2,\n  \"b\": [1, 2]\n}\n")
	if err := compareOrUpdateJSON(golden, []byte(`{"b":[1,2],"a":2}`), false); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

func TestCompareOrUpdateJSONProjectionIgnoresOtherKeys(t *testing.T) {
	golden := writeTemp(t, "golden.json", `{"ReviewModel": "opus", "ReviewEffort": "high"}`)
	produced := `{"ReviewModel": "opus", "ReviewEffort": "high", "PromptFile": "/tmp/run-specific"}`
	if err := compareOrUpdateJSON(golden, []byte(produced), false, "ReviewModel", "ReviewEffort"); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

func TestCompareOrUpdateJSONUpdateWritesCanonicalProjection(t *testing.T) {
	golden := writeTemp(t, "golden.json", `{"ReviewModel": "stale-model", "ReviewEffort": "low", "PromptFile": "/tmp/stale"}`)
	produced := `{"ReviewModel": "opus", "ReviewEffort": "high", "PromptFile": "/tmp/fresh"}`

	if err := compareOrUpdateJSON(golden, []byte(produced), true, "ReviewModel", "ReviewEffort"); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}

	want := "{\n  \"ReviewEffort\": \"high\",\n  \"ReviewModel\": \"opus\"\n}\n"
	if got := readFile(t, golden); got != want {
		t.Errorf("golden = %q, want %q", got, want)
	}
}

// jq -S prints sorted keys, <>& unescaped, and an absent projected key as null.
func TestCanonicalJSONMatchesJqSortedOutput(t *testing.T) {
	got, err := canonicalJSON([]byte(`{"z":"a<b>&c","m":{"y":[],"x":{}},"n":1}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"m\": {\n    \"x\": {},\n    \"y\": []\n  },\n  \"n\": 1,\n  \"z\": \"a<b>&c\"\n}\n"
	if string(got) != want {
		t.Errorf("canonicalJSON =\n%s\nwant\n%s", got, want)
	}

	got, err = canonicalJSON([]byte(`{"A":"x"}`), []string{"A", "B"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "{\n  \"A\": \"x\",\n  \"B\": null\n}\n"; string(got) != want {
		t.Errorf("projection = %q, want %q", got, want)
	}
}

func TestRemoveGoldenIfUpdate(t *testing.T) {
	golden := writeTemp(t, "golden.json", "{}")

	if err := removeGoldenIfUpdate(golden, false); err == nil {
		t.Error("compare mode: err = nil, want a stray-golden error")
	}
	if err := removeGoldenIfUpdate(golden, true); err != nil {
		t.Fatalf("update mode: err = %v", err)
	}
	if _, err := os.Stat(golden); !os.IsNotExist(err) {
		t.Errorf("golden still on disk after update-mode removal: %v", err)
	}
	if err := removeGoldenIfUpdate(golden, false); err != nil {
		t.Errorf("compare mode, golden absent: err = %v, want nil", err)
	}
	if err := removeGoldenIfUpdate(golden, true); err != nil {
		t.Errorf("update mode, golden already absent: err = %v, want nil", err)
	}
}

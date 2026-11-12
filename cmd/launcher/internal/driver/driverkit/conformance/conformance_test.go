package conformance

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/driver/driverkit"
)

// failures embeds a testing.TB so a guard can run against it while the test
// asserts on what the guard reported, instead of failing itself.
type failures struct {
	testing.TB
	errs []string
}

func (f *failures) Errorf(format string, args ...any) {
	f.errs = append(f.errs, fmt.Sprintf(format, args...))
}

func writeCase(t *testing.T, root, name, body string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pass.jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCredentialGuardFailsOnPlantedCredential(t *testing.T) {
	root := t.TempDir()
	// Split so this file is itself clean under the guard.
	token := "gh" + "p_" + strings.Repeat("a", 36)
	writeCase(t, root, "leaky", `{"type":"text","text":"`+token+`"}`+"\n")

	f := &failures{TB: t}
	checkNoCredentials(f, root)
	if len(f.errs) == 0 {
		t.Fatal("guard reported nothing for a corpus holding a credential-shaped string")
	}
	if !strings.Contains(f.errs[0], "github-token") || !strings.Contains(f.errs[0], "scrub") {
		t.Errorf("failure %q should name the pattern and the scrubber", f.errs[0])
	}
	if strings.Contains(f.errs[0], token) {
		t.Errorf("failure %q leaks the credential", f.errs[0])
	}
}

func TestCredentialGuardPassesCleanCorpus(t *testing.T) {
	root := t.TempDir()
	writeCase(t, root, "clean", `{"type":"text","text":"hello"}`+"\n")

	f := &failures{TB: t}
	checkNoCredentials(f, root)
	if len(f.errs) != 0 {
		t.Fatalf("guard failed a clean corpus: %v", f.errs)
	}
}

func TestCaseDirsFailsWhenCorpusHasNoCase(t *testing.T) {
	f := &failures{TB: t}
	if got := caseDirs(f, t.TempDir()); len(got) != 0 {
		t.Fatalf("caseDirs = %v, want none", got)
	}
	if len(f.errs) == 0 {
		t.Fatal("an empty corpus directory must fail")
	}

	root := t.TempDir()
	writeCase(t, root, "one", "{}\n")
	f = &failures{TB: t}
	if got := caseDirs(f, root); len(got) != 1 || got[0] != "one" || len(f.errs) != 0 {
		t.Fatalf("caseDirs = %v, errs %v", got, f.errs)
	}
}

// markWrites is a filter whose output depends on how its input is chunked: it
// appends a "|" per Write call.
type markWrites struct{ w io.Writer }

func (m markWrites) Write(p []byte) (int, error) {
	if _, err := m.w.Write(append(append([]byte{}, p...), '|')); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (markWrites) Flush() error { return nil }

func streamCase(t *testing.T, golden string) (root string, d driverkit.Driver) {
	t.Helper()
	root = t.TempDir()
	writeCase(t, root, "c", "line one\nline two\n")
	if golden != "" {
		if err := os.WriteFile(filepath.Join(root, "c", "stream.golden.txt"), []byte(golden), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root, driverkit.New(driverkit.Strategy{})
}

func TestCheckStreamFailsWhenChunkingChangesTheOutput(t *testing.T) {
	root, _ := streamCase(t, "line one\nline two\n|")
	d := driverkit.New(driverkit.Strategy{NewStreamFilter: func(w io.Writer) driverkit.StreamFilter { return markWrites{w} }})
	f := &failures{TB: t}
	checkStream(f, replay{d: d, filtered: true}, filepath.Join(root, "c"))
	if len(f.errs) == 0 || !strings.Contains(f.errs[0], "differs from the single-write run") {
		t.Fatalf("errs = %v, want a chunking failure", f.errs)
	}
}

func TestCheckStreamFailsOnGoldenDrift(t *testing.T) {
	root, d := streamCase(t, "stale\n")
	f := &failures{TB: t}
	checkStream(f, replay{d: d, filtered: true}, filepath.Join(root, "c"))
	if len(f.errs) == 0 || !strings.Contains(f.errs[0], "stream.golden.txt") {
		t.Fatalf("errs = %v, want a golden mismatch", f.errs)
	}
}

func TestCheckStreamRejectsGoldenWithoutAFilter(t *testing.T) {
	root, d := streamCase(t, "line one\nline two\n")
	f := &failures{TB: t}
	checkStream(f, replay{d: d}, filepath.Join(root, "c"))
	if len(f.errs) != 1 || !strings.Contains(f.errs[0], "no such output") {
		t.Fatalf("errs = %v, want the stray golden reported", f.errs)
	}

	root, d = streamCase(t, "")
	f = &failures{TB: t}
	checkStream(f, replay{d: d}, filepath.Join(root, "c"))
	if len(f.errs) != 0 {
		t.Fatalf("a no-filter Driver with no golden failed: %v", f.errs)
	}
}

func TestCheckStreamReplaysStdoutRawInPreferenceToThePassLog(t *testing.T) {
	root, d := streamCase(t, "raw\n")
	if err := os.WriteFile(filepath.Join(root, "c", "stdout.raw"), []byte("raw\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &failures{TB: t}
	checkStream(f, replay{d: d, filtered: true}, filepath.Join(root, "c"))
	if len(f.errs) != 0 {
		t.Fatalf("stdout.raw was not the replayed input: %v", f.errs)
	}
}

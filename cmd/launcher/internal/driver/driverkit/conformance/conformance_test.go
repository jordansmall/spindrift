package conformance

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

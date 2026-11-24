package primaryrecord_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/hostpaths"
	"spindrift.dev/launcher/internal/primaryrecord"
)

func TestName_CarriesBackendAndSuffix(t *testing.T) {
	n := primaryrecord.Name("jira", ".log")
	if !strings.HasPrefix(n, "primary-jira-") || !strings.HasSuffix(n, ".log") {
		t.Errorf("Name = %q", n)
	}
	if got, want := primaryrecord.Name("github", ".log"), strings.Replace(n, "jira", "github", 1); got != want {
		t.Errorf("backends do not share one stamp: %q vs %q", got, want)
	}
}

func TestNote_AppendsOpAndError(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	boom := errors.New("boom")

	if got := primaryrecord.Note("jira", "search", boom); got != boom {
		t.Errorf("Note = %v, want the error unchanged", got)
	}
	if got := primaryrecord.Note("jira", "search", nil); got != nil {
		t.Errorf("Note(nil) = %v", got)
	}

	raw, err := os.ReadFile(filepath.Join(hostpaths.LogDir(dir), primaryrecord.Name("jira", ".log")))
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"op":"search","error":"boom"}` + "\n"; string(raw) != want {
		t.Errorf("log = %q, want %q", raw, want)
	}
}

// A record that cannot be written falls back to stderr, naming both the failed
// operation and why the record was lost.
func TestNote_UnwritableRecordNamesBothErrors(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	logDir := hostpaths.LogDir(dir)
	if err := os.MkdirAll(filepath.Dir(logDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logDir, nil, 0o644); err != nil { // a file where the log directory belongs
		t.Fatal(err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	readErr := errors.New("the read broke")

	got := primaryrecord.Note("github", "read an issue", readErr)

	os.Stderr = old
	_ = w.Close()
	raw, _ := io.ReadAll(r)
	if got != readErr {
		t.Errorf("Note = %v, want the read error unchanged", got)
	}
	for _, want := range []string{"read an issue", "the read broke", "not a directory"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("stderr %q lacks %q", raw, want)
		}
	}
}

// blockLogDir puts a regular file where the log directory belongs, so no
// record file under it can open.
func blockLogDir(t *testing.T, dir string) {
	t.Helper()
	logDir := hostpaths.LogDir(dir)
	if err := os.MkdirAll(filepath.Dir(logDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logDir, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestOpenFile_UnopenableReturnsNoWriterAndNamesThePath(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	blockLogDir(t, dir)

	w, closeFile, err := primaryrecord.OpenFile("x.log")
	if w != nil {
		t.Errorf("writer = %v, want nil: no failed open may substitute a discard", w)
	}
	if closeFile == nil {
		t.Error("close is nil; callers defer it")
	}
	if err == nil || !strings.Contains(err.Error(), hostpaths.LogDir(dir)) {
		t.Errorf("err = %v, want one naming %s", err, hostpaths.LogDir(dir))
	}
}

func TestAuditFile_OpensOnceAndAppends(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	w1, err := primaryrecord.AuditFile("jira")
	if err != nil {
		t.Fatal(err)
	}
	w2, err := primaryrecord.AuditFile("jira")
	if err != nil {
		t.Fatal(err)
	}
	if w1 != w2 {
		t.Error("a second call opened a second handle")
	}
	_, _ = io.WriteString(w1, "one\n")
	_, _ = io.WriteString(w2, "two\n")
	raw, err := os.ReadFile(filepath.Join(hostpaths.LogDir(dir), primaryrecord.Name("jira", ".audit.jsonl")))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "one\ntwo\n" {
		t.Errorf("audit file = %q", raw)
	}
}

func TestAuditFile_UnopenableNamesThePathAndIsNotCached(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	blockLogDir(t, dir)

	w, err := primaryrecord.AuditFile("jira")
	if w != nil {
		t.Errorf("writer = %v, want nil", w)
	}
	want := filepath.Join(hostpaths.LogDir(dir), primaryrecord.Name("jira", ".audit.jsonl"))
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want one naming %s", err, want)
	}

	if err := os.Remove(hostpaths.LogDir(dir)); err != nil {
		t.Fatal(err)
	}
	if _, err := primaryrecord.AuditFile("jira"); err != nil {
		t.Errorf("a later call after the cause is fixed = %v; the failure was cached", err)
	}
}

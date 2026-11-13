package nuget_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/bindregistry"
	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/ecosystem/nuget"
)

const committedNuGetConfig = `<?xml version="1.0" encoding="utf-8"?>
<configuration>
  <packageSources>
    <clear />
    <add key="artifactory" value="https://artifactory.example.test/api/nuget/v3/repo/index.json" />
    <add key="nuget.org" value="https://api.nuget.org/v3/index.json" />
  </packageSources>
  <packageSourceCredentials>
    <artifactory>
      <add key="Username" value="synth" />
      <add key="ClearTextPassword" value="not-a-secret" />
    </artifactory>
  </packageSourceCredentials>
  <packageSourceMapping>
    <packageSource key="artifactory">
      <package pattern="*" />
    </packageSource>
  </packageSourceMapping>
</configuration>
`

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	// gc.auto=0 keeps a detached `git gc` from racing t.TempDir's cleanup.
	full := append([]string{"-C", dir, "-c", "gc.auto=0", "-c", "maintenance.auto=false"}, args...)
	out, err := exec.Command("git", full...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}

func nugetRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitIn(t, dir, "init")
	gitIn(t, dir, "config", "user.email", "test@example.com")
	gitIn(t, dir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "nuget.config"), []byte(committedNuGetConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", "nuget.config")
	gitIn(t, dir, "commit", "-m", "add nuget.config")
	return dir
}

func applyNuGetBinding(t *testing.T, dir string) {
	t.Helper()
	row := bindregistry.BoundInTreeRow(dir, nuget.Strategy)
	rewrites := []bindregistry.HostRewrite{{UpstreamHost: "artifactory.example.test", LocalURL: "http://127.0.0.1:27182/r0"}}
	outcome, err := bindregistry.ApplyInTreeBinding(dir, row, rewrites)
	if err != nil {
		t.Fatalf("ApplyInTreeBinding: %v", err)
	}
	if outcome != bindregistry.ApplyApplied {
		t.Fatalf("outcome = %v, want %v", outcome, bindregistry.ApplyApplied)
	}
}

func TestRevert_RestoresTheCommittedNuGetConfigByteForByte(t *testing.T) {
	dir := nugetRepo(t)
	applyNuGetBinding(t, dir)

	rewritten, err := os.ReadFile(filepath.Join(dir, "nuget.config"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rewritten), `allowInsecureConnections="true"`) || strings.Contains(string(rewritten), "ClearTextPassword") {
		t.Fatalf("binding did not land:\n%s", rewritten)
	}
	if !strings.HasPrefix(gitIn(t, dir, "ls-files", "-v", "--", "nuget.config"), "S ") {
		t.Fatal("skip-worktree bit not set after apply")
	}

	var out bytes.Buffer
	rows := bindregistry.InTreeBindings([]ecosystem.Strategy{nuget.Strategy})
	if failed := bindregistry.RevertInTreeBindings(dir, rows, "who", &out); failed {
		t.Fatalf("RevertInTreeBindings failed: %s", out.String())
	}
	got, err := os.ReadFile(filepath.Join(dir, "nuget.config"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != committedNuGetConfig {
		t.Errorf("nuget.config after revert:\n%s\nwant the committed bytes", got)
	}
	if strings.HasPrefix(gitIn(t, dir, "ls-files", "-v", "--", "nuget.config"), "S ") {
		t.Error("skip-worktree bit still set after revert")
	}
	if want := "==> in-tree nuget config nuget.config restored and un-hidden from git\n"; out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
}

// A revert error surfaces through the shared RevertInTreeBindings line format,
// the same one npm's rows go through, and prints one line per row, not one
// per spelling.
func TestRevert_FailureSurfacesLikeNpms(t *testing.T) {
	dir := nugetRepo(t)
	// A file standing in for the work dir makes every stat of <dir>/nuget.config
	// fail with ENOTDIR, which is not "does not exist".
	notADir := filepath.Join(dir, "nuget.config")

	var out bytes.Buffer
	rows := bindregistry.InTreeBindings([]ecosystem.Strategy{nuget.Strategy})
	if failed := bindregistry.RevertInTreeBindings(notADir, rows, "who", &out); !failed {
		t.Fatal("failed = false, want true")
	}
	if got := strings.Count(out.String(), "who: revert in-tree "); got != 1 {
		t.Errorf("failure lines = %d, want 1: %q", got, out.String())
	}
}

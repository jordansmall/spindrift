//go:build integration

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// flakeLockNixpkgsRef returns the nixpkgs flake-ref pinned in the repo's own
// flake.lock, so the throwaway devShell below reuses a revision the build
// already has cached instead of triggering an unrelated fetch.
func flakeLockNixpkgsRef(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Skipf("resolve repo root: %v", err)
	}
	root := strings.TrimSpace(string(out))
	data, err := os.ReadFile(filepath.Join(root, "flake.lock"))
	if err != nil {
		t.Fatalf("read flake.lock: %v", err)
	}
	var lock struct {
		Nodes map[string]struct {
			Locked struct {
				Owner string `json:"owner"`
				Repo  string `json:"repo"`
				Rev   string `json:"rev"`
			} `json:"locked"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(data, &lock); err != nil {
		t.Fatalf("parse flake.lock: %v", err)
	}
	n, ok := lock.Nodes["nixpkgs"]
	if !ok || n.Locked.Rev == "" {
		t.Fatal("flake.lock: no nixpkgs node with a pinned rev")
	}
	return fmt.Sprintf("github:%s/%s/%s", n.Locked.Owner, n.Locked.Repo, n.Locked.Rev)
}

func runIn(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v: %s", name, strings.Join(args, " "), err, out)
	}
}

// chdir points the process cwd at dir and returns a restore func. buildCmd's
// `nix develop .#<name>` resolves the flake against the process cwd, so
// exercising a real devShell needs it on the throwaway flake this test writes,
// not the package dir.
func chdir(t *testing.T, dir string) func() {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	return func() {
		if err := os.Chdir(prev); err != nil {
			t.Fatalf("chdir back: %v", err)
		}
	}
}

// TestRunDevshellRealNixKeepsHarnessToolsReachable pins the invariant buildCmd
// and ADR 0014 rely on but no earlier test checked (issue #798): a real
// `nix develop` devShell naming neither git nor gh still leaves both reachable.
// Non-pure nix develop prepends the devShell's own paths onto the existing PATH,
// so the image's /bin bake survives at the tail, and the caller's PATH stands in.
func TestRunDevshellRealNixKeepsHarnessToolsReachable(t *testing.T) {
	for _, bin := range []string{"nix", "git", "gh"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not found on PATH", bin)
		}
	}

	nixpkgsRef := flakeLockNixpkgsRef(t)
	dir := t.TempDir()
	flake := fmt.Sprintf(`{
  inputs.nixpkgs.url = %q;
  outputs = { self, nixpkgs }:
    let
      forAllSystems = f: nixpkgs.lib.genAttrs
        [ "x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin" ] f;
    in {
      devShells = forAllSystems (system: {
        default = nixpkgs.legacyPackages.${system}.mkShellNoCC {
          shellHook = "export SPINDRIFT_TEST_DEVSHELL_MARKER=798";
        };
      });
    };
}
`, nixpkgsRef)
	if err := os.WriteFile(filepath.Join(dir, "flake.nix"), []byte(flake), 0o644); err != nil {
		t.Fatalf("write flake.nix: %v", err)
	}
	// nix only evaluates a local flake that's tracked by a git repo.
	runIn(t, dir, "git", "init", "-q")
	runIn(t, dir, "git", "add", "-A")

	// The shellHook marker distinguishes a real devShell entry from run()'s
	// relaunch-on-launch-failure fallback (run.go:59), which reruns the Driver
	// directly with no devShell and no marker if the wrap produces no output.
	// Without the check, a broken throwaway devShell would silently degrade to
	// that path and still pass.
	bin := writeFakeDriver(t, dir, "fake-driver", `set -e
[ "$SPINDRIFT_TEST_DEVSHELL_MARKER" = "798" ] || { echo "devshell not entered"; exit 1; }
command -v git >/dev/null || { echo "git not on PATH"; exit 1; }
command -v gh >/dev/null || { echo "gh not on PATH"; exit 1; }
echo harness-tools-reachable
`)

	restore := chdir(t, dir)
	defer restore()

	cfg := execConfig{
		driverBin:    bin,
		devshell:     true,
		devshellName: "default",
		logPath:      filepath.Join(dir, "stream.log"),
		heartbeatLog: filepath.Join(dir, "heartbeat.log"),
		issue:        "798",
	}
	var stdout bytes.Buffer
	rc, err := run(cfg, &stdout)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rc != 0 {
		t.Fatalf("exit code = %d, want 0 (stdout: %s)", rc, stdout.String())
	}
	if !strings.Contains(stdout.String(), "harness-tools-reachable") {
		t.Errorf("stdout = %q, want it to confirm git/gh reachable", stdout.String())
	}
}

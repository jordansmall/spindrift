package ecosystem_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/bindregistry"
	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/registrymanifest"
)

// Classify walks ecosystem.Table directly, so this test guards drift without a
// hand-mirrored map. It checks every lockfile name in a row, not just the
// first: the corpus classifies one lockfile per row, so a name added to a row
// is proven here.
func TestClassify_MatchesEcosystemTable(t *testing.T) {
	for _, row := range ecosystem.Table {
		t.Run(row.Name, func(t *testing.T) {
			if len(row.LockfileNames) == 0 {
				t.Fatalf("row %q has no lockfile names", row.Name)
			}
			for _, lockfile := range row.LockfileNames {
				t.Run(lockfile, func(t *testing.T) {
					dir := t.TempDir()
					if err := os.WriteFile(filepath.Join(dir, lockfile), nil, 0o644); err != nil {
						t.Fatalf("WriteFile: %v", err)
					}

					got := bindregistry.Classify(dir)
					if got != row.Classification {
						t.Errorf("Classify(%q) = %q, want %q", dir, got, row.Classification)
					}
				})
			}
		})
	}
}

// Table order is the precedence that agent/entrypoint.sh's old if/elif chain
// had (#2930); no corpus case holds two lockfiles at once. The npm family sits
// ahead of go, and cargo ahead of both.
func TestClassify_TableOrderDecidesPrecedence(t *testing.T) {
	cases := []struct {
		name  string
		files []string
		want  string
	}{
		{"cargo beats go", []string{"Cargo.lock", "go.sum"}, "cargo"},
		{"npm family beats go", []string{"package-lock.json", "go.sum"}, "npm/pnpm/yarn"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, name := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}
			}

			if got := bindregistry.Classify(dir); got != tc.want {
				t.Errorf("Classify(%q) = %q, want %q", dir, got, tc.want)
			}
		})
	}
}

// WarnStaleLockfiles scans ecosystem.Table: a tracked first lockfile of every
// row naming the Forwarder is reported under that row. The lockfile scan has no
// corpus stage, so this is the only proof the production wrapper passes the
// real rows.
func TestWarnStaleLockfiles_ScansEveryTableRow(t *testing.T) {
	m := registrymanifest.Manifest{Endpoint: registrymanifest.NewUnixEndpoint(filepath.Join(t.TempDir(), "p.sock"))}
	encoded, err := registrymanifest.Encode(m)
	if err != nil {
		t.Fatalf("registrymanifest.Encode: %v", err)
	}
	t.Setenv(registrymanifest.EnvVar, encoded)

	for _, row := range ecosystem.Table {
		t.Run(row.Name, func(t *testing.T) {
			dir := t.TempDir()
			runGit(t, dir, "init", "-q")
			lockfile := row.LockfileNames[0]
			content := "url = http://127.0.0.1:" + strconv.Itoa(bindregistry.ForwarderPort) + "/\n"
			if err := os.WriteFile(filepath.Join(dir, lockfile), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			runGit(t, dir, "add", lockfile)

			var out bytes.Buffer
			bindregistry.WarnStaleLockfiles(&out, dir)

			want := "==> WARNING: " + row.Name + " lockfile " + lockfile + " still names"
			if !strings.Contains(out.String(), want) {
				t.Errorf("output = %q, want it to contain %q", out.String(), want)
			}
		})
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

package ecosystem_test

import (
	"bytes"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/bindregistry"
	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/registrymanifest"
	"spindrift.dev/launcher/internal/registryvocab"
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

// Order is asserted, not just membership: the verb's apply/revert loop
// iterates InTreeBindings() in whatever order it returns, so npm/yarn/pnpm
// must match ecosystem.Table's own order (issue #3180, rows from issue
// #2933). cargo is absent by design (issue #3201), pinned by
// TestInTreeBindings_ExcludesCargo below.
func TestInTreeBindingTableHasExpectedRows(t *testing.T) {
	want := []struct {
		name       string
		configPath string
	}{
		{name: "npm", configPath: ".npmrc"},
		{name: "yarn", configPath: ".yarnrc.yml"},
		{name: "pnpm", configPath: "pnpm-workspace.yaml"},
	}

	got := bindregistry.InTreeBindings()
	if len(got) != len(want) {
		t.Fatalf("InTreeBindings() has %d rows, want exactly %d: %+v", len(got), len(want), got)
	}
	for i, tc := range want {
		if got[i].Name != tc.name || got[i].InTreeConfigPath != tc.configPath {
			t.Errorf("row %d = {Name: %q, InTreeConfigPath: %q}, want {Name: %q, InTreeConfigPath: %q}",
				i, got[i].Name, got[i].InTreeConfigPath, tc.name, tc.configPath)
		}
	}
}

// Cargo's ecosystem.Table row keeps a non-empty InTreeConfigPath for
// host-side discovery but carries a non-nil RepoAwareHomeConfig, so
// InTreeBindings' filter must exclude it (issue #3201). A regression would
// dual-write the in-tree rewrite and cargo's source-replacement home config,
// which its 1:1 URL to source-name rule cannot tolerate.
func TestInTreeBindings_ExcludesCargo(t *testing.T) {
	for _, row := range bindregistry.InTreeBindings() {
		if row.Name == "cargo" {
			t.Fatalf("InTreeBindings() = %+v, want it to never include the cargo row", bindregistry.InTreeBindings())
		}
	}
}

// bindGoRowSummary runs bindings mode over the go row alone against a ready
// Forwarder, the manifest naming route, and returns what the verb prints. The
// corpus binding stage calls the renderers directly, so the summary lines
// BindHomes prints around the GOPROXY export are pinned here.
func bindGoRowSummary(t *testing.T, route registrymanifest.Route) string {
	t.Helper()
	goRow, ok := tableRow("go")
	if !ok {
		t.Fatal("ecosystem.Table has no go row")
	}
	for _, v := range []string{"GOTOOLCHAIN", "GONOPROXY", "GOPRIVATE", "GOSUMDB", "GONOSUMDB"} {
		t.Setenv(v, "")
	}

	// AF_UNIX paths cap at 108 bytes, which t.TempDir() can exceed.
	sockDir, err := os.MkdirTemp("", "brsock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	socketPath := filepath.Join(sockDir, "registry-proxy.sock")
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen(unix): %v", err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()

	encoded, err := registrymanifest.Encode(registrymanifest.Manifest{
		Endpoint: registrymanifest.NewUnixEndpoint(socketPath),
		Routes:   []registrymanifest.Route{route},
	})
	if err != nil {
		t.Fatalf("registrymanifest.Encode: %v", err)
	}
	t.Setenv(registrymanifest.EnvVar, encoded)

	out := new(bytes.Buffer)
	gate := bindregistry.ResolveGate(out, bindregistry.GateDeps{
		Probe:        func(int) bool { return true },
		Spawn:        func(string, int) (int, error) { return 0, nil },
		LookPath:     func(string) (string, error) { return "/fake/bin/socat", nil },
		Timeout:      bindregistry.ForwarderReadyTimeout,
		PollInterval: bindregistry.ForwarderPollInterval,
	})
	if !bindregistry.BindHomes(out, []ecosystem.Row{goRow}, gate, nil) {
		t.Fatalf("BindHomes failed (output=%q)", out.String())
	}
	return out.String()
}

// The "go bound" line reads the computed GOPROXY export rather than
// re-deriving the URL, so a host-rooted route declaring a go path prints the
// full path (issue #3260).
func TestBindHomes_HostRootedDeclaredGoPathPrintsFullPathGoLine(t *testing.T) {
	out := bindGoRowSummary(t, registrymanifest.Route{
		Prefix:     "r0",
		Ecosystems: registryvocab.RouteEcosystems{"go": registryvocab.RouteDeclaration{registryvocab.RouteDeclarationPathKey: "/artifactory/api/go/go-local"}},
	})

	want := "==> go bound to it via GOPROXY=http://127.0.0.1:" + strconv.Itoa(bindregistry.ForwarderPort) + "/r0/artifactory/api/go/go-local"
	if !strings.Contains(out, want) {
		t.Errorf("output = %q, want it to contain %q", out, want)
	}
}

// No declared go path means no GOPROXY export, so no line claims the binding.
func TestBindHomes_HostRootedNoDeclaredGoPathOmitsGoLine(t *testing.T) {
	out := bindGoRowSummary(t, registrymanifest.Route{Prefix: "r0"})

	if strings.Contains(out, "go bound to it via GOPROXY=") {
		t.Errorf("output = %q, want it NOT to contain a go-bound-via-GOPROXY line (no go path declared)", out)
	}
}

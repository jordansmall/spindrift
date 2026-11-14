//go:build integration

package seamtest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/connectorforge/connectorforgetest"
	"spindrift.dev/launcher/internal/connectorsandbox"
	"spindrift.dev/launcher/internal/connectorsandbox/sandbox"
	"spindrift.dev/launcher/internal/driver/driverkit/conformance"
	"spindrift.dev/launcher/internal/fixturebuild"
	"spindrift.dev/launcher/internal/forge/forgetest"
	"spindrift.dev/launcher/internal/inputdoc"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/runner/bwrap"
	"spindrift.dev/launcher/internal/runner/oci"
)

// sandboxFamily is one runner family that starts a Connector sandbox against
// its fake, which runs the real connector-host from the sandbox argv.
type sandboxFamily struct {
	name string
	// start returns a sandbox on the family's real runner, the file its fake
	// records argvs to, and, when refuse is set, a fake that refuses to start it.
	start func(t *testing.T, refuse string) (*sandbox.Sandbox, string)
	// starts counts the sandbox starts in the fake's record.
	starts func(record [][]string) int
}

var sandboxFamilies = []sandboxFamily{
	{"podman", podmanSandbox, func(record [][]string) int {
		n := 0
		for _, argv := range record {
			if argv[0] == "run" {
				n++
			}
		}
		return n
	}},
	{"bwrap", bwrapSandbox, func(record [][]string) int {
		n := 0
		for _, argv := range record {
			if _, ok := bwrapSandboxHost(argv); ok {
				n++
			}
		}
		return n
	}},
}

func podmanSandbox(t *testing.T, refuse string) (*sandbox.Sandbox, string) {
	t.Helper()
	rec := UsePodman(t, PodmanConfig{ImagePresent: true, RefuseSandbox: refuse})
	art := inputdoc.OCIArtifacts{
		ImageTag:                 inputdoc.ValueOf("spindrift:test"),
		ConnectorSandboxImageTag: inputdoc.ValueOf("spindrift-connector-sandbox:test"),
	}
	r, err := oci.New(runner.Config{Runtime: "podman"}, art, func(string) (string, bool) { return "", false }, "")
	if err != nil {
		t.Fatal(err)
	}
	return newSandbox(t, r, "podman"), rec
}

// bwrapSandbox's connector-host path is a placeholder directory: the fake
// execs its own configured host, so the adapter only needs the path to exist.
func bwrapSandbox(t *testing.T, refuse string) (*sandbox.Sandbox, string) {
	t.Helper()
	rec := UseBwrap(t, BwrapConfig{RefuseSandbox: refuse})
	filter := filepath.Join(t.TempDir(), "seccomp.bpf")
	if err := os.WriteFile(filter, []byte("filter"), 0o644); err != nil {
		t.Fatal(err)
	}
	art := inputdoc.BwrapArtifacts{
		ConnectorHost: inputdoc.ValueOf(t.TempDir()),
		SyscallFilter: inputdoc.ValueOf(filter),
	}
	r, err := bwrap.New(runner.Config{NetworkMode: runner.NetworkModeHost}, art, func(string) (string, bool) { return "", false }, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return newSandbox(t, r, "bwrap"), rec
}

func newSandbox(t *testing.T, r runner.Runner, label string) *sandbox.Sandbox {
	t.Helper()
	sb := sandbox.New(r, label)
	t.Cleanup(func() { _ = sb.Close(context.Background()) })
	return sb
}

// eachSandbox runs f as a subtest per runner family.
func eachSandbox(t *testing.T, f func(t *testing.T, fam sandboxFamily)) {
	for _, fam := range sandboxFamilies {
		t.Run(fam.name, func(t *testing.T) { f(t, fam) })
	}
}

func TestStrategyCorpus_Sandbox(t *testing.T) {
	dir, err := fixturebuild.ConnectorDir()
	if err != nil {
		t.Fatal(err)
	}
	// UseBwrap changes directory, so the corpus path must not be relative.
	corpus, err := filepath.Abs(filepath.Join("testdata", "corpus"))
	if err != nil {
		t.Fatal(err)
	}
	wasm, err := os.ReadFile(filepath.Join(dir, "seamtest-driver.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	eachSandbox(t, func(t *testing.T, fam sandboxFamily) {
		sb, _ := fam.start(t, "")
		c, err := sb.Load(context.Background(), wasm)
		if err != nil {
			t.Fatal(err)
		}
		s, err := c.DriverStrategy()
		if err != nil {
			t.Fatal(err)
		}
		conformance.Run(t, s, corpus)
	})
}

func TestComposedForgeCodeForgeContract_Sandbox(t *testing.T) {
	eachSandbox(t, func(t *testing.T, fam sandboxFamily) {
		sb, _ := fam.start(t, "")
		forgetest.RunCodeForgeContract(t, connectorforgetest.NewVia(t, sb.LoadNetwork))
	})
}

func TestComposedForgePRForgeContract_Sandbox(t *testing.T) {
	eachSandbox(t, func(t *testing.T, fam sandboxFamily) {
		sb, _ := fam.start(t, "")
		forgetest.RunPRForgeContract(t, connectorforgetest.NewPRVia(t, sb.LoadNetwork))
	})
}

func TestSandboxStartFailureNamesTheRunner(t *testing.T) {
	eachSandbox(t, func(t *testing.T, fam sandboxFamily) {
		sb, rec := fam.start(t, "Error: sandbox refused by fake")
		_, err := sb.Load(context.Background(), []byte("unused"))
		if !errors.Is(err, connectorsandbox.ErrConnectorSandboxStart) {
			t.Fatalf("Load error = %v; want ErrConnectorSandboxStart", err)
		}
		for _, want := range []string{fam.name, "sandbox refused by fake"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not name %q", err, want)
			}
		}
		if again := sb.Err(); !errors.Is(again, connectorsandbox.ErrConnectorSandboxStart) {
			t.Errorf("Err() = %v; want the start failure remembered", again)
		}
		if n := fam.starts(ReadRecord(t, rec)); n != 1 {
			t.Errorf("%s started %d sandboxes; want the one refused start", fam.name, n)
		}
	})
}

// A host that denies unprivileged user namespaces makes bwrap exit at once, so
// the load must fail with the named start error rather than wait out the hello
// timeout or fall back to loading the Connector in the Launcher.
func TestSandboxUsernsDeniedNamesTheRunner(t *testing.T) {
	const denied = "bwrap: No permissions to creating new namespace, likely because the kernel does not allow " +
		"non-privileged user namespaces. On e.g. debian this can be enabled with 'sysctl kernel.unprivileged_userns_clone=1'."
	sb, _ := bwrapSandbox(t, denied)
	began := time.Now()
	c, err := sb.Load(context.Background(), []byte("unused"))
	// The sandbox's own hello timeout is 30s, so a bound well under it tells
	// a prompt start failure from bwrap hanging until that timeout.
	if took := time.Since(began); took > 5*time.Second {
		t.Errorf("Load took %v; a refused start must not wait out the hello timeout", took)
	}
	if c != nil {
		t.Errorf("Load returned a Connector %v; want none loaded", c)
	}
	if !errors.Is(err, connectorsandbox.ErrConnectorSandboxStart) {
		t.Fatalf("Load error = %v; want ErrConnectorSandboxStart", err)
	}
	for _, want := range []string{"bwrap", "No permissions to creating new namespace"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

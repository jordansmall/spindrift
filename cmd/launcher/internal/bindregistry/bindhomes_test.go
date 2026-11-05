package bindregistry

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/registrymanifest"
)

// stub-a's renderer also emits stub-b's var, so stub-b binds without opting in
// and the opt-out side of the announce assertion has a Strategy to stay silent for.
func bindHomesStubRows(announce bool) []ecosystem.Strategy {
	render := func(port int, prefix string, _ func(string) string, _ []registrymanifest.Route) ([]ecosystem.EnvExport, []string) {
		return []ecosystem.EnvExport{
			{Name: "STUB_A_REGISTRY", Value: "http://127.0.0.1:" + strconv.Itoa(port) + "/" + prefix},
			{Name: "STUB_B_REGISTRY", Value: "http://127.0.0.1:" + strconv.Itoa(port) + "/" + prefix + "-b"},
		}, nil
	}
	return []ecosystem.Strategy{
		{Name: "stub-a", EnvExports: render, BindingEnvVar: "STUB_A_REGISTRY", AnnounceBindingValue: announce},
		{Name: "stub-b", BindingEnvVar: "STUB_B_REGISTRY"},
	}
}

func bindHomesManifest() registrymanifest.Manifest {
	return registrymanifest.Manifest{Routes: []registrymanifest.Route{{Prefix: "r0"}}}
}

func noEnv(string) string { return "" }

// The announce line is Strategy-generic: it names the Strategy and var from the Strategy, prints
// after the Forwarder line, and only for a Strategy that opted in.
func TestBindHomes_AnnouncesBindingValueOnlyForOptedInRow(t *testing.T) {
	var out strings.Builder
	if !BindHomes(&out, bindHomesStubRows(true), ReadyGate(bindHomesManifest()), nil, noEnv) {
		t.Fatalf("BindHomes returned false; output:\n%s", out.String())
	}

	port := strconv.Itoa(ForwarderPort)
	want := "==> registry proxy Forwarder up on 127.0.0.1:" + port +
		" — stub-a bound to it via STUB_A_REGISTRY and stub-b bound to it via STUB_B_REGISTRY\n" +
		"==> stub-a bound to it via STUB_A_REGISTRY=http://127.0.0.1:" + port + "/r0\n"
	if got := out.String(); got != want {
		t.Errorf("BindHomes output =\n%s\nwant\n%s", got, want)
	}
}

func TestBindHomes_NoAnnounceLineWithoutOptIn(t *testing.T) {
	var out strings.Builder
	if !BindHomes(&out, bindHomesStubRows(false), ReadyGate(bindHomesManifest()), nil, noEnv) {
		t.Fatalf("BindHomes returned false; output:\n%s", out.String())
	}

	if got := out.String(); strings.Count(got, "\n") != 1 || strings.Contains(got, "STUB_A_REGISTRY=") {
		t.Errorf("BindHomes output = %q, want only the Forwarder line", got)
	}
}

// An inert HomeConfig render (bound=false) still overwrites its file, so a stale redirect
// cannot survive, but the summary must not advertise a binding it lacks.
func TestBindHomes_InertHomeConfigIsWrittenButNotSummarized(t *testing.T) {
	home := t.TempDir()
	homeConfig := func(configPath, content string, bound bool) *ecosystem.HomeConfig {
		return &ecosystem.HomeConfig{
			HomeEnvVar: "STUB_HOME",
			ConfigPath: configPath,
			Render: func(int, string, []registrymanifest.Route) (string, bool) {
				return content, bound
			},
		}
	}
	rows := []ecosystem.Strategy{
		{Name: "stub-bound", HomeConfig: homeConfig("bound/config", "bound content\n", true)},
		{Name: "stub-inert", HomeConfig: homeConfig("inert/config", "inert content\n", false)},
	}
	getenv := func(k string) string {
		if k == "STUB_HOME" {
			return home
		}
		return ""
	}

	if err := os.MkdirAll(filepath.Join(home, "inert"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "inert/config"), []byte("stale redirect\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if !BindHomes(&out, rows, ReadyGate(bindHomesManifest()), nil, getenv) {
		t.Fatalf("BindHomes returned false; output:\n%s", out.String())
	}

	for configPath, want := range map[string]string{"bound/config": "bound content\n", "inert/config": "inert content\n"} {
		got, err := os.ReadFile(filepath.Join(home, configPath))
		if err != nil {
			t.Fatalf("read %s: %v", configPath, err)
		}
		if string(got) != want {
			t.Errorf("%s = %q, want %q", configPath, got, want)
		}
	}

	want := "==> registry proxy Forwarder up on 127.0.0.1:" + strconv.Itoa(ForwarderPort) +
		" — stub-bound bound to it via " + filepath.Join(home, "bound/config") + "\n"
	if got := out.String(); got != want {
		t.Errorf("BindHomes output =\n%s\nwant\n%s", got, want)
	}
}

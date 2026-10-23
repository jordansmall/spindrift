package bindregistry

import (
	"strconv"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/registrymanifest"
)

// stub-a's renderer also emits stub-b's var, so stub-b binds without opting in
// and the opt-out side of the announce assertion has a row to stay silent for.
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

// The announce line is row-generic: it names the row and var from the row, prints
// after the Forwarder line, and only for a row that opted in.
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

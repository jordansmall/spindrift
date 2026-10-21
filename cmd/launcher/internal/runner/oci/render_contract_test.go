package oci

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/runner/runnertest"
	"spindrift.dev/launcher/internal/runner/runnertest/contract"
)

// contractImage is the image Render hands the adapter, and so the positional
// that ends the flag list in the rendered argv.
const contractImage = "spindrift:contract"

func TestRenderingContract(t *testing.T) {
	contract.Rendering(t, ociRenderingHarness{})
}

type ociRenderingHarness struct{}

func (ociRenderingHarness) Family() string { return runner.KindOCI }

// Baked is empty: the image carries its own env and mounts, so nothing rides argv.
func (ociRenderingHarness) Baked(contract.Launch) contract.BakedSet { return contract.BakedSet{} }

func (ociRenderingHarness) Render(t *testing.T, l contract.Launch) []string {
	t.Helper()
	// Inspect misses, the run prints the marker, and the post-success reap's
	// inspect misses again.
	script, dir := runnertest.NewFakeCLI(t,
		runnertest.Call{Exit: 1},
		runnertest.Call{Stdout: contract.Stdout},
		runnertest.Call{Exit: 1},
	)
	// a.cli is exec'd by name from PATH, so the fake is reached through a
	// podman-named link; the plan below resolves against Runtime "podman".
	bin := t.TempDir()
	if err := os.Symlink(script, filepath.Join(bin, "podman")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	a := &ociAdapter{
		cli:         "podman",
		image:       contractImage,
		network:     runner.Config{Runtime: "podman", NetworkMode: l.Network}.OCINetwork("", "pasta"),
		mountParams: runner.MountParams{DriverSessionCacheDir: runnertest.DriverSessionCacheTarget},
	}
	if err := a.Run(l.Box); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for i := 0; i < runnertest.CallCount(t, dir); i++ {
		if argv := runnertest.ReadCall(t, dir, i); argv[0] == "run" {
			return argv
		}
	}
	t.Fatal("fake CLI recorded no run invocation")
	return nil
}

func (ociRenderingHarness) Decode(t *testing.T, argv []string) contract.Decoded {
	t.Helper()
	d := contract.Decoded{Env: map[string]string{}, Network: runner.NetworkModeOpen}
	flagValue := func(i int) string {
		if i+1 >= len(argv) {
			t.Fatalf("flag %s has no value: %v", argv[i], argv)
		}
		return argv[i+1]
	}
	// The image is the first positional; what follows it is the entrypoint,
	// not flags.
	i := 1
	for ; i < len(argv) && argv[i] != contractImage; i++ {
		switch arg := argv[i]; {
		case arg == "--name":
			d.Name = flagValue(i)
			i++
		case arg == "-e":
			kv := flagValue(i)
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				t.Fatalf("-e %q has no KEY=VALUE separator: %v", kv, argv)
			}
			if err := contract.PutEnv(d.Env, k, v); err != nil {
				t.Fatalf("%v: %v", err, argv)
			}
			i++
		case arg == "-v":
			spec := flagValue(i)
			raw := spec
			i++
			m := contract.Mount{}
			if rest, ok := strings.CutSuffix(spec, ":ro"); ok {
				spec, m.ReadOnly = rest, true
			}
			var ok bool
			if m.Source, m.Target, ok = strings.Cut(spec, ":"); !ok {
				t.Fatalf("-v %q has no source:target separator: %v", raw, argv)
			}
			d.Mounts = append(d.Mounts, m)
		case arg == "--add-host":
			mapping := flagValue(i)
			host, ok := strings.CutSuffix(mapping, ":host-gateway")
			if !ok {
				t.Fatalf("--add-host %q does not map to host-gateway: %v", mapping, argv)
			}
			d.AddHosts = append(d.AddHosts, host)
			i++
		case arg == "--network":
			switch v := flagValue(i); v {
			case "pasta":
				d.Network = runner.NetworkModeNoHostLoopback
			case "none":
				d.Network = runner.NetworkModeNone
			default:
				t.Fatalf("unrecognised --network %q", v)
			}
			i++
		case arg == "--cap-drop=all", arg == "--security-opt=no-new-privileges",
			strings.HasPrefix(arg, "--pids-limit="), strings.HasPrefix(arg, "--memory="):
			// Rendered by buildRunArgs but outside the rendering contract.
		default:
			t.Fatalf("unrecognised flag %q: %v", arg, argv)
		}
	}
	if i >= len(argv) {
		t.Fatalf("image %q not found in argv: %v", contractImage, argv)
	}
	return d
}

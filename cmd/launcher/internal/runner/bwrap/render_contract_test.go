package bwrap

import (
	"errors"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/runner/runnertest"
	"spindrift.dev/launcher/internal/runner/runnertest/contract"
)

func TestRenderingContract(t *testing.T) {
	contract.Rendering(t, bwrapRenderingHarness{})
}

// The adapter's baked closure, which the decoder compares a launch against to
// tell "no generation bound" from "the baked one".
const (
	bakedAgentFiles    = "/nix/store/baked-agent-files"
	bakedAgentEnv      = "/nix/store/baked-agent-env"
	bakedNixConfigFile = "/nix/store/baked-nix-conf"
	bakedGeneration    = "baked-gen"
)

type bwrapRenderingHarness struct{}

func (bwrapRenderingHarness) Family() string { return runner.KindBwrap }

// Baked is what a launch renders whatever the Box: the closure env, the store
// and identity files, nix.conf, and the agent home. The adapter binds nix.conf
// only when nixConfigFile is set and resolv.conf only when statResolvConf finds
// one and the network is not isolated without pasta (mode none); Render sets
// the former and stubs the latter, so both are baked here. Values vary per
// generation and run, so only keys and targets are named.
func (bwrapRenderingHarness) Baked(l contract.Launch) contract.BakedSet {
	baked := contract.BakedSet{
		EnvKeys:      []string{"HOME", "PATH", "PREFETCH", "SSL_CERT_FILE", "GIT_SSL_CAINFO"},
		MountTargets: []string{"/nix/store", "/etc/passwd", "/etc/group", "/etc/nix/nix.conf", "/bin", "/agent", "/home-agent-staged"},
	}
	if l.Network != runner.NetworkModeNone {
		baked.MountTargets = append(baked.MountTargets, "/etc/resolv.conf")
	}
	return baked
}

func (bwrapRenderingHarness) Render(t *testing.T, l contract.Launch) []string {
	t.Helper()
	script, dir := runnertest.NewFakeCLI(t, runnertest.Call{Stdout: contract.Stdout})

	// Whichever program Run picks (bwrap, or pasta wrapping bwrap) runs the fake.
	origExec := execCommand
	t.Cleanup(func() { execCommand = origExec })
	execCommand = func(_ string, args ...string) *exec.Cmd { return exec.Command(script, args...) }

	// An erroring self-cgroup read makes provisionCgroup warn and skip, so Run
	// never touches the real cgroupfs or attempts CLONE_INTO_CGROUP.
	origSelf := readSelfCgroup
	t.Cleanup(func() { readSelfCgroup = origSelf })
	readSelfCgroup = func() (string, error) { return "", errors.New("no cgroup in contract test") }
	t.Cleanup(stubResolvConfPresent())

	// Run locks the snapshot dir of the generation it mounts and recreates a
	// missing one, so each carries a db.sqlite.
	snapshotRoot := t.TempDir()
	dirs := []string{bakedGeneration}
	if g := l.Box.ClosureGeneration; g != nil {
		dirs = append(dirs, g.Generation)
	}
	for _, d := range dirs {
		seedSnapshot(t, snapshotDBPath(filepath.Join(snapshotRoot, d)), "snapshot")
	}

	a := &bwrapAdapter{
		agentFiles:         bakedAgentFiles,
		agentEnv:           bakedAgentEnv,
		passwdFile:         "/nix/store/contract-passwd",
		groupFile:          "/nix/store/contract-group",
		bakedPrefetch:      "echo ok",
		nixConfigFile:      bakedNixConfigFile,
		nixVarSnapshotRoot: snapshotRoot,
		nixVarSnapshotDir:  filepath.Join(snapshotRoot, bakedGeneration),
		network:            runner.Config{NetworkMode: l.Network}.BwrapNetwork(false),
		mountParams:        runner.MountParams{DriverSessionCacheDir: runnertest.DriverSessionCacheTarget},
	}
	if err := a.Run(l.Box); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n := runnertest.CallCount(t, dir); n != 1 {
		t.Fatalf("fake CLI recorded %d invocations, want 1", n)
	}
	return runnertest.ReadCall(t, dir, 0)
}

func (bwrapRenderingHarness) Decode(t *testing.T, argv []string) contract.Decoded {
	t.Helper()
	d := contract.Decoded{Env: map[string]string{}, Network: runner.NetworkModeHost}

	// pasta wraps bwrap as `<pasta flags> -- bwrap <bwrap argv>`.
	if len(argv) > 0 && argv[0] == "-t" {
		i := slices.Index(argv, "--")
		if i < 0 || i+1 >= len(argv) || argv[i+1] != "bwrap" {
			t.Fatalf("pasta argv has no `-- bwrap`: %v", argv)
		}
		argv = argv[i+2:]
		d.Network = runner.NetworkModeNoHostLoopback
	}

	var overlaySrc, agentFilesAgent, agentEnvPath, binSource, nixConf string
	arg := func(i, n int) []string {
		if i+n >= len(argv) {
			t.Fatalf("flag %s is missing %d value(s): %v", argv[i], n, argv)
		}
		return argv[i+1 : i+1+n]
	}
	for i := 0; i < len(argv); i++ {
		switch flag := argv[i]; flag {
		case "--":
			i = len(argv)
		case "--bind", "--bind-try", "--dev-bind", "--dev-bind-try", "--ro-bind", "--ro-bind-try":
			v := arg(i, 2)
			i += 2
			ro := strings.HasPrefix(flag, "--ro-")
			d.Mounts = append(d.Mounts, contract.Mount{Source: v[0], Target: v[1], ReadOnly: ro})
			switch {
			case ro && v[1] == "/agent":
				agentFilesAgent = v[0]
			case ro && v[1] == "/etc/nix/nix.conf":
				nixConf = v[0]
			case v[1] == "/bin":
				if !ro {
					t.Fatalf("/bin must be bound read-only: %v", argv)
				}
				binSource = v[0]
			}
		case "--setenv":
			v := arg(i, 2)
			i += 2
			if err := contract.PutEnv(d.Env, v[0], v[1]); err != nil {
				t.Fatalf("%v: %v", err, argv)
			}
			if v[0] == "PATH" {
				agentEnvPath = v[1]
			}
		case "--overlay-src":
			if v := arg(i, 1); v[0] != "/nix/store" {
				overlaySrc = v[0]
			}
			i++
		case "--symlink", "--chmod":
			i += len(arg(i, 2))
		case "--dir", "--tmpfs", "--proc", "--dev", "--tmp-overlay", "--uid", "--gid", "--seccomp":
			i += len(arg(i, 1))
		case "--unshare-user", "--unshare-pid", "--unshare-ipc", "--unshare-uts", "--die-with-parent":
		case "--unshare-net":
			if d.Network == runner.NetworkModeHost {
				d.Network = runner.NetworkModeNone
			}
		default:
			t.Fatalf("unrecognised bwrap flag %q at %d: %v", flag, i, argv)
		}
	}

	// A /bin bind from any other closure than PATH's would let shebangs and
	// libc system() run different binaries than the agent's own PATH resolves.
	if binSource == "" || binSource != agentEnvPath {
		t.Fatalf("/bin bind source %q must equal PATH %q: %v", binSource, agentEnvPath, argv)
	}

	g := runner.AgentGeneration{
		AgentFiles:    strings.TrimSuffix(agentFilesAgent, "/agent"),
		AgentEnv:      strings.TrimSuffix(agentEnvPath, "/bin"),
		NixConfigFile: nixConf,
		Generation:    filepath.Base(overlaySrc),
	}
	baked := runner.AgentGeneration{
		AgentFiles:    bakedAgentFiles,
		AgentEnv:      bakedAgentEnv,
		NixConfigFile: bakedNixConfigFile,
		Generation:    bakedGeneration,
	}
	if g != baked {
		d.ClosureGeneration = &g
		prefetch, ok := d.Env["PREFETCH"]
		if !ok {
			t.Fatalf("bound generation renders no --setenv PREFETCH (bwrap keeps the launcher's ambient env): %v", argv)
		}
		d.ClosurePrefetch = prefetch
	}
	return d
}

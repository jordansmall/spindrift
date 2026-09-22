package runner

import (
	"path/filepath"
	"strings"
	"testing"
)

// Values of offArgvKeys must stay off bwrap's argv, where ps and /proc would
// expose them.
func TestBwrapArgs_NoOffArgvKeysOnArgv(t *testing.T) {
	a := &bwrapAdapter{
		agentFiles:    "/fake/agent",
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
	}
	box := Box{
		Env: map[string]string{
			"GH_TOKEN":                "gh-secret-value",
			"CLAUDE_CODE_OAUTH_TOKEN": "claude-secret-value",
			"ANTHROPIC_API_KEY":       "anthropic-secret-value",
			"ISSUE_TEXT":              "private issue body\nwith a secret-shaped line",
			"REPO_SLUG":               "owner/repo",
			"ISSUE_NUMBER":            "42",
		},
	}

	args := a.buildArgs("/tmp/fake-etc", box)

	offArgvValues := []string{"gh-secret-value", "claude-secret-value", "anthropic-secret-value", "private issue body", "with a secret-shaped line"}
	for _, arg := range args {
		for _, val := range offArgvValues {
			if strings.Contains(arg, val) {
				t.Errorf("offArgvKeys value %q found in bwrap argv: %v", val, args)
			}
		}
	}
}

// Issue #3470: ISSUE_TEXT absent from box.Env, or present as an empty string,
// must emit no "--setenv ISSUE_TEXT" pair. buildArgs emits --setenv for a key
// only when !offArgvKeys[k], and ISSUE_TEXT is always in offArgvKeys, so this
// holds for any value, unlike the OCI runner's bare "-e KEY" rendering.
func TestBwrapArgs_IssueTextAbsentOrEmpty(t *testing.T) {
	a := &bwrapAdapter{
		agentFiles:    "/fake/agent",
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
	}

	t.Run("absent", func(t *testing.T) {
		args := a.buildArgs("/tmp/fake-etc", Box{Env: map[string]string{"ISSUE_NUMBER": "1"}})
		for i, arg := range args {
			if arg == "--setenv" && i+1 < len(args) && args[i+1] == "ISSUE_TEXT" {
				t.Errorf("buildArgs emitted --setenv ISSUE_TEXT for a box.Env with no ISSUE_TEXT key: %v", args)
			}
		}
	})

	t.Run("empty string", func(t *testing.T) {
		args := a.buildArgs("/tmp/fake-etc", Box{Env: map[string]string{"ISSUE_TEXT": ""}})
		for i, arg := range args {
			if arg == "--setenv" && i+1 < len(args) && args[i+1] == "ISSUE_TEXT" {
				t.Errorf("buildArgs emitted --setenv ISSUE_TEXT for an empty-string box.Env value: %v", args)
			}
		}
	})
}

// Without --clearenv the sandbox inherits secrets from the launcher's process
// environment.
func TestBwrapArgs_NoClearEnv(t *testing.T) {
	a := &bwrapAdapter{
		agentFiles:    "/fake/agent",
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
	}
	args := a.buildArgs("/tmp/fake-etc", Box{Env: map[string]string{"GH_TOKEN": "s"}})
	for _, arg := range args {
		if arg == "--clearenv" {
			t.Errorf("--clearenv found in bwrap argv; secrets would not reach sandbox")
		}
	}
}

// --die-with-parent must be unconditional so bwrap registers PR_SET_PDEATHSIG
// against its own parent and the sandbox dies with the launcher (issue #2669).
func TestBwrapArgs_DieWithParent(t *testing.T) {
	a := &bwrapAdapter{
		agentFiles:    "/fake/agent",
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
	}
	args := a.buildArgs("/tmp/fake-etc", Box{Env: map[string]string{}})

	if !containsArg(args, "--die-with-parent") {
		t.Errorf("expected --die-with-parent in args: %v", args)
	}
}

// The operator-override skills mount lands at the fixed /operator-skills
// staging path (issue #2489); entrypoint.sh merges it into the Driver's real
// skills dir at box startup, so bwrap.go never binds onto that dir directly.
func TestBwrapArgs_SkillsDirMounted(t *testing.T) {
	dir := t.TempDir()
	a := &bwrapAdapter{
		agentFiles:    "/fake/agent",
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
		mountParams:   MountParams{SkillsDir: dir},
	}
	args := a.buildArgs("/tmp/fake-etc", Box{Env: map[string]string{}})

	argStr := strings.Join(args, " ")
	want := "--ro-bind " + dir + " /operator-skills"
	if !strings.Contains(argStr, want) {
		t.Errorf("skills bind %q not found in args: %v", want, args)
	}
}

// A Box.Sockets entry's unix path becomes a --bind onto its fixed target
// (ADR 0044, issue #2849; issue #3723).
func TestBwrapArgs_RegistryProxySocketMounted(t *testing.T) {
	sock := newTestSocket(t, "registry-proxy.sock")
	a := &bwrapAdapter{
		agentFiles:    "/fake/agent",
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
	}
	args := a.buildArgs("/tmp/fake-etc", Box{Env: map[string]string{}, Sockets: []SocketMount{{Source: sock, Target: RegistryProxySocketTarget}}})

	argStr := strings.Join(args, " ")
	want := "--bind " + sock + " /registry-proxy.sock"
	if !strings.Contains(argStr, want) {
		t.Errorf("registry-proxy socket bind %q not found in args: %v", want, args)
	}
}

// issue #3723: two Box.Sockets entries each render their own --dir parent +
// --bind pair, none as --ro-bind.
func TestBwrapArgs_MultipleSockets_TwoBindPairs(t *testing.T) {
	sockA := newTestSocket(t, "registry-proxy.sock")
	sockB := newTestSocket(t, "signal.sock")
	a := &bwrapAdapter{
		agentFiles:    "/fake/agent",
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
	}
	box := Box{Env: map[string]string{}, Sockets: []SocketMount{
		{Source: sockA, Target: RegistryProxySocketTarget},
		{Source: sockB, Target: SignalSocketTarget},
	}}
	args := a.buildArgs("/tmp/fake-etc", box)
	argStr := strings.Join(args, " ")

	for _, m := range []struct{ source, target string }{
		{sockA, RegistryProxySocketTarget},
		{sockB, SignalSocketTarget},
	} {
		if !strings.Contains(argStr, "--dir "+filepath.Dir(m.target)) {
			t.Errorf("missing --dir for parent of %q in args: %v", m.target, args)
		}
		if !strings.Contains(argStr, "--bind "+m.source+" "+m.target) {
			t.Errorf("missing --bind %s %s in args: %v", m.source, m.target, args)
		}
		if strings.Contains(argStr, "--ro-bind "+m.source+" "+m.target) {
			t.Errorf("socket mount %s -> %s must not be --ro-bind; args: %v", m.source, m.target, args)
		}
	}
}

// Issue #2843: the staged /home/agent subtree must land at a fixed top-level
// path, not under /agent, because /agent is already bound read-only by then
// and bwrap cannot create a mountpoint inside a read-only bind. The real
// /home/agent stays a fresh writable tmpfs that entrypoint.sh copies this
// staged content into at startup.
func TestBwrapArgs_HomeAgentStagingMounted(t *testing.T) {
	a := &bwrapAdapter{
		agentFiles:    "/fake/agent",
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
	}
	args := a.buildArgs("/tmp/fake-etc", Box{Env: map[string]string{}})

	if !wantTriple(args, "--ro-bind", "/fake/agent/home/agent", "/home-agent-staged") {
		t.Errorf("expected --ro-bind /fake/agent/home/agent /home-agent-staged in args: %v", args)
	}
}

// A Box.ClosureGeneration override (issue #2681) replaces the adapter's
// startup-baked agentFiles in the /agent and /home-agent-staged binds, and the
// baked path must not leak into argv anywhere.
func TestBwrapArgs_ClosureGenerationOverridesAgentFiles(t *testing.T) {
	a := &bwrapAdapter{
		agentFiles:    "/baked/agent-files",
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
	}
	box := Box{
		Env:               map[string]string{},
		ClosureGeneration: &AgentGeneration{AgentFiles: "/gen2/agent-files"},
	}
	args := a.buildArgs("/tmp/fake-etc", box)

	if !wantTriple(args, "--ro-bind", "/gen2/agent-files/agent", "/agent") {
		t.Errorf("expected --ro-bind /gen2/agent-files/agent /agent in args: %v", args)
	}
	if !wantTriple(args, "--ro-bind", "/gen2/agent-files/home/agent", homeAgentStagingDir) {
		t.Errorf("expected --ro-bind /gen2/agent-files/home/agent %s in args: %v", homeAgentStagingDir, args)
	}
	for _, arg := range args {
		if strings.Contains(arg, "/baked/agent-files") {
			t.Errorf("baked agentFiles %q leaked into args despite ClosureGeneration override: %v", "/baked/agent-files", args)
		}
	}
}

// Issue #2681 review finding: a non-nil ClosureGeneration with an empty
// AgentFiles must fall back to the baked agentFiles. Binding a bare "/agent"
// and "/home/agent" would pull the host's own directories into the sandbox
// instead of a store closure.
func TestBwrapArgs_ClosureGenerationEmptyAgentFilesFallsBackToBaked(t *testing.T) {
	a := &bwrapAdapter{
		agentFiles:    "/baked/agent-files",
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
	}
	box := Box{
		Env:               map[string]string{},
		ClosureGeneration: &AgentGeneration{Generation: "gen2"},
	}
	args := a.buildArgs("/tmp/fake-etc", box)

	if !wantTriple(args, "--ro-bind", "/baked/agent-files/agent", "/agent") {
		t.Errorf("expected --ro-bind /baked/agent-files/agent /agent in args: %v", args)
	}
	if !wantTriple(args, "--ro-bind", "/baked/agent-files/home/agent", homeAgentStagingDir) {
		t.Errorf("expected --ro-bind /baked/agent-files/home/agent %s in args: %v", homeAgentStagingDir, args)
	}
	if wantTriple(args, "--ro-bind", "/agent", "/agent") {
		t.Errorf("bare host /agent bound into sandbox despite empty ClosureGeneration.AgentFiles: %v", args)
	}
	if wantTriple(args, "--ro-bind", "/home/agent", homeAgentStagingDir) {
		t.Errorf("bare host /home/agent bound into sandbox despite empty ClosureGeneration.AgentFiles: %v", args)
	}
}

// A nil Box.ClosureGeneration, the zero value every existing Box literal
// carries, keeps the binds deriving from the baked agentFiles (issue #2681).
func TestBwrapArgs_ClosureGenerationNilKeepsBakedAgentFiles(t *testing.T) {
	a := &bwrapAdapter{
		agentFiles:    "/baked/agent-files",
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
	}
	args := a.buildArgs("/tmp/fake-etc", Box{Env: map[string]string{}})

	if !wantTriple(args, "--ro-bind", "/baked/agent-files/agent", "/agent") {
		t.Errorf("expected --ro-bind /baked/agent-files/agent /agent in args: %v", args)
	}
	if !wantTriple(args, "--ro-bind", "/baked/agent-files/home/agent", homeAgentStagingDir) {
		t.Errorf("expected --ro-bind /baked/agent-files/home/agent %s in args: %v", homeAgentStagingDir, args)
	}
}

// Issue #2663: /etc/passwd and /etc/group come from the nix store paths on the
// adapter, not a runner-written temp-dir copy. The fake paths below are shaped
// like real store paths on purpose.
func TestBwrapArgs_AccountFilesBindStorePaths(t *testing.T) {
	a := &bwrapAdapter{
		agentFiles: "/fake/agent",
		agentEnv:   "/fake/env",
		passwdFile: "/nix/store/abc123-passwd/passwd",
		groupFile:  "/nix/store/def456-group/group",
	}
	args := a.buildArgs("/tmp/fake-etc", Box{Env: map[string]string{}})

	if !wantTriple(args, "--ro-bind", "/nix/store/abc123-passwd/passwd", "/etc/passwd") {
		t.Errorf("expected --ro-bind /nix/store/abc123-passwd/passwd /etc/passwd in args: %v", args)
	}
	if !wantTriple(args, "--ro-bind", "/nix/store/def456-group/group", "/etc/group") {
		t.Errorf("expected --ro-bind /nix/store/def456-group/group /etc/group in args: %v", args)
	}
}

// Some nix build sandboxes have no /etc/resolv.conf, so a test that needs one
// present stubs statResolvConf and defers the returned restore func.
func stubResolvConfPresent() func() {
	prev := statResolvConf
	statResolvConf = func() error { return nil }
	return func() { statResolvConf = prev }
}

// networkMode="none" stays helper-free: --unshare-net, no resolv.conf bind,
// and no pasta either, so the sandbox has no egress at all and build-time
// no-network probes can use it. Every other isolating mode since issue #2666
// gets pasta.
func TestBwrapArgs_NetworkModeNoneUnsharesNet(t *testing.T) {
	a := &bwrapAdapter{
		agentFiles:    "/fake/agent",
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
		networkMode:   "none",
	}
	args := a.buildArgs("/tmp/fake-etc", Box{Env: map[string]string{}})

	if !containsArg(args, "--unshare-net") {
		t.Errorf("--unshare-net missing for networkMode=none; args: %v", args)
	}
	if containsArg(args, "/etc/resolv.conf") {
		t.Errorf("resolv.conf must not be bound for networkMode=none; args: %v", args)
	}
	if containsArg(args, "pasta") {
		t.Errorf("pasta must be absent for networkMode=none (fully helper-free, no egress); args: %v", args)
	}
}

// The composition order asserted here is what the pasta manual requires: pasta
// is the outer process that creates and configures the namespace before
// execing its COMMAND, bwrap, inside it (issue #2666).
func assertPastaExecTarget(t *testing.T, a *bwrapAdapter, etcDir string, box Box) {
	t.Helper()
	bwrapArgs := a.buildArgs(etcDir, box)
	program, args, childExecsByName := a.execTarget(etcDir, box)

	if program != "pasta" {
		t.Fatalf("execTarget program = %q, want %q", program, "pasta")
	}
	if !childExecsByName {
		t.Error("execTarget childExecsByName = false, want true: pasta execvp's \"bwrap\" by bare name")
	}
	want := append([]string{}, pastaHardenedFlags...)
	want = append(want, "--dns-forward", pastaDNSForwardAddr, "-f", "--", "bwrap")
	want = append(want, bwrapArgs...)
	if len(args) != len(want) {
		t.Fatalf("execTarget args length = %d, want %d\ngot:  %v\nwant: %v", len(args), len(want), args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Errorf("execTarget args[%d] = %q, want %q\ngot:  %v\nwant: %v", i, args[i], want[i], args, want)
		}
	}
}

// The non-isolating "host" and fully-offline "none" cases involve no pasta, so
// execTarget returns bwrap itself with buildArgs' own output.
func assertBareBwrapExecTarget(t *testing.T, a *bwrapAdapter, etcDir string, box Box) {
	t.Helper()
	bwrapArgs := a.buildArgs(etcDir, box)
	program, args, childExecsByName := a.execTarget(etcDir, box)

	if program != "bwrap" {
		t.Fatalf("execTarget program = %q, want %q", program, "bwrap")
	}
	if childExecsByName {
		t.Error("execTarget childExecsByName = true, want false: bare bwrap execs no child by name")
	}
	if len(args) != len(bwrapArgs) {
		t.Fatalf("execTarget args length = %d, want %d (buildArgs' own output)\ngot:  %v\nwant: %v", len(args), len(bwrapArgs), args, bwrapArgs)
	}
	for i := range bwrapArgs {
		if args[i] != bwrapArgs[i] {
			t.Errorf("execTarget args[%d] = %q, want %q (buildArgs' own output)\ngot:  %v\nwant: %v", i, args[i], bwrapArgs[i], args, bwrapArgs)
		}
	}
}

// Issue #2666 review finding: pasta is the outer process that configures the
// fresh netns, so buildArgs must inherit it rather than unshare a second empty
// one on top. TestExecTarget_NetworkModeOpenWrapsWithPasta asserts the pasta
// composition; the synthesized resolv.conf bind pointing at pasta's
// --dns-forward address is a buildArgs decision, so it is asserted here.
func TestBwrapArgs_NetworkModeOpenOmitsBwrapSideUnshareNet(t *testing.T) {
	a := &bwrapAdapter{
		agentFiles:    "/fake/agent",
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
		networkMode:   "open",
	}
	args := a.buildArgs("/tmp/fake-etc", Box{Env: map[string]string{}})

	if containsArg(args, "--unshare-net") {
		t.Errorf("--unshare-net must be absent from buildArgs' own return for networkMode=open; bwrap must inherit pasta's already-configured netns, not re-unshare into an empty one: args: %v", args)
	}
	if containsArg(args, "pasta") {
		t.Errorf("pasta must be absent from buildArgs' own return for networkMode=open; it is now the outer process (see execTarget), not an argv token inside bwrap's own exec target: args: %v", args)
	}
	want := "--ro-bind /tmp/fake-etc/resolv.conf /etc/resolv.conf"
	if !strings.Contains(strings.Join(args, " "), want) {
		t.Errorf("synthesized resolv.conf bind %q not found for networkMode=open; args: %v", want, args)
	}
}

// Characterization test (issue #2562 review finding, closed by issue #2666):
// nix eval and main.go's checkNetworkModeRuntimeGate keep "no-host-loopback"
// from reaching the adapter in production, but one constructed directly now
// isolates with pasta-wrapped egress instead of falling open to the shared
// host netns. This is defense in depth, not a new supported mode.
func TestBwrapArgs_NetworkModeNoHostLoopbackDefaultsToIsolate(t *testing.T) {
	a := &bwrapAdapter{
		agentFiles:    "/fake/agent",
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
		networkMode:   "no-host-loopback",
	}
	args := a.buildArgs("/tmp/fake-etc", Box{Env: map[string]string{}})

	if containsArg(args, "--unshare-net") {
		t.Errorf("--unshare-net must be absent from buildArgs' own return for networkMode=no-host-loopback; hazard reopened: args: %v", args)
	}
	if containsArg(args, "pasta") {
		t.Errorf("pasta must be absent from buildArgs' own return for networkMode=no-host-loopback; args: %v", args)
	}
	if !containsArg(args, "/tmp/fake-etc/resolv.conf") {
		t.Errorf("synthesized resolv.conf bind source missing for networkMode=no-host-loopback; args: %v", args)
	}
}

// Issue #2666's "by default a Box has its own network namespace and working
// egress" criterion: the Go zero value for networkMode, which is what most
// callers of NewBwrap/Config leave it at, isolates and pasta-wraps the same as
// an explicit "open".
func TestBwrapArgs_NetworkModeUnsetDefaultsToIsolate(t *testing.T) {
	a := &bwrapAdapter{
		agentFiles:    "/fake/agent",
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
	}
	args := a.buildArgs("/tmp/fake-etc", Box{Env: map[string]string{}})

	if containsArg(args, "--unshare-net") {
		t.Errorf("--unshare-net must be absent from buildArgs' own return for zero-value networkMode; args: %v", args)
	}
	if containsArg(args, "pasta") {
		t.Errorf("pasta must be absent from buildArgs' own return for zero-value networkMode; args: %v", args)
	}
	if !containsArg(args, "/tmp/fake-etc/resolv.conf") {
		t.Errorf("synthesized resolv.conf bind source missing for zero-value networkMode; args: %v", args)
	}
}

// The documented opt-out (issue #2666): "host" restores the pre-#2666 shared
// host netns, so no --unshare-net, no pasta, and the /etc/resolv.conf bind is
// back because there is no isolated netns to supply DNS.
func TestBwrapArgs_NetworkModeHostSharesHostNetns(t *testing.T) {
	restore := stubResolvConfPresent()
	defer restore()

	a := &bwrapAdapter{
		agentFiles:    "/fake/agent",
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
		networkMode:   "host",
	}
	args := a.buildArgs("/tmp/fake-etc", Box{Env: map[string]string{}})

	if containsArg(args, "--unshare-net") {
		t.Errorf("--unshare-net must be absent for networkMode=host; args: %v", args)
	}
	if containsArg(args, "pasta") {
		t.Errorf("pasta must be absent for networkMode=host; args: %v", args)
	}
	if !containsArg(args, "/etc/resolv.conf") {
		t.Errorf("resolv.conf bind must be present for networkMode=host; args: %v", args)
	}
}

// Issue #2666: the raw BwrapUnshareNet knob now renders through the pasta path
// like the default, instead of the old bare --unshare-net with no helper that
// left the sandbox without DNS or egress. buildArgs omits --unshare-net and
// pasta here for the same composition-order reason as the other isolating
// modes.
func TestBwrapArgs_UnshareNetKnobOmitsBwrapSideUnshareNet(t *testing.T) {
	a := &bwrapAdapter{
		agentFiles:    "/fake/agent",
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
		unshareNet:    true,
	}
	args := a.buildArgs("/tmp/fake-etc", Box{Env: map[string]string{}})

	if containsArg(args, "--unshare-net") {
		t.Errorf("--unshare-net must be absent from buildArgs' own return for unshareNet=true; args: %v", args)
	}
	if containsArg(args, "pasta") {
		t.Errorf("pasta must be absent from buildArgs' own return for unshareNet=true; args: %v", args)
	}
	if !containsArg(args, "/tmp/fake-etc/resolv.conf") {
		t.Errorf("synthesized resolv.conf bind source missing for unshareNet=true; args: %v", args)
	}
}

// execTarget wraps bwrap with pasta as the outer process for
// networkMode="open" (issue #2666, ADR 0042).
func TestExecTarget_NetworkModeOpenWrapsWithPasta(t *testing.T) {
	a := &bwrapAdapter{
		agentFiles:    "/fake/agent",
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
		networkMode:   "open",
	}
	assertPastaExecTarget(t, a, "/tmp/fake-etc", Box{Env: map[string]string{}})
}

// This is the execTarget half of
// TestBwrapArgs_NetworkModeNoHostLoopbackDefaultsToIsolate.
func TestExecTarget_NetworkModeNoHostLoopbackWrapsWithPasta(t *testing.T) {
	a := &bwrapAdapter{
		agentFiles:    "/fake/agent",
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
		networkMode:   "no-host-loopback",
	}
	assertPastaExecTarget(t, a, "/tmp/fake-etc", Box{Env: map[string]string{}})
}

// This is the execTarget half of
// TestBwrapArgs_NetworkModeUnsetDefaultsToIsolate.
func TestExecTarget_NetworkModeUnsetWrapsWithPasta(t *testing.T) {
	a := &bwrapAdapter{
		agentFiles:    "/fake/agent",
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
	}
	assertPastaExecTarget(t, a, "/tmp/fake-etc", Box{Env: map[string]string{}})
}

// This is the execTarget half of
// TestBwrapArgs_UnshareNetKnobOmitsBwrapSideUnshareNet.
func TestExecTarget_UnshareNetKnobWrapsWithPasta(t *testing.T) {
	a := &bwrapAdapter{
		agentFiles:    "/fake/agent",
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
		unshareNet:    true,
	}
	assertPastaExecTarget(t, a, "/tmp/fake-etc", Box{Env: map[string]string{}})
}

func TestExecTarget_NetworkModeHostReturnsBareBwrap(t *testing.T) {
	restore := stubResolvConfPresent()
	defer restore()

	a := &bwrapAdapter{
		agentFiles:    "/fake/agent",
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
		networkMode:   "host",
	}
	assertBareBwrapExecTarget(t, a, "/tmp/fake-etc", Box{Env: map[string]string{}})
}

func TestExecTarget_NetworkModeNoneReturnsBareBwrap(t *testing.T) {
	a := &bwrapAdapter{
		agentFiles:    "/fake/agent",
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
		networkMode:   "none",
	}
	assertBareBwrapExecTarget(t, a, "/tmp/fake-etc", Box{Env: map[string]string{}})
}

// Issue #2489 removed TestBwrapArgs_SkillsMountTarget_FromDriverDeclaration:
// the skills mount always lands at the fixed /operator-skills path (see
// operatorSkillsDir in mount.go), so no driver-declared target remains.

// Issue #3471: a zero-value mountParams must never render an /issues bind. The discriminating pins live in mount_test.go
// (structural) and main_test.go (end to end).
func TestBwrapArgs_NeverRendersIssuesBind(t *testing.T) {
	a := &bwrapAdapter{
		agentFiles:    "/fake/agent",
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
		mountParams:   MountParams{},
	}
	args := a.buildArgs("/tmp/fake-etc", Box{Env: map[string]string{}})

	argStr := strings.Join(args, " ")
	if strings.Contains(argStr, "/issues") {
		t.Errorf("unexpected /issues bind: %v", args)
	}
}

func TestBwrapArgs_DriverCacheDirMountedWritable(t *testing.T) {
	dir := t.TempDir()
	a := &bwrapAdapter{
		agentFiles:    "/fake/agent",
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
		mountParams:   MountParams{DriverSessionCacheDir: "/home/agent/.claude/projects"},
	}
	args := a.buildArgs("/tmp/fake-etc", Box{Env: map[string]string{}, DriverCacheDir: dir})

	argStr := strings.Join(args, " ")
	want := "--bind " + dir + " /home/agent/.claude/projects"
	if !strings.Contains(argStr, want) {
		t.Errorf("driver cache bind %q not found in args: %v", want, args)
	}
	if strings.Contains(argStr, "--ro-bind "+dir+" /home/agent/.claude/projects") {
		t.Errorf("driver cache mount must be writable (--bind), not --ro-bind; args: %v", args)
	}
}

func TestBwrapArgs_DriverCacheDirMounted_HardeningPreserved(t *testing.T) {
	dir := t.TempDir()
	a := &bwrapAdapter{
		agentFiles:    "/fake/agent",
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
		mountParams:   MountParams{DriverSessionCacheDir: "/home/agent/.claude/projects"},
	}
	args := a.buildArgs("/tmp/fake-etc", Box{Env: map[string]string{}, DriverCacheDir: dir})

	for _, flag := range []string{"--unshare-user", "--unshare-pid", "--unshare-ipc", "--unshare-uts"} {
		if !containsArg(args, flag) {
			t.Errorf("writable driver cache bind must not weaken hardening; missing %q in args: %v", flag, args)
		}
	}
}

// Issue #447: --dir /home/agent/.claude must come before the driver-cache bind
// so the parent is agent-owned in the tmpfs rather than created as root by
// bwrap's bind-target auto-creation.
func TestBwrapArgs_DriverCacheDir_DotClaudeParentCreated(t *testing.T) {
	dir := t.TempDir()
	a := &bwrapAdapter{
		agentFiles:    "/fake/agent",
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
		mountParams:   MountParams{DriverSessionCacheDir: "/home/agent/.claude/projects"},
	}
	args := a.buildArgs("/tmp/fake-etc", Box{Env: map[string]string{}, DriverCacheDir: dir})

	dirIdx := -1
	bindIdx := -1
	for i, arg := range args {
		if arg == "/home/agent/.claude" && i > 0 && args[i-1] == "--dir" {
			dirIdx = i
		}
		if arg == "/home/agent/.claude/projects" && i > 0 && args[i-1] == dir {
			bindIdx = i
		}
	}
	if dirIdx == -1 {
		t.Errorf("--dir /home/agent/.claude not found in args: %v", args)
	}
	if bindIdx == -1 {
		t.Errorf("bind target /home/agent/.claude/projects not found in args: %v", args)
	}
	if dirIdx != -1 && bindIdx != -1 && dirIdx >= bindIdx {
		t.Errorf("--dir /home/agent/.claude (idx %d) must precede bind target (idx %d)", dirIdx, bindIdx)
	}
}

// The bind target and the --dir parent it creates first come from the
// adapter's driverSessionCacheDir field, populated by the Driver declaration
// (ADR 0009), not a hardcoded ".claude/projects" literal.
func TestBwrapArgs_DriverCacheMountTarget_FromDriverDeclaration(t *testing.T) {
	dir := t.TempDir()
	a := &bwrapAdapter{
		agentFiles:    "/fake/agent",
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
		mountParams:   MountParams{DriverSessionCacheDir: "/home/agent/custom-driver/state"},
	}
	args := a.buildArgs("/tmp/fake-etc", Box{Env: map[string]string{}, DriverCacheDir: dir})

	argStr := strings.Join(args, " ")
	wantBind := "--bind " + dir + " /home/agent/custom-driver/state"
	if !strings.Contains(argStr, wantBind) {
		t.Errorf("driver cache bind %q not found in args: %v", wantBind, args)
	}
	wantDir := "--dir /home/agent/custom-driver"
	if !strings.Contains(argStr, wantDir) {
		t.Errorf("parent %q not found in args: %v", wantDir, args)
	}
}

// Issue #448: a Driver declaring no session-state dir has no in-box target to
// bind a host DriverCacheDir over, so nothing is bound.
func TestBwrapArgs_DriverSessionCacheDirUndeclared_NoMount(t *testing.T) {
	dir := t.TempDir()
	a := &bwrapAdapter{
		agentFiles:    "/fake/agent",
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
	}
	args := a.buildArgs("/tmp/fake-etc", Box{Env: map[string]string{}, DriverCacheDir: dir})
	for _, arg := range args {
		if arg == dir {
			t.Errorf("unexpected driver cache bind in args when Driver declares no session-cache dir: %v", args)
		}
	}
}

func TestBwrapArgs_DriverCacheDirUnset_NoMount(t *testing.T) {
	a := &bwrapAdapter{
		agentFiles:    "/fake/agent",
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
		mountParams:   MountParams{DriverSessionCacheDir: "/home/agent/.claude/projects"},
	}
	args := a.buildArgs("/tmp/fake-etc", Box{Env: map[string]string{}})
	argStr := strings.Join(args, " ")
	if strings.Contains(argStr, "/home/agent/.claude/projects") {
		t.Errorf("unexpected driver cache bind in args when DriverCacheDir is empty: %v", args)
	}
}

func TestBwrapArgs_SkillsDirUnset_NoMount(t *testing.T) {
	a := &bwrapAdapter{
		agentFiles:    "/fake/agent",
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
		mountParams:   MountParams{SkillsDir: ""},
	}
	args := a.buildArgs("/tmp/fake-etc", Box{Env: map[string]string{}})
	argStr := strings.Join(args, " ")
	if strings.Contains(argStr, ".claude/skills") {
		t.Errorf("unexpected skills bind in args when skillsDir is empty: %v", args)
	}
}

// Issue #2489 removed TestBwrapArgs_BakedSkillsMounted,
// TestBwrapArgs_RuntimeSkillsTakePrecedence and
// TestBwrapArgs_SkillsDirInvalid_NoFallback along with bwrap.go's
// baked-skills-fallback bind. Baked skills now reach the box through the
// /agent ro-bind and entrypoint.sh's own copy step at startup.

// A non-empty nixConfigFile means the nixInBox knob is on (ADR 0042), which
// renders both the nix.conf ro-bind and the store-DB snapshot overlay onto
// /nix/var.
func TestBwrapArgs_MountsNixConfigAndStoreDBSnapshotWhenSet(t *testing.T) {
	a := &bwrapAdapter{
		agentFiles:        "/fake/agent",
		agentEnv:          "/fake/env",
		nixConfigFile:     "/nix/store/fake-hash-nix-conf/nix.conf",
		nixVarSnapshotDir: "/fake/pwd/.spindrift/nix-var-snapshot",
	}
	args := a.buildArgs("/tmp/fake-etc", Box{Env: map[string]string{}})

	if !wantTriple(args, "--ro-bind", "/nix/store/fake-hash-nix-conf/nix.conf", "/etc/nix/nix.conf") {
		t.Errorf("expected --ro-bind /nix/store/fake-hash-nix-conf/nix.conf /etc/nix/nix.conf in args: %v", args)
	}
	if !strings.Contains(strings.Join(args, " "), "--overlay-src /fake/pwd/.spindrift/nix-var-snapshot --tmp-overlay /nix/var") {
		t.Errorf("expected --overlay-src /fake/pwd/.spindrift/nix-var-snapshot --tmp-overlay /nix/var in args: %v", args)
	}
}

// A Box.ClosureGeneration override (issue #2681) replaces the baked
// nixVarSnapshotDir in the /nix/var overlay bind, deriving the per-launch
// snapshot dir from the adapter's pwd-derived nixVarSnapshotRoot instead.
func TestBwrapArgs_ClosureGenerationOverridesNixVarSnapshotDir(t *testing.T) {
	a := &bwrapAdapter{
		agentFiles:         "/fake/agent",
		agentEnv:           "/fake/env",
		nixConfigFile:      "/nix/store/fake-hash-nix-conf/nix.conf",
		nixVarSnapshotDir:  "/fake/pwd/.spindrift/nix-var-snapshot/gen1",
		nixVarSnapshotRoot: "/fake/pwd/.spindrift/nix-var-snapshot",
	}
	box := Box{
		Env:               map[string]string{},
		ClosureGeneration: &AgentGeneration{AgentFiles: "/gen2/agent-files", Generation: "gen2"},
	}
	args := a.buildArgs("/tmp/fake-etc", box)

	if !strings.Contains(strings.Join(args, " "), "--overlay-src /fake/pwd/.spindrift/nix-var-snapshot/gen2 --tmp-overlay /nix/var") {
		t.Errorf("expected --overlay-src /fake/pwd/.spindrift/nix-var-snapshot/gen2 --tmp-overlay /nix/var in args: %v", args)
	}
	for _, arg := range args {
		if strings.Contains(arg, "/nix-var-snapshot/gen1") {
			t.Errorf("baked nixVarSnapshotDir %q leaked into args despite ClosureGeneration override: %v", a.nixVarSnapshotDir, args)
		}
	}
}

// Issue #2681 review finding: a non-nil ClosureGeneration with an empty
// Generation must fall back to the baked nixVarSnapshotDir. Overlaying the
// bare nixVarSnapshotRoot would pick the container of every generation dir,
// which holds no db.sqlite of its own.
func TestBwrapArgs_ClosureGenerationEmptyGenerationFallsBackToBaked(t *testing.T) {
	a := &bwrapAdapter{
		agentFiles:         "/fake/agent",
		agentEnv:           "/fake/env",
		nixConfigFile:      "/nix/store/fake-hash-nix-conf/nix.conf",
		nixVarSnapshotDir:  "/fake/pwd/.spindrift/nix-var-snapshot/gen1",
		nixVarSnapshotRoot: "/fake/pwd/.spindrift/nix-var-snapshot",
	}
	box := Box{
		Env:               map[string]string{},
		ClosureGeneration: &AgentGeneration{AgentFiles: "/gen2/agent-files"},
	}
	args := a.buildArgs("/tmp/fake-etc", box)

	argStr := strings.Join(args, " ")
	if !strings.Contains(argStr, "--overlay-src /fake/pwd/.spindrift/nix-var-snapshot/gen1 --tmp-overlay /nix/var") {
		t.Errorf("expected fallback --overlay-src /fake/pwd/.spindrift/nix-var-snapshot/gen1 --tmp-overlay /nix/var in args: %v", args)
	}
	if strings.Contains(argStr, "--overlay-src /fake/pwd/.spindrift/nix-var-snapshot --tmp-overlay") {
		t.Errorf("bare nixVarSnapshotRoot bound as overlay-src despite empty ClosureGeneration.Generation: %v", args)
	}
}

// Issue #2681 review finding: a Generation of ".." would resolve one level
// above nixVarSnapshotRoot through a raw filepath.Join, so it is rejected the
// same way closureGeneration rejects an unsafe imageTag-derived label and
// falls back to the baked nixVarSnapshotDir.
func TestBwrapArgs_ClosureGenerationUnsafeGenerationFallsBackToBaked(t *testing.T) {
	a := &bwrapAdapter{
		agentFiles:         "/fake/agent",
		agentEnv:           "/fake/env",
		nixConfigFile:      "/nix/store/fake-hash-nix-conf/nix.conf",
		nixVarSnapshotDir:  "/fake/pwd/.spindrift/nix-var-snapshot/gen1",
		nixVarSnapshotRoot: "/fake/pwd/.spindrift/nix-var-snapshot",
	}
	box := Box{
		Env:               map[string]string{},
		ClosureGeneration: &AgentGeneration{AgentFiles: "/gen2/agent-files", Generation: ".."},
	}
	args := a.buildArgs("/tmp/fake-etc", box)

	argStr := strings.Join(args, " ")
	if !strings.Contains(argStr, "--overlay-src /fake/pwd/.spindrift/nix-var-snapshot/gen1 --tmp-overlay /nix/var") {
		t.Errorf("expected fallback --overlay-src /fake/pwd/.spindrift/nix-var-snapshot/gen1 --tmp-overlay /nix/var in args: %v", args)
	}
	for _, arg := range args {
		if strings.Contains(arg, "/nix-var-snapshot/..") {
			t.Errorf("unsafe Generation \"..\" escaped nixVarSnapshotRoot: %v", args)
		}
	}
}

// Both nix mounts are gated on nixConfigFile alone: even with a non-empty
// nixVarSnapshotDir, which production always computes (ADR 0042), a zero-value
// nixConfigFile (nixInBox off) skips them.
func TestBwrapArgs_NoNixMountsWhenNixConfigFileEmpty(t *testing.T) {
	a := &bwrapAdapter{
		agentFiles:        "/fake/agent",
		agentEnv:          "/fake/env",
		nixVarSnapshotDir: "/fake/pwd/.spindrift/nix-var-snapshot",
	}
	args := a.buildArgs("/tmp/fake-etc", Box{Env: map[string]string{}})

	argStr := strings.Join(args, " ")
	for _, unwanted := range []string{"/etc/nix/nix.conf", "--overlay-src", "/nix/var"} {
		if strings.Contains(argStr, unwanted) {
			t.Errorf("unexpected %q in args when nixConfigFile is empty: %v", unwanted, args)
		}
	}
}

// Off by default (ADR 0042, issue #2665): nixConfigFile set but
// nixStoreWritable false leaves /nix/store a plain --ro-bind, never overlaid.
func TestBwrapArgs_StoreReadOnlyBindWhenNotWritable(t *testing.T) {
	a := &bwrapAdapter{
		agentFiles:        "/fake/agent",
		agentEnv:          "/fake/env",
		nixConfigFile:     "/nix/store/fake/nix.conf",
		nixVarSnapshotDir: "/fake/snap",
		nixStoreWritable:  false,
	}
	args := a.buildArgs("/tmp/fake-etc", Box{Env: map[string]string{}})

	if !wantTriple(args, "--ro-bind", "/nix/store", "/nix/store") {
		t.Errorf("expected --ro-bind /nix/store /nix/store in args: %v", args)
	}
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--overlay-src" && args[i+1] == "/nix/store" {
			t.Errorf("/nix/store must not be overlaid when nixStoreWritable is false: %v", args)
		}
	}
}

// nixConfigFile set and nixStoreWritable true render /nix/store as an
// ephemeral tmpfs overlay instead of a plain read-only bind (ADR 0042, issue
// #2665).
func TestBwrapArgs_StoreOverlayWhenWritable(t *testing.T) {
	a := &bwrapAdapter{
		agentFiles:        "/fake/agent",
		agentEnv:          "/fake/env",
		nixConfigFile:     "/nix/store/fake/nix.conf",
		nixVarSnapshotDir: "/fake/snap",
		nixStoreWritable:  true,
	}
	args := a.buildArgs("/tmp/fake-etc", Box{Env: map[string]string{}})

	if !strings.Contains(strings.Join(args, " "), "--overlay-src /nix/store --tmp-overlay /nix/store") {
		t.Errorf("expected --overlay-src /nix/store --tmp-overlay /nix/store in args: %v", args)
	}
	if wantTriple(args, "--ro-bind", "/nix/store", "/nix/store") {
		t.Errorf("/nix/store must not be plain read-only bound when nixStoreWritable is true: %v", args)
	}
}

// The AND-gate is real: nixStoreWritable alone, with nixConfigFile empty and
// so nixInBox off, must not trigger the overlay, since nix is not even on PATH
// in the Box then.
func TestBwrapArgs_StoreReadOnlyWhenConfigFileEmptyEvenIfWritable(t *testing.T) {
	a := &bwrapAdapter{
		agentFiles:       "/fake/agent",
		agentEnv:         "/fake/env",
		nixStoreWritable: true,
	}
	args := a.buildArgs("/tmp/fake-etc", Box{Env: map[string]string{}})

	if !wantTriple(args, "--ro-bind", "/nix/store", "/nix/store") {
		t.Errorf("expected --ro-bind /nix/store /nix/store in args: %v", args)
	}
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--overlay-src" && args[i+1] == "/nix/store" {
			t.Errorf("/nix/store must not be overlaid when nixConfigFile is empty, even if nixStoreWritable is true: %v", args)
		}
	}
}

// Non-secret env vars still reach the sandbox through --setenv, so they do
// appear in argv.
func TestBwrapArgs_NonSecretOnArgv(t *testing.T) {
	a := &bwrapAdapter{
		agentFiles:    "/fake/agent",
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
	}
	box := Box{
		Env: map[string]string{
			"GH_TOKEN":     "gh-secret-value",
			"REPO_SLUG":    "owner/repo",
			"ISSUE_NUMBER": "42",
		},
	}

	args := a.buildArgs("/tmp/fake-etc", box)

	argStr := strings.Join(args, " ")
	for _, name := range []string{"REPO_SLUG", "ISSUE_NUMBER"} {
		if !strings.Contains(argStr, name) {
			t.Errorf("non-secret %q missing from bwrap argv", name)
		}
	}
}

// Issue #2670: bwrap reads the compiled BPF filter off fd 3, the one entry the
// adapter's Run ever adds to cmd.ExtraFiles.
func TestBwrapArgs_SyscallFilterFlagWhenSet(t *testing.T) {
	a := &bwrapAdapter{
		agentFiles:        "/fake/agent",
		agentEnv:          "/fake/env",
		syscallFilterPath: "/nix/store/fake-hash-seccomp/filter.bpf",
	}
	args := a.buildArgs("/tmp/fake-etc", Box{Env: map[string]string{}})

	if !strings.Contains(strings.Join(args, " "), "--seccomp 3") {
		t.Errorf("expected --seccomp 3 in args: %v", args)
	}
}

// Regression guard: a zero-value syscallFilterPath must never render --seccomp
// at all, matching the empty-knob-disables convention used throughout this
// file (nixConfigFile, for one).
func TestBwrapArgs_NoSyscallFilterFlagWhenEmpty(t *testing.T) {
	a := &bwrapAdapter{
		agentFiles: "/fake/agent",
		agentEnv:   "/fake/env",
	}
	args := a.buildArgs("/tmp/fake-etc", Box{Env: map[string]string{}})

	for _, arg := range args {
		if arg == "--seccomp" {
			t.Errorf("unexpected --seccomp in args when syscallFilterPath is empty: %v", args)
		}
	}
}

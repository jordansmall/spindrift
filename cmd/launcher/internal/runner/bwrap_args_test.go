package runner

import (
	"strings"
	"testing"
)

// TestBwrapArgs_NoSecretOnArgv verifies that secret env var values are not
// passed as bwrap command-line arguments (which would expose them via ps/proc).
func TestBwrapArgs_NoSecretOnArgv(t *testing.T) {
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
			"REPO_SLUG":               "owner/repo",
			"ISSUE_NUMBER":            "42",
		},
	}

	args := a.buildArgs("/tmp/fake-etc", box)

	secrets := []string{"gh-secret-value", "claude-secret-value", "anthropic-secret-value"}
	for _, arg := range args {
		for _, secret := range secrets {
			if strings.Contains(arg, secret) {
				t.Errorf("secret value %q found in bwrap argv: %v", secret, args)
			}
		}
	}
}

// TestBwrapArgs_NoClearEnv verifies that --clearenv is not in the args so that
// the sandbox inherits secrets from the launcher's process environment.
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

// TestBwrapArgs_SkillsDirMounted verifies that a valid SPINDRIFT_SKILLS_DIR
// produces a --ro-bind entry for the fixed operator-override staging path
// /operator-skills (issue #2489) — entrypoint.sh merges it into the real
// Driver skills dir at box startup, rather than bwrap.go binding directly
// onto the Driver's declared skills dir.
func TestBwrapArgs_SkillsDirMounted(t *testing.T) {
	dir := t.TempDir()
	a := &bwrapAdapter{
		agentFiles:    "/fake/agent",
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
		skillsDir:     dir,
	}
	args := a.buildArgs("/tmp/fake-etc", Box{Env: map[string]string{}})

	argStr := strings.Join(args, " ")
	want := "--ro-bind " + dir + " /operator-skills"
	if !strings.Contains(argStr, want) {
		t.Errorf("skills bind %q not found in args: %v", want, args)
	}
}

// TestBwrapArgs_RegistryProxySocketMounted verifies that a Box-derived
// RegistryProxySocketPath produces a --bind <source> /registry-proxy.sock
// entry (ADR 0044, issue #2849).
func TestBwrapArgs_RegistryProxySocketMounted(t *testing.T) {
	sock := newTestSocket(t, "registry-proxy.sock")
	a := &bwrapAdapter{
		agentFiles:    "/fake/agent",
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
	}
	args := a.buildArgs("/tmp/fake-etc", Box{Env: map[string]string{}, RegistryProxySocketPath: sock})

	argStr := strings.Join(args, " ")
	want := "--bind " + sock + " /registry-proxy.sock"
	if !strings.Contains(argStr, want) {
		t.Errorf("registry-proxy socket bind %q not found in args: %v", want, args)
	}
}

// TestBwrapArgs_HomeAgentStagingMounted verifies that buildArgs ro-binds the
// baked agentFiles' /home/agent subtree (hooks, settings.json, opencode agent
// files) to a fixed top-level staging path, rather than exposing it nowhere
// in the sandbox (issue #2843). It must not nest under /agent: /agent is
// already bound read-only by the time this mount is added, and bwrap cannot
// fabricate a new mountpoint inside an existing read-only bind. The real
// /home/agent stays a fresh writable tmpfs (see the --tmpfs /home/agent line
// above in buildArgs); entrypoint.sh is responsible for copying this staged
// content into it at startup.
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

// TestBwrapArgs_AccountFilesBindStorePaths verifies that buildArgs binds
// /etc/passwd and /etc/group from the nix-sourced store paths carried on the
// adapter (issue #2663), rather than a runner-written temp-dir copy — the
// fake paths below deliberately look like real nix store paths.
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

// stubResolvConfPresent forces statResolvConf to report /etc/resolv.conf as
// present, independent of whether the test host actually has one (some nix
// build sandboxes don't). Returns a restore func to defer.
func stubResolvConfPresent() func() {
	prev := statResolvConf
	statResolvConf = func() error { return nil }
	return func() { statResolvConf = prev }
}

// TestBwrapArgs_NetworkModeNoneUnsharesNet verifies that networkMode="none"
// stays fully helper-free: --unshare-net is appended, the /etc/resolv.conf
// bind is skipped, and — unlike every other isolating mode since issue
// #2666 — pasta is absent too, so the sandbox has no egress at all (used by
// build-time no-network probes; a Driver can't reach its Provider under
// it, documented elsewhere). See
// TestBwrapArgs_NetworkModeNoHostLoopbackDefaultsToIsolate for how the
// no-host-loopback fail-open hazard this test's sibling used to document is
// now closed by the same default-isolate change.
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

// assertPastaExecTarget checks that a.execTarget(etcDir, box) returns
// program "pasta" with args = pastaHardenedFlags, then "--dns-forward",
// pastaDNSForwardAddr, "-f", "--", "bwrap", then a.buildArgs' own output
// verbatim -- the exact composition order the pasta manual requires: pasta
// is the outer process that creates/configures the namespace before execing
// its COMMAND (bwrap) inside it (issue #2666).
func assertPastaExecTarget(t *testing.T, a *bwrapAdapter, etcDir string, box Box) {
	t.Helper()
	bwrapArgs := a.buildArgs(etcDir, box)
	program, args := a.execTarget(etcDir, box)

	if program != "pasta" {
		t.Fatalf("execTarget program = %q, want %q", program, "pasta")
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

// assertBareBwrapExecTarget checks that a.execTarget(etcDir, box) returns
// program "bwrap" with args identical to a.buildArgs' own output -- the
// non-isolating ("host") and fully-offline ("none") cases, neither of which
// involves pasta.
func assertBareBwrapExecTarget(t *testing.T, a *bwrapAdapter, etcDir string, box Box) {
	t.Helper()
	bwrapArgs := a.buildArgs(etcDir, box)
	program, args := a.execTarget(etcDir, box)

	if program != "bwrap" {
		t.Fatalf("execTarget program = %q, want %q", program, "bwrap")
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

// TestBwrapArgs_NetworkModeOpenOmitsBwrapSideUnshareNet verifies that
// networkMode="open" no longer unshares net or wraps with pasta inside
// buildArgs' own return (issue #2666 review finding): pasta must be the
// *outer* process that creates and configures the fresh network namespace
// before bwrap ever runs, so bwrap has to inherit that namespace rather than
// unsharing a second, empty one on top of it. See
// TestExecTarget_NetworkModeOpenWrapsWithPasta for where the pasta
// composition is actually asserted, and buildArgs' own comment for why the
// namespace is still effectively isolated even though buildArgs itself adds
// no --unshare-net here. The synthesized <etcDir>/resolv.conf ro-bind
// (pointing at pasta's --dns-forward address) is asserted here since it's a
// buildArgs-level decision, not an execTarget one.
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

// TestBwrapArgs_NetworkModeNoHostLoopbackDefaultsToIsolate is a
// characterization test (issue #2562 review finding, closed by issue
// #2666): it proves that if networkMode="no-host-loopback" were ever
// constructed directly against a bwrapAdapter -- bypassing main.go's
// checkNetworkModeRuntimeGate, which is what actually prevents this
// combination from reaching the adapter in practice -- the sandbox still
// ends up isolated from the host netns with working pasta-wrapped egress
// (see TestExecTarget_NetworkModeNoHostLoopbackWrapsWithPasta), the same as
// every other mode except the explicit "host" opt-out, rather than silently
// falling open to the shared host netns as it did before #2666.
// networkMode="no-host-loopback" still never legitimately reaches bwrap in
// production (nix eval + checkNetworkModeRuntimeGate), so this remains
// defense-in-depth characterization, not a new supported mode.
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

// TestBwrapArgs_NetworkModeUnsetDefaultsToIsolate verifies the literal
// "by default a Box has its own network namespace and working egress"
// acceptance criterion for issue #2666: leaving networkMode at its Go zero
// value (unset, matching most callers of NewBwrap/Config today) isolates
// and pasta-wraps the same as an explicit "open" (see
// TestExecTarget_NetworkModeUnsetWrapsWithPasta).
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

// TestBwrapArgs_NetworkModeHostSharesHostNetns verifies the documented
// opt-out (issue #2666): networkMode="host" restores the pre-#2666
// shared-host-netns behavior — no --unshare-net, no pasta, and the
// /etc/resolv.conf bind restored (there's no isolated netns to supply DNS
// for).
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

// TestBwrapArgs_UnshareNetKnobOmitsBwrapSideUnshareNet verifies that the raw
// BwrapUnshareNet knob (networkMode left unset) now renders through the same
// pasta path as the default (see
// TestExecTarget_UnshareNetKnobWrapsWithPasta), rather than the old bare
// --unshare-net with no helper that left the sandbox with no DNS/egress
// (issue #2666) — and that buildArgs' own return omits --unshare-net/pasta
// here for the same inside-out-composition reason as the other isolating
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

// TestExecTarget_NetworkModeOpenWrapsWithPasta verifies that execTarget
// wraps bwrap with pasta as the outer process for networkMode="open" (issue
// #2666, ADR 0042): program is "pasta", its args are pastaHardenedFlags then
// --dns-forward/pastaDNSForwardAddr/-f/--/bwrap, then buildArgs' own output
// verbatim.
func TestExecTarget_NetworkModeOpenWrapsWithPasta(t *testing.T) {
	a := &bwrapAdapter{
		agentFiles:    "/fake/agent",
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
		networkMode:   "open",
	}
	assertPastaExecTarget(t, a, "/tmp/fake-etc", Box{Env: map[string]string{}})
}

// TestExecTarget_NetworkModeNoHostLoopbackWrapsWithPasta is the
// execTarget-level half of TestBwrapArgs_NetworkModeNoHostLoopbackDefaultsToIsolate:
// networkMode="no-host-loopback" gets the same pasta-wrapped exec target as
// every other isolating mode.
func TestExecTarget_NetworkModeNoHostLoopbackWrapsWithPasta(t *testing.T) {
	a := &bwrapAdapter{
		agentFiles:    "/fake/agent",
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
		networkMode:   "no-host-loopback",
	}
	assertPastaExecTarget(t, a, "/tmp/fake-etc", Box{Env: map[string]string{}})
}

// TestExecTarget_NetworkModeUnsetWrapsWithPasta is the execTarget-level half
// of TestBwrapArgs_NetworkModeUnsetDefaultsToIsolate: the Go zero value for
// networkMode gets the same pasta-wrapped exec target as an explicit "open".
func TestExecTarget_NetworkModeUnsetWrapsWithPasta(t *testing.T) {
	a := &bwrapAdapter{
		agentFiles:    "/fake/agent",
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
	}
	assertPastaExecTarget(t, a, "/tmp/fake-etc", Box{Env: map[string]string{}})
}

// TestExecTarget_UnshareNetKnobWrapsWithPasta is the execTarget-level half of
// TestBwrapArgs_UnshareNetKnobOmitsBwrapSideUnshareNet: the raw
// BwrapUnshareNet knob gets the same pasta-wrapped exec target as the
// default.
func TestExecTarget_UnshareNetKnobWrapsWithPasta(t *testing.T) {
	a := &bwrapAdapter{
		agentFiles:    "/fake/agent",
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
		unshareNet:    true,
	}
	assertPastaExecTarget(t, a, "/tmp/fake-etc", Box{Env: map[string]string{}})
}

// TestExecTarget_NetworkModeHostReturnsBareBwrap verifies execTarget's
// non-pasta branch for the "host" opt-out: program "bwrap", args identical to
// buildArgs' own output.
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

// TestExecTarget_NetworkModeNoneReturnsBareBwrap verifies execTarget's
// non-pasta branch for the fully-offline "none" mode: program "bwrap", args
// identical to buildArgs' own output (bare --unshare-net, no pasta, no
// egress at all).
func TestExecTarget_NetworkModeNoneReturnsBareBwrap(t *testing.T) {
	a := &bwrapAdapter{
		agentFiles:    "/fake/agent",
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
		networkMode:   "none",
	}
	assertBareBwrapExecTarget(t, a, "/tmp/fake-etc", Box{Env: map[string]string{}})
}

// TestBwrapArgs_SkillsMountTarget_FromDriverDeclaration is gone (issue
// #2489): the operator-override skills mount now always lands at the fixed
// /operator-skills staging path (see operatorSkillsDir in mount.go),
// independent of the Driver's declared skills dir, so there is no longer a
// driver-declaration-driven mount target for this test to exercise.

// TestBwrapArgs_IssuesDirMounted verifies that ISSUE_TRACKER=local plus a
// resolved localIssuesDir renders a top-level --ro-bind /issues entry (issue
// #1691, ADR 0032) — top-level, not nested under /agent, since bwrap cannot
// fabricate a mountpoint inside its read-only /agent store bind.
func TestBwrapArgs_IssuesDirMounted(t *testing.T) {
	dir := t.TempDir()
	a := &bwrapAdapter{
		agentFiles:               "/fake/agent",
		agentEnv:                 "/fake/env",
		bakedPrefetch:            "echo ok",
		hostMediatedIssueTracker: true,
		localIssuesDir:           dir,
	}
	args := a.buildArgs("/tmp/fake-etc", Box{Env: map[string]string{}})

	argStr := strings.Join(args, " ")
	want := "--ro-bind " + dir + " /issues"
	if !strings.Contains(argStr, want) {
		t.Errorf("issues bind %q not found in args: %v", want, args)
	}
	if strings.Contains(argStr, "/agent/issues") {
		t.Errorf("issues mount must not nest under /agent: %v", args)
	}
}

// TestBwrapArgs_IssuesDirUnset_NoMount verifies that a non-local tracker never
// renders an /issues bind.
func TestBwrapArgs_IssuesDirUnset_NoMount(t *testing.T) {
	dir := t.TempDir()
	a := &bwrapAdapter{
		agentFiles:               "/fake/agent",
		agentEnv:                 "/fake/env",
		bakedPrefetch:            "echo ok",
		hostMediatedIssueTracker: false,
		localIssuesDir:           dir,
	}
	args := a.buildArgs("/tmp/fake-etc", Box{Env: map[string]string{}})

	argStr := strings.Join(args, " ")
	if strings.Contains(argStr, "/issues") {
		t.Errorf("unexpected /issues bind for a non-local tracker: %v", args)
	}
}

// TestBwrapArgs_DriverCacheDirMountedWritable verifies that a Box.DriverCacheDir
// produces a writable --bind (not --ro-bind) entry for
// /home/agent/.claude/projects.
func TestBwrapArgs_DriverCacheDirMountedWritable(t *testing.T) {
	dir := t.TempDir()
	a := &bwrapAdapter{
		agentFiles:            "/fake/agent",
		agentEnv:              "/fake/env",
		bakedPrefetch:         "echo ok",
		driverSessionCacheDir: "/home/agent/.claude/projects",
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

// TestBwrapArgs_DriverCacheDirMounted_HardeningPreserved verifies that the
// writable driver-cache bind does not disturb the unshare/uid hardening
// flags bwrap always applies.
func TestBwrapArgs_DriverCacheDirMounted_HardeningPreserved(t *testing.T) {
	dir := t.TempDir()
	a := &bwrapAdapter{
		agentFiles:            "/fake/agent",
		agentEnv:              "/fake/env",
		bakedPrefetch:         "echo ok",
		driverSessionCacheDir: "/home/agent/.claude/projects",
	}
	args := a.buildArgs("/tmp/fake-etc", Box{Env: map[string]string{}, DriverCacheDir: dir})

	for _, flag := range []string{"--unshare-user", "--unshare-pid", "--unshare-ipc", "--unshare-uts"} {
		if !containsArg(args, flag) {
			t.Errorf("writable driver cache bind must not weaken hardening; missing %q in args: %v", flag, args)
		}
	}
}

// TestBwrapArgs_DriverCacheDir_DotClaudeParentCreated verifies that a
// --dir /home/agent/.claude appears before the driver-cache bind so the
// parent directory is agent-owned in the tmpfs rather than fabricated as
// root by bwrap's bind-target auto-creation (issue #447).
func TestBwrapArgs_DriverCacheDir_DotClaudeParentCreated(t *testing.T) {
	dir := t.TempDir()
	a := &bwrapAdapter{
		agentFiles:            "/fake/agent",
		agentEnv:              "/fake/env",
		bakedPrefetch:         "echo ok",
		driverSessionCacheDir: "/home/agent/.claude/projects",
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

// TestBwrapArgs_DriverCacheMountTarget_FromDriverDeclaration verifies the
// box-side session-cache bind target, and the --dir parent it creates first,
// come from the adapter's driverSessionCacheDir field (populated by the
// Driver declaration, ADR 0009) rather than a hardcoded ".claude/projects"
// literal.
func TestBwrapArgs_DriverCacheMountTarget_FromDriverDeclaration(t *testing.T) {
	dir := t.TempDir()
	a := &bwrapAdapter{
		agentFiles:            "/fake/agent",
		agentEnv:              "/fake/env",
		bakedPrefetch:         "echo ok",
		driverSessionCacheDir: "/home/agent/custom-driver/state",
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

// TestBwrapArgs_DriverSessionCacheDirUndeclared_NoMount verifies that a
// Driver declaring no session-state dir yields no cache bind even when a
// host DriverCacheDir is present -- there is no in-box target to bind it
// over (issue #448).
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

// TestBwrapArgs_DriverCacheDirUnset_NoMount verifies that omitting
// Box.DriverCacheDir produces no /home/agent/.claude/projects bind.
func TestBwrapArgs_DriverCacheDirUnset_NoMount(t *testing.T) {
	a := &bwrapAdapter{
		agentFiles:            "/fake/agent",
		agentEnv:              "/fake/env",
		bakedPrefetch:         "echo ok",
		driverSessionCacheDir: "/home/agent/.claude/projects",
	}
	args := a.buildArgs("/tmp/fake-etc", Box{Env: map[string]string{}})
	argStr := strings.Join(args, " ")
	if strings.Contains(argStr, "/home/agent/.claude/projects") {
		t.Errorf("unexpected driver cache bind in args when DriverCacheDir is empty: %v", args)
	}
}

// TestBwrapArgs_SkillsDirUnset_NoMount verifies that omitting skillsDir
// produces no skills bind in the bwrap args.
func TestBwrapArgs_SkillsDirUnset_NoMount(t *testing.T) {
	a := &bwrapAdapter{
		agentFiles:    "/fake/agent",
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
		skillsDir:     "",
	}
	args := a.buildArgs("/tmp/fake-etc", Box{Env: map[string]string{}})
	argStr := strings.Join(args, " ")
	if strings.Contains(argStr, ".claude/skills") {
		t.Errorf("unexpected skills bind in args when skillsDir is empty: %v", args)
	}
}

// TestBwrapArgs_BakedSkillsMounted, TestBwrapArgs_RuntimeSkillsTakePrecedence,
// and TestBwrapArgs_SkillsDirInvalid_NoFallback are gone (issue #2489): they
// covered bwrap.go's baked-skills-fallback bind (agentFiles' own
// .claude/skills re-bound when skillsDir was unset), which has been deleted.
// Baked skills now reach the box via the existing top-level /agent ro-bind
// plus entrypoint.sh's own copy-into-DRIVER_SKILLS_DIR step at box startup,
// not a bwrap.go-issued mount, so there is nothing left in this adapter for
// these tests to exercise; TestBwrapArgs_SkillsDirUnset_NoMount above already
// covers "no skills bind when skillsDir is empty".

// TestBwrapArgs_MountsNixConfigAndStoreDBSnapshotWhenSet verifies that a
// non-empty nixConfigFile (ADR 0042: nixInBox knob on) renders both the
// nix.conf ro-bind and the store-DB snapshot overlay onto /nix/var.
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

// TestBwrapArgs_NoNixMountsWhenNixConfigFileEmpty verifies that the nix.conf
// and store-DB snapshot mounts are gated on nixConfigFile alone: even with a
// non-empty nixVarSnapshotDir (as production always computes, ADR 0042),
// leaving nixConfigFile at its zero value (nixInBox off) skips both mounts.
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

// TestBwrapArgs_StoreReadOnlyBindWhenNotWritable pins the off-by-default
// behavior (ADR 0042, issue #2665): with nixConfigFile set but
// nixStoreWritable explicitly false, /nix/store stays a plain, unconditional
// --ro-bind, never overlaid.
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

// TestBwrapArgs_StoreOverlayWhenWritable verifies that nixConfigFile set AND
// nixStoreWritable true renders /nix/store as an ephemeral tmpfs overlay
// (ADR 0042, issue #2665) instead of a plain read-only bind.
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

// TestBwrapArgs_StoreReadOnlyWhenConfigFileEmptyEvenIfWritable proves the
// AND-gate is real: nixStoreWritable alone (with nixConfigFile empty, i.e.
// nixInBox off) must not trigger the overlay, since nix isn't even on PATH
// in the Box in that case.
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

// TestBwrapArgs_NonSecretOnArgv verifies that non-secret env vars still reach
// the sandbox via --setenv (so they appear in argv).
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

// TestBwrapArgs_SyscallFilterFlagWhenSet verifies that a non-empty
// syscallFilterPath renders --seccomp 3 (issue #2670): bwrap reads the
// compiled BPF filter off fd 3, the one entry the adapter's Run ever adds to
// cmd.ExtraFiles.
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

// TestBwrapArgs_NoSyscallFilterFlagWhenEmpty is a regression guard: leaving
// syscallFilterPath at its zero value must never render --seccomp at all,
// matching the empty-knob-disables convention used throughout this file
// (e.g. nixConfigFile).
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

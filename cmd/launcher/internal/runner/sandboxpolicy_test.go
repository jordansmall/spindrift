package runner

import (
	"slices"
	"testing"

	"spindrift.dev/launcher/internal/runner/runnertest"
)

func goodSandboxArgv() []string {
	return []string{
		"run", "-i", "--rm", "--name", ConnectorSandboxPrefix + "x", "--network=none",
		"--cap-drop=all", "--security-opt=no-new-privileges", "--read-only", "--pull=never",
		"--pids-limit=128", "--memory=512m", runnertest.SandboxImageID,
	}
}

func TestSandboxViolations_FlagsAnUnpinnedOrPullableImage(t *testing.T) {
	good := goodSandboxArgv()
	for name, argv := range map[string][]string{
		"no never-pull":      slices.DeleteFunc(slices.Clone(good), func(a string) bool { return a == "--pull=never" }),
		"later pull=always":  slices.Insert(slices.Clone(good), len(good)-1, "--pull=always"),
		"later pull=missing": slices.Insert(slices.Clone(good), len(good)-1, "--pull=missing"),
		"bare --pull":        slices.Insert(slices.Clone(good), len(good)-1, "--pull"),
		"image by tag":       slices.Replace(slices.Clone(good), len(good)-1, len(good), "img:tag"),
	} {
		if got := SandboxViolations(argv); len(got) == 0 {
			t.Errorf("%s: SandboxViolations(%v) = none, want a violation", name, argv)
		}
	}
	if got := SandboxViolations(slices.Replace(slices.Clone(good), len(good)-1, len(good), "reg.io/img@"+runnertest.SandboxImageID)); len(got) != 0 {
		t.Errorf("SandboxViolations(name@digest) = %v, want none", got)
	}
}

func TestSandboxViolations_AcceptsTheIsolatedArgv(t *testing.T) {
	if got := SandboxViolations(goodSandboxArgv()); len(got) != 0 {
		t.Errorf("SandboxViolations(good argv) = %v, want none", got)
	}
}

func TestSandboxViolations_FlagsForbiddenForms(t *testing.T) {
	for _, extra := range [][]string{
		{"-t"}, {"-it"}, {"-eFOO=bar"}, {"-vX:Y"}, {"-p80:80"}, {"--env=FOO=bar"}, {"--env", "FOO=bar"},
		{"--device=/dev/fuse"}, {"--publish=80:80"}, {"--pid=host"}, {"--ipc=host"}, {"--userns=host"},
		{"--network=host"}, {"--security-opt=seccomp=unconfined"},
	} {
		argv := slices.Insert(goodSandboxArgv(), 1, extra...)
		if got := SandboxViolations(argv); len(got) == 0 {
			t.Errorf("SandboxViolations with %v = none, want a violation", extra)
		}
	}
}

const testConnectorHost = "/nix/store/aaa-connector-host"

func goodBwrapSandboxArgv() []string {
	return []string{
		"--unshare-all", "--clearenv", "--cap-drop", "ALL", "--new-session", "--die-with-parent",
		"--proc", "/proc", "--dev", "/dev",
		"--ro-bind", testConnectorHost, testConnectorHost,
		"--remount-ro", "/",
		"--seccomp", "3", "--", testConnectorHost + "/bin/connector-host",
	}
}

func seccompFDIndex() int {
	return slices.Index(goodBwrapSandboxArgv(), "--seccomp") + 1
}

func TestBwrapSandboxViolations_AcceptsTheIsolatedArgv(t *testing.T) {
	if got := BwrapSandboxViolations(goodBwrapSandboxArgv(), testConnectorHost); len(got) != 0 {
		t.Errorf("BwrapSandboxViolations(good argv) = %v, want none", got)
	}
}

func TestBwrapSandboxViolations_FlagsDepartures(t *testing.T) {
	without := func(drop ...string) []string {
		var out []string
		for _, a := range goodBwrapSandboxArgv() {
			if !slices.Contains(drop, a) {
				out = append(out, a)
			}
		}
		return out
	}
	for name, argv := range map[string][]string{
		"no unshare-all":       without("--unshare-all"),
		"no clearenv":          without("--clearenv"),
		"no cap-drop":          without("--cap-drop", "ALL"),
		"cap-drop not ALL":     slices.Replace(goodBwrapSandboxArgv(), 3, 4, "NET_ADMIN"),
		"no new-session":       without("--new-session"),
		"no die-with-parent":   without("--die-with-parent"),
		"no proc":              without("--proc"),
		"no dev":               without("--dev"),
		"no seccomp":           without("--seccomp", "3"),
		"no host bind":         without("--ro-bind"),
		"writable root":        without("--remount-ro", "/"),
		"wrong exec target":    append(goodBwrapSandboxArgv()[:len(goodBwrapSandboxArgv())-1], "/bin/sh"),
		"whole store":          slices.Insert(goodBwrapSandboxArgv(), 0, "--ro-bind", "/nix/store", "/nix/store"),
		"writable bind":        slices.Insert(goodBwrapSandboxArgv(), 0, "--bind", "/tmp", "/tmp"),
		"dev bind":             slices.Insert(goodBwrapSandboxArgv(), 0, "--dev-bind", "/dev", "/dev"),
		"try bind":             slices.Insert(goodBwrapSandboxArgv(), 0, "--ro-bind-try", "/etc", "/etc"),
		"overlay":              slices.Insert(goodBwrapSandboxArgv(), 0, "--overlay-src", "/a"),
		"tmp overlay":          slices.Insert(goodBwrapSandboxArgv(), 0, "--tmp-overlay", "/a"),
		"setenv":               slices.Insert(goodBwrapSandboxArgv(), 0, "--setenv", "TOKEN", "x"),
		"share-net":            slices.Insert(goodBwrapSandboxArgv(), 0, "--share-net"),
		"seccomp other fd":     slices.Replace(goodBwrapSandboxArgv(), seccompFDIndex(), seccompFDIndex()+1, "4"),
		"args from fd":         slices.Insert(goodBwrapSandboxArgv(), 0, "--args", "4"),
		"bare whole store":     slices.Insert(goodBwrapSandboxArgv(), 0, "/nix/store"),
		"cap-add":              slices.Insert(goodBwrapSandboxArgv(), 0, "--cap-add", "ALL"),
		"tmpfs before remount": slices.Insert(goodBwrapSandboxArgv(), slices.Index(goodBwrapSandboxArgv(), "--remount-ro"), "--tmpfs", "/x"),
		"dir before remount":   slices.Insert(goodBwrapSandboxArgv(), slices.Index(goodBwrapSandboxArgv(), "--remount-ro"), "--dir", "/x"),
	} {
		if got := BwrapSandboxViolations(argv, testConnectorHost); len(got) == 0 {
			t.Errorf("%s: BwrapSandboxViolations(%v) = none, want a violation", name, argv)
		}
	}
}

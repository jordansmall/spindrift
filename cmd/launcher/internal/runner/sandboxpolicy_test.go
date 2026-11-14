package runner

import (
	"slices"
	"testing"
)

func goodSandboxArgv() []string {
	return []string{
		"run", "-i", "--rm", "--name", ConnectorSandboxPrefix + "x", "--network=none",
		"--cap-drop=all", "--security-opt=no-new-privileges", "--read-only",
		"--pids-limit=128", "--memory=512m", "img:tag",
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

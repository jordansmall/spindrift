//go:build integration

package main

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/inputdoc"
	"spindrift.dev/launcher/internal/seamtest"
)

// The smoke matrix drives the real launcher binary with a rendered input
// document, faking only the tools it shells out to, and checks that the
// runtime and the forge each receive what the document specifies. One test
// per distinguishing path: dispatch kind x container runtime.
//
// The expectations come from the document and the recorded argv alone, via
// checkRuntimeArgv and checkForgeCalls, which return problems instead of
// failing a test: a caller can run the launcher on a different document than
// the one it takes expectations from and assert the checks notice.

const (
	smokeRepo = "owner/repo"
	smokePR   = "https://github.com/owner/repo/pull/9"
)

// smokeRuntimeSettings are the values the document leaves for run time; they
// win over the document when a check compares a forwarded env value.
var smokeRuntimeSettings = map[string]string{
	"REPO_SLUG": smokeRepo, "BOX_SIGNAL_CARRIER": "log",
	"GIT_USER_NAME": "Seam Bot", "GIT_USER_EMAIL": "seam@example.com",
}

// smokeSecrets are exported to the launcher's own environment; none may land
// on a runner argv.
var smokeSecrets = map[string]string{"GH_TOKEN": "fake-token", "CLAUDE_CODE_OAUTH_TOKEN": "fake-oauth"}

// argvEnv scans a runner argv for the env it hands the Box. flag is "-e"
// (OCI: NAME=value, or a bare NAME for a secret taken from the launcher's own
// environment, recorded as nil) or "--setenv" (bwrap: NAME VALUE pairs).
// mounts maps each bind target to its source.
func argvEnv(argv []string, flag string) (env map[string]*string, mounts map[string]string) {
	env, mounts = map[string]*string{}, map[string]string{}
	for i := 0; i < len(argv); i++ {
		switch a := argv[i]; {
		case a == "-e" && flag == "-e" && i+1 < len(argv):
			if k, v, ok := strings.Cut(argv[i+1], "="); ok {
				env[k] = &v
			} else {
				env[k] = nil
			}
			i++
		case a == flag && flag == "--setenv" && i+2 < len(argv):
			v := argv[i+2]
			env[argv[i+1]] = &v
			i += 2
		case a == "-v" && i+1 < len(argv):
			src, rest, _ := strings.Cut(argv[i+1], ":")
			target, _, _ := strings.Cut(rest, ":")
			mounts[target] = src
			i++
		case (a == "--bind" || a == "--ro-bind") && i+2 < len(argv):
			mounts[argv[i+2]] = argv[i+1]
			i += 2
		}
	}
	return env, mounts
}

// checkRuntimeArgv returns what is wrong with the one Box-run argv the
// document's runtime received.
func checkRuntimeArgv(doc *inputdoc.Document, argv []string) []string {
	var bad []string
	bwrap := doc.Artifacts["RUNTIME"] == "bwrap"
	image := doc.Artifacts["IMAGE_TAG"]
	flag := "-e"
	if bwrap {
		flag = "--setenv"
	}
	if n := len(argv); n < 1 || argv[n-1] != "/agent/entrypoint.sh" {
		bad = append(bad, fmt.Sprintf("run argv does not end at the baked entrypoint: %q", argv))
	} else if !bwrap && (n < 2 || argv[n-2] != image) {
		bad = append(bad, fmt.Sprintf("run argv names image %q; document specifies %q", argv[max(0, n-2)], image))
	}
	env, mounts := argvEnv(argv, flag)
	if cache := doc.Artifacts["DRIVER_SESSION_CACHE_DIR"]; mounts[cache] == "" {
		bad = append(bad, fmt.Sprintf("no mount targets the driver session cache %q", cache))
	}
	if bwrap && mounts["/nix/store"] != "/nix/store" {
		bad = append(bad, "bwrap does not ro-bind /nix/store")
	}
	secretNames := []string{"GH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN"}
	for _, name := range strings.Fields(doc.Artifacts["BOX_ENV_VARS"]) {
		got, ok := env[name]
		want, inDoc := doc.Settings[name]
		if rt, ok := smokeRuntimeSettings[name]; ok {
			want, inDoc = rt, true
		}
		switch {
		case bwrap && slices.Contains(secretNames, name):
			if ok {
				bad = append(bad, fmt.Sprintf("bwrap put secret %s on argv", name))
			}
		case !bwrap && !ok:
			bad = append(bad, fmt.Sprintf("BOX_ENV_VARS names %s but the run argv has no -e for it", name))
		case bwrap && !ok && inDoc && want != "":
			bad = append(bad, fmt.Sprintf("BOX_ENV_VARS names %s but the run argv has no --setenv for it", name))
		case ok && got != nil && inDoc && *got != want:
			bad = append(bad, fmt.Sprintf("%s=%q on argv; document specifies %q", name, *got, want))
		}
	}
	if bwrap {
		for _, a := range argv {
			for _, s := range smokeSecrets {
				if strings.Contains(a, s) {
					bad = append(bad, "a secret value appears on the bwrap argv")
				}
			}
		}
	}
	return bad
}

// forgeCall reports whether some recorded gh call contains want in order.
func forgeCall(calls [][]string, want ...string) bool {
	for _, c := range calls {
		i := 0
		for _, a := range c {
			if i < len(want) && a == want[i] {
				i++
			}
		}
		if i == len(want) {
			return true
		}
	}
	return false
}

// checkForgeCalls returns what is wrong with the gh calls a dispatch of
// issue 7 made. The work family's labels come from the document; the
// research family's are fixed by the launcher (ADR 0022).
func checkForgeCalls(doc *inputdoc.Document, kind string, calls [][]string) []string {
	var bad []string
	need := func(why string, want ...string) {
		if !forgeCall(calls, want...) {
			bad = append(bad, fmt.Sprintf("no gh call %s: want %q", why, want))
		}
	}
	switch kind {
	case "work":
		queue, active, done := doc.Settings["LABEL"], doc.Settings["IN_PROGRESS_LABEL"], doc.Settings["COMPLETE_LABEL"]
		need("discovers the work queue", "issue", "list", "--label", queue)
		need("claims the issue", "issue", "edit", "7", "--add-label", active, "--remove-label", queue)
		need("merges the landing PR", "pr", "merge", smokePR)
		need("marks the issue complete", "issue", "edit", "7", "--add-label", done, "--remove-label", active)
	case "research":
		need("claims the issue", "issue", "edit", "7", "--add-label", "agent-research-in-progress", "--remove-label", "agent-research")
		need("applies the verdict label", "issue", "edit", "7", "--add-label", "agent-research-recommend")
		if forgeCall(calls, "pr", "merge") {
			bad = append(bad, "research dispatch merged a PR")
		}
	}
	return bad
}

type smokeCase struct {
	name, fixture, runtime, kind string
}

var smokeMatrix = []smokeCase{
	{"work-podman", "launcher-run-input.json", "podman", "work"},
	{"work-docker", "launcher-run-input-docker.json", "docker", "work"},
	{"work-bwrap", "launcher-run-input-bwrap.json", "bwrap", "work"},
	{"research-podman", "launcher-run-input.json", "podman", "research"},
}

// runSmoke runs the launcher on docPath and returns the Box-run argv the
// runtime recorded, plus every gh call.
func runSmoke(t *testing.T, c smokeCase, docPath string) (boxRun []string, gh [][]string) {
	t.Helper()
	bin := seamtest.BuildLauncher(t)
	fakeDir := seamtest.InstallFakes(t, "podman", "docker", "bwrap", "gh")
	recDir := t.TempDir()
	workDir := t.TempDir()

	issue := seamtest.GhIssue{Number: 7, Title: "T", Body: "B", Labels: []string{"ready-for-agent"}}
	outcome := "SPINDRIFT_OUTCOME issue=7 landing=" + smokePR + " status=ready note=seam\n"
	args := []string{"--input", docPath, "--repo-slug", smokeRepo, "--box-signal-carrier", "log",
		"--user-name", "Seam Bot", "--user-email", "seam@example.com", "--no-build",
		"--merge-policy", "immediate", "--merge-poll-interval", "1"}
	// bwrap would otherwise exec a real pasta helper to restore egress in the
	// isolated netns; the fake bwrap needs no network.
	if c.runtime == "bwrap" {
		args = append(args, "--network-mode", "host")
	}
	if c.kind == "research" {
		issue.Labels = []string{"agent-research"}
		outcome = "SPINDRIFT_OUTCOME issue=7 landing=none status=recommend note=seam\n"
		args = append(args, "research", "7")
	} else {
		args = append(args, "dispatch")
	}

	env := seamtest.WriteFakeConfig(t, "gh", seamtest.GhConfig{
		Record: recDir + "/gh.jsonl",
		Issues: []seamtest.GhIssue{issue},
		PRs: []seamtest.GhPR{{Number: 9, URL: smokePR, HeadRefName: "agent/issue-7", BaseRefName: "main",
			HeadRefOid: "abc123", Checks: "SUCCESS", Mergeable: "MERGEABLE"}},
	})
	runs := []seamtest.PodmanRun{{Outcome: outcome}}
	for _, tool := range []string{"podman", "docker"} {
		cfg := seamtest.PodmanConfig{Record: recDir + "/" + tool + ".jsonl", ImagePresent: true}
		if tool == c.runtime {
			cfg.Runs = runs
		}
		for k, v := range seamtest.WriteFakeConfig(t, tool, cfg) {
			env[k] = v
		}
	}
	// bwrap runs with an allowlisted env, so its config travels by cwd.
	bwrapCfg := seamtest.BwrapConfig{Record: recDir + "/bwrap.jsonl"}
	if c.runtime == "bwrap" {
		bwrapCfg.Runs = runs
	}
	seamtest.WriteFakeConfigFile(t, workDir, "bwrap", bwrapCfg)
	for k, v := range smokeSecrets {
		env[k] = v
	}

	res := seamtest.Run(t, seamtest.Cmd{Bin: bin, Args: args, Env: env, CleanEnv: true, PathDirs: []string{fakeDir}, Dir: workDir})
	res.WantExit(t, 0)

	for _, tool := range []string{"podman", "docker", "bwrap"} {
		recs := seamtest.ReadRecord(t, recDir+"/"+tool+".jsonl")
		if tool != c.runtime {
			for _, r := range recs {
				if len(r) > 0 && (r[0] == "run" || r[0] == "load") || tool == "bwrap" {
					t.Errorf("%s was used for a %s document: %q", tool, c.runtime, r)
				}
			}
			continue
		}
		for _, r := range recs {
			if tool == "bwrap" || len(r) > 0 && r[0] == "run" {
				boxRun = r
			}
		}
		if tool == "bwrap" && len(recs) != 1 {
			t.Errorf("bwrap ran %d times; want one Box per issue", len(recs))
		}
	}
	return boxRun, seamtest.ReadRecord(t, recDir+"/gh.jsonl")
}

func TestSeamLauncherSmokeMatrix(t *testing.T) {
	for _, c := range smokeMatrix {
		t.Run(c.name, func(t *testing.T) {
			docPath := seamtest.Path(t, c.fixture)
			doc, err := inputdoc.Load(docPath)
			if err != nil {
				t.Fatal(err)
			}
			if got := doc.Artifacts["RUNTIME"]; got != c.runtime {
				t.Fatalf("fixture %s renders RUNTIME=%q; case expects %q", c.fixture, got, c.runtime)
			}
			boxRun, gh := runSmoke(t, c, docPath)
			if boxRun == nil {
				t.Fatalf("%s never ran a Box", c.runtime)
			}
			for _, p := range checkRuntimeArgv(doc, boxRun) {
				t.Errorf("runtime: %s", p)
			}
			for _, p := range checkForgeCalls(doc, c.kind, gh) {
				t.Errorf("forge: %s", p)
			}
		})
	}
}

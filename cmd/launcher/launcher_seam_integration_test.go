//go:build integration

package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/branchrecovery"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/fixturebuild"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/hostpaths"
	"spindrift.dev/launcher/internal/inputdoc"
	"spindrift.dev/launcher/internal/ledger"
	"spindrift.dev/launcher/internal/seambundle"
	"spindrift.dev/launcher/internal/seamtest"
)

// The smoke matrix drives the real launcher binary with a rendered input
// document, faking only the tools it shells out to, with one test per dispatch
// kind x container runtime. checkRuntimeArgv and checkForge take expectations
// from the document and the run's record alone and return problems instead of
// failing, so a caller can run the launcher on a different document and assert
// they notice.

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

// smokeLocalSettings are the values a run takes at run time from
// useLocalForge, which moves the tracker and forge off the fixture's github.
// A butler run needs a forge that hosts a Ledger, which the git forge does
// not, so it takes the local forge. CODE_FORGE_REMOTE_URL is the per-run bare
// remote, so only its tail is pinned.
func smokeLocalSettings(kind string) map[string]string {
	if kind == "butler" {
		return map[string]string{"ISSUE_TRACKER": "local", "CODE_FORGE": "local"}
	}
	return map[string]string{"ISSUE_TRACKER": "local", "CODE_FORGE": "git"}
}

const smokeLocalRemoteTail = "/remote.git"

// tmpOverlay is argvEnv's mount source for a bwrap --tmp-overlay target.
const tmpOverlay = "<tmp-overlay>"

// bwrapStoreOverlay mirrors the launcher's AND-gate for a bwrap document: a
// writable store with a nix.conf swaps the store's ro-bind for a tmpfs
// overlay, and the launcher probes overlayfs before the Box run.
func bwrapStoreOverlay(doc *inputdoc.Document) bool {
	b := doc.Artifacts.Bwrap
	return b != nil && b.NixStoreWritable.Bool() && b.NixConfigFile.String() != ""
}

// argvEnv scans a runner argv for the env it hands the Box. flag is "-e"
// (OCI: NAME=value, or a bare NAME for a secret taken from the launcher's own
// environment, recorded as nil) or "--setenv" (bwrap: NAME VALUE pairs).
// mounts maps each bind target to its source, and a bwrap --tmp-overlay
// target to tmpOverlay.
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
		case a == "--tmp-overlay" && i+1 < len(argv):
			mounts[argv[i+1]] = tmpOverlay
			i++
		}
	}
	return env, mounts
}

// checkRuntimeArgv returns what is wrong with the one Box-run argv the
// document's runtime received. kind names the dispatch kind, whose descriptor
// can pin a value the document leaves open.
func checkRuntimeArgv(doc *inputdoc.Document, kind string, argv []string) []string {
	var bad []string
	bwrap := doc.Artifacts.Shared.Runtime.String() == "bwrap"
	image := docImageTag(doc)
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
	if cache := doc.Artifacts.Shared.DriverSessionCacheDir.String(); mounts[cache] == "" {
		bad = append(bad, fmt.Sprintf("no mount targets the driver session cache %q", cache))
	}
	if bwrap {
		if bwrapStoreOverlay(doc) {
			if mounts["/nix/store"] != tmpOverlay {
				bad = append(bad, "bwrap does not overlay the writable /nix/store")
			}
		} else if mounts["/nix/store"] != "/nix/store" {
			bad = append(bad, "bwrap does not ro-bind /nix/store")
		}
	}
	secretNames := []string{"GH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN"}
	// Every Box is read-only and holds no forge credential (issue #6900), so the
	// forge token names BOX_ENV_VARS lists never reach its argv.
	forgeTokens := map[string]bool{}
	for _, name := range []string{"GH_TOKEN", "FORGEJO_TOKEN"} {
		forgeTokens[name] = true
	}
	for _, name := range strings.Fields(doc.Artifacts.Shared.BoxEnvVars.String()) {
		got, ok := env[name]
		want, inDoc := doc.Settings[name]
		if rt, ok := smokeRuntimeSettings[name]; ok {
			want, inDoc = rt, true
		}
		if rt, ok := smokeLocalSettings(kind)[name]; ok {
			want, inDoc = rt, true
		}
		switch {
		case name == "CODE_FORGE_REMOTE_URL" && kind != "butler":
			if !ok || got == nil || !strings.HasSuffix(*got, smokeLocalRemoteTail) {
				bad = append(bad, fmt.Sprintf("CODE_FORGE_REMOTE_URL=%v on argv; want the local remote", got))
			}
		case forgeTokens[name]:
			if ok {
				bad = append(bad, fmt.Sprintf("read-only Box got forge token %s on argv", name))
			}
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

// checkForge returns what is wrong with what a dispatch of issue 7 did to
// its tracker and forge. The work family's labels come from the document; the
// research family's are fixed by the launcher (ADR 0022). A work or research
// run is checked by the issue file's end state and whether the change landed,
// since the local tracker and git forge make no gh calls; a butler run by the
// issue files it filed. No kind may reach gh.
func checkForge(doc *inputdoc.Document, kind string, r smokeRun) []string {
	var bad []string
	if len(r.gh) > 0 {
		bad = append(bad, fmt.Sprintf("%s run made gh calls: %q", kind, r.gh))
	}
	switch kind {
	case "work":
		if want := doc.Settings["COMPLETE_LABEL"]; r.issueState != want {
			bad = append(bad, fmt.Sprintf("issue 7 ends in state %q; want the complete label %q", r.issueState, want))
		}
		if !r.landed {
			bad = append(bad, "the agent branch's change never reached main on the remote")
		}
	case "research":
		if want := forge.ResearchVerdictLabels().Label(forge.Recommend); r.issueState != want {
			bad = append(bad, fmt.Sprintf("issue 7 ends in state %q; want the verdict label %q", r.issueState, want))
		}
		if r.landed {
			bad = append(bad, "research dispatch merged a change")
		}
	case "butler":
		if !slices.ContainsFunc(r.filed, func(f string) bool { return strings.Contains(f, dispatchkind.Butler.FindingLabel) }) {
			bad = append(bad, fmt.Sprintf("no filed issue carries the butler provenance label %q: %q", dispatchkind.Butler.FindingLabel, r.filed))
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
	{"butler-podman", "launcher-run-input-butler.json", "podman", "butler"},
}

// smokeFinding is the one finding the butler Box relays.
const smokeFinding = `{"title":"seam finding","body":"found by the seam","type":"bug"}`

// seedLedgerRemote stands up a bare repo holding a main branch with one
// commit, the clone every kind seeds its mirror from, and returns the env that
// makes git treat the repo's github URL as that bare repo, so nothing else in
// the run touches the network.
func seedLedgerRemote(t *testing.T) (bare string, env map[string]string) {
	t.Helper()
	root := t.TempDir()
	bare = root + "/remote.git"
	seed := root + "/seed"
	gitEnv := append(os.Environ(), "GIT_AUTHOR_NAME=seam", "GIT_AUTHOR_EMAIL=seam@example.com",
		"GIT_COMMITTER_NAME=seam", "GIT_COMMITTER_EMAIL=seam@example.com")
	if err := os.WriteFile(root+"/README.md", []byte("seam\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q", "--bare", "-b", "main", bare},
		{"init", "-q", "-b", "main", seed},
	} {
		seamGit(t, gitEnv, "", args...)
	}
	if err := os.Rename(root+"/README.md", seed+"/README.md"); err != nil {
		t.Fatal(err)
	}
	seamGit(t, gitEnv, seed, "add", "README.md")
	seamGit(t, gitEnv, seed, "commit", "-q", "-m", "seed")
	seamGit(t, gitEnv, seed, "push", "-q", bare, "main")
	// A global file, not GIT_CONFIG_COUNT: the relay clone persists the same
	// identity rules in its local config, which beat GIT_CONFIG_COUNT on a tie
	// but lose to global config.
	cfg := root + "/gitconfig"
	url := "https://github.com/" + smokeRepo + ".git"
	// pushInsteadOf too: GitRemote pins both keys to the exact URL (issue
	// #4665), and a matching pushInsteadOf shadows insteadOf for pushes.
	rules := "[url \"" + bare + "\"]\n\tinsteadOf = " + url + "\n\tpushInsteadOf = " + url + "\n"
	if err := os.WriteFile(cfg, []byte(rules), 0o644); err != nil {
		t.Fatal(err)
	}
	return bare, map[string]string{"GIT_CONFIG_GLOBAL": cfg}
}

func seamGit(t *testing.T, env []string, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir, cmd.Env = dir, env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %q: %v\n%s", args, err, out)
	}
}

// smokeRun is what a runSmoke launcher run left behind.
type smokeRun struct {
	// boxRun is the Box-run argv the runtime recorded.
	boxRun []string
	// gh is every gh call. Every kind runs on the local tracker and forge, so
	// none should appear.
	gh [][]string
	// filed is the content of each issue file a butler run filed.
	filed []string
	// ledgerRefs lists the Ledger refs a butler sweep left in the Accumulation repo.
	ledgerRefs []string
	// issueState is issue 7's frontmatter state after a work or research run.
	issueState string
	// landed reports whether the agent branch's change reached main on the remote.
	landed bool
}

// useLocalForge points the document at docPath at a local tracker holding one
// issue file per n in issues, each in state, under workDir, and a git forge
// landing on the bare remote. It edits docPath, so pass a copy. The git forge
// merges in its own clone of bare, which ignores the launcher's insteadOf
// rules, so the remote is named directly. The github path is not runnable
// here: every github read needs a routed Connector row.
func useLocalForge(t *testing.T, docPath, workDir, bare string, issues []int, state string) {
	t.Helper()
	setDocSettings(t, docPath, map[string]string{"ISSUE_TRACKER": "local", "CODE_FORGE": "git", "CODE_FORGE_REMOTE_URL": bare})
	dir := filepath.Join(workDir, ".spindrift", "issues")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range issues {
		body := "---\ntitle: T\nstate: " + state + "\n---\nB\n"
		if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(n)+".md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// runSmoke runs the launcher on docPath and returns what it left behind. A
// work or research run reads a local issue and lands through the git forge, on
// a scratch copy of the document. extra are launcher flags appended before
// the subcommand.
func runSmoke(t *testing.T, c smokeCase, docPath string, extra ...string) (res smokeRun) {
	t.Helper()
	bin := seamtest.BuildLauncher(t)
	fakeDir := seamtest.InstallFakes(t, "podman", "docker", "bwrap", "gh")
	recDir := t.TempDir()
	workDir := t.TempDir()
	doc, err := inputdoc.Load(docPath)
	if err != nil {
		t.Fatal(err)
	}
	// Every Connector load goes through the sandbox, so the fakes carry the
	// connector-host it runs.
	host, err := fixturebuild.ConnectorHost()
	if err != nil {
		t.Fatal(err)
	}
	var sandboxArtifacts map[string]string
	if c.runtime == "bwrap" {
		sandboxArtifacts = bwrapSandboxArtifacts(t, host)
	}
	// Every kind runs read-only, so the launcher clones the Target repo host-side
	// (SeedMirror); the same insteadOf remote serves that clone, the work kind's
	// bundle relay, and the Ledger.
	bare, gitEnv := seedLedgerRemote(t)
	local := c.kind != "butler"
	if local {
		state := doc.Settings["LABEL"]
		if c.kind == "research" {
			state = forge.ResearchDispatchLabels().Dispatchable
		}
		docPath = scratchDoc(t, docPath, func(a map[string]string) { maps.Copy(a, sandboxArtifacts) })
		useLocalForge(t, docPath, workDir, bare, []int{7}, state)
	} else {
		// The local forge seeds its Accumulation repo, which is also the Ledger,
		// from the cwd's base branch, so the cwd must be a clone of the remote.
		docPath = scratchDoc(t, docPath, func(a map[string]string) { maps.Copy(a, sandboxArtifacts) })
		setDocSettings(t, docPath, smokeLocalSettings(c.kind))
		gitEnv := os.Environ()
		seamGit(t, gitEnv, "", "init", "-q", "-b", "main", workDir)
		seamGit(t, gitEnv, workDir, "fetch", "-q", bare, "main")
		seamGit(t, gitEnv, workDir, "update-ref", "refs/heads/main", "FETCH_HEAD")
	}
	overlay := c.runtime == "bwrap" && bwrapStoreOverlay(doc)
	if overlay {
		// The nix-var snapshot a `launcher build` leaves behind, which --no-build
		// requires. It is a versioned db-<N> snapshot: with no sqlite3 here the
		// host probe fails, and Run never falls back to a pre-#6072 unversioned
		// one. The generation mirrors bwrap.ClosureGeneration's
		// SafePathComponent (an unusable tag yields a path with no generation
		// segment).
		tag := docImageTag(doc)
		gen := filepath.Base(tag)
		if tag == "" || gen == "." || gen == ".." || gen == string(filepath.Separator) {
			gen = ""
		}
		db := filepath.Join(workDir, ".spindrift", "nix-var-snapshot", gen, "db-1", "nix", "db", "db.sqlite")
		if err := os.MkdirAll(filepath.Dir(db), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(db, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	outcome := "SPINDRIFT_OUTCOME issue=7 landing=" + smokePR + " status=ready note=seam\n"
	args := []string{"--input", docPath, "--repo-slug", smokeRepo, "--box-signal-carrier", "log",
		"--user-name", "Seam Bot", "--user-email", "seam@example.com", "--no-build",
		"--merge-policy", "immediate", "--merge-poll-interval", "1"}
	args = append(args, extra...)
	// bwrap would otherwise exec a real pasta helper to restore egress in the
	// isolated netns; the fake bwrap needs no network.
	if c.runtime == "bwrap" {
		args = append(args, "--network-mode", "host")
	}
	var intents []string
	var bareLedger string
	switch c.kind {
	case "research":
		outcome = "SPINDRIFT_OUTCOME issue=7 landing=none status=recommend note=seam\n"
		args = append(args, "research", "7")
	case "butler":
		outcome = "SPINDRIFT_OUTCOME issue=butler-bugs landing=none status=ready note=seam\n"
		intents = []string{smokeFinding}
		args = append(args, "butler", "--chore", "bugs")
	default:
		args = append(args, "dispatch")
	}

	env := seamtest.WriteFakeConfig(t, "gh", seamtest.GhConfig{
		Record: recDir + "/gh.jsonl",
	})
	if c.kind == "butler" {
		bareLedger = filepath.Join(workDir, ".spindrift", "accum.git")
	}
	for k, v := range gitEnv {
		env[k] = v
	}
	run := seamtest.PodmanRun{Outcome: outcome, Intents: intents}
	if c.kind == "research" {
		// A local tracker has no in-box client, so the Box relays its verdict comment.
		run.Comment = "verdict: recommend\n"
	}
	if c.kind == "work" {
		// A read-only Box cannot push: it hands the launcher a bundle in its outbox.
		run.Files = map[string]string{
			filepath.Join(hostpaths.OutboxDir(workDir, "7"), seambundle.FileName): seamBundle(t, bare),
		}
	}
	runs := []seamtest.PodmanRun{run}
	for _, tool := range []string{"podman", "docker"} {
		cfg := seamtest.PodmanConfig{Record: recDir + "/" + tool + ".jsonl", ImagePresent: true, ConnectorHost: host}
		if tool == c.runtime {
			cfg.Runs = runs
		}
		for k, v := range seamtest.WriteFakeConfig(t, tool, cfg) {
			env[k] = v
		}
	}
	// bwrap runs with an allowlisted env, so its config travels by cwd.
	bwrapCfg := seamtest.BwrapConfig{Record: recDir + "/bwrap.jsonl", ConnectorHost: host}
	if c.runtime == "bwrap" {
		bwrapCfg.Runs = runs
	}
	seamtest.WriteFakeConfigFile(t, workDir, "bwrap", bwrapCfg)
	for k, v := range smokeSecrets {
		env[k] = v
	}

	seamtest.Run(t, seamtest.Cmd{Bin: bin, Args: args, Env: env, CleanEnv: true, PathDirs: []string{fakeDir}, Dir: workDir}).WantExit(t, 0)

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
		probes, boxes := 0, 0
		for _, r := range recs {
			switch {
			case tool == "bwrap" && seamtest.IsBwrapOverlayProbe(r):
				probes++
				if boxes > 0 {
					t.Errorf("bwrap overlay probe ran after the Box run: %q", r)
				}
			case tool == "bwrap" && strings.HasSuffix(r[len(r)-1], "/bin/connector-host"),
				tool != "bwrap" && len(r) > 0 && r[0] == "run" && sandboxName(r) != "":
				// The Connector sandbox, not the Box.
			case tool == "bwrap":
				boxes++
				res.boxRun = r
			case len(r) > 0 && r[0] == "run":
				res.boxRun = r
			}
		}
		if tool == "bwrap" {
			wantProbes := 0
			if overlay {
				wantProbes = 1
			}
			if boxes != 1 {
				t.Errorf("bwrap ran %d Boxes; want one Box per issue", boxes)
			}
			if probes != wantProbes {
				t.Errorf("bwrap ran %d overlay probes; want %d", probes, wantProbes)
			}
		}
	}
	if bareLedger != "" {
		out, err := exec.Command("git", "-C", bareLedger, "for-each-ref", "--format=%(refname)", ledger.RefPrefix).Output()
		if err != nil {
			t.Fatalf("list ledger refs: %v", err)
		}
		res.ledgerRefs = strings.Fields(string(out))
	}
	res.gh = seamtest.ReadRecord(t, recDir+"/gh.jsonl")
	if c.kind == "butler" {
		files, _ := filepath.Glob(filepath.Join(workDir, ".spindrift", "issues", "*.md"))
		for _, name := range files {
			data, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			res.filed = append(res.filed, string(data))
		}
	}
	if local {
		data, err := os.ReadFile(filepath.Join(workDir, ".spindrift", "issues", "7.md"))
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			if v, ok := strings.CutPrefix(line, "state: "); ok {
				res.issueState = strings.Trim(v, "\"")
			}
		}
		// The change is the agent branch's one file; it reaches main only by a merge.
		res.landed = exec.Command("git", "-C", bare, "cat-file", "-e", "main:change.txt").Run() == nil
	}
	return res
}

func TestSeamLauncherSmokeMatrix(t *testing.T) {
	t.Parallel()
	for _, c := range smokeMatrix {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			docPath := seamtest.Path(t, c.fixture)
			doc, err := inputdoc.Load(docPath)
			if err != nil {
				t.Fatal(err)
			}
			if got := doc.Artifacts.Shared.Runtime.String(); got != c.runtime {
				t.Fatalf("fixture %s renders RUNTIME=%q; case expects %q", c.fixture, got, c.runtime)
			}
			run := runSmoke(t, c, docPath)
			boxRun := run.boxRun
			if boxRun == nil {
				t.Fatalf("%s never ran a Box", c.runtime)
			}
			for _, p := range checkRuntimeArgv(doc, c.kind, boxRun) {
				t.Errorf("runtime: %s", p)
			}
			for _, p := range checkForge(doc, c.kind, run) {
				t.Errorf("forge: %s", p)
			}
			if c.kind == "butler" {
				if want := []string{ledger.RefPrefix + "bugs"}; !slices.Equal(run.ledgerRefs, want) {
					t.Errorf("ledger refs on the remote = %q; want %q", run.ledgerRefs, want)
				}
				if env, _ := argvEnv(boxRun, "-e"); env["CHORE_NAME"] == nil || *env["CHORE_NAME"] != "bugs" {
					t.Errorf("butler Box was not handed CHORE_NAME=bugs: %q", boxRun)
				}
			}
		})
	}
}

// TestSeamSmokeReadOnlyBoxCarriesNoForgeToken runs the work-podman document
// under the default read-only access while the launcher's own env holds GH_TOKEN
// (smokeSecrets): the Box run must still land, and its argv
// must carry no forge token key, forwarded bare or by value (issues #7140,
// #6900), and the host's open-PR answer for branch recovery (issue #7146).
func TestSeamSmokeReadOnlyBoxCarriesNoForgeToken(t *testing.T) {
	t.Parallel()
	i := slices.IndexFunc(smokeMatrix, func(c smokeCase) bool { return c.name == "work-podman" })
	if i < 0 {
		t.Fatal("smokeMatrix has no work-podman row")
	}
	c := smokeMatrix[i]
	run := runSmoke(t, c, seamtest.Path(t, c.fixture))
	boxRun := run.boxRun
	if boxRun == nil {
		t.Fatalf("%s never ran a Box", c.runtime)
	}
	env, _ := argvEnv(boxRun, "-e")
	for _, name := range []string{"GH_TOKEN", "GITEA_TOKEN", "FORGEJO_TOKEN"} {
		if _, ok := env[name]; ok {
			t.Errorf("Box argv carries %s under read-only access: %q", name, boxRun)
		}
	}
	if got := env[branchrecovery.HostOpenPREnv]; got == nil || *got != branchrecovery.HostOpenPRNone {
		t.Errorf("%s = %v; want %s (the git forge has no PRs, so its lookup answers none)", branchrecovery.HostOpenPREnv, got, branchrecovery.HostOpenPRNone)
	}
	doc, err := inputdoc.Load(seamtest.Path(t, c.fixture))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range checkForge(doc, c.kind, run) {
		t.Errorf("forge: %s", p)
	}
}

// TestSeamSmokeCatchesDroppedKnob is the smoke set's own regression test: a
// rendering regression must fail at least one check. It runs the launcher on
// a scratch copy of the work-podman document with one name dropped from
// BOX_ENV_VARS, the list of settings forwarded into the Box, and takes the
// expectations from the unmodified document.
func TestSeamSmokeCatchesDroppedKnob(t *testing.T) {
	t.Parallel()
	const dropped = "BRANCH_PREFIX"
	c := smokeMatrix[0]
	docPath := seamtest.Path(t, c.fixture)
	doc, err := inputdoc.Load(docPath)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(strings.Fields(doc.Artifacts.Shared.BoxEnvVars.String()), dropped) {
		t.Fatalf("fixture %s does not forward %s; pick another knob", c.fixture, dropped)
	}

	scratch := scratchDoc(t, docPath, func(artifacts map[string]string) {
		names := slices.DeleteFunc(strings.Fields(artifacts["BOX_ENV_VARS"]), func(n string) bool { return n == dropped })
		artifacts["BOX_ENV_VARS"] = strings.Join(names, " ")
	})

	run := runSmoke(t, c, scratch)
	if run.boxRun == nil {
		t.Fatalf("%s never ran a Box", c.runtime)
	}
	problems := append(checkRuntimeArgv(doc, c.kind, run.boxRun), checkForge(doc, c.kind, run)...)
	if !slices.ContainsFunc(problems, func(p string) bool { return strings.Contains(p, dropped) }) {
		t.Fatalf("dropping %s from the document went unnoticed by every smoke check; problems: %q", dropped, problems)
	}
	t.Logf("caught: %q", problems)
}

// scratchDoc writes a copy of the input document at docPath, with edit applied
// to its artifacts, and returns the copy's path.
func scratchDoc(t *testing.T, docPath string, edit func(artifacts map[string]string)) string {
	t.Helper()
	var raw struct {
		Settings  map[string]string `json:"settings"`
		Artifacts map[string]string `json:"artifacts"`
		// Carried through untouched: claude's Connector row is the Driver, so a
		// copy without it cannot run.
		Drivers  json.RawMessage `json:"drivers,omitempty"`
		Backends json.RawMessage `json:"backends,omitempty"`
	}
	data, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	edit(raw.Artifacts)
	scratch := filepath.Join(t.TempDir(), "scratch-input.json")
	if data, err = json.Marshal(raw); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(scratch, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return scratch
}

// TestSeamSmokeBwrapOverlayProbe runs the launcher on a bwrap document with
// nixStoreWritable and a nix.conf, the pair that makes the bwrap overlay launch gate
// probe overlayfs before the Box run. The shipped fixture leaves nixInBox off
// (issue #2664), so the knobs are set on a scratch copy.
func TestSeamSmokeBwrapOverlayProbe(t *testing.T) {
	t.Parallel()
	c := smokeCase{"work-bwrap-overlay", "launcher-run-input-bwrap.json", "bwrap", "work"}
	nixConf := filepath.Join(t.TempDir(), "nix.conf")
	if err := os.WriteFile(nixConf, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	scratch := scratchDoc(t, seamtest.Path(t, c.fixture), func(artifacts map[string]string) {
		artifacts["NIX_STORE_WRITABLE"] = "true"
		artifacts["NIX_CONFIG_FILE"] = nixConf
	})
	doc, err := inputdoc.Load(scratch)
	if err != nil {
		t.Fatal(err)
	}
	if !bwrapStoreOverlay(doc) {
		t.Fatalf("scratch document does not enable the store overlay: %+v", doc.Artifacts)
	}

	run := runSmoke(t, c, scratch)
	if run.boxRun == nil {
		t.Fatal("bwrap never ran a Box")
	}
	for _, p := range checkRuntimeArgv(doc, c.kind, run.boxRun) {
		t.Errorf("runtime: %s", p)
	}
	for _, p := range checkForge(doc, c.kind, run) {
		t.Errorf("forge: %s", p)
	}
}

// docImageTag reads IMAGE_TAG from whichever runner family part the document
// sets; both rows render it.
func docImageTag(doc *inputdoc.Document) string {
	switch a := doc.Artifacts; {
	case a.Bwrap != nil:
		return a.Bwrap.ImageTag.String()
	case a.OCI != nil:
		return a.OCI.ImageTag.String()
	}
	return ""
}

// seamBundle writes a one-commit bundle of the agent branch cut from bare's
// main, the code-out a read-only Box leaves in its outbox, and returns its path.
func seamBundle(t *testing.T, bare string) string {
	t.Helper()
	root := t.TempDir()
	env := append(os.Environ(), "GIT_AUTHOR_NAME=seam", "GIT_AUTHOR_EMAIL=seam@example.com",
		"GIT_COMMITTER_NAME=seam", "GIT_COMMITTER_EMAIL=seam@example.com")
	work := root + "/work"
	seamGit(t, env, "", "clone", "-q", bare, work)
	seamGit(t, env, work, "checkout", "-q", "-b", "agent/issue-7")
	if err := os.WriteFile(work+"/change.txt", []byte("seam\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	seamGit(t, env, work, "add", "change.txt")
	seamGit(t, env, work, "commit", "-q", "-m", "seam change")
	path := root + "/" + seambundle.FileName
	seamGit(t, env, work, "bundle", "create", "-q", path, "origin/main..agent/issue-7")
	return path
}

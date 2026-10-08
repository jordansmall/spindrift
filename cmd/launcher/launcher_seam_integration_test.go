//go:build integration

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/inputdoc"
	"spindrift.dev/launcher/internal/ledger"
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

// tmpOverlay is argvEnv's mount source for a bwrap --tmp-overlay target.
const tmpOverlay = "<tmp-overlay>"

// bwrapStoreOverlay mirrors the launcher's AND-gate for a bwrap document: a
// writable store with a nix.conf swaps the store's ro-bind for a tmpfs
// overlay, and the launcher probes overlayfs before the Box run.
func bwrapStoreOverlay(doc *inputdoc.Document) bool {
	return doc.Artifacts["NIX_STORE_WRITABLE"] == "true" && doc.Artifacts["NIX_CONFIG_FILE"] != ""
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
	for _, name := range strings.Fields(doc.Artifacts["BOX_ENV_VARS"]) {
		got, ok := env[name]
		want, inDoc := doc.Settings[name]
		if rt, ok := smokeRuntimeSettings[name]; ok {
			want, inDoc = rt, true
		}
		// A read-only kind overrides the document's access mode.
		if d, ok := dispatchkind.ByName(kind); ok && d.ReadOnlyBox && name == "BOX_FORGE_AND_ISSUE_ACCESS" {
			want, inDoc = "read-only", true
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
		labels := forge.ResearchDispatchLabels()
		need("claims the issue", "issue", "edit", "7", "--add-label", labels.InProgress, "--remove-label", labels.Dispatchable)
		need("applies the verdict label", "issue", "edit", "7", "--add-label", forge.ResearchVerdictLabels().Label(forge.Recommend))
		if forgeCall(calls, "pr", "merge") {
			bad = append(bad, "research dispatch merged a PR")
		}
	case "butler":
		need("files the finding under the butler provenance label", "issue", "create", "--label", dispatchkind.Butler.FindingLabel)
		for _, verb := range []string{"merge", "create"} {
			if forgeCall(calls, "pr", verb) {
				bad = append(bad, "butler sweep ran gh pr "+verb)
			}
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
// commit, the clone the butler scans, and returns the env that makes git
// treat the repo's github URL as that bare repo: the Ledger is a real git
// remote, and nothing else in the run touches the network.
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
		{"init", "-q", "--bare", bare},
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
	return bare, map[string]string{
		// pushInsteadOf too: GitRemote pins both keys to the exact URL (issue
		// #4665), and a matching pushInsteadOf shadows insteadOf for pushes.
		"GIT_CONFIG_COUNT":   "2",
		"GIT_CONFIG_KEY_0":   "url." + bare + ".insteadOf",
		"GIT_CONFIG_VALUE_0": "https://github.com/" + smokeRepo + ".git",
		"GIT_CONFIG_KEY_1":   "url." + bare + ".pushInsteadOf",
		"GIT_CONFIG_VALUE_1": "https://github.com/" + smokeRepo + ".git",
	}
}

func seamGit(t *testing.T, env []string, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir, cmd.Env = dir, env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %q: %v\n%s", args, err, out)
	}
}

// runSmoke runs the launcher on docPath and returns the Box-run argv the
// runtime recorded, plus every gh call. For a butler case, ledgerRefs lists
// the Ledger refs the sweep left on the remote.
func runSmoke(t *testing.T, c smokeCase, docPath string) (boxRun []string, gh [][]string, ledgerRefs []string) {
	t.Helper()
	bin := seamtest.BuildLauncher(t)
	fakeDir := seamtest.InstallFakes(t, "podman", "docker", "bwrap", "gh")
	recDir := t.TempDir()
	workDir := t.TempDir()
	doc, err := inputdoc.Load(docPath)
	if err != nil {
		t.Fatal(err)
	}
	overlay := c.runtime == "bwrap" && bwrapStoreOverlay(doc)
	if overlay {
		// The nix-var snapshot a `launcher build` leaves behind, which --no-build
		// requires; the generation mirrors runner.closureGeneration's
		// safePathComponent (an unusable tag yields the flat legacy path).
		tag := doc.Artifacts["IMAGE_TAG"]
		gen := filepath.Base(tag)
		if tag == "" || gen == "." || gen == ".." || gen == string(filepath.Separator) {
			gen = ""
		}
		db := filepath.Join(workDir, ".spindrift", "nix-var-snapshot", gen, "nix", "db", "db.sqlite")
		if err := os.MkdirAll(filepath.Dir(db), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(db, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

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
	var intents []string
	var replies []seamtest.GhReply
	var bareLedger string
	switch c.kind {
	case "research":
		issue.Labels = []string{forge.ResearchDispatchLabels().Dispatchable}
		outcome = "SPINDRIFT_OUTCOME issue=7 landing=none status=recommend note=seam\n"
		args = append(args, "research", "7")
	case "butler":
		outcome = "SPINDRIFT_OUTCOME issue=butler-bugs landing=none status=ready note=seam\n"
		intents = []string{smokeFinding}
		args = append(args, "butler", "--chore", "bugs")
		// The filing's URL is what the launcher records in the Ledger.
		replies = []seamtest.GhReply{{Args: []string{"issue", "create"}, Stdout: "https://github.com/" + smokeRepo + "/issues/12\n"}}
	default:
		args = append(args, "dispatch")
	}

	env := seamtest.WriteFakeConfig(t, "gh", seamtest.GhConfig{
		Record:  recDir + "/gh.jsonl",
		Issues:  []seamtest.GhIssue{issue},
		Replies: replies,
		PRs: []seamtest.GhPR{{Number: 9, URL: smokePR, HeadRefName: "agent/issue-7", BaseRefName: "main",
			HeadRefOid: "abc123", Checks: "SUCCESS", Mergeable: "MERGEABLE"}},
	})
	if c.kind == "butler" {
		var gitEnv map[string]string
		bareLedger, gitEnv = seedLedgerRemote(t)
		for k, v := range gitEnv {
			env[k] = v
		}
	}
	runs := []seamtest.PodmanRun{{Outcome: outcome, Intents: intents}}
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
		probes, boxes := 0, 0
		for _, r := range recs {
			switch {
			case tool == "bwrap" && seamtest.IsBwrapOverlayProbe(r):
				probes++
				if boxes > 0 {
					t.Errorf("bwrap overlay probe ran after the Box run: %q", r)
				}
			case tool == "bwrap":
				boxes++
				boxRun = r
			case len(r) > 0 && r[0] == "run":
				boxRun = r
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
		ledgerRefs = strings.Fields(string(out))
	}
	return boxRun, seamtest.ReadRecord(t, recDir+"/gh.jsonl"), ledgerRefs
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
			boxRun, gh, ledgerRefs := runSmoke(t, c, docPath)
			if boxRun == nil {
				t.Fatalf("%s never ran a Box", c.runtime)
			}
			for _, p := range checkRuntimeArgv(doc, c.kind, boxRun) {
				t.Errorf("runtime: %s", p)
			}
			for _, p := range checkForgeCalls(doc, c.kind, gh) {
				t.Errorf("forge: %s", p)
			}
			if c.kind == "butler" {
				if want := []string{ledger.RefPrefix + "bugs"}; !slices.Equal(ledgerRefs, want) {
					t.Errorf("ledger refs on the remote = %q; want %q", ledgerRefs, want)
				}
				if env, _ := argvEnv(boxRun, "-e"); env["CHORE_NAME"] == nil || *env["CHORE_NAME"] != "bugs" {
					t.Errorf("butler Box was not handed CHORE_NAME=bugs: %q", boxRun)
				}
			}
		})
	}
}

// TestSeamSmokeCatchesDroppedKnob is the smoke set's own regression test: a
// rendering regression must fail at least one check. It runs the launcher on
// a scratch copy of the work-podman document with one name dropped from
// BOX_ENV_VARS, the list of settings forwarded into the Box, and takes the
// expectations from the unmodified document.
func TestSeamSmokeCatchesDroppedKnob(t *testing.T) {
	const dropped = "MAX_REBASE_ATTEMPTS"
	c := smokeMatrix[0]
	docPath := seamtest.Path(t, c.fixture)
	doc, err := inputdoc.Load(docPath)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(strings.Fields(doc.Artifacts["BOX_ENV_VARS"]), dropped) {
		t.Fatalf("fixture %s does not forward %s; pick another knob", c.fixture, dropped)
	}

	scratch := scratchDoc(t, docPath, func(artifacts map[string]string) {
		names := slices.DeleteFunc(strings.Fields(artifacts["BOX_ENV_VARS"]), func(n string) bool { return n == dropped })
		artifacts["BOX_ENV_VARS"] = strings.Join(names, " ")
	})

	boxRun, gh, _ := runSmoke(t, c, scratch)
	if boxRun == nil {
		t.Fatalf("%s never ran a Box", c.runtime)
	}
	problems := append(checkRuntimeArgv(doc, c.kind, boxRun), checkForgeCalls(doc, c.kind, gh)...)
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
// nixStoreWritable and a nix.conf, the pair that makes checkBwrapOverlayGate
// probe overlayfs before the Box run. The shipped fixture leaves nixInBox off
// (issue #2664), so the knobs are set on a scratch copy.
func TestSeamSmokeBwrapOverlayProbe(t *testing.T) {
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
		t.Fatalf("scratch document does not enable the store overlay: %q", doc.Artifacts)
	}

	boxRun, gh, _ := runSmoke(t, c, scratch)
	if boxRun == nil {
		t.Fatal("bwrap never ran a Box")
	}
	for _, p := range checkRuntimeArgv(doc, c.kind, boxRun) {
		t.Errorf("runtime: %s", p)
	}
	for _, p := range checkForgeCalls(doc, c.kind, gh) {
		t.Errorf("forge: %s", p)
	}
}

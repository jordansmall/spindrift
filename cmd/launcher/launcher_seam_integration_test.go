//go:build integration

package main

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/inputdoc"
	"spindrift.dev/launcher/internal/seamtest"
)

// TestSeamLauncherRunReachesPodman drives the real launcher binary with the
// rendered input document, faking only the tools it shells out to, and checks
// the `podman run` it issues carries what the document specifies.
func TestSeamLauncherRunReachesPodman(t *testing.T) {
	docPath := seamtest.Path(t, "launcher-run-input.json")
	doc, err := inputdoc.Load(docPath)
	if err != nil {
		t.Fatal(err)
	}
	image := doc.Artifacts["IMAGE_TAG"]
	cacheDir := doc.Artifacts["DRIVER_SESSION_CACHE_DIR"]
	if image == "" || cacheDir == "" {
		t.Fatalf("document lacks IMAGE_TAG or DRIVER_SESSION_CACHE_DIR: %v", doc.Artifacts)
	}

	bin := seamtest.BuildLauncher(t)
	fakeDir := seamtest.InstallFakes(t, "podman", "gh")
	podmanRec := t.TempDir() + "/podman.jsonl"
	// gh answers the issue reads a dispatch makes before it reaches the Box;
	// the label edit and the closing comment succeed silently.
	env := seamtest.WriteFakeConfig(t, "gh", seamtest.GhConfig{
		Record: t.TempDir() + "/gh.jsonl",
		Issues: []seamtest.GhIssue{{Number: 7, Title: "T", Body: "B", Labels: []string{doc.Settings["LABEL"]}}},
	})
	for k, v := range seamtest.WriteFakeConfig(t, "podman", seamtest.PodmanConfig{
		Record:       podmanRec,
		ImagePresent: true,
		Runs:         []seamtest.PodmanRun{{Outcome: "SPINDRIFT_OUTCOME issue=7 landing=none status=already-resolved note=seam\n"}},
	}) {
		env[k] = v
	}
	env["GH_TOKEN"] = "fake-token"
	env["CLAUDE_CODE_OAUTH_TOKEN"] = "fake-oauth"

	// The values the document leaves for run time: the target repo and the
	// git identity. The "log" carrier is pinned because the fake podman cannot
	// answer the socket carrier's transport probe.
	runtimeSettings := map[string]string{"REPO_SLUG": "owner/repo", "BOX_SIGNAL_CARRIER": "log", "GIT_USER_NAME": "Seam Bot", "GIT_USER_EMAIL": "seam@example.com"}
	res := seamtest.Run(t, seamtest.Cmd{
		Bin:      bin,
		Args:     []string{"--input", docPath, "--repo-slug", "owner/repo", "--box-signal-carrier", "log", "--user-name", "Seam Bot", "--user-email", "seam@example.com", "--no-build", "dispatch", "7"},
		Env:      env,
		CleanEnv: true,
		PathDirs: []string{fakeDir},
	})
	res.WantExit(t, 0)
	if !strings.Contains(res.Stdout, "status=already-resolved") {
		t.Errorf("dispatch did not settle on the scripted outcome:\n%s", res.Stdout)
	}

	var run []string
	for _, argv := range seamtest.ReadRecord(t, podmanRec) {
		if len(argv) > 0 && argv[0] == "run" {
			run = argv
		}
	}
	if run == nil {
		t.Fatalf("podman never ran a Box; record: %v", seamtest.ReadRecord(t, podmanRec))
	}

	if n := len(run); n < 2 || run[n-2] != image || run[n-1] != "/agent/entrypoint.sh" {
		t.Errorf("run argv ends %q; want image %q then the baked entrypoint", run[max(0, len(run)-2):], image)
	}

	mounted := false
	// forwarded maps each -e name to its inline value; a bare "-e NAME"
	// (a secret taken from the launcher's own environment) has none.
	forwarded := map[string]*string{}
	for i := 0; i+1 < len(run)-2; i++ {
		switch run[i] {
		case "-v":
			if _, target, _ := strings.Cut(run[i+1], ":"); strings.TrimSuffix(target, ":ro") == cacheDir {
				mounted = true
			}
		case "-e":
			if k, v, ok := strings.Cut(run[i+1], "="); ok {
				forwarded[k] = &v
			} else {
				forwarded[k] = nil
			}
		}
	}
	if !mounted {
		t.Errorf("no -v mount targets the driver session cache %q: %q", cacheDir, run)
	}

	for _, name := range strings.Fields(doc.Artifacts["BOX_ENV_VARS"]) {
		got, ok := forwarded[name]
		if !ok {
			t.Errorf("BOX_ENV_VARS names %s but the run argv has no -e for it", name)
			continue
		}
		want, inDoc := doc.Settings[name]
		if rt, ok := runtimeSettings[name]; ok {
			want, inDoc = rt, true
		}
		if inDoc && got != nil && *got != want {
			t.Errorf("-e %s=%q; document specifies %q", name, *got, want)
		}
	}
}

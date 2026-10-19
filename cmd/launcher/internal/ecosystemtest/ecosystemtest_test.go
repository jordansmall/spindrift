package ecosystemtest

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/registrymanifest"
)

func TestCorpus(t *testing.T) {
	Run(t, Synthetic, filepath.Join("testdata", "corpus"))
}

// copyCase copies a corpus case into a temp dir so a test can mutate it,
// dropping the goldens unless keepGoldens.
func copyCase(t *testing.T, name string, keepGoldens bool) string {
	t.Helper()
	src := filepath.Join("testdata", "corpus", name)
	dst := t.TempDir()
	err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		if !keepGoldens && strings.Contains(rel, ".golden.") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

func TestReplayCaseMissingGoldenFails(t *testing.T) {
	if errs := replayCase(Synthetic, copyCase(t, "matched", true), false); len(errs) != 0 {
		t.Fatalf("intact case failed: %v", errs)
	}
	for _, s := range stages {
		t.Run(s.name, func(t *testing.T) {
			dir := copyCase(t, "matched", true)
			if err := os.Remove(filepath.Join(dir, s.golden)); err != nil {
				t.Fatal(err)
			}
			errs := replayCase(Synthetic, dir, false)
			if len(errs) != 1 || !strings.Contains(errs[0].Error(), s.name) {
				t.Fatalf("errs = %v, want exactly one naming %q", errs, s.name)
			}
		})
	}
}

func TestReplayCaseDriftedGoldenFails(t *testing.T) {
	dir := copyCase(t, "matched", true)
	if err := os.WriteFile(filepath.Join(dir, "pathset.golden.json"), []byte("[]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if errs := replayCase(Synthetic, dir, false); len(errs) != 1 {
		t.Fatalf("errs = %v, want exactly the drifted path-set", errs)
	}
}

func TestReplayCaseUpdateWritesEveryGolden(t *testing.T) {
	dir := copyCase(t, "matched", false)
	if errs := replayCase(Synthetic, dir, true); len(errs) != 0 {
		t.Fatal(errs)
	}
	for _, s := range stages {
		want, err := os.ReadFile(filepath.Join("testdata", "corpus", "matched", s.golden))
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(dir, s.golden))
		if err != nil {
			t.Fatalf("%s: not regenerated: %v", s.name, err)
		}
		if string(got) != string(want) {
			t.Errorf("%s: regenerated golden differs from the committed one", s.name)
		}
	}
}

func TestReplayCaseSkipsStageRowDoesNotTakePartIn(t *testing.T) {
	// A bare row takes part in route resolution and path-set derivation alone.
	bare := ecosystem.Row{Name: "bare"}
	// collided carries an operator routes file: with no parser, discovery proposes none.
	dir := copyCase(t, "collided", false)
	if errs := replayCase(bare, dir, true); len(errs) != 0 {
		t.Fatalf("bare row failed to write its goldens: %v", errs)
	}
	if errs := replayCase(bare, dir, false); len(errs) != 0 {
		t.Fatalf("bare row failed against its own goldens: %v", errs)
	}

	stale := filepath.Join(dir, "discovery.golden.toml")
	if err := os.WriteFile(stale, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if errs := replayCase(bare, dir, false); len(errs) != 1 {
		t.Fatalf("errs = %v, want one stale-golden failure", errs)
	}
	if errs := replayCase(bare, dir, true); len(errs) != 0 {
		t.Fatal(errs)
	}
	if _, err := os.Stat(stale); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("update mode left the stale golden: %v", err)
	}
}

func TestSyntheticTakesPartInEveryStage(t *testing.T) {
	for _, s := range stages {
		if !s.takesPart(Synthetic) {
			t.Errorf("Synthetic skips %s", s.name)
		}
	}
}

func readGolden(t *testing.T, c, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "corpus", c, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestBindingGoldenCarriesTheRowsRenderings(t *testing.T) {
	got := readGolden(t, "matched", "binding.golden.json")
	for _, want := range []string{
		"SYNTHETIC_REGISTRY",
		"SYNTHETIC_HOME",
		// the in-tree config is rewritten to the Forwarder, not left on the upstream
		"http://127.0.0.1:27182/reg-example-test/api/synth",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("binding golden lacks %q:\n%s", want, got)
		}
	}
}

func TestRewritesGoldenShowsTheRewriteAndTheDeclinedForeignHost(t *testing.T) {
	if got := readGolden(t, "matched", "rewrites.golden.json"); !strings.Contains(got, "127.0.0.1:27182/reg-example-test/api/synth/pkgs/") {
		t.Errorf("matched rewrites golden does not repoint the download at the Forwarder:\n%s", got)
	}
	if got := readGolden(t, "declared", "rewrites.golden.json"); !strings.Contains(got, "https://cdn.example.test/") {
		t.Errorf("declared rewrites golden does not leave the foreign host alone:\n%s", got)
	}
}

func TestStagesSkippedPerRow(t *testing.T) {
	// The generic `path` route key needs no hook, so a row with no parser, no
	// declaration hook, no bindings and no rewrite rows still resolves routes
	// and has an enforced path-set.
	var took []string
	for _, s := range stages {
		if s.takesPart(ecosystem.Row{Name: "bare"}) {
			took = append(took, s.name)
		}
	}
	if want := []string{"route resolution", "path-set derivation"}; !slices.Equal(took, want) {
		t.Fatalf("took part in %v, want %v", took, want)
	}
}

func TestPathSetGoldenHoldsDerivedAndEnforced(t *testing.T) {
	got := readGolden(t, "declared", "pathset.golden.json")
	for _, want := range []string{`"derived"`, `"enforced"`, `"matchHost"`, `"upstream"`, `"enforcedPaths"`, `"enforcedSubtrees"`} {
		if !strings.Contains(got, want) {
			t.Errorf("declared path-set golden lacks %s:\n%s", want, got)
		}
	}
}

func TestRefusedRoutesFileSkipsLaterRouteStages(t *testing.T) {
	dir := copyCase(t, "refused", true)
	if errs := replayCase(Synthetic, dir, false); len(errs) != 0 {
		t.Fatalf("intact refused case failed: %v", errs)
	}
	if got := readGolden(t, "refused", "routes.golden.json"); !strings.Contains(got, `"refused"`) || !strings.Contains(got, "mirror-path") || !strings.Contains(got, "must be an absolute path") {
		t.Errorf("routes golden does not hold the refusal:\n%s", got)
	}
	for _, g := range []string{"pathset.golden.json", "binding.golden.json", "rewrites.golden.json"} {
		if _, err := os.Stat(filepath.Join("testdata", "corpus", "refused", g)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("refused case holds %s: %v", g, err)
		}
	}

	stale := filepath.Join(dir, "binding.golden.json")
	if err := os.WriteFile(stale, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if errs := replayCase(Synthetic, dir, false); len(errs) != 1 {
		t.Fatalf("errs = %v, want one stale-golden failure", errs)
	}
	if errs := replayCase(Synthetic, dir, true); len(errs) != 0 {
		t.Fatal(errs)
	}
	if _, err := os.Stat(stale); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("update mode left the stale golden: %v", err)
	}
}

func TestDiscoveryFedParseErrorStaysAStageFailure(t *testing.T) {
	// Discovery's own output is never an operator's mistake: its refusal is a
	// stage failure, not a golden.
	dir := copyCase(t, "matched", false)
	row := Synthetic
	row.ConfigParser = func(string) ([]ecosystem.Declaration, bool, error) {
		return nil, false, errors.New("unparseable")
	}
	var named bool
	for _, err := range replayCase(row, dir, true) {
		named = named || strings.Contains(err.Error(), "discovery")
	}
	if !named {
		t.Error("a discovery failure was not reported as a discovery stage failure")
	}
	if b, err := os.ReadFile(filepath.Join(dir, "routes.golden.json")); err == nil && strings.Contains(string(b), "refused") {
		t.Errorf("a discovery failure was recorded as a refusal:\n%s", b)
	}
}

// echoingSynthetic is Synthetic with an EnvExports that echoes a prior
// environment value, so a case's env.json is observable in its binding stage.
func echoingSynthetic() ecosystem.Row {
	row := Synthetic
	base := row.EnvExports
	row.EnvExports = func(port int, prefix string, getenv func(string) string, routes []registrymanifest.Route) ([]ecosystem.EnvExport, []string) {
		exports, warnings := base(port, prefix, getenv, routes)
		if v := getenv("SYNTHETIC_EXTRA"); v != "" {
			exports = append(exports, ecosystem.EnvExport{Name: "SYNTHETIC_EXTRA", Value: v})
		}
		return exports, warnings
	}
	return row
}

func TestCaseEnvIsThePriorEnvironment(t *testing.T) {
	binding := func(name string) string {
		dir := copyCase(t, name, false)
		if errs := replayCase(echoingSynthetic(), dir, true); len(errs) != 0 {
			t.Fatal(errs)
		}
		b, err := os.ReadFile(filepath.Join(dir, "binding.golden.json"))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if got := binding("env"); !strings.Contains(got, "SYNTHETIC_EXTRA") || !strings.Contains(got, "from-env-json") {
		t.Errorf("env case binding does not carry env.json's value:\n%s", got)
	}
	if got := binding("matched"); strings.Contains(got, "SYNTHETIC_EXTRA") {
		t.Errorf("a case with no env.json read a prior environment:\n%s", got)
	}
}

// recorder is a testing.TB that records failures instead of failing the test.
type recorder struct {
	testing.TB
	failed bool
}

func (r *recorder) Helper()               {}
func (r *recorder) Errorf(string, ...any) { r.failed = true }

func TestCaseDirsFailsOnEmptyCorpus(t *testing.T) {
	for name, dir := range map[string]string{
		"empty":   t.TempDir(),
		"missing": filepath.Join(t.TempDir(), "absent"),
	} {
		t.Run(name, func(t *testing.T) {
			r := &recorder{TB: t}
			if got := caseDirs(r, dir); len(got) != 0 || !r.failed {
				t.Fatalf("cases = %v, failed = %v; want none and failed", got, r.failed)
			}
		})
	}
}

func TestClassificationGoldenPinsTheLockfileMatch(t *testing.T) {
	if got := readGolden(t, "matched", "classification.golden.txt"); got != "synthetic\n" {
		t.Errorf("matched classification golden = %q, want %q", got, "synthetic\n")
	}
	if got := readGolden(t, "unmatched", "classification.golden.txt"); got != "\n" {
		t.Errorf("unmatched classification golden = %q, want a bare newline", got)
	}
}

func TestReplayCaseDriftedClassificationGoldenFails(t *testing.T) {
	dir := copyCase(t, "matched", true)
	if err := os.WriteFile(filepath.Join(dir, "classification.golden.txt"), []byte("\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	errs := replayCase(Synthetic, dir, false)
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "lockfile classification") {
		t.Fatalf("errs = %v, want exactly the drifted classification", errs)
	}
}

func TestEnvJSONWithoutEnvExportsFails(t *testing.T) {
	row := Synthetic
	row.EnvExports = nil
	errs := replayCase(row, copyCase(t, "env", false), true)
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "env.json") || !strings.Contains(errs[0].Error(), "EnvExports") {
		t.Fatalf("errs = %v, want one naming env.json and EnvExports", errs)
	}
}

func TestNullEnvJSONWithoutEnvExportsFails(t *testing.T) {
	row := Synthetic
	row.EnvExports = nil
	dir := copyCase(t, "env", false)
	if err := os.WriteFile(filepath.Join(dir, "env.json"), []byte("null"), 0o644); err != nil {
		t.Fatal(err)
	}
	errs := replayCase(row, dir, true)
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "env.json") || !strings.Contains(errs[0].Error(), "EnvExports") {
		t.Fatalf("errs = %v, want one naming env.json and EnvExports", errs)
	}
}

func TestMalformedEnvJSONFails(t *testing.T) {
	dir := copyCase(t, "env", false)
	if err := os.WriteFile(filepath.Join(dir, "env.json"), []byte(`{"SYNTHETIC_EXTRA": 1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	errs := replayCase(echoingSynthetic(), dir, true)
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "env.json") {
		t.Fatalf("errs = %v, want one naming env.json", errs)
	}
}

package ecosystemtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/ecosystem"
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
	// A row with no config parser and no route declaration takes part in no stage.
	bare := ecosystem.Row{Name: "bare"}
	dir := copyCase(t, "matched", false)
	if errs := replayCase(bare, dir, false); len(errs) != 0 {
		t.Fatalf("non-participating row failed without goldens: %v", errs)
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
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
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
	// A route-declaration-only row, with no bindings and no rewrite rows, takes
	// part in route resolution alone.
	row := ecosystem.Row{Name: "decl-only", RouteDeclaration: Synthetic.RouteDeclaration}
	var took []string
	for _, s := range stages {
		if s.takesPart(row) {
			took = append(took, s.name)
		}
	}
	if len(took) != 1 || took[0] != "route resolution" {
		t.Fatalf("took part in %v, want route resolution alone", took)
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

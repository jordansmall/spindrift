package driver_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/driver"
)

// module is the launcher module path, derived from the registry package.
var module = strings.TrimSuffix(registry, "/internal/driver")

// bundleBinaries are the packages that link the bundled Drivers into a
// shipped binary (ADR 0066 decision 8). quickstart validates DRIVER through
// driver.New; daemon links dispatchrecord's log parsing.
var bundleBinaries = []string{
	module,
	module + "/box",
	module + "/daemon",
	module + "/driver-exec",
	module + "/orchestrator",
	module + "/quickstart",
}

// bundleFixtures are permanent exceptions: seamtest/bundled tests
// RegisterDriver's bundle-present no-op.
var bundleFixtures = []string{
	module + "/internal/seamtest/bundled",
}

// coreBackends are the backends core keeps (ADR 0068, decision 7). Every other
// backend row is guarded by default. Mirrored by coreBackendNames in
// nix/checks/go.nix.
var coreBackends = []string{"git", "local"}

// guardedBackends returns the forge package path of every backend row not in
// coreBackends.
func guardedBackends(t *testing.T) []string {
	t.Helper()
	var pkgs []string
	for _, c := range coreBackends {
		if !slices.ContainsFunc(backend.Registry, func(d backend.Descriptor) bool { return d.Name == c }) {
			t.Fatalf("coreBackends names %q, which is no backend.Registry row", c)
		}
	}
	for _, d := range backend.Registry {
		if !slices.Contains(coreBackends, d.Name) {
			pkgs = append(pkgs, module+"/internal/forge/"+d.Name)
		}
	}
	if len(pkgs) == 0 {
		t.Fatal("no guarded backend packages; nothing to guard")
	}
	return pkgs
}

// isGuarded reports whether path is the bundle, any registered Driver's package
// (names), any guarded backend's package (backends), or a subpackage of one.
func isGuarded(path string, names, backends []string) bool {
	roots := slices.Clone(backends)
	for _, n := range append([]string{"bundle"}, names...) {
		roots = append(roots, registry+"/"+n)
	}
	for _, pkg := range roots {
		if path == pkg || strings.HasPrefix(path, pkg+"/") {
			return true
		}
	}
	return false
}

// bundleReach parses `go list -test -f '{{.ImportPath}}|{{.ForTest}}|{{join
// .Deps " "}}'` output into, per package, the subject packages its test
// closure reaches. A package's closure is its own Deps plus those of its
// synthetic "P.test" binary, which covers its test and xtest files. Subject
// packages are skipped as importers: they are what is guarded.
func bundleReach(out string, isSubject func(string) bool) map[string][]string {
	reach := map[string][]string{}
	for _, line := range strings.Split(out, "\n") {
		path, rest, ok := strings.Cut(strings.TrimSpace(line), "|")
		if !ok {
			continue
		}
		forTest, deps, _ := strings.Cut(rest, "|")
		// Skip the "P_test [P.test]" and "P [P.test]" variants: P.test's
		// Deps already include their imports.
		if forTest != "" || strings.Contains(path, " ") {
			continue
		}
		path = strings.TrimSuffix(path, ".test")
		if isSubject(path) {
			continue
		}
		for _, dep := range strings.Fields(deps) {
			if isSubject(dep) && !slices.Contains(reach[path], dep) {
				reach[path] = append(reach[path], dep)
			}
		}
		if _, seen := reach[path]; !seen {
			reach[path] = nil
		}
	}
	return reach
}

// bundleProblems names each package that reaches a subject package without
// being listed in binaries; list names the Go list the message points at.
func bundleProblems(reach map[string][]string, binaries []string, list string) []string {
	var problems []string
	pkgs := make([]string, 0, len(reach))
	for pkg := range reach {
		pkgs = append(pkgs, pkg)
	}
	sort.Strings(pkgs)
	for _, pkg := range pkgs {
		if len(reach[pkg]) > 0 && !slices.Contains(binaries, pkg) {
			problems = append(problems, fmt.Sprintf("%s's test closure reaches %s; only %s may", pkg, strings.Join(reach[pkg], ", "), list))
		}
	}
	return problems
}

// TestOnlyBinariesReachTheBundle pins issues #5541, #5545 and #5648: only the
// binaries (and the seamtest/bundled fixture) may reach the bundle, a Driver or
// a non-core backend adapter (github, forgejo, jira) in their tests; every
// library package tests through seamtest's Strategy or the forge fake. The
// scan is one go list over the module, so it costs one invocation, not one per
// package.
func TestOnlyBinariesReachTheBundle(t *testing.T) {
	names := driver.DescriptorNames()
	if len(names) == 0 {
		t.Fatal("driver.DescriptorNames() is empty; nothing to guard")
	}
	out := goList(t, "-test", "-f", `{{.ImportPath}}|{{.ForTest}}|{{join .Deps " "}}`, module+"/...")
	backends := guardedBackends(t)
	isSubject := func(path string) bool { return isGuarded(path, names, backends) }
	for _, p := range bundleProblems(bundleReach(out, isSubject), slices.Concat(bundleBinaries, bundleFixtures), bundleList) {
		t.Error(p)
	}
}

// hostBundleBinaries are the binaries that run the butler sweep and so link the
// built-in Records digests; the Box and driver-exec must not (ADR 0070
// decision 2).
var hostBundleBinaries = []string{
	module,
}

const (
	bundleList     = "bundleBinaries and bundleFixtures"
	hostBundleList = "hostBundleBinaries"
)

func isHostBundle(path string) bool { return path == module+"/internal/hostbundle" }

// TestOnlyButlerBinariesReachTheHostBundle keeps the host bundle, and with it
// settle, tuning and the Records store, out of every other package's test
// closure.
func TestOnlyButlerBinariesReachTheHostBundle(t *testing.T) {
	out := goList(t, "-test", "-f", `{{.ImportPath}}|{{.ForTest}}|{{join .Deps " "}}`, module+"/...")
	for _, p := range bundleProblems(bundleReach(out, isHostBundle), hostBundleBinaries, hostBundleList) {
		t.Error(p)
	}
}

// hostSidePackages are the packages the in-Box binaries' closures must not
// link: the butler sweep's (hostbundle through butler), dispatch, and the
// Records store, dispatchrecord (issue #5822). chore needs no entry: it
// imports dispatchrecord, so any reach to it is already caught.
var hostSidePackages = []string{
	"hostbundle", "recordsdigest", "tuning", "settle", "butler", "dispatch", "dispatchrecord",
}

// hostSideProblems names each hostSidePackages entry in deps, the -deps
// closure of the module's bin binary.
func hostSideProblems(bin string, deps []string) []string {
	var problems []string
	for _, f := range hostSidePackages {
		if slices.Contains(deps, module+"/internal/"+f) {
			problems = append(problems, fmt.Sprintf("%s's closure reaches internal/%s", bin, f))
		}
	}
	return problems
}

// TestBoxAndDriverExecLinkNoHostSide pins that the in-Box binaries' closures
// do not grow the butler sweep's packages, dispatch or the Records store.
func TestBoxAndDriverExecLinkNoHostSide(t *testing.T) {
	for _, bin := range []string{"box", "driver-exec"} {
		for _, p := range hostSideProblems(bin, strings.Fields(goList(t, "-deps", module+"/"+bin))) {
			t.Error(p)
		}
	}
}

func TestBundleProblemsClassifiesPlantedReach(t *testing.T) {
	names := []string{"claude", "opencode"}
	m := module
	backends := []string{m + "/internal/forge/jira", m + "/internal/forge/forgejo"}
	out := strings.Join([]string{
		m + "/internal/stray||fmt " + registry + "/claude",
		m + "/internal/stray.test||fmt " + registry + "/claude",
		m + "/internal/stray [" + m + "/internal/stray.test]|" + m + "/internal/stray|fmt",
		m + "/internal/clean.test||fmt " + registry,
		m + "/box||" + registry + "/bundle",
		registry + "/claude.test||" + registry + "/opencode",
		registry + "/bundle||" + registry + "/claude",
		m + "/internal/stray2.test||fmt " + m + "/internal/forge/jira",
		m + "/internal/stray3.test||fmt " + m + "/internal/forge/git",
		m + "/internal/forge/jira.test||" + m + "/internal/forge/forgejo",
	}, "\n")
	isSubject := func(path string) bool { return isGuarded(path, names, backends) }
	reach := bundleReach(out, isSubject)
	got := bundleProblems(reach, []string{m + "/box"}, bundleList)
	want := []string{
		m + "/internal/stray's test closure reaches " + registry + "/claude; only bundleBinaries and bundleFixtures may",
		m + "/internal/stray2's test closure reaches " + m + "/internal/forge/jira; only bundleBinaries and bundleFixtures may",
	}
	if !slices.Equal(got, want) {
		t.Errorf("bundleProblems = %q, want %q", got, want)
	}

	hostOut := strings.Join([]string{
		m + "/internal/stray.test||fmt " + m + "/internal/hostbundle",
		m + "||" + m + "/internal/hostbundle",
		m + "/box||" + registry + "/bundle",
		m + "/internal/hostbundle||fmt",
	}, "\n")
	gotHost := bundleProblems(bundleReach(hostOut, isHostBundle), []string{m}, hostBundleList)
	wantHost := []string{
		m + "/internal/stray's test closure reaches " + m + "/internal/hostbundle; only hostBundleBinaries may",
	}
	if !slices.Equal(gotHost, wantHost) {
		t.Errorf("host bundleProblems = %q, want %q", gotHost, wantHost)
	}
}

func TestHostSideProblemsClassifiesPlantedReach(t *testing.T) {
	m := module
	planted := []string{
		"fmt", m + "/internal/dispatch", m + "/internal/dispatchrecord",
		m + "/internal/dispatchkey", m + "/internal/dispatchkind", m + "/internal/hostpaths",
	}
	want := []string{"box's closure reaches internal/dispatch", "box's closure reaches internal/dispatchrecord"}
	if got := hostSideProblems("box", planted); !slices.Equal(got, want) {
		t.Errorf("hostSideProblems = %q, want %q", got, want)
	}
}

// orchestratorExcludedBundleFiles are the bundle files lib/harness-core.nix
// driverTreeFiles drops from the orchestrator's source (the Box and
// driver-exec add some back). The inbox-bins-register-ecosystems Nix check
// fails unless this list equals the bundle files the orchestrator's src lacks.
var orchestratorExcludedBundleFiles = []string{"runners.go", "backends.go", "ecosystems.go"}

// ecosystemProblems names each package in deps that is internal/ecosystem or
// lives under it.
func ecosystemProblems(bin string, deps []string) []string {
	root := module + "/internal/ecosystem"
	var problems []string
	for _, d := range deps {
		if d == root || strings.HasPrefix(d, root+"/") {
			problems = append(problems, fmt.Sprintf("%s's closure reaches %s", bin, strings.TrimPrefix(d, module+"/")))
		}
	}
	return problems
}

// TestOrchestratorLinksNoEcosystem pins that the orchestrator never binds an
// ecosystem, so ecosystem churn leaves its drvPath and vendor hash alone. The
// full tree's orchestrator does link the bundle, and through it ecosystems.go;
// Nix drops that file from the orchestrator's source, so the closure is taken
// with the same files deleted through a go -overlay.
func TestOrchestratorLinksNoEcosystem(t *testing.T) {
	bundleDir, err := filepath.Abs("bundle")
	if err != nil {
		t.Fatal(err)
	}
	replace := map[string]string{}
	for _, f := range orchestratorExcludedBundleFiles {
		path := filepath.Join(bundleDir, f)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("orchestratorExcludedBundleFiles names %s: %v", f, err)
		}
		replace[path] = ""
	}
	overlay, err := json.Marshal(map[string]map[string]string{"Replace": replace})
	if err != nil {
		t.Fatal(err)
	}
	overlayPath := filepath.Join(t.TempDir(), "overlay.json")
	if err := os.WriteFile(overlayPath, overlay, 0o600); err != nil {
		t.Fatal(err)
	}

	deps := strings.Fields(goList(t, "-overlay", overlayPath, "-deps", module+"/orchestrator"))
	if !slices.Contains(deps, module+"/internal/driver/bundle") {
		t.Fatal("orchestrator's closure lost the bundle; the overlay no longer models the Nix source")
	}
	for _, p := range ecosystemProblems("orchestrator", deps) {
		t.Error(p)
	}
}

func TestEcosystemProblemsClassifiesPlantedReach(t *testing.T) {
	m := module
	planted := []string{
		"fmt", m + "/internal/ecosystem", m + "/internal/ecosystem/ecosystemtest",
		m + "/internal/ecosystems", m + "/internal/registryvocab",
	}
	want := []string{
		"orchestrator's closure reaches internal/ecosystem",
		"orchestrator's closure reaches internal/ecosystem/ecosystemtest",
	}
	if got := ecosystemProblems("orchestrator", planted); !slices.Equal(got, want) {
		t.Errorf("ecosystemProblems = %q, want %q", got, want)
	}
}

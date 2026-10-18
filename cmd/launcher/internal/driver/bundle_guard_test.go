package driver_test

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"

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

// bundleAllowList is the ADR 0066 decision 8 migration's to-do list: the
// library packages whose tests still blank-import the bundle (or a Driver)
// for lack of a Strategy. Each entry leaves as its package moves onto
// seamtest's Strategy; a package that no longer reaches a Driver is reported
// stale.
var bundleAllowList = []string{
	registry,
}

// isBundleOrDriver reports whether path is the bundle or a Driver package or
// subpackage, for every registered Driver in names.
func isBundleOrDriver(path string, names []string) bool {
	for _, n := range append([]string{"bundle"}, names...) {
		pkg := registry + "/" + n
		if path == pkg || strings.HasPrefix(path, pkg+"/") {
			return true
		}
	}
	return false
}

// bundleReach parses `go list -test -f '{{.ImportPath}}|{{.ForTest}}|{{join
// .Deps " "}}'` output into, per package, the bundle and Driver packages its
// test closure reaches. A package's closure is its own Deps plus those of its
// synthetic "P.test" binary, which covers its test and xtest files. Packages
// inside the bundle and Driver trees are skipped: they are the subject.
func bundleReach(out string, names []string) map[string][]string {
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
		if isBundleOrDriver(path, names) {
			continue
		}
		for _, dep := range strings.Fields(deps) {
			if isBundleOrDriver(dep, names) && !slices.Contains(reach[path], dep) {
				reach[path] = append(reach[path], dep)
			}
		}
		if _, seen := reach[path]; !seen {
			reach[path] = nil
		}
	}
	return reach
}

// bundleProblems names each package that reaches the bundle or a Driver
// without being a binary or on the allow-list, and each allow-list entry that
// no longer does.
func bundleProblems(reach map[string][]string, binaries, allow []string) []string {
	var problems []string
	pkgs := make([]string, 0, len(reach))
	for pkg := range reach {
		pkgs = append(pkgs, pkg)
	}
	sort.Strings(pkgs)
	for _, pkg := range pkgs {
		reached := len(reach[pkg]) > 0
		switch {
		case reached && !slices.Contains(binaries, pkg) && !slices.Contains(allow, pkg):
			problems = append(problems, fmt.Sprintf("%s's test closure reaches %s; only the binaries and the allow-list may", pkg, strings.Join(reach[pkg], ", ")))
		case !reached && slices.Contains(allow, pkg):
			problems = append(problems, pkg+" is on the allow-list but reaches no Driver; remove the stale entry")
		}
	}
	for _, pkg := range allow {
		if _, ok := reach[pkg]; !ok {
			problems = append(problems, pkg+" is on the allow-list but is not a module package")
		}
	}
	return problems
}

// TestBundleImportsStayOnAllowList pins issue #5541: only the binaries and a
// shrinking allow-list of library packages may reach the bundle or a Driver
// in their tests; everything else tests through seamtest's Strategy. The scan
// is one go list over the module, so it costs one invocation, not one per
// package.
func TestBundleImportsStayOnAllowList(t *testing.T) {
	names := driver.DescriptorNames()
	if len(names) == 0 {
		t.Fatal("driver.DescriptorNames() is empty; nothing to guard")
	}
	out := goList(t, "-test", "-f", `{{.ImportPath}}|{{.ForTest}}|{{join .Deps " "}}`, module+"/...")
	for _, p := range bundleProblems(bundleReach(out, names), slices.Concat(bundleBinaries, bundleFixtures), bundleAllowList) {
		t.Error(p)
	}
}

func TestBundleProblemsClassifiesPlantedReach(t *testing.T) {
	names := []string{"claude", "opencode"}
	m := module
	out := strings.Join([]string{
		m + "/internal/stray||fmt " + registry + "/claude",
		m + "/internal/stray.test||fmt " + registry + "/claude",
		m + "/internal/stray [" + m + "/internal/stray.test]|" + m + "/internal/stray|fmt",
		m + "/internal/listed.test||fmt " + registry + "/bundle",
		m + "/internal/clean.test||fmt " + registry,
		m + "/internal/stale||fmt",
		m + "/box||" + registry + "/bundle",
		registry + "/claude.test||" + registry + "/opencode",
		registry + "/bundle||" + registry + "/claude",
	}, "\n")
	reach := bundleReach(out, names)
	got := bundleProblems(reach, []string{m + "/box"}, []string{m + "/internal/listed", m + "/internal/stale", m + "/internal/gone"})
	want := []string{
		m + "/internal/stale is on the allow-list but reaches no Driver; remove the stale entry",
		m + "/internal/stray's test closure reaches " + registry + "/claude; only the binaries and the allow-list may",
		m + "/internal/gone is on the allow-list but is not a module package",
	}
	if !slices.Equal(got, want) {
		t.Errorf("bundleProblems = %q, want %q", got, want)
	}
}

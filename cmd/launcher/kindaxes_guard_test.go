package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"spindrift.dev/launcher/internal/dispatchkind"
)

// kindAxisForbidden pins one old kind-shaped comparison that the descriptor
// axes replaced (issue #3989, #5673). why names the sanctioned form a caller
// should use instead; a line that also matches unless is a legitimate use of
// the same spelling.
type kindAxisForbidden struct {
	pattern *regexp.Regexp
	unless  *regexp.Regexp
	why     string
}

func (fp kindAxisForbidden) matches(line string) bool {
	return fp.pattern.MatchString(line) && (fp.unless == nil || !fp.unless.MatchString(line))
}

const dispatchkindDir = "internal/dispatchkind"

// kindAxisConsts maps every tag axis of dispatchkind.Descriptor -- each field
// whose type is an exported, package-declared signed or unsigned integer
// type -- to the exported constants of that type found in dir's non-test
// files. A constant counts as typed when its spec names the type
// (`C Keying = ...`), is a conversion (`C = Keying(3)`), or inherits either
// from an earlier spec of the block (an iota run). Both halves are derived,
// so a new axis or value is guarded without editing this file. An axis with
// no constants is an error: it would leave an empty alternation in the
// patterns.
func kindAxisConsts(dir string) (map[string][]string, error) {
	desc := reflect.TypeOf(dispatchkind.Descriptor{})
	axes := map[string][]string{}
	for i := 0; i < desc.NumField(); i++ {
		ft := desc.Field(i).Type
		if k := ft.Kind(); k >= reflect.Int && k <= reflect.Uint64 && ft.PkgPath() == desc.PkgPath() && ast.IsExported(ft.Name()) {
			axes[ft.Name()] = nil
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, 0)
		if err != nil {
			return nil, err
		}
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			// A spec with neither type nor values repeats the previous spec's
			// type and expression, which is how an iota block is written.
			typ := ""
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				if vs.Type != nil || len(vs.Values) > 0 {
					typ = specTypeName(vs)
				}
				if _, isAxis := axes[typ]; !isAxis {
					continue
				}
				for _, n := range vs.Names {
					if n.IsExported() {
						axes[typ] = append(axes[typ], n.Name)
					}
				}
			}
		}
	}
	for name, consts := range axes {
		if len(consts) == 0 {
			return nil, fmt.Errorf("tag axis %s has no exported constants in %s", name, dir)
		}
	}
	return axes, nil
}

// specTypeName is the type a constant spec declares, either explicitly or
// through a conversion value; "" when it names neither.
func specTypeName(vs *ast.ValueSpec) string {
	if id, ok := vs.Type.(*ast.Ident); ok {
		return id.Name
	}
	if vs.Type == nil && len(vs.Values) == 1 {
		if call, ok := vs.Values[0].(*ast.CallExpr); ok {
			if id, ok := call.Fun.(*ast.Ident); ok {
				return id.Name
			}
		}
	}
	return ""
}

// kindAxes is kindAxisConsts over the real package, read on first use rather
// than at package init: the seam tests re-exec this test binary as a runtime
// fake from another working directory, where dispatchkindDir does not resolve.
var kindAxes = sync.OnceValue(func() map[string][]string {
	axes, err := kindAxisConsts(dispatchkindDir)
	if err != nil {
		panic("kindaxes guard: " + err.Error())
	}
	return axes
})

func allKindAxisConsts() []string {
	var all []string
	for _, consts := range kindAxes() {
		all = append(all, consts...)
	}
	slices.Sort(all)
	return all
}

const tagWhy = "compares a descriptor tag -- switch on it in the package the tag selects, or add a method on the enum in dispatchkind"

// keyingWireValues is every distinct Keying.String() across the registry, the
// DISPATCH_KEYING values a hand-spelled literal could duplicate.
func keyingWireValues() []string {
	var wire []string
	for _, d := range dispatchkind.All {
		if w := regexp.QuoteMeta(d.Keying.String()); !slices.Contains(wire, w) {
			wire = append(wire, w)
		}
	}
	return wire
}

// kindAxisForbiddenPatterns read one line at a time, so they catch the
// spellings the tree has used, not every way to write a comparison: a
// comparison split across lines, a parenthesised or converted constant, or a
// wire value copied into a local first all slip past.
var kindAxisForbiddenPatterns = sync.OnceValue(func() []kindAxisForbidden {
	consts := "(" + strings.Join(allKindAxisConsts(), "|") + ")"
	wireRead := `(\bDispatchKeying\b|[gG]etenv\("DISPATCH_KEYING"\)|\.Keying\.String\(\)|\["DISPATCH_KEYING"\])`
	return []kindAxisForbidden{
		// The tag on the right also catches dispatchkind.ByChore.String(),
		// since \b fires before the dot.
		{pattern: regexp.MustCompile(`[!=]=\s*dispatchkind\.` + consts + `\b`), why: tagWhy},
		{pattern: regexp.MustCompile(`dispatchkind\.` + consts + `\b(\.String\(\))?\s*[!=]=`), why: tagWhy},
		// The DISPATCH_KEYING wire value is Keying.String(), so comparing it to
		// anything but "" is a Keying comparison, however the env is read; the
		// operand next to the operator may not be a bare "".
		{pattern: regexp.MustCompile(wireRead + `\s*[!=]=\s*([^"\s]|"[^"])`), why: tagWhy},
		{pattern: regexp.MustCompile(`([^"\s]|[^"]")\s*[!=]=\s*[\w.()]*` + wireRead), why: tagWhy},
		// A wire literal in a case clause duplicates a Keying constant's String().
		// The line cannot show the switch subject, so this also flags the same
		// literal in a switch on some other string; the message says so.
		{pattern: regexp.MustCompile(`\bcase\s+("[^"]*",\s*)*"(` + strings.Join(keyingWireValues(), "|") + `)"`), why: "spells a DISPATCH_KEYING wire value -- use dispatchkind.<Const>.String(); in a switch on some other string, name the value as a const instead"},
		{pattern: regexp.MustCompile(`SelfContainedBase\s*!=\s*""`), why: "tests prompt-file presence as a kind test -- read Tracker instead"},
		// == "" is legitimate only as the --self-contained capability check,
		// which always pairs it with the non-negated selfContained flag.
		{pattern: regexp.MustCompile(`SelfContainedBase\s*==\s*""`), unless: regexp.MustCompile(`(^|[^!\w.])(\w+\.)*[sS]elfContained\s*&&`), why: "tests prompt-file absence as a kind test -- read Tracker instead"},
	}
})

// kindNameWhy explains namesKind's finding; Default is not in its pattern.
const kindNameWhy = "names one kind -- read the axis from the descriptor in hand, walk dispatchkind.All, or use dispatchkind.Default for an unset kind; only an entry point selecting its own verb's kind, or a listed work-kind fact, may name it"

// pickerFile is the one file allowed to bind a per-verb Kind<Verb> alias.
const pickerFile = "internal/console/pick.go"

// workKindFacts lists, per file, the exact source lines allowed to name Work,
// each with the number of sites it covers there: they state a fact about the
// work kind itself (only work Dispatches land code; frozen old-log shapes,
// like the pre-#734 implementing verb, are work's), not an unset kind, so
// re-pointing Default must not move them (issue #5729). The allowance matches
// line text, so TestWorkKindFactsSites pins the counts: a new line sharing an
// allowed text still fails.
var workKindFacts = map[string]map[string]int{
	"internal/tuning/tuning.go": {
		`if k, _ := dispatchkind.ByName(r.Kind); k == dispatchkind.Work {`: 1,
	},
	"internal/dispatchrecord/dispatchrecord.go": {
		`case announced != "" && announcedDesc != dispatchkind.Work:`: 1,
		`kind = dispatchkind.Work.Name`:                               2,
	},
}

var (
	kindNameRef       = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`\bdispatchkind\.` + kindVarAlternation() + `\b`) })
	kindBootstrapCall = regexp.MustCompile(`\b(bootstrap|applyDispatchKind)\(`)
	kindPickerAlias   = regexp.MustCompile(`^\s*Kind(\w+)\s*=\s*dispatchkind\.(\w+)\s*$`)
)

// namesKind reports whether line, read from path, names one kind outside an
// entry point selecting its own verb's kind: package main's bootstrap calls,
// and the console picker's per-verb aliases, each bound to its own verb. The
// workKindFacts lines are also exempt, matched whole at their own path.
func namesKind(path, line string) bool {
	if !kindNameRef().MatchString(line) {
		return false
	}
	p := filepath.ToSlash(path)
	if !strings.Contains(p, "/") && kindBootstrapCall.MatchString(line) {
		return false
	}
	if _, ok := workKindFacts[p][strings.TrimSpace(line)]; ok {
		return false
	}
	m := kindPickerAlias.FindStringSubmatch(line)
	return p != pickerFile || m == nil || m[1] != m[2]
}

// kindVarAlternation is a capturing group of the exported var each registered
// kind is generated as (its row Name, first letter upper-cased). Default is
// left out: it is the sanctioned way to name "the unset kind".
func kindVarAlternation() string {
	var alts []string
	for _, d := range dispatchkind.All {
		alts = append(alts, regexp.QuoteMeta(kindVarName(d.Name)))
	}
	return "(" + strings.Join(alts, "|") + ")"
}

func kindVarName(rowName string) string {
	return strings.ToUpper(rowName[:1]) + rowName[1:]
}

// findingLabelPattern is a capturing group alternating every non-empty
// FindingLabel in the registry, so a new kind's label is guarded without
// editing this file. Kinds that share a FindingLabel repeat an alternative,
// which is harmless.
var findingLabelPattern = func() string {
	var alts []string
	for _, d := range dispatchkind.All {
		if d.FindingLabel != "" {
			alts = append(alts, regexp.QuoteMeta(d.FindingLabel))
		}
	}
	return "(" + strings.Join(alts, "|") + ")"
}()

var findingLabelLiteral = regexp.MustCompile(`"` + findingLabelPattern + `"`)

const settleDir = "internal/settle/"

// kindAxisViolations returns why line, read from path, fails the guard: one
// distinct reason per forbidden spelling it carries.
func kindAxisViolations(path, line string) []string {
	if strings.HasPrefix(strings.TrimSpace(line), "//") {
		return nil // prose mentioning an axis's old name isn't a comparison
	}
	var whys []string
	for _, fp := range kindAxisForbiddenPatterns() {
		if fp.matches(line) && !slices.Contains(whys, fp.why) {
			whys = append(whys, fp.why) // two patterns can read one comparison
		}
	}
	if namesKind(path, line) {
		whys = append(whys, kindNameWhy)
	}
	if strings.Contains(filepath.ToSlash(path), settleDir) && findingLabelLiteral.MatchString(line) {
		whys = append(whys, "finding-label literal instead of the FindingLabel axis")
	}
	return whys
}

// TestKindAxesGuard walks the launcher module tree (prior art: the marker and
// parity checks) and fails on any of the old kind == kind comparisons the
// descriptor axes replaced, so a reintroduced comparison is caught here
// instead of drifting back in unnoticed.
func TestKindAxesGuard(t *testing.T) {
	settleFiles := 0
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != "." && (name == "dispatchkind" || name == "testdata" || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(filepath.ToSlash(path), settleDir) {
			settleFiles++
		}
		for i, line := range strings.Split(string(raw), "\n") {
			for _, why := range kindAxisViolations(path, line) {
				t.Errorf("%s:%d: %s\n\t%s", path, i+1, why, strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk .: %v", err)
	}
	if settleFiles == 0 {
		t.Fatalf("no .go files under %s: the finding-label guard is matching the wrong path", settleDir)
	}
}

// guardHits reports whether the guard flags line in an ordinary file: one
// outside settle, package main and the console picker.
func guardHits(line string) bool {
	return len(kindAxisViolations("internal/x/x.go", line)) > 0
}

// TestKindAxesGuardPatterns pins which spellings the guard treats as a kind
// test, so a loosened or over-broad pattern shows up here rather than as a
// silently passing or spuriously failing walk.
func TestKindAxesGuardPatterns(t *testing.T) {
	forbidden := []string{
		`if c.kind().Settle == dispatchkind.SettleVerdict {`,
		`if dispatchkind.SettleVerdict == c.kind().Settle {`,
		`if d.Settle != dispatchkind.SettleMerge {`,
		`if d.Prompts.SelfContainedBase != "" {`,
		`if d.Prompts.SelfContainedBase == "" {`,
		`if !c.selfContained && d.Prompts.SelfContainedBase == "" {`,
		`if d.Labels == dispatchkind.LabelsResearch {`,
		`if dispatchkind.LabelsNone != d.Labels {`,
		`ResearchDispatch: c.kind().Tracker == dispatchkind.TrackerResearch,`,
		`if t == dispatchkind.TrackerResearch {`,
		`if kindPriority(k) == dispatchkind.PriorityReserved {`,
		`reported: src == dispatchkind.DemandChildReported && probe[k] == 0, upperBound: src == dispatchkind.DemandHostOutbox}`,
		`if d.Enablement != dispatchkind.EnabledByOutboxRelay {`,
		`if d.Contract == dispatchkind.ContractLanding {`,
		`return ok && d.Keying == dispatchkind.ByChore`,
		`if dispatchkind.ByIssue != d.Keying {`,
		`if r.env.DispatchKeying == dispatchkind.ByChore.String() {`,
		`if dispatchkind.ByChore.String() != r.env.DispatchKeying {`,
		`if getenv("DISPATCH_KEYING") == "chore" {`,
		`if os.Getenv("DISPATCH_KEYING") != "issue" {`,
		`if "chore" == getenv("DISPATCH_KEYING") {`,
		`if d.Keying.String() == "chore" {`,
		`if c.kind().Keying.String() != x {`,
		`if x == d.Keying.String() {`,
		`if x != c.kind().Keying.String() {`,
		`if "issue" != env["DISPATCH_KEYING"] {`,
		`if env["DISPATCH_KEYING"] == "chore" {`,
		`case "chore":`,
		`case "issue", "chore":`,
		`x := dispatchkind.Work.FindingLabel`,
		`return dispatchkind.Recover`,
		`if d == dispatchkind.Butler {`,
		`KindWork := dispatchkind.Work.Name`,
	}
	allowed := []string{
		`switch c.kind().Settle {`,
		`if r.Settle == nil {`,
		`if c.selfContained && c.kind().Prompts.SelfContainedBase == "" {`,
		`if e.SelfContained && d.Prompts.Base != "" && d.Prompts.SelfContainedBase == "" {`,
		// Compares two descriptors' families to skip the kind's own; it never
		// branches on a tag value, so a new family cannot slip past it.
		`if d.Labels == ownFamily {`,
		`switch d.Keying {`,
		`case dispatchkind.ByChore:`,
		`switch kindPriority(k) {`,
		`switch r.env.DispatchKeying {`,
		`switch getenv("DISPATCH_KEYING") {`,
		`case dispatchkind.ByChore.String():`,
		`switch d.Keying.String() {`,
		`switch env["DISPATCH_KEYING"] {`,
		`if e.DispatchKeying != "" {`,
		`if getenv("DISPATCH_KEYING") == "" {`,
		`if "" != env["DISPATCH_KEYING"] {`,
		`case jiraIssueType:`,
		`d := dispatchkind.Default.Name`,
		`for _, d := range dispatchkind.All {`,
	}
	for _, line := range forbidden {
		if !guardHits(line) {
			t.Errorf("guard misses kind test: %s", line)
		}
	}
	for _, line := range allowed {
		if guardHits(line) {
			t.Errorf("guard flags legitimate use: %s", line)
		}
	}
}

// TestKindAxesGuardEntryPoints pins where an entry point may name its own
// verb's kind: a bootstrap call in package main, and a Kind<Verb> alias bound
// to its own verb in the console picker -- nowhere else, and no other alias.
func TestKindAxesGuardEntryPoints(t *testing.T) {
	const (
		bootstrapCall = `lc, err := bootstrap(stdout, true, dispatchkind.Work, false)`
		applyCall     = `rc := applyDispatchKind(lc.config, dispatchkind.Research)`
		workAlias     = `KindWork     = dispatchkind.Work`

		tuningFile       = "internal/tuning/tuning.go"
		recordFile       = "internal/dispatchrecord/dispatchrecord.go"
		tuningLine       = `if k, _ := dispatchkind.ByName(r.Kind); k == dispatchkind.Work {`
		announcedLine    = `case announced != "" && announcedDesc != dispatchkind.Work:`
		workFallbackLine = `kind = dispatchkind.Work.Name`
	)
	for _, tc := range []struct {
		path, line string
		want       bool
	}{
		{"main.go", bootstrapCall, false},
		{"bootstrap.go", applyCall, false},
		{"internal/daemon/x.go", bootstrapCall, true},
		{"internal/daemon/x.go", applyCall, true},
		{pickerFile, workAlias, false},
		{pickerFile, `KindResearch = dispatchkind.Research`, false},
		{pickerFile, `KindWork = dispatchkind.Butler`, true},
		{"internal/console/launcher.go", workAlias, true},
		{pickerFile, `kind := dispatchkind.Work`, true},
		{tuningFile, tuningLine, false},
		{recordFile, announcedLine, false},
		{recordFile, "\t\t" + workFallbackLine + "  ", false},
		{recordFile, tuningLine, true},
		{"internal/daemon/x.go", tuningLine, true},
		{tuningFile, announcedLine, true},
		{tuningFile, `kind := dispatchkind.Work`, true},
		{tuningFile, strings.Replace(tuningLine, "Work", "Research", 1), true},
		{recordFile, strings.Replace(workFallbackLine, "Work", "Research", 1), true},
	} {
		if got := slices.Contains(kindAxisViolations(tc.path, tc.line), kindNameWhy); got != tc.want {
			t.Errorf("%s: %s flagged = %v, want %v", tc.path, tc.line, got, tc.want)
		}
	}
}

// TestWorkKindFactsSites pins how many lines in each file read each
// workKindFacts entry, keeping the by-text allowance to its granted sites.
func TestWorkKindFactsSites(t *testing.T) {
	for path, lines := range workKindFacts {
		src, err := os.ReadFile(filepath.FromSlash(path))
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]int{}
		for _, l := range strings.Split(string(src), "\n") {
			got[strings.TrimSpace(l)]++
		}
		for line, want := range lines {
			if got[line] != want {
				t.Errorf("%s: %d lines read %q, want %d", path, got[line], line, want)
			}
		}
	}
}

// TestKindAxesGuardFindingLabels plants every registered kind's FindingLabel
// as a literal and checks the guard flags it under settle, and only there.
func TestKindAxesGuardFindingLabels(t *testing.T) {
	const inSettle, outsideSettle = settleDir + "x.go", "internal/dispatch/x.go"
	planted := 0
	for _, d := range dispatchkind.All {
		if d.FindingLabel == "" {
			continue
		}
		planted++
		t.Run(d.Name, func(t *testing.T) {
			line := `labels = append(labels, ` + strconv.Quote(d.FindingLabel) + `)`
			if got := kindAxisViolations(inSettle, line); len(got) != 1 {
				t.Errorf("guard misses %s finding-label literal in %s: %s", d.Name, inSettle, line)
			}
			if got := kindAxisViolations(outsideSettle, line); len(got) != 0 {
				t.Errorf("guard flags %s finding-label literal outside settle: %q", d.Name, got)
			}
			if got := kindAxisViolations(inSettle, "// "+line); len(got) != 0 {
				t.Errorf("guard flags %s finding-label literal in a comment: %q", d.Name, got)
			}
		})
	}
	// With no label the alternation degenerates to `"()"`, which flags every
	// empty string literal in settle.
	if planted == 0 {
		t.Fatal("no kind in dispatchkind.All has a FindingLabel: the settle guard has nothing to match")
	}
	if got := kindAxisViolations(inSettle, `labels = append(labels, d.FindingLabel)`); len(got) != 0 {
		t.Errorf("guard flags legitimate use: %q", got)
	}
}

// TestKindAxesGuardEveryAxis plants every constant of every derived tag axis
// on either side of a comparison, so a value the alternation drops is caught.
func TestKindAxesGuardEveryAxis(t *testing.T) {
	for _, want := range []string{"Keying", "LabelFamily", "Settle", "PromptContract", "Tracker", "DaemonPriority", "Enablement", "DemandSource"} {
		if len(kindAxes()[want]) == 0 {
			t.Errorf("axis %s missing from the derived axes %v: the reflection dropped it", want, kindAxes())
		}
	}
	for axis, consts := range kindAxes() {
		for _, c := range consts {
			for _, line := range []string{
				`if x == dispatchkind.` + c + ` {`,
				`if dispatchkind.` + c + ` != x {`,
			} {
				if !guardHits(line) {
					t.Errorf("guard misses %s constant %s: %s", axis, c, line)
				}
			}
		}
	}
}

// TestKindAxisConstsSpellings plants each real axis in a temp package, in turn
// as a typed iota block, as conversions, and as a conversion-headed iota run,
// and expects every name back.
func TestKindAxisConstsSpellings(t *testing.T) {
	var src strings.Builder
	src.WriteString("package x\n")
	want := map[string][]string{}
	// Sorted, so each axis gets the same spelling on every run.
	for i, axis := range slices.Sorted(maps.Keys(kindAxes())) {
		a, b := fmt.Sprintf("A%d", i), fmt.Sprintf("B%d", i)
		switch i % 3 {
		case 0:
			fmt.Fprintf(&src, "const (\n\t%s %s = iota\n\t%s\n)\n", a, axis, b)
		case 1:
			fmt.Fprintf(&src, "const %s = %s(1)\nconst %s = %s(2)\n", a, axis, b, axis)
		default:
			fmt.Fprintf(&src, "const (\n\t%s = %s(iota)\n\t%s\n)\n", a, axis, b)
		}
		want[axis] = []string{a, b}
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte(src.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := kindAxisConsts(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("kindAxisConsts = %v, want %v", got, want)
	}
}

func TestKindAxisConstsRejectsEmptyAxis(t *testing.T) {
	if _, err := kindAxisConsts(t.TempDir()); err == nil {
		t.Fatal("an empty package yielded axes with no constants without error")
	}
}

// TestKindVarNamesAreDeclared pins the name derivation the named-kind pattern
// rests on: each registered kind's derived var name must be declared in
// kinds_gen.go, and Default must stay out of the pattern.
func TestKindVarNamesAreDeclared(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dispatchkindDir, "kinds_gen.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	declared := map[string]bool{}
	for _, decl := range f.Decls {
		if gd, ok := decl.(*ast.GenDecl); ok && gd.Tok == token.VAR {
			for _, spec := range gd.Specs {
				for _, n := range spec.(*ast.ValueSpec).Names {
					declared[n.Name] = true
				}
			}
		}
	}
	for _, d := range dispatchkind.All {
		if name := kindVarName(d.Name); !declared[name] {
			t.Errorf("kind %q: derived var %s is not declared in kinds_gen.go", d.Name, name)
		}
	}
	if strings.Contains(kindVarAlternation(), "Default") {
		t.Error("named-kind pattern must not include Default")
	}
}

package seamtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The in-process Connector engine runs a Connector inside the host's own
// process. A Connector the Launcher hosts must run only in the Connector
// sandbox (internal/connectorsandbox), so no production binary may reach an
// in-process load of one. connectorhost is linked everywhere (the sandbox
// client needs its session side), so a link-level check cannot prove this:
// the guard allowlists the call sites instead.
const (
	modulePath       = "spindrift.dev/launcher"
	connectorhostPkg = modulePath + "/internal/connectorhost"
	forgePkg         = modulePath + "/internal/forge"
)

// engineCall is a call to an exported connectorhost Load* function (every
// in-process engine entry), or forge.NewFake (whose loader runs the engine
// in-process).
type engineCall struct {
	Pos    string   // file:line
	Func   string   // enclosing function, "Recv.Name" for a method
	Call   string   // e.g. "connectorhost.Load"
	Guards []string // identifiers an enclosing if-condition compares with == (see eqIdents)
}

// engineCalls returns every engineCall in src, resolving the package through
// the file's own import name rather than matching text.
func engineCalls(t *testing.T, filename string, src any) []engineCall {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}
	names := map[string]string{} // local import name -> import path
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		if p != connectorhostPkg && p != forgePkg {
			continue
		}
		name := p[strings.LastIndex(p, "/")+1:]
		if imp.Name != nil {
			name = imp.Name.Name
		}
		names[name] = p
	}
	var out []engineCall
	for _, decl := range f.Decls {
		// A package-level var or const has no enclosing function (Func "").
		fn := ""
		if fd, ok := decl.(*ast.FuncDecl); ok {
			fn = fd.Name.Name
			if fd.Recv != nil && len(fd.Recv.List) == 1 {
				rt := fd.Recv.List[0].Type
				if s, ok := rt.(*ast.StarExpr); ok {
					rt = s.X
				}
				if id, ok := rt.(*ast.Ident); ok {
					fn = id.Name + "." + fn
				}
			}
		}
		var stack []ast.Node
		ast.Inspect(decl, func(n ast.Node) bool {
			if n == nil {
				stack = stack[:len(stack)-1]
				return true
			}
			stack = append(stack, n)
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			id, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			if p := names[id.Name]; !(p == connectorhostPkg && strings.HasPrefix(sel.Sel.Name, "Load") ||
				p == forgePkg && sel.Sel.Name == "NewFake") {
				return true
			}
			var guards []string
			for i, a := range stack[:len(stack)-1] {
				if ifs, ok := a.(*ast.IfStmt); ok && stack[i+1] == ast.Node(ifs.Body) {
					guards = append(guards, eqIdents(ifs.Cond)...)
				}
			}
			out = append(out, engineCall{fset.Position(sel.Pos()).String(), fn, id.Name + "." + sel.Sel.Name, guards})
			return true
		})
	}
	return out
}

// allowedCall is a call site permitted to reach the in-process engine.
type allowedCall struct {
	File   string // module-relative, slash-separated
	Func   string
	Guard  string // identifier an enclosing if-condition must compare with ==; "" for none
	Reason string
}

var (
	// Both sides may reach these.
	commonAllowed = []allowedCall{
		{"internal/driver/driverrows/driverrows.go", "loadPolicy.load", "boxLoads",
			"the Box loads its first-party Drivers in-process; the Launcher's hostLoads path goes to connectorsandbox.Load"},
		{"internal/forge/fake_connector.go", "loadInProcess", "",
			"forge.Fake's test seam; only tests construct a Fake (checked below), never a production binary"},
	}
	// The Box mains also replay a shadow, which loads the shadow Module in-process.
	boxAllowed = append(slices.Clone(commonAllowed), allowedCall{
		"internal/driver/shadow/shadow.go", "Run", "",
		"driver-exec's shadow replay inside the Box; no Launcher-side main imports internal/driver/shadow",
	})
)

// mains are the production binaries, as module-relative package directories.
// The Launcher side hosts Connectors for the operator, so the engine must
// never run a Connector there; the Box side runs inside the sandbox it is
// confined to.
var (
	launcherMains = []string{".", "daemon", "quickstart"}
	boxMains      = []string{"orchestrator", "box", "driver-exec"}
)

// testOnlyPkgs are test-support packages (module-relative prefixes) that load
// Connectors in-process for tests. No production binary may link one.
var testOnlyPkgs = []string{
	"internal/seamtest",
	"internal/connectorsandbox/connectorsandboxtest",
	"internal/connectorforge/connectorforgetest",
	"internal/driver/driverkit/conformance",
	"internal/driver/drivertest",
	"internal/runner/runnertest",
	"internal/ecosystemtest",
}

// prodClosure is the module-relative package directories main reaches through
// non-test files, main included. Build constraints are ignored, so the result
// is a superset of what any one platform links.
func prodClosure(t *testing.T, root, main string) []string {
	t.Helper()
	seen := map[string]bool{main: true}
	queue := []string{main}
	for len(queue) > 0 {
		dir := queue[0]
		queue = queue[1:]
		for _, file := range prodFiles(t, filepath.Join(root, dir)) {
			f, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatalf("parse %s: %v", file, err)
			}
			for _, imp := range f.Imports {
				p, _ := strconv.Unquote(imp.Path.Value)
				rel, ok := strings.CutPrefix(p, modulePath+"/")
				if !ok || seen[rel] {
					continue
				}
				seen[rel] = true
				queue = append(queue, rel)
			}
		}
	}
	return slices.Sorted(func(yield func(string) bool) {
		for d := range seen {
			if !yield(d) {
				return
			}
		}
	})
}

// prodFiles are the non-test .go files directly in dir; it fails on an
// unreadable directory so an incomplete tree never passes vacuously.
func prodFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	var files []string
	for _, e := range entries {
		if n := e.Name(); !e.IsDir() && strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, "_test.go") {
			files = append(files, filepath.Join(dir, n))
		}
	}
	return files
}

func (a allowedCall) permits(rel string, c engineCall) bool {
	return a.File == rel && a.Func == c.Func && (a.Guard == "" || slices.Contains(c.Guards, a.Guard))
}

// violations are the engineCalls in files that no allowlist entry permits.
func violations(t *testing.T, root string, dirs []string, allowed []allowedCall) []string {
	t.Helper()
	var out []string
	for _, dir := range dirs {
		for _, file := range prodFiles(t, filepath.Join(root, dir)) {
			rel := filepath.ToSlash(filepath.Join(dir, filepath.Base(file)))
			for _, c := range engineCalls(t, file, nil) {
				if !slices.ContainsFunc(allowed, func(a allowedCall) bool { return a.permits(rel, c) }) {
					out = append(out, c.Pos+": "+c.Call+" in "+c.Func)
				}
			}
		}
	}
	return out
}

// TestNoProductionBinaryLoadsAConnectorInProcess fails when a production main
// can reach the in-process Connector engine other than through the allowlist,
// or links a test-support package that does. It needs every main's source, so
// the core go test, which strips them, skips it; launcher-binaries-go-test
// sets SPINDRIFT_ENGINE_GUARD_FULL_TREE, where an absent main is an error.
func TestNoProductionBinaryLoadsAConnectorInProcess(t *testing.T) {
	root := filepath.Join("..", "..")
	if _, err := os.Stat(filepath.Join(root, "main.go")); err != nil {
		if os.Getenv("SPINDRIFT_ENGINE_GUARD_FULL_TREE") != "" {
			t.Fatalf("SPINDRIFT_ENGINE_GUARD_FULL_TREE set but the mains are absent: %v", err)
		}
		t.Skip("the binaries' source is not in this tree")
	}
	for _, side := range []struct {
		mains   []string
		allowed []allowedCall
	}{{launcherMains, commonAllowed}, {boxMains, boxAllowed}} {
		for _, main := range side.mains {
			dirs := prodClosure(t, root, main)
			if len(dirs) < 2 {
				t.Fatalf("%s: closure %v is inert", main, dirs)
			}
			for _, d := range dirs {
				for _, p := range testOnlyPkgs {
					if d == p || strings.HasPrefix(d, p+"/") {
						t.Errorf("%s links test-support package %s", main, d)
					}
				}
			}
			for _, v := range violations(t, root, dirs, side.allowed) {
				t.Errorf("%s: %s: a production binary can load a Connector in-process; load it through connectorsandbox.Load", main, v)
			}
		}
	}
}

func TestEngineCallDetector(t *testing.T) {
	const pre = "package p\nimport \"" + connectorhostPkg + "\"\n"
	for name, tc := range map[string]struct {
		src  string
		want int
	}{
		"load":      {pre + "func f() { connectorhost.Load(ctx, w) }\n", 1},
		"network":   {pre + "func f() { _, _ = connectorhost.LoadNetworkCompiled(ctx, w, n, d) }\n", 1},
		"alias":     {"package p\nimport ch \"" + connectorhostPkg + "\"\nfunc f() { ch.LoadRemote(ctx, w, c) }\n", 1},
		"method":    {pre + "func (r *R) f() { connectorhost.LoadNetwork(ctx, w, n) }\n", 1},
		"as-value":  {pre + "var load = connectorhost.Load\n", 1},
		"in-func":   {pre + "func f() { g := connectorhost.Load; g(ctx, w) }\n", 1},
		"fake":      {"package p\nimport \"" + forgePkg + "\"\nfunc f() { forge.NewFake() }\n", 1},
		"sessions":  {pre + "func f() { connectorhost.NewSessions(ctx, r, w, k, wait) }\n", 0},
		"local":     {"package p\nfunc f() { connectorhost.Load(ctx, w) }\n", 0},
		"otherpkg":  {"package p\nimport \"other/connectorhost\"\nfunc f() { connectorhost.Load(ctx, w) }\n", 0},
		"prefix":    {pre + "func f() { connectorhost.Loaded(ctx) }\n", 1},
		"unrelated": {pre + "func f() { connectorhost.Network{} }\n", 0},
	} {
		if got := engineCalls(t, name+".go", tc.src); len(got) != tc.want {
			t.Errorf("%s: got %v, want %d call(s)", name, got, tc.want)
		}
	}
}

func TestEngineCallGuardsAndAllowlist(t *testing.T) {
	const src = "package p\nimport \"" + connectorhostPkg + "\"\n" +
		"func (p lp) load() {\n\tif p == boxLoads {\n\t\tconnectorhost.Load(ctx, w)\n\t}\n\tconnectorhost.Load(ctx, w)\n}\n"
	calls := engineCalls(t, "x.go", src)
	if len(calls) != 2 || !slices.Contains(calls[0].Guards, "boxLoads") || len(calls[1].Guards) != 0 {
		t.Fatalf("guards not tracked per call: %+v", calls)
	}
	a := allowedCall{File: "x.go", Func: "lp.load", Guard: "boxLoads"}
	if !a.permits("x.go", calls[0]) || a.permits("x.go", calls[1]) || a.permits("y.go", calls[0]) {
		t.Errorf("allowlist entry permits the wrong calls")
	}
}

func TestViolationsReportAPlantedInProcessLoad(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "daemon")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := "package daemon\nimport \"" + connectorhostPkg + "\"\nfunc start() { connectorhost.Load(nil, nil) }\n"
	if err := os.WriteFile(filepath.Join(dir, "daemon.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	got := violations(t, root, []string{"daemon"}, commonAllowed)
	if len(got) != 1 || !strings.Contains(got[0], "connectorhost.Load in start") {
		t.Fatalf("planted in-process load not reported: %v", got)
	}
}

func TestEngineCallGuardRequiresEquality(t *testing.T) {
	const pre = "package p\nimport \"" + connectorhostPkg + "\"\n"
	for name, tc := range map[string]struct {
		cond string
		want bool
	}{
		"equal":    {"p == boxLoads", true},
		"swapped":  {"boxLoads == p", true},
		"and":      {"ok && p == boxLoads", true},
		"inverted": {"p != boxLoads", false},
		"or":       {"ok || p == boxLoads", false},
		"negated":  {"!(p == boxLoads)", false},
		"call":     {"use(boxLoads)", false},
	} {
		src := pre + "func f() {\n\tif " + tc.cond + " {\n\t\tconnectorhost.Load(ctx, w)\n\t}\n}\n"
		calls := engineCalls(t, name+".go", src)
		if len(calls) != 1 {
			t.Fatalf("%s: got %+v, want one call", name, calls)
		}
		if got := slices.Contains(calls[0].Guards, "boxLoads"); got != tc.want {
			t.Errorf("%s: `if %s` guards boxLoads = %v, want %v", name, tc.cond, got, tc.want)
		}
	}
}

// eqIdents are the identifiers compared with == in cond, looking through &&
// only: a != or || condition does not establish the equality, so naming the
// identifier there is not a guard.
func eqIdents(cond ast.Expr) []string {
	switch e := cond.(type) {
	case *ast.ParenExpr:
		return eqIdents(e.X)
	case *ast.BinaryExpr:
		switch e.Op {
		case token.LAND:
			return append(eqIdents(e.X), eqIdents(e.Y)...)
		case token.EQL:
			var out []string
			for _, x := range []ast.Expr{e.X, e.Y} {
				if id, ok := x.(*ast.Ident); ok {
					out = append(out, id.Name)
				}
			}
			return out
		}
	}
	return nil
}

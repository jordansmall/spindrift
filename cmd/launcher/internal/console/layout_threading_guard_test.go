package console

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// layoutThreadingSourceFiles returns every non-test .go file in this
// directory, parsed once per test. Mirrors the file-discovery loop in
// TestActiveModeDoesNotDependOnResolveLayout (mode_layout_guard_test.go).
func layoutThreadingSourceFiles(t *testing.T, fset *token.FileSet) []*ast.File {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var files []*ast.File
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("ParseFile(%s): %v", name, err)
		}
		files = append(files, file)
	}
	return files
}

// TestResolveLayoutCallSitesArePinned guards the #3018 threading fix at its
// root: resolveLayout rebuilds the header text (via bodyBudget) on every
// resolve, so a design that re-resolves it from every consumer (once per
// Update, once per keymap Action, once per View) made a single keystroke
// pay for that rebuild close to a dozen times — bodyBudget no longer pays
// for a real lipgloss render on top of it (issue #3019 cut that leg;
// layout_purity_guard_test.go pins it), but the rebuild-per-resolve cost
// alone is still worth guarding against. #3018 fixed that by
// resolving the layout exactly once per Update (model.go's updateLayout)
// and threading that one value through the tea layer's cache
// (teaModel.currentLayout) and View's own parameter (viewWithLayout) rather
// than letting any consumer ask resolveLayout again.
//
// This pins the fix by construction: layout.go only defines resolveLayout,
// it never calls itself, so the only files allowed to *call* it are
// model.go (the one tail resolve updateLayout performs per message), tea.go
// (currentLayout's fallback for a teaModel built without going through
// apply), and view.go (View's top-level wrapper around viewWithLayout). A
// resolveLayout call appearing anywhere else — a keymap Action, a new
// helper — silently reintroduces the per-keystroke pile-up #3018 removed,
// because nothing about a stray call fails to compile or fails any
// behavioral test: the result is identical, just recomputed. Each of those
// three files is also pinned at exactly one call site, so a second call
// creeping into an already-listed file (e.g. a helper added to model.go
// that resolves its own layout instead of taking the tail one as a
// parameter) is caught too.
func TestResolveLayoutCallSitesArePinned(t *testing.T) {
	const wantCallers = "model.go, tea.go, view.go"
	wantFiles := map[string]bool{"model.go": true, "tea.go": true, "view.go": true}

	fset := token.NewFileSet()
	files := layoutThreadingSourceFiles(t, fset)

	gotPositions := make(map[string][]token.Position)
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			ident, ok := call.Fun.(*ast.Ident)
			if !ok || ident.Name != "resolveLayout" {
				return true
			}
			pos := fset.Position(call.Pos())
			gotPositions[pos.Filename] = append(gotPositions[pos.Filename], pos)
			return true
		})
	}

	for name, positions := range gotPositions {
		if !wantFiles[name] {
			for _, pos := range positions {
				t.Errorf("%s:%d: resolveLayout called outside the pinned call sites (%s) — "+
					"this re-introduces the per-keystroke pile-up issue #3018 fixed; read the "+
					"already-resolved layout instead (t.currentLayout() in the tea layer, or the "+
					"threaded layout parameter elsewhere)", pos.Filename, pos.Line, wantCallers)
			}
			continue
		}
		if len(positions) != 1 {
			var lines []string
			for _, pos := range positions {
				lines = append(lines, pos.String())
			}
			t.Errorf("%s: want exactly 1 resolveLayout call, got %d (%s) — a second call site in an "+
				"already-pinned file re-resolves a layout that should instead be threaded through as a "+
				"parameter or read from t.currentLayout() (issue #3018)", name, len(positions), strings.Join(lines, ", "))
		}
	}
	for name := range wantFiles {
		if _, ok := gotPositions[name]; !ok {
			t.Errorf("%s: no resolveLayout call found — was the pinned call site moved or removed? "+
				"update this guard's wantFiles if that was intentional (issue #3018)", name)
		}
	}
}

// layoutThreadingTMAssignmentAllowed reports whether fd is one of the two
// functions #3018 designated as the tea layer's mutation seam onto t.m:
// apply and withModel in tea.go. Every other production assignment to t.m
// must go through one of those two, never write the field directly.
func layoutThreadingTMAssignmentAllowed(filename string, fd *ast.FuncDecl) bool {
	if fd == nil || filename != "tea.go" {
		return false
	}
	return fd.Name.Name == "apply" || fd.Name.Name == "withModel"
}

// layoutThreadingIsTMSelector reports whether expr is the selector t.m —
// name-matching on the receiver/parameter identifier "t", the convention
// every teaModel method and keymap Action closure in this package uses,
// rather than full type resolution (the same tradeoff
// modeLayoutCalleeName documents: a false positive here is a loud,
// immediately fixable test failure, not a silently lost invariant).
func layoutThreadingIsTMSelector(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	return ok && ident.Name == "t" && sel.Sel.Name == "m"
}

// TestTMAssignmentsStayInSeam guards the other half of #3018's fix: caching
// the resolved layout on teaModel only pays off if every write to t.m is
// known to the cache. apply resolves updateLayout and refreshes t.layout in
// the same step; withModel installs a Model from outside updateLayout (a
// launcher-driven re-sync, "gg"'s own leader resolution) and invalidates
// the cache instead, since it has no fresh layout to offer. A direct
// `t.m = ...` anywhere else — a new keymap Action, a new tea.go helper —
// changes the Model without touching t.layout, so t.currentLayout() goes on
// returning a layout describing the *previous* Model, not the one t.m now
// holds; the bug is otherwise invisible until a render reads the stale
// value. This walks every non-test file's assignments (including tuple
// assignments, e.g. `t.m, cmd = ...`) rather than grepping, so it also
// catches an assignment nested inside a keymap Action closure, not just a
// top-level teaModel method.
func TestTMAssignmentsStayInSeam(t *testing.T) {
	fset := token.NewFileSet()
	files := layoutThreadingSourceFiles(t, fset)

	for _, file := range files {
		pos := fset.Position(file.Package)
		filename := pos.Filename

		// enclosing mirrors the FuncDecl (if any) an AssignStmt is
		// lexically nested in, tracked via ast.Inspect's paired f(nil)
		// call after a node's children are done — the standard way to get
		// exit notifications out of Inspect's enter-only callback.
		var stack []*ast.FuncDecl
		var enclosing *ast.FuncDecl
		ast.Inspect(file, func(n ast.Node) bool {
			if n == nil {
				stack = stack[:len(stack)-1]
				if len(stack) > 0 {
					enclosing = stack[len(stack)-1]
				} else {
					enclosing = nil
				}
				return true
			}
			if fd, ok := n.(*ast.FuncDecl); ok {
				enclosing = fd
			}
			stack = append(stack, enclosing)

			assign, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			if layoutThreadingTMAssignmentAllowed(filename, enclosing) {
				return true
			}
			for _, lhs := range assign.Lhs {
				if !layoutThreadingIsTMSelector(lhs) {
					continue
				}
				assignPos := fset.Position(assign.Pos())
				t.Errorf("%s:%d: t.m assigned outside the mutation seam (tea.go's apply/withModel) — "+
					"a direct t.m = ... leaves teaModel.layout describing the previous Model; route this "+
					"through t.apply(msg) so the cache refreshes with it, or t.withModel(m) when the new "+
					"Model didn't come from updateLayout (issue #3018)", assignPos.Filename, assignPos.Line)
			}
			return true
		})
	}
}

package console

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// layoutThreadingSourceFiles mirrors the file-discovery loop in
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

// TestResolveLayoutCallSitesArePinned pins issue #3018: resolveLayout rebuilds
// the header text on every call, so #3018 resolves it once in model.go's
// updateLayout and threads that value through tea.go's currentLayout fallback
// and view.go's viewWithLayout. A stray call elsewhere still compiles and
// returns the same answer, so only this guard catches the per-keystroke pile-up.
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

// layoutThreadingTMAssignmentAllowed names the two functions issue #3018 made
// the tea layer's only direct writers of t.m.
func layoutThreadingTMAssignmentAllowed(filename string, fd *ast.FuncDecl) bool {
	if fd == nil || filename != "tea.go" {
		return false
	}
	return fd.Name.Name == "apply" || fd.Name.Name == "withModel"
}

// layoutThreadingIsTMSelector matches the identifier name "t", the convention
// every teaModel method and keymap Action closure here uses, rather than
// resolving types. It is the tradeoff modeLayoutCalleeName documents: a false
// positive is a loud test failure, not a silently lost invariant.
func layoutThreadingIsTMSelector(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	return ok && ident.Name == "t" && sel.Sel.Name == "m"
}

// TestTMAssignmentsStayInSeam pins the other half of issue #3018: caching the
// resolved layout on teaModel works only if every write to t.m goes through
// apply, which refreshes the cache, or withModel, which invalidates it. A
// direct t.m = ... leaves t.currentLayout() describing the previous Model,
// and nothing shows that until a render reads the stale value.
func TestTMAssignmentsStayInSeam(t *testing.T) {
	fset := token.NewFileSet()
	files := layoutThreadingSourceFiles(t, fset)

	for _, file := range files {
		pos := fset.Position(file.Package)
		filename := pos.Filename

		// ast.Inspect's callback fires on entry only, so the paired f(nil)
		// after a node's children pops the stack holding the enclosing
		// FuncDecl.
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

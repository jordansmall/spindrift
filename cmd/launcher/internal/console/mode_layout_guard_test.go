package console

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// modeLayoutFuncInfo is one declared function or method this guard's call
// closure can walk into: its body, and the file:line to blame a violation
// on if resolveLayout is reached through it.
type modeLayoutFuncInfo struct {
	file string
	line int
	body *ast.BlockStmt
}

// modeLayoutCalleeName resolves a call expression's function name for
// matching against this package's own declared function/method names — a
// bare identifier (package-level func) or a selector's final name (method
// call, e.g. m.sidebarDocked(...)) — mirroring calleeName in
// gh_error_guard_test.go. This is name-matching, not real type resolution:
// package console genuinely overloads names (interface marker methods like
// isConsoleMsg, plus Update/View/Snapshot/Refresh/Discover/String declared
// on several types), so one name can resolve to several unrelated bodies.
// The guard deliberately over-approximates rather than picks one: it walks
// every same-named declaration and flags a violation if any of them reaches
// resolveLayout. That's the safe direction for a guard test — a false
// positive here is a loud, immediately fixable failure, while resolving to
// the wrong single body could produce a false negative that silently loses
// the invariant this guard exists to hold.
func modeLayoutCalleeName(fun ast.Expr) (string, bool) {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name, true
	case *ast.SelectorExpr:
		return f.Sel.Name, true
	}
	return "", false
}

// callsResolveLayout reports whether resolveLayout is reached anywhere in
// body's transitive in-package call closure — directly, or through any
// number of intermediate calls to other funcs/methods declared in funcs —
// and, if so, the position of the direct resolveLayout call itself, which
// the recursion propagates back up unchanged rather than reporting the
// intermediate hop. funcs maps a name to every declaration sharing
// it, since the package overloads several names (see modeLayoutCalleeName);
// a call is only clean if none of the same-named bodies reach resolveLayout.
// visited guards the walk against a call cycle: a name already on the
// current path is never re-entered, so a helper that (directly or
// indirectly) calls itself — through any of its same-named bodies — cannot
// hang this walk. The one shape it cannot see is a call that names no
// function: resolveLayout stored in a function value, passed as a callback,
// or reached through an interface whose method has no same-named
// declaration here. None of those exist on this path, and reintroducing the
// dependency the obvious way — a plain call — is what this guard catches.
func callsResolveLayout(body ast.Node, funcs map[string][]*modeLayoutFuncInfo, visited map[string]bool) (token.Pos, bool) {
	var foundPos token.Pos
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name, ok := modeLayoutCalleeName(call.Fun)
		if !ok {
			return true
		}
		if name == "resolveLayout" {
			found = true
			foundPos = call.Pos()
			return false
		}
		if visited[name] {
			return true
		}
		callees, ok := funcs[name]
		if !ok {
			return true
		}
		nv := make(map[string]bool, len(visited)+1)
		for k := range visited {
			nv[k] = true
		}
		nv[name] = true
		for _, callee := range callees {
			if pos, ok := callsResolveLayout(callee.body, funcs, nv); ok {
				found = true
				foundPos = pos
				return false
			}
		}
		return true
	})
	return foundPos, found
}

// TestActiveModeDoesNotDependOnResolveLayout guards the mode-authority
// direction issue #3017 restructured: ActiveMode (and the modeActive helper
// it drives its precedence loop through) must derive the active Mode from
// Model's own fields alone, never by asking resolveLayout for the render
// geometry first. Before #3017, ActiveMode called resolveLayout to learn
// the sidebar's docked/modal/fullscreen branch, which closed a latent cycle
// with the render path: ActiveMode -> resolveLayout -> bodyBudget ->
// renderHeader -> (indirectly) back to Mode-dependent rendering decisions.
// #2922 established layout as a value View and Update each consume once,
// derived without needing to know the active Mode first; a mode decision
// that itself depends on layout inverts that.
//
// This can't be a whole-file grep for resolveLayout in model.go: model.go
// legitimately calls resolveLayout from several Update-path branches (the
// consumer side #2922 intends) that have nothing to do with mode
// resolution. Only ActiveMode's and modeActive's own transitive in-package
// call closures are in scope, which is why this walks the call graph
// instead of scanning text.
func TestActiveModeDoesNotDependOnResolveLayout(t *testing.T) {
	funcs := make(map[string][]*modeLayoutFuncInfo)

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("ParseFile(%s): %v", name, err)
		}
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			pos := fset.Position(fd.Pos())
			funcs[fd.Name.Name] = append(funcs[fd.Name.Name], &modeLayoutFuncInfo{file: pos.Filename, line: pos.Line, body: fd.Body})
		}
	}

	for _, root := range []string{"ActiveMode", "modeActive"} {
		infos, ok := funcs[root]
		if !ok {
			t.Fatalf("TestActiveModeDoesNotDependOnResolveLayout: %s not found in package console — was it renamed? this guard must track the rename", root)
		}
		// root itself isn't among the overloaded names this guard's doc
		// comment calls out, but check every same-named declaration anyway
		// rather than assuming that stays true.
		for _, info := range infos {
			pos, found := callsResolveLayout(info.body, funcs, map[string]bool{root: true})
			if !found {
				continue
			}
			callSite := fset.Position(pos)
			t.Errorf("%s:%d: %s's call closure reaches resolveLayout via %s:%d — mode resolution must not depend on the layout resolver (issue #3017); "+
				"derive the decision from Model's own fields, mirroring sidebarDocked, instead of asking resolveLayout first",
				info.file, info.line, root, callSite.Filename, callSite.Line)
		}
	}
}

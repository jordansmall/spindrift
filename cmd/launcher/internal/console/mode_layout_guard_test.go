package console

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// modeLayoutFuncInfo is one declared function or method the guard can walk
// into, with the file:line to blame a violation on.
type modeLayoutFuncInfo struct {
	file string
	line int
	body *ast.BlockStmt
}

// modeLayoutCalleeName resolves a call's callee to a bare identifier or a
// selector's final name, mirroring calleeName in gh_error_guard_test.go. This
// matches names rather than resolving types, and package console overloads
// names, so the guard walks every same-named body: a false positive fails
// loudly, while picking the wrong one would hide a real violation.
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
// body's transitive in-package call closure, and returns the position of the
// direct call rather than the intermediate hop. funcs maps a name to every
// declaration sharing it; visited stops a call cycle from hanging the walk.
// The walk cannot see a call made through a function value or an interface, and
// no such call reaches resolveLayout on this path.
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

// TestActiveModeDoesNotDependOnResolveLayout pins issue #3017: ActiveMode and
// modeActive must derive the active Mode from Model's own fields, not from
// resolveLayout, whose old call closed a cycle into the render path and broke
// #2922's rule that layout is derived without knowing the Mode. Other calls to
// resolveLayout in model.go are legitimate, so this walks call closures, not text.
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
		// root is not an overloaded name today, but check every same-named
		// declaration rather than assume that holds.
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

package console

import (
	"go/ast"
	"go/token"
	"strings"
	"testing"
)

// Reaching any of these from resolveLayout is the #3019 regression itself: a
// lipgloss render (a renderer pick, a Role turned into styled text, or a real
// Style.Render) happening while resolveLayout computes the console's render
// geometry, instead of the pure predictions that replaced it.
var resolveLayoutForbiddenCallees = map[string]bool{
	"colorProfile":      true,
	"rendererFor":       true,
	"roleStyle":         true,
	"styledText":        true,
	"renderBoxedColumn": true,
}

// Guards #3019: resolveLayout must reach no renderer, no styler and no .Render(
// call. A behavioral test cannot catch that regression because the predicted and
// rendered counts agree by construction. An edge is recorded wherever a function
// is named, not only in call position (#3019 passed styledText as an argument),
// so the graph over-approximates and can only fail falsely, never miss.
func TestResolveLayoutCallGraphNeverRenders(t *testing.T) {
	fset := token.NewFileSet()
	files := layoutThreadingSourceFiles(t, fset)

	declaredFuncs := map[string]bool{}
	declaredMethods := map[string]bool{}
	var funcDecls []*ast.FuncDecl
	for _, file := range files {
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			if fd.Recv != nil {
				declaredMethods[fd.Name.Name] = true
			} else {
				declaredFuncs[fd.Name.Name] = true
			}
			funcDecls = append(funcDecls, fd)
		}
	}

	// calls maps each caller's name to the names it reaches, in source order,
	// so a failure can print the path that got there.
	calls := map[string][]string{}
	callsRenderMethod := map[string]bool{}

	for _, fd := range funcDecls {
		name := fd.Name.Name
		// On a SelectorExpr, visit recurses into X itself and returns false so
		// ast.Inspect never revisits Sel as a bare ident. Sel yields an edge
		// only when it names a declared method, which keeps package calls like
		// lipgloss.Width and field reads like m.Width out of the graph.
		var visit func(n ast.Node) bool
		visit = func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.SelectorExpr:
				if node.Sel.Name == "Render" {
					callsRenderMethod[name] = true
				}
				if declaredMethods[node.Sel.Name] {
					calls[name] = append(calls[name], node.Sel.Name)
				}
				ast.Inspect(node.X, visit)
				return false
			case *ast.Ident:
				if declaredFuncs[node.Name] || resolveLayoutForbiddenCallees[node.Name] {
					calls[name] = append(calls[name], node.Name)
				}
			}
			return true
		}
		ast.Inspect(fd.Body, visit)
	}

	type frame struct {
		name string
		path []string
	}
	visited := map[string]bool{"resolveLayout": true}
	queue := []frame{{name: "resolveLayout", path: []string{"resolveLayout"}}}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		if resolveLayoutForbiddenCallees[cur.name] {
			t.Fatalf("resolveLayout's call graph reaches %s, which it must never call (issue #3019): %s",
				cur.name, strings.Join(cur.path, " -> "))
		}
		if callsRenderMethod[cur.name] {
			t.Fatalf("resolveLayout's call graph reaches a .Render( call inside %s (issue #3019): %s",
				cur.name, strings.Join(cur.path, " -> ")+" -> Render(...)")
		}

		for _, callee := range calls[cur.name] {
			if visited[callee] {
				continue
			}
			visited[callee] = true
			path := append(append([]string{}, cur.path...), callee)
			queue = append(queue, frame{name: callee, path: path})
		}
	}
}

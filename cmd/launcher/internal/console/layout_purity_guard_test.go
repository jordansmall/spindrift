package console

import (
	"go/ast"
	"go/token"
	"strings"
	"testing"
)

// resolveLayoutForbiddenCallees names the functions resolveLayout's call
// graph must never reach: the two globals that pick a lipgloss renderer
// (colorProfile, rendererFor), the two ways a Role turns into rendered text
// (roleStyle, styledText), and the one remaining helper that still pays for
// a real lipgloss.Style.Render (renderBoxedColumn). Reaching any of them
// from resolveLayout is the #3019 regression itself: a render happening
// while resolveLayout computes the console's render geometry, rather than
// the pure predictions (headerGeometry, detailModalLabelLinesCappedWith
// driven by plainText) that replaced it.
var resolveLayoutForbiddenCallees = map[string]bool{
	"colorProfile":      true,
	"rendererFor":       true,
	"roleStyle":         true,
	"styledText":        true,
	"renderBoxedColumn": true,
}

// TestResolveLayoutCallGraphNeverRenders walks the package-local call graph
// reachable from resolveLayout and fails if it reaches a function named in
// resolveLayoutForbiddenCallees, or any function whose body calls a
// `.Render(` method — the two ways #3019's fix (predicting a rendered
// line/label count instead of rendering and counting) could regress
// without any behavioral test noticing, since the predicted and rendered
// output agree by construction
// (TestHeaderGeometry_MirrorsRenderBoxedHeader and
// TestDetailModalLabelLinesWith_PlainText_MatchesStyledStripped pin that).
//
// An edge is recorded for a function value named anywhere in a body, not
// just in call position: the #3019 regression this guard exists to catch
// is exactly a styler passed as an *argument* (detailModalScrollBudget's
// plainText swapped for styledText), which never appears as call.Fun.
// Receiver methods are walked too, so a reachable method's own
// `.Render(` call or forbidden call doesn't hide behind fd.Recv != nil.
// Naming a function is not proof of calling it, so the graph is a
// deliberate over-approximation: a local variable shadowing a function
// name, or a same-named method on an unrelated type, yields an edge that
// no real call backs. The bias only ever costs a false failure, never a
// missed regression.
// The failure message spells out the call path from resolveLayout to the
// offending function, so a future regression is diagnosable without
// re-deriving the chain by hand.
func TestResolveLayoutCallGraphNeverRenders(t *testing.T) {
	fset := token.NewFileSet()
	files := layoutThreadingSourceFiles(t, fset)

	declaredFuncs := map[string]bool{}   // plain (non-method) function names declared in this package
	declaredMethods := map[string]bool{} // receiver method names declared in this package
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

	calls := map[string][]string{}         // caller func name -> callee func names, in source order
	callsRenderMethod := map[string]bool{} // func name -> body directly calls some x.Render(...)

	for _, fd := range funcDecls {
		name := fd.Name.Name
		// visit records an edge for every *ast.Ident naming a declared
		// plain function or a forbidden callee, and — for a
		// *ast.SelectorExpr — an edge for its Sel only when Sel names a
		// declared method (never a bare package-qualified call like
		// lipgloss.Width or a struct field read like m.Width, since
		// neither is a declared method of this package). SelectorExpr
		// recurses into X by hand and returns false so ast.Inspect's own
		// traversal never revisits Sel as a bare ident.
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

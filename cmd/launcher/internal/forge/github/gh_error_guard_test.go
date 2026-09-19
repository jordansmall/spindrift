package github

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// ghFuncInfo describes one top-level function or method under scan. It records
// params in declaration order so an error passed as an argument traces to the
// correspondingly-positioned parameter.
type ghFuncInfo struct {
	name   string
	line   int
	params []string
	body   *ast.BlockStmt
}

// ghCallSite is one recognized gh exec call found while walking a function's
// body. checkBody is the block searched for this call's own error routing:
// the enclosing if's body, or the body of an `if errVar != nil` that
// immediately follows the assignment. A nil checkBody means no recognizable
// check followed the call, which is always a violation, never a silent skip.
type ghCallSite struct {
	pos       token.Pos
	errVar    string
	checkBody *ast.BlockStmt
}

// isGhExecCommandCall reports whether expr is exec.Command("gh", ...) or
// exec.CommandContext(ctx, "gh", ...), whose "gh" literal sits at Args[1]
// because Args[0] is the context argument.
func isGhExecCommandCall(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	x, ok := sel.X.(*ast.Ident)
	if !ok || x.Name != "exec" {
		return false
	}
	var ghArgIdx int
	switch sel.Sel.Name {
	case "Command":
		ghArgIdx = 0
	case "CommandContext":
		ghArgIdx = 1
	default:
		return false
	}
	if len(call.Args) <= ghArgIdx {
		return false
	}
	lit, ok := call.Args[ghArgIdx].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return false
	}
	v, err := strconv.Unquote(lit.Value)
	return err == nil && v == "gh"
}

func isGhResultCall(call *ast.CallExpr, ghVars map[string]bool) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	switch sel.Sel.Name {
	case "Output", "Run", "CombinedOutput":
	default:
		return false
	}
	switch x := sel.X.(type) {
	case *ast.Ident:
		return ghVars[x.Name]
	case *ast.CallExpr:
		return isGhExecCommandCall(x)
	}
	return false
}

// lastIdent returns the name of the last identifier in an assignment's LHS.
// That is the error variable: Output and CombinedOutput return (out, err) and
// Run returns just err, so err is always last.
func lastIdent(exprs []ast.Expr) (string, bool) {
	if len(exprs) == 0 {
		return "", false
	}
	id, ok := exprs[len(exprs)-1].(*ast.Ident)
	if !ok {
		return "", false
	}
	return id.Name, true
}

func condChecksErrVar(cond ast.Expr, errVar string) bool {
	bin, ok := cond.(*ast.BinaryExpr)
	if !ok || bin.Op != token.NEQ {
		return false
	}
	isErrVar := func(e ast.Expr) bool {
		id, ok := e.(*ast.Ident)
		return ok && id.Name == errVar
	}
	isNil := func(e ast.Expr) bool {
		id, ok := e.(*ast.Ident)
		return ok && id.Name == "nil"
	}
	return (isErrVar(bin.X) && isNil(bin.Y)) || (isErrVar(bin.Y) && isNil(bin.X))
}

// collectFuncLits finds function literals directly reachable from n without
// crossing into a nested block-bearing statement's body, which
// collectGhCallSites walks itself. It reaches gh exec calls written inside a
// closure argument, such as relay.go's RelayBundle passing a
// func(dir string) error literal that runs gh repo clone.
func collectFuncLits(n ast.Node) []*ast.FuncLit {
	var lits []*ast.FuncLit
	ast.Inspect(n, func(x ast.Node) bool {
		switch lit := x.(type) {
		case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.SelectStmt, *ast.BlockStmt:
			return false
		case *ast.FuncLit:
			lits = append(lits, lit)
			return false
		}
		return true
	})
	return lits
}

// collectGhCallSites returns one ghCallSite per gh exec call in body,
// recursing into nested blocks and function-literal bodies. It recognizes the
// two shapes this package's source uses: `if _, err := cmd.Output(); err !=
// nil`, and an assignment immediately followed by `if err != nil`. Any other
// shape still yields a site with a nil checkBody, so it cannot pass silently.
func collectGhCallSites(body *ast.BlockStmt) []ghCallSite {
	var sites []ghCallSite
	ghVars := make(map[string]bool)

	var walkList func(stmts []ast.Stmt)
	walkList = func(stmts []ast.Stmt) {
		for i, stmt := range stmts {
			for _, lit := range collectFuncLits(stmt) {
				walkList(lit.Body.List)
			}
			switch s := stmt.(type) {
			case *ast.AssignStmt:
				if len(s.Rhs) != 1 {
					continue
				}
				if isGhExecCommandCall(s.Rhs[0]) {
					// cmd is only bound here; the error arrives at a later
					// Output or Run on it.
					if len(s.Lhs) == 1 {
						if id, ok := s.Lhs[0].(*ast.Ident); ok {
							ghVars[id.Name] = true
						}
					}
					continue
				}
				call, ok := s.Rhs[0].(*ast.CallExpr)
				if !ok || !isGhResultCall(call, ghVars) {
					continue
				}
				errVar, ok := lastIdent(s.Lhs)
				if !ok {
					continue
				}
				var checkBody *ast.BlockStmt
				if i+1 < len(stmts) {
					if ifs, ok := stmts[i+1].(*ast.IfStmt); ok && ifs.Init == nil && condChecksErrVar(ifs.Cond, errVar) {
						checkBody = ifs.Body
					}
				}
				sites = append(sites, ghCallSite{pos: call.Pos(), errVar: errVar, checkBody: checkBody})
			case *ast.ExprStmt:
				if call, ok := s.X.(*ast.CallExpr); ok && isGhResultCall(call, ghVars) {
					// The error is never captured, so it routes nowhere.
					sites = append(sites, ghCallSite{pos: call.Pos()})
				}
			case *ast.DeferStmt:
				if isGhResultCall(s.Call, ghVars) {
					sites = append(sites, ghCallSite{pos: s.Call.Pos()})
				}
			case *ast.GoStmt:
				if isGhResultCall(s.Call, ghVars) {
					sites = append(sites, ghCallSite{pos: s.Call.Pos()})
				}
			case *ast.IfStmt:
				if s.Init != nil {
					if as, ok := s.Init.(*ast.AssignStmt); ok && len(as.Rhs) == 1 {
						if call, ok := as.Rhs[0].(*ast.CallExpr); ok && isGhResultCall(call, ghVars) {
							if errVar, ok := lastIdent(as.Lhs); ok {
								sites = append(sites, ghCallSite{pos: call.Pos(), errVar: errVar, checkBody: s.Body})
							}
						}
					}
				}
				walkList(s.Body.List)
				switch e := s.Else.(type) {
				case *ast.BlockStmt:
					walkList(e.List)
				case *ast.IfStmt:
					walkList([]ast.Stmt{e})
				}
			case *ast.ForStmt:
				walkList(s.Body.List)
			case *ast.RangeStmt:
				walkList(s.Body.List)
			case *ast.SwitchStmt:
				for _, c := range s.Body.List {
					if cc, ok := c.(*ast.CaseClause); ok {
						walkList(cc.Body)
					}
				}
			case *ast.TypeSwitchStmt:
				for _, c := range s.Body.List {
					if cc, ok := c.(*ast.CaseClause); ok {
						walkList(cc.Body)
					}
				}
			case *ast.SelectStmt:
				for _, c := range s.Body.List {
					if cc, ok := c.(*ast.CommClause); ok {
						walkList(cc.Body)
					}
				}
			case *ast.BlockStmt:
				walkList(s.List)
			}
		}
	}
	walkList(body.List)
	return sites
}

// calleeName resolves a call's function name for matching against the
// package's own declarations, without real type resolution. Name matching is
// enough for this package's single-package call graph.
func calleeName(fun ast.Expr) (string, bool) {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name, true
	case *ast.SelectorExpr:
		return f.Sel.Name, true
	}
	return "", false
}

func argIndexOfIdent(args []ast.Expr, name string) int {
	for i, a := range args {
		if id, ok := a.(*ast.Ident); ok && id.Name == name {
			return i
		}
	}
	return -1
}

// routesErr reports whether errVar reaches a ghCommandErr or
// ghCommandErrText call within body, either directly or by being passed to a
// locally-declared function whose correspondingly-positioned parameter routes
// it in turn. visited stops the recursion from re-entering a function already
// on the path, so a call cycle cannot loop forever.
func routesErr(body ast.Node, errVar string, funcs map[string]*ghFuncInfo, visited map[string]bool) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name, ok := calleeName(call.Fun)
		if !ok {
			return true
		}
		if name == "ghCommandErr" || name == "ghCommandErrText" {
			if argIndexOfIdent(call.Args, errVar) >= 0 {
				found = true
				return false
			}
			return true
		}
		if visited[name] {
			return true
		}
		callee, ok := funcs[name]
		if !ok {
			return true
		}
		idx := argIndexOfIdent(call.Args, errVar)
		if idx < 0 || idx >= len(callee.params) || callee.params[idx] == "" {
			return true
		}
		nv := make(map[string]bool, len(visited)+1)
		for k := range visited {
			nv[k] = true
		}
		nv[name] = true
		if routesErr(callee.body, callee.params[idx], funcs, nv) {
			found = true
			return false
		}
		return true
	})
	return found
}

// ghExecGuardViolations returns one message per violation of the gh-error
// adoption guard (issue #2864): a gh exec call site whose own error does not
// route through ghCommandErr or ghCommandErrText, and any .CombinedOutput()
// call, whose stderr ghCommandErr cannot extract. Tracking is per call site,
// so a second bare call in an otherwise-routed function is still caught.
func ghExecGuardViolations(filename, source string) []string {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, source, 0)
	if err != nil {
		return []string{fmt.Sprintf("%s: parse error: %v", filename, err)}
	}

	funcs := make(map[string]*ghFuncInfo)
	var order []string
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		info := &ghFuncInfo{
			name: fd.Name.Name,
			line: fset.Position(fd.Pos()).Line,
			body: fd.Body,
		}
		if fd.Type.Params != nil {
			for _, field := range fd.Type.Params.List {
				if len(field.Names) == 0 {
					info.params = append(info.params, "")
					continue
				}
				for _, n := range field.Names {
					info.params = append(info.params, n.Name)
				}
			}
		}
		funcs[info.name] = info
		order = append(order, info.name)
	}

	var violations []string
	for _, name := range order {
		info := funcs[name]
		for _, site := range collectGhCallSites(info.body) {
			if site.checkBody != nil && routesErr(site.checkBody, site.errVar, funcs, map[string]bool{name: true}) {
				continue
			}
			violations = append(violations, fmt.Sprintf(
				"%s:%d: gh exec call site does not route its error through ghCommandErr/ghCommandErrText (issue #2864)",
				filename, fset.Position(site.pos).Line,
			))
		}
	}

	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name == "CombinedOutput" {
			violations = append(violations, fmt.Sprintf(
				"%s:%d: contains .CombinedOutput() — CombinedOutput bypasses ghCommandErr's automatic *exec.ExitError.Stderr extraction and risks double-reporting stderr (issue #2864); use cmd.Output() and ghCommandErr/ghCommandErrText instead",
				filename, fset.Position(sel.Pos()).Line,
			))
		}
		return true
	})

	return violations
}

// TestGhExecSitesUseSharedErrorHelper parses every non-test .go file in this
// package and fails on any gh exec call site whose error skips ghCommandErr
// or ghCommandErrText, and on any .CombinedOutput() (issue #2864). exec.go
// stays in the walk even though it defines those helpers: neither helper body
// runs gh, so it has nothing to flag.
func TestGhExecSitesUseSharedErrorHelper(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", name, err)
		}
		for _, msg := range ghExecGuardViolations(name, string(data)) {
			t.Error(msg)
		}
	}
}

// TestGhExecGuardViolation_HasTeeth proves ghExecGuardViolations can fail a
// file, so the guard above cannot pass vacuously against this package's
// already-converted source. The fixtures pin shapes a coarser per-function or
// per-file check gets wrong, including the real Merge/classifyMergeFailure
// case, where the error routes through a helper's parameter and must pass.
func TestGhExecGuardViolation_HasTeeth(t *testing.T) {
	cases := []struct {
		name     string
		source   string
		wantFail bool
	}{
		{
			name: "bare gh exec with no helper call",
			source: `package github

import "os/exec"

func run() error {
	cmd := exec.Command("gh", "issue", "list")
	_, err := cmd.Output()
	return err
}
`,
			wantFail: true,
		},
		{
			name: "CombinedOutput anywhere in the file",
			source: `package github

import "os/exec"

func run() error {
	cmd := exec.Command("something-else")
	out, err := cmd.CombinedOutput()
	_ = out
	return ghCommandErr("something-else", err)
}
`,
			wantFail: true,
		},
		{
			name: "gh exec routed through ghCommandErr",
			source: `package github

import "os/exec"

func run() error {
	cmd := exec.Command("gh", "issue", "list")
	_, err := cmd.Output()
	if err != nil {
		return ghCommandErr("gh issue list", err)
	}
	return nil
}
`,
			wantFail: false,
		},
		{
			name: "gh exec routed through ghCommandErrText",
			source: `package github

import "os/exec"

func run() error {
	cmd := exec.Command("gh", "issue", "list")
	out, err := cmd.Output()
	if err != nil {
		return ghCommandErrText("gh issue list", err, string(out))
	}
	return nil
}
`,
			wantFail: false,
		},
		{
			name: "no gh exec at all",
			source: `package github

func run() error {
	return nil
}
`,
			wantFail: false,
		},
		{
			name: "second unrouted gh exec site in an otherwise-routed file",
			source: `package github

import (
	"fmt"
	"os/exec"
)

func runRouted() error {
	cmd := exec.Command("gh", "issue", "list")
	_, err := cmd.Output()
	if err != nil {
		return ghCommandErr("gh issue list", err)
	}
	return nil
}

func runUnrouted() error {
	cmd := exec.Command("gh", "issue", "pin", "1")
	_, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("gh issue pin 1: %w", err)
	}
	return nil
}
`,
			wantFail: true,
		},
		{
			name: "gh exec routed transitively through a locally-declared helper function",
			source: `package github

import "os/exec"

type execClient struct{}

func (e *execClient) Merge(url string) error {
	cmd := exec.Command("gh", "pr", "merge", url)
	if err := cmd.Run(); err != nil {
		return e.classifyMergeFailure(url, err)
	}
	return nil
}

func (e *execClient) classifyMergeFailure(url string, mergeErr error) error {
	return ghCommandErrText(fmt.Sprintf("gh pr merge %s", url), mergeErr, "")
}
`,
			wantFail: false,
		},
		{
			// Models NeedsUpdate (exec_pr.go). A per-function "routes
			// something" boolean passes this vacuously, because the first
			// site flips the flag for the whole function.
			name: "two gh exec sites in the same function, only one routed",
			source: `package github

import (
	"fmt"
	"os/exec"
)

func run() error {
	if _, err := exec.Command("gh", "issue", "list").Output(); err != nil {
		return ghCommandErr("gh issue list", err)
	}
	if _, err := exec.Command("gh", "issue", "pin", "1").Output(); err != nil {
		return fmt.Errorf("gh issue pin 1: %w", err)
	}
	return nil
}
`,
			wantFail: true,
		},
		{
			// Under exec.CommandContext the "gh" literal sits at Args[1]
			// because Args[0] is the context. isGhExecCommandCall must
			// recognize that shape too.
			name: "bare-wrapped exec.CommandContext(ctx, \"gh\", ...) with no helper call",
			source: `package github

import (
	"context"
	"os/exec"
)

func run(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "gh", "issue", "list")
	_, err := cmd.Output()
	return err
}
`,
			wantFail: true,
		},
		{
			// A site reached only via defer or go, not through a function
			// literal that collectFuncLits already handles. The result is
			// never captured, so it is always a violation.
			name: "bare gh exec reached only via defer",
			source: `package github

import "os/exec"

func run() {
	cmd := exec.Command("gh", "issue", "list")
	defer cmd.Run()
}
`,
			wantFail: true,
		},
		{
			// Models CloseMergedIssue (exec_issues.go): a method whose own gh
			// call is bare-wrapped, but which first calls a helper that
			// routes a different error. The old transitive closure exempted
			// it for calling something that routes, regardless of relevance.
			name: "bare gh exec in a function that also calls an unrelated routed helper",
			source: `package github

import (
	"fmt"
	"os/exec"
)

type execClient struct{}

func (e *execClient) helperCheck(id string) (bool, error) {
	cmd := exec.Command("gh", "issue", "view", id)
	out, err := cmd.Output()
	if err != nil {
		return false, ghCommandErr("gh issue view", err)
	}
	return len(out) > 0, nil
}

func (e *execClient) doWork(id string) error {
	ok, err := e.helperCheck(id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("not found")
	}
	cmd := exec.Command("gh", "issue", "close", id)
	_, err = cmd.Output()
	if err != nil {
		return fmt.Errorf("gh issue close %s: %w", id, err)
	}
	return nil
}
`,
			wantFail: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			violations := ghExecGuardViolations("fixture.go", tc.source)
			got := len(violations) > 0
			if got != tc.wantFail {
				t.Errorf("ghExecGuardViolations() flagged=%v (%v), want flagged=%v (source:\n%s)", got, violations, tc.wantFail, tc.source)
			}
		})
	}
}

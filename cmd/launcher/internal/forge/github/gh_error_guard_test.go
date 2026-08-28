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

// ghFuncInfo is what ghExecGuardViolations needs to know about one top-level
// function or method declared in a file under scan: its own body (to walk
// for gh exec call sites) and its parameter names in declaration order, so a
// routed error passed as an argument to this function can be traced to the
// correspondingly-positioned parameter when tracking whether some other call
// site's error reaches a helper through it.
type ghFuncInfo struct {
	name   string
	line   int
	params []string
	body   *ast.BlockStmt
}

// ghCallSite is one recognized gh exec call — cmd.Output()/cmd.Run()/
// cmd.CombinedOutput() on a variable assigned from exec.Command("gh", ...),
// or that chain inlined — found while walking a function's body. checkBody
// is the block ghExecGuardViolations searches to decide whether *this
// call's own* error value routes through ghCommandErr/ghCommandErrText:
// either the enclosing if's body (`if _, err := cmd.Output(); err != nil {
// checkBody }`) or the body of an `if errVar != nil { checkBody }` that
// immediately follows the call's own assignment statement. checkBody is nil
// when the call's result was produced but no recognizable if-check
// immediately followed it — that shape is always a violation, never
// something silently skipped.
type ghCallSite struct {
	pos       token.Pos
	errVar    string
	checkBody *ast.BlockStmt
}

// isGhExecCommandCall reports whether expr is exec.Command("gh", ...) or
// exec.CommandContext(ctx, "gh", ...) — the latter's "gh" literal sits at
// Args[1], since Args[0] is the context argument.
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

// isGhResultCall reports whether call is cmd.Output()/cmd.Run()/
// cmd.CombinedOutput() where cmd is a variable previously assigned from
// exec.Command("gh", ...) (tracked in ghVars), or the exec.Command("gh",
// ...) chain is inlined directly into the call.
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

// lastIdent returns the name of the last identifier in exprs (an
// assignment's LHS) — the error variable's own name, since .Output()/
// .CombinedOutput() return (out, err) and .Run() returns just err, always
// last.
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

// condChecksErrVar reports whether cond is (syntactically) `errVar != nil`
// or `nil != errVar`.
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

// collectFuncLits finds every function-literal expression directly reachable
// from n without crossing into a nested block-bearing statement's own body
// (those are walked separately, by collectGhCallSites' own recursion, so
// finding their func-lits from here too would just double-process them).
// This exists to reach a gh exec call site written inside a closure argument
// — e.g. relay.go's RelayBundle/CommitSubjects, which each pass a
// func(dir string) error literal containing their own `gh repo clone` call
// straight to bundlerelay.Relay/CommitSubjects.
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

// collectGhCallSites walks body's statement list — recursing into nested
// if/for/range/switch/select bodies and into function-literal bodies found
// along the way — and returns one ghCallSite per gh exec call recognized
// there, in the two shapes this package's real source uses:
//
//   - (a) `if _, err := cmd.Output(); err != nil { ... }` (or the
//     exec.Command("gh", ...) chain inlined into the Init) — the error
//     variable and check-body both come from the IfStmt itself.
//   - (b) `out, err := cmd.Output()` (or inlined) as its own statement,
//     immediately followed in the same statement list by `if err != nil {
//     ... }` — the check-body is that following if's body.
//
// A gh exec result produced in neither shape (e.g. the assignment's very
// next statement isn't a matching if) still yields a ghCallSite, just one
// with a nil checkBody — the caller treats that as an automatic violation,
// so an unrecognized shape can never silently pass.
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
					// cmd := exec.Command("gh", ...) — remember cmd as a gh
					// exec variable for a later cmd.Output()/.Run() to find;
					// this statement produces no error of its own yet.
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
					// A gh exec result whose error isn't even captured —
					// definitely not routed anywhere.
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

// calleeName resolves a call expression's function name for name-matching
// against the package's own declared function/method names — a bare
// identifier (package-level func) or a selector's final name (method call,
// e.g. e.classifyMergeFailure(...)) — without real type resolution, which is
// sufficient for this package's own single-package call graph.
func calleeName(fun ast.Expr) (string, bool) {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name, true
	case *ast.SelectorExpr:
		return f.Sel.Name, true
	}
	return "", false
}

// argIndexOfIdent returns the position of the first argument in args that is
// the bare identifier name, or -1 if none matches.
func argIndexOfIdent(args []ast.Expr, name string) int {
	for i, a := range args {
		if id, ok := a.(*ast.Ident); ok && id.Name == name {
			return i
		}
	}
	return -1
}

// routesErr reports whether errVar's value, somewhere within body
// (recursively, including nested statements), reaches a ghCommandErr/
// ghCommandErrText call — directly, as one of that call's own arguments, or
// by being passed as an argument to another locally-declared function/method
// (funcs) whose correspondingly-positioned parameter is itself, recursively,
// routed the same way. visited guards against infinite recursion around a
// call cycle: a function already on the path is never re-entered.
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

// ghExecGuardViolations parses source (the contents of filename) and returns
// one message per genuine violation of the gh-error adoption guard
// TestGhExecSitesUseSharedErrorHelper polices:
//
//   - a violation is reported for each individual gh exec call site —
//     cmd.Output()/cmd.Run()/cmd.CombinedOutput() on a variable assigned
//     from exec.Command("gh", ...), or that chain inlined — whose own error
//     value does not route through ghCommandErr/ghCommandErrText, either
//     directly at the call site or by being passed as an argument to a
//     locally-declared function/method whose correspondingly-positioned
//     parameter is (recursively) itself routed the same way (e.g. Merge's
//     own gh exec error, passed to classifyMergeFailure's mergeErr
//     parameter, which classifyMergeFailure itself passes to
//     ghCommandErrText). This is real per-call-site taint tracking, not a
//     per-function or per-file boolean: a function with two gh exec calls,
//     only one of which routes, is flagged for the other one; a function
//     that merely calls some unrelated locally-declared function that
//     happens to route a *different* gh error is not credited for its own,
//     separate bare-wrapped call.
//   - a violation is reported for every .CombinedOutput() call anywhere in
//     the file: CombinedOutput can't have its stderr auto-extracted by
//     ghCommandErr, and manually wiring stderr back in reintroduces the
//     double-report risk issue #2864 eliminated.
//
// This is a per-call-site check, not a per-function or per-file one: adding
// a brand-new, bare-wrapped gh exec call anywhere — a second call in a
// function that already has one correctly-routed call, or a call in a
// function that merely calls something else that happens to route a
// different error — is still caught, because each call site's own error is
// traced independently rather than folded into one function-wide "does this
// function route *something*" flag.
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

// TestGhExecSitesUseSharedErrorHelper walks every non-test .go file directly
// in this package (the gh-exec adapter, package github) and, for each,
// parses it and fails on every gh exec call site whose own error does not
// route — directly, or by being passed to a locally-declared function/method
// that itself routes it — through ghCommandErr( or ghCommandErrText( — the
// shared helpers that fold gh's own stderr into the returned error. It also
// forbids .CombinedOutput() anywhere in the package's non-test source: after
// issue #2864's adoption, no gh exec site here should need it, since
// CombinedOutput can't have its stderr auto-extracted by ghCommandErr and
// manually wiring it back in reintroduces the double-report risk that ticket
// eliminated.
//
// This check is deliberately per-call-site (via ghExecGuardViolations'
// statement-by-statement AST walk and per-site error taint tracking), not
// per-function or per-file — a coarser check that credits a whole function
// (or file) as soon as it contains one correctly-routed call anywhere can
// miss a second, separate bare-wrapped call in the same function, or a bare
// call in a function that merely calls something else that happens to
// route a different error. That's a materially different (finer)
// granularity than cmd/launcher/pins_test.go's seam guards
// (TestNoGhExecOutsideForge, TestNoRunnerExecOutsidePackage), which check
// for *zero occurrences* of a pattern across a file or package — an exact,
// file-level guard that this one's per-call-site, locally-transitive design
// does not attempt to match.
//
// exec.go is not excluded from the walk even though it's where
// ghCommandErr/ghCommandErrText are defined: neither helper's own body
// contains an exec.Command("gh", ...) call, so exec.go has nothing for the
// first half of the check to flag regardless.
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

// TestGhExecGuardViolation_HasTeeth proves ghExecGuardViolations can actually
// fail a file, not just pass this package's already-fully-converted source
// vacuously (the same failure mode
// TestDispatchLabels_ClaimRemoveLabels_MatchesWorkflowFiles's "would parity
// check pass vacuously" guard at
// cmd/launcher/internal/forge/claim_strip_parity_test.go avoids): it runs
// the checker against small inline fixtures, one pattern at a time, and
// asserts each is flagged (or not) as expected.
//
// Four cases in particular exercise the per-call-site design directly, since
// they're exactly what a coarser, per-function or per-file "some call in
// here routes" check would get wrong:
//
//   - "second unrouted gh exec site in an otherwise-routed file" models a
//     file with one correctly-routed call site in one function and a
//     second, separate function with its own bare-wrapped gh exec call —
//     the file-level blind spot this checker replaces a file-scoped design
//     specifically to close. A whole-file Contains check would pass this
//     fixture vacuously, because the file does contain "ghCommandErr(" —
//     just not anywhere near the offending call.
//   - "gh exec routed transitively through a locally-declared helper
//     function" models this package's real Merge/classifyMergeFailure
//     shape: Merge's own body has the bare exec.Command("gh", "pr",
//     "merge", ...) call and, on error, calls the separate local method
//     classifyMergeFailure, which is the one that actually calls
//     ghCommandErrText with that same error value. A naive per-function
//     (non-transitive) count would false-positive on this shape, since
//     Merge itself never calls a helper directly — but it's a genuine
//     same-error-flows-through-a-parameter case, so this must still pass.
//   - "two gh exec sites in the same function, only one routed" models the
//     NeedsUpdate-shaped regression a per-function boolean design misses:
//     a single function with two separate gh exec call sites, one of which
//     routes and one of which doesn't. A design that tracks "does this
//     function route *something*" as one bool passes this vacuously, since
//     the first site's route flips the whole function's flag; per-call-site
//     tracking must flag the second site on its own.
//   - "bare gh exec in a function that also calls an unrelated routed
//     helper" models the CloseMergedIssue-shaped regression a transitive,
//     any-locally-called-function closure misses: a function whose own gh
//     exec call is bare-wrapped, but which also calls a separate,
//     unrelated locally-declared method that happens to route a
//     *different* gh error. A closure that credits a function as soon as
//     it calls anything that routes, regardless of relevance, would pass
//     this vacuously; per-call-site taint tracking must not credit an
//     unrelated call.
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
			// Models NeedsUpdate (exec_pr.go): a single function with two
			// separate gh exec call sites, one correctly routed and one
			// bare-wrapped. A per-function "routes something" boolean would
			// pass this vacuously since the first site flips it; per-call-
			// site tracking must flag the second site on its own.
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
			// Models a gh exec call site wrapped via exec.CommandContext
			// instead of exec.Command — the "gh" literal sits at Args[1]
			// (Args[0] is the context), which isGhExecCommandCall must also
			// recognize.
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
			// Models a gh exec call site reached only via defer/go, never a
			// wrapping function literal (collectFuncLits already handles
			// those) — a bare, uncaptured gh exec result here is still
			// always a violation.
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
			// Models CloseMergedIssue (exec_issues.go): a helper method with
			// its own routed gh call, and a second method that calls the
			// helper first for an unrelated reason (a precondition check)
			// and then makes its own bare-wrapped gh call. The old
			// transitive closure exempted the second method because it
			// calls *something* that routes, regardless of relevance;
			// per-call-site tracking must not credit that unrelated call.
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

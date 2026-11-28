package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/inputdoc"
)

// remedyVerb matches an operator-facing "set X" / "lower X" / "raise X"
// instruction, any verb case so a sentence-initial "Set X" is caught; \b keeps
// "unset X" out.
var remedyVerb = regexp.MustCompile(`\b(?i:set|lower|raise) ([A-Z][A-Z0-9_]+)\b`)

// TestRemedyKnobGuard fails when a remedy tells the operator to set a knob env
// var that inputdoc.AmbientKnobError refuses (issue #7265): remedies name the
// --flag or settings path instead. A DoctorSlugHint carries no verb, so any
// whole-word mention of a refused env var in one fails too.
func TestRemedyKnobGuard(t *testing.T) {
	refused := map[string]bool{}
	var envs []string
	for _, e := range inputdoc.SchemaFlags {
		if e.Flag != "" {
			refused[e.Env] = true
			envs = append(envs, regexp.QuoteMeta(e.Env))
		}
	}
	if len(refused) == 0 {
		t.Fatal("inputdoc.SchemaFlags has no flagged rows: the guard has nothing to look for")
	}
	hintWord := regexp.MustCompile(`\b(?:` + strings.Join(envs, "|") + `)\b`)

	scanned := 0
	err := walkOwnSource(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != "." && (d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		scanned++
		str := func(e ast.Expr) (string, bool) {
			lit, ok := e.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return "", false
			}
			v, err := strconv.Unquote(lit.Value)
			return v, err == nil
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.BasicLit:
				v, ok := str(n)
				if !ok {
					break
				}
				for _, m := range remedyVerb.FindAllStringSubmatch(v, -1) {
					if refused[m[1]] {
						t.Errorf("%s: remedy names refused knob env var %s; name its --flag or settings path", fset.Position(n.Pos()), m[1])
					}
				}
			case *ast.KeyValueExpr:
				if k, ok := n.Key.(*ast.Ident); ok && k.Name == "DoctorSlugHint" {
					if v, ok := str(n.Value); ok {
						for _, m := range hintWord.FindAllString(v, -1) {
							t.Errorf("%s: DoctorSlugHint names refused knob env var %s; name its --flag or settings path", fset.Position(n.Pos()), m)
						}
					}
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk .: %v", err)
	}
	if scanned == 0 {
		t.Fatal("no Go files scanned: the walk is matching the wrong tree")
	}
}

package console

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"
)

// keyHandlerModes maps each handleKey sub-handler's function name to the
// Mode(s) whose keyboard ownership it exercises — restating handleKey's own
// dispatch switch (tea.go) so TestKeymapParity below can attribute every
// literal key it finds in a handler's body to the right Mode column in
// keymap. A new mode's handler joins by adding one entry here, the same
// "append, don't insert" growth path modePrecedence (model.go) documents for
// itself (issue #1789).
var keyHandlerModes = map[string][]Mode{
	"handleHelpKey":             {ModeHelp},
	"handlePickChordKey":        {ModePick},
	"handleListKey":             {ModeList},
	"handleRebuildOutputKey":    {ModeRebuildOutput},
	"handleDetailModalKey":      {ModeDetailModal},
	"handleSidebarKey":          {ModeSidebar},
	"handleTerminateConfirmKey": {ModeTerminateConfirm},
	"handleQuitConfirmKey":      {ModeQuitConfirm},
	"handleFilterKey":           {ModeFilterEdit},
}

// teaKeyTypeNames maps a tea.KeyType selector's identifier (as it appears in
// a `case tea.KeyEnter:`-style clause) to the same key name msg.String()
// would report for it — handleFilterKey is the one handler that switches on
// msg.Type instead of msg.String() (issue #1789).
var teaKeyTypeNames = map[string]string{
	"KeyEnter":     "enter",
	"KeyEsc":       "esc",
	"KeyBackspace": "backspace",
	"KeyRunes":     "runes",
	"KeySpace":     "space",
}

// literalKeys returns every key string a boolean condition, switch-case
// value, or isQuitKey call names — the handful of shapes handleKey's
// sub-handlers use to test a keypress against a literal key (issue #1789).
func literalKeys(e ast.Expr) []string {
	switch n := e.(type) {
	case *ast.BasicLit:
		if n.Kind == token.STRING {
			if v, err := strconv.Unquote(n.Value); err == nil {
				return []string{v}
			}
		}
	case *ast.BinaryExpr:
		if n.Op == token.EQL || n.Op == token.LOR || n.Op == token.LAND {
			return append(literalKeys(n.X), literalKeys(n.Y)...)
		}
	case *ast.CallExpr:
		if id, ok := n.Fun.(*ast.Ident); ok && id.Name == "isQuitKey" {
			return []string{"q", "ctrl+c"}
		}
	case *ast.SelectorExpr:
		if name, ok := teaKeyTypeNames[n.Sel.Name]; ok {
			return []string{name}
		}
	}
	return nil
}

// handledKeys parses tea.go and returns, per Mode, every key some
// handleKey sub-handler actually recognizes — the "what dispatch really
// does" half of TestKeymapParity's comparison against keymap's declared
// entries (issue #1789).
func handledKeys(t *testing.T) map[Mode]map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "tea.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing tea.go: %v", err)
	}

	got := make(map[Mode]map[string]bool)
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		modes, ok := keyHandlerModes[fn.Name.Name]
		if !ok {
			continue
		}
		keys := map[string]bool{}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch stmt := n.(type) {
			case *ast.IfStmt:
				for _, k := range literalKeys(stmt.Cond) {
					keys[k] = true
				}
			case *ast.CaseClause:
				for _, expr := range stmt.List {
					for _, k := range literalKeys(expr) {
						keys[k] = true
					}
				}
			}
			return true
		})
		for _, mode := range modes {
			if got[mode] == nil {
				got[mode] = map[string]bool{}
			}
			for k := range keys {
				got[mode][k] = true
			}
		}
	}
	return got
}

// declaredKeys mirrors handledKeys' shape for keymap itself — the table's
// own claim about which (Mode, key) pairs it documents (issue #1789).
func declaredKeys() map[Mode]map[string]bool {
	want := make(map[Mode]map[string]bool)
	for _, b := range keymap {
		for _, mode := range b.Modes {
			if want[mode] == nil {
				want[mode] = map[string]bool{}
			}
			for _, k := range b.Keys {
				want[mode][k] = true
			}
		}
	}
	return want
}

// TestKeymapParity fails if handleKey handles a key its Mode has no keymap
// entry for, or keymap declares a (Mode, key) pair handleKey doesn't
// actually handle — the bijection issue #1789 AC demands, so a future
// rebind touching only one side is caught immediately instead of silently
// drifting the hint text out of sync with dispatch.
func TestKeymapParity(t *testing.T) {
	got := handledKeys(t)
	want := declaredKeys()

	for mode, keys := range got {
		for k := range keys {
			if !want[mode][k] {
				t.Errorf("handleKey handles %q in %v, but keymap has no entry for it", k, mode)
			}
		}
	}
	for mode, keys := range want {
		for k := range keys {
			if !got[mode][k] {
				t.Errorf("keymap declares %q for %v, but handleKey doesn't handle it", k, mode)
			}
		}
	}
}

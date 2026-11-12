package event

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// coreDir is the canonical vocabulary (ADR 0065). The nix check copies it
// beside sdk/ at the same relative layout so this path resolves there too.
const coreDir = "../../cmd/launcher/internal/driver/driverkit"

// unpublished lists core fields the copy deliberately omits, as Type.Field.
// Event.HostOp's type lives in a core-internal package.
var unpublished = map[string]bool{"Event.HostOp": true}

// published is the vocabulary the copy must hold.
var published = []string{"Kind", "Event", "Tokens"}

// vocab is the normalised view of one package's vocabulary declarations:
// types as one line per struct field (or one line for a non-struct), and the
// Kind constants as name -> "Type = value". Comments never reach it.
type vocab struct {
	types  map[string][]string
	consts map[string]string
}

func render(t *testing.T, fset *token.FileSet, n any) string {
	t.Helper()
	var b bytes.Buffer
	if err := printer.Fprint(&b, fset, n); err != nil {
		t.Fatal(err)
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func parseVocab(t *testing.T, dir string, skip func(name string) bool) vocab {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0) // mode 0: comments are dropped at parse time
	if err != nil {
		t.Fatal(err)
	}
	v := vocab{types: map[string][]string{}, consts: map[string]string{}}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, d := range file.Decls {
				gd, ok := d.(*ast.GenDecl)
				if !ok {
					continue
				}
				var lastType string
				for _, spec := range gd.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						v.types[s.Name.Name] = typeLines(t, fset, s, skip)
					case *ast.ValueSpec:
						if gd.Tok != token.CONST {
							continue
						}
						if s.Type != nil {
							lastType = render(t, fset, s.Type)
						}
						if lastType != "Kind" {
							continue
						}
						for i, name := range s.Names {
							val := ""
							if i < len(s.Values) {
								val = render(t, fset, s.Values[i])
							}
							v.consts[name.Name] = fmt.Sprintf("%s = %s", lastType, val)
						}
					}
				}
			}
		}
	}
	return v
}

func typeLines(t *testing.T, fset *token.FileSet, s *ast.TypeSpec, skip func(string) bool) []string {
	st, ok := s.Type.(*ast.StructType)
	if !ok {
		return []string{render(t, fset, s.Type)}
	}
	var lines []string
	for _, f := range st.Fields.List {
		typ := render(t, fset, f.Type)
		tag := ""
		if f.Tag != nil {
			tag = f.Tag.Value
		}
		// An embedded field has no Names; its rendered type stands in so it
		// still counts as drift.
		names := []string{typ}
		if len(f.Names) > 0 {
			names = names[:0]
			for _, n := range f.Names {
				names = append(names, n.Name)
			}
		}
		for _, n := range names {
			if skip != nil && skip(s.Name.Name+"."+n) {
				continue
			}
			lines = append(lines, fmt.Sprintf("%s %s %s", n, typ, tag))
		}
	}
	return lines
}

func TestVocabularyMatchesCore(t *testing.T) {
	core := parseVocab(t, coreDir, func(n string) bool { return unpublished[n] })
	mine := parseVocab(t, ".", nil)

	for _, name := range published {
		if _, ok := mine.types[name]; !ok {
			t.Errorf("copy lacks type %s", name)
		}
	}
	names := make([]string, 0, len(mine.types))
	for n := range mine.types {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		want, ok := core.types[n]
		if !ok {
			t.Errorf("copy declares type %s, which core does not", n)
			continue
		}
		if !reflect.DeepEqual(mine.types[n], want) {
			t.Errorf("type %s drifted from core\n copy: %q\n core: %q", n, mine.types[n], want)
		}
	}
	for n, want := range core.consts {
		if got, ok := mine.consts[n]; !ok {
			t.Errorf("copy lacks Kind constant %s (core: %s)", n, want)
		} else if got != want {
			t.Errorf("Kind constant %s drifted: copy %q, core %q", n, got, want)
		}
	}
	for n := range mine.consts {
		if _, ok := core.consts[n]; !ok {
			t.Errorf("copy declares Kind constant %s, which core does not", n)
		}
	}
	if len(core.consts) == 0 {
		t.Error("found no Kind constants in core: parser is vacuous")
	}
	full := parseVocab(t, coreDir, nil)
	for k := range unpublished {
		typ, field, _ := strings.Cut(k, ".")
		found := false
		for _, l := range full.types[typ] {
			if strings.HasPrefix(l, field+" ") {
				found = true
			}
		}
		if !found {
			t.Errorf("unpublished %s no longer exists in core; drop the exemption", k)
		}
	}
}

func TestTypeLinesEmbedded(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x.go", "package x\ntype E struct{ A int; Base }", 0)
	if err != nil {
		t.Fatal(err)
	}
	ts := f.Decls[0].(*ast.GenDecl).Specs[0].(*ast.TypeSpec)
	if got := typeLines(t, fset, ts, nil); len(got) != 2 {
		t.Fatalf("got %q", got)
	}
	if got := typeLines(t, fset, ts, func(n string) bool { return n == "E.Base" }); len(got) != 1 {
		t.Fatalf("skip: got %q", got)
	}
}

// funcSource returns the normalised source of the top-level func name (no
// receiver) declared in dir's non-test files, "" when there is none.
func funcSource(t *testing.T, dir, name string) string {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			for _, d := range f.Decls {
				fn, ok := d.(*ast.FuncDecl)
				if !ok || fn.Recv != nil || fn.Name.Name != name {
					continue
				}
				fn.Doc = nil
				return render(t, fset, fn)
			}
		}
	}
	return ""
}

// A Connector cannot import driverkit (issue #6870), so identical source is
// what makes the SDK's IsProgress answer as core's for every Kind.
func TestIsProgressParity(t *testing.T) {
	core := funcSource(t, coreDir, "IsProgress")
	if core == "" {
		t.Fatal("core has no func IsProgress")
	}
	if sdk := funcSource(t, ".", "IsProgress"); sdk != core {
		t.Errorf("SDK IsProgress differs from core's:\nSDK:  %s\ncore: %s", sdk, core)
	}
}

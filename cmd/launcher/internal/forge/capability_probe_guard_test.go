package forge

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// capabilitySet is what counts as a CodeForge capability: the interface names
// ResolveCodeForge probes, and the methods those interfaces declare (so an
// anonymous interface literal asserting one of them is caught too).
type capabilitySet struct {
	types, methods map[string]bool
	// consumers are the declExemptions interfaces: they embed or declare
	// capabilities, so a use of one is a probe even though its declaration is not.
	// Kept apart from types, which also exempts a declaration in any file.
	consumers map[string]bool
}

// resolverCapabilities derives the set from ResolveCodeForge, the interface
// declarations next to it, and declExemptions, so a capability or consumer
// interface added there is guarded with no second list to edit.
func resolverCapabilities(t *testing.T, filename string) capabilitySet {
	t.Helper()
	caps := capabilitySet{types: map[string]bool{}, methods: map[string]bool{}, consumers: map[string]bool{}}
	for _, names := range declExemptions {
		for name := range names {
			caps.consumers[name] = true
		}
	}
	inspectFuncs(t, filename, []string{"ResolveCodeForge"}, func(n ast.Node) bool {
		if ix, ok := n.(*ast.IndexExpr); ok && typeName(ix.X) == "AsCodeForge" {
			if name := typeName(ix.Index); name != "" {
				caps.types[name] = true
			}
		}
		return true
	})
	for _, f := range packageGoFiles(t) {
		file, err := parser.ParseFile(token.NewFileSet(), f, nil, 0)
		if err != nil {
			t.Fatalf("ParseFile(%s): %v", f, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok || !caps.types[ts.Name.Name] {
				return true
			}
			if it, ok := ts.Type.(*ast.InterfaceType); ok {
				methods, _ := interfaceMembers(it)
				for _, name := range methods {
					caps.methods[name] = true
				}
			}
			return true
		})
	}
	return caps
}

// interfaceMembers splits an interface literal's fields into declared method
// names and embedded type expressions.
func interfaceMembers(it *ast.InterfaceType) (methods []string, embeds []ast.Expr) {
	for _, m := range it.Methods.List {
		if len(m.Names) == 0 {
			embeds = append(embeds, m.Type)
		}
		for _, name := range m.Names {
			methods = append(methods, name.Name)
		}
	}
	return methods, embeds
}

// typeName returns the bare name of an Ident or <pkg>.Name type expression,
// looking through parentheses.
func typeName(e ast.Expr) string {
	switch e := ast.Unparen(e).(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return e.Sel.Name
	}
	return ""
}

// probedType names the capability a type expression asserts: a capability or
// consumer interface by name (a consumer such as butler.PatchForge embeds
// capabilities, so using it probes them), or an anonymous interface literal
// declaring a capability method or embedding one of those names. It returns ""
// for anything else.
func (c capabilitySet) probedType(e ast.Expr) string {
	e = ast.Unparen(e)
	if name := typeName(e); c.types[name] || c.consumers[name] {
		return name
	}
	if it, ok := e.(*ast.InterfaceType); ok {
		methods, embeds := interfaceMembers(it)
		for _, name := range methods {
			if c.methods[name] {
				return "interface{ " + name + " }"
			}
		}
		for _, e := range embeds {
			if name := typeName(e); c.types[name] || c.consumers[name] {
				return "interface{ " + name + " }"
			}
		}
	}
	return ""
}

// capabilityProbeSites returns file:line for every probe of a CodeForge
// capability in src: any AsCodeForge instantiation, a generic call
// instantiated with a capability (whatever the function is named), a type
// assertion or type-switch case naming a capability, or a type declaration
// that names, embeds, or declares a method of one (which would let a probe name
// it unseen). Probes inside the func named ex.fn (a plain function, not a
// method) and the declarations named in ex.types are skipped; a use of an
// ex.types name elsewhere in the file is still a probe.
func capabilityProbeSites(t *testing.T, filename string, src any, caps capabilitySet, ex fileExemptions) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, 0)
	if err != nil {
		t.Fatalf("ParseFile(%s): %v", filename, err)
	}
	var sites []string
	hit := func(p token.Pos, what string) {
		sites = append(sites, fset.Position(p).String()+": "+what)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncDecl:
			if ex.fn != "" && n.Recv == nil && n.Name.Name == ex.fn {
				return false
			}
		case *ast.IndexExpr:
			if name := caps.probedType(n.Index); name != "" {
				hit(n.Pos(), "generic instantiation with "+name)
			} else if typeName(n.X) == "AsCodeForge" {
				hit(n.Pos(), "AsCodeForge instantiation")
			}
		case *ast.IndexListExpr:
			for _, e := range n.Indices {
				if name := caps.probedType(e); name != "" {
					hit(n.Pos(), "generic instantiation with "+name)
				}
			}
		case *ast.TypeAssertExpr:
			if n.Type != nil {
				if name := caps.probedType(n.Type); name != "" {
					hit(n.Pos(), "type assertion to "+name)
				}
			}
		case *ast.CaseClause:
			for _, e := range n.List {
				if name := caps.probedType(e); name != "" {
					hit(e.Pos(), "type switch case "+name)
				}
			}
		case *ast.TypeSpec:
			if name := caps.probedType(n.Type); name != "" && !caps.types[n.Name.Name] && !ex.types[n.Name.Name] {
				hit(n.Pos(), "type "+n.Name.Name+" names "+name)
			}
		}
		return true
	})
	return sites
}

// fileExemptions is what one file may do: probe inside the plain func fn, and
// declare the consumer interfaces in types.
type fileExemptions struct {
	fn    string
	types map[string]bool
}

// probeExemptions maps a file (relative to cmd/launcher) to the one func in
// which probing is allowed. unwrap.go's generic AsCodeForge asserts a type
// parameter, which the guard cannot confuse for a capability, so needs none.
var probeExemptions = map[string]string{
	"internal/forge/capabilities.go": "ResolveCodeForge",
}

// declExemptions maps a file to consumer-side interfaces that declare
// capability methods but are only ever filled from resolved Capabilities (see
// hostPatchForge and butlerPatchForge in cmd/launcher/butler.go), never probed.
// Only the declaration is exempt: asserting to or instantiating one of these
// anywhere is still a probe of the capabilities it embeds.
var declExemptions = map[string]map[string]bool{
	"internal/butler/butler.go": {"PatchForge": true, "MergeReader": true},
}

// TestNoCapabilityProbeOutsideResolver fails when a CodeForge capability is
// probed anywhere but ResolveCodeForge. A Connector's handshake capabilities
// fill the resolver's answer (ADR 0073 decision 5), so a probe elsewhere would
// bypass them. Tracker-side As[...] probes are out of scope.
//
// launcher-go-test copies only the core tree, where package main and the
// Drivers are absent; launcher-binaries-go-test copies the whole tree and sets
// SPINDRIFT_PROBE_GUARD_FULL_TREE, where the walk must also reach package main.
func TestNoCapabilityProbeOutsideResolver(t *testing.T) {
	caps := resolverCapabilities(t, "capabilities.go")
	for _, want := range []string{"BundleRelay", "PRForge"} {
		if !caps.types[want] {
			t.Fatalf("ResolveCodeForge probe set %v lacks %s; the guard is inert", caps.types, want)
		}
	}
	if !caps.methods["RelayBundle"] {
		t.Fatalf("capability methods %v lack RelayBundle; the guard is inert", caps.methods)
	}
	root := filepath.Join("..", "..")
	sawSettle, sawMain := false, false
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			t.Fatalf("Rel(%s, %s): %v", root, path, err)
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			switch d.Name() {
			case "vendor", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		// Test code may probe capabilities directly (fakes, contract checks), so
		// only non-test files are guarded.
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// forgetest contracts probe the adapter directly by design; its other
		// files are guarded.
		if strings.HasPrefix(rel, "internal/forge/forgetest/") && strings.HasSuffix(rel, "_contract.go") {
			return nil
		}
		sawSettle = sawSettle || rel == "internal/settle/ready.go"
		sawMain = sawMain || rel == "readshadow.go"
		for _, s := range capabilityProbeSites(t, path, nil, caps, fileExemptions{probeExemptions[rel], declExemptions[rel]}) {
			t.Errorf("%s: CodeForge capability probed outside the resolver; read it off forge.ResolveCodeForge / Capabilities instead", s)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sawSettle {
		t.Fatalf("walk under %s never reached internal/settle/ready.go; the walk is inert", root)
	}
	if os.Getenv("SPINDRIFT_PROBE_GUARD_FULL_TREE") != "" && !sawMain {
		t.Fatalf("walk under %s never reached readshadow.go in package main; the full tree is not copied", root)
	}
}

func TestCapabilityProbeDetector(t *testing.T) {
	caps := capabilitySet{
		types:     map[string]bool{"BundleRelay": true, "PRForge": true, "LandingRef": true},
		methods:   map[string]bool{"RelayBundle": true, "OpenPRForBranch": true},
		consumers: map[string]bool{"PatchForge": true},
	}
	const pre = "package p\nimport \"spindrift.dev/launcher/internal/forge\"\n"
	const resolver = "package forge\nfunc ResolveCodeForge(cf CodeForge) { _, _ = AsCodeForge[BundleRelay](cf) }\n"
	for name, tc := range map[string]struct {
		src         string
		exemptFunc  string
		exemptTypes map[string]bool
		want        int
	}{
		"as-call":         {src: pre + "func f(cf forge.CodeForge) { _, _ = forge.AsCodeForge[forge.BundleRelay](cf) }\n", want: 1},
		"bare-as-call":    {src: "package forge\nfunc f(cf CodeForge) { _, _ = AsCodeForge[BundleRelay](cf) }\n", want: 1},
		"paren-assertion": {src: pre + "func f(cf forge.CodeForge) { _, _ = cf.((forge.PRForge)) }\n", want: 1},
		"paren-generic":   {src: pre + "func f(cf forge.CodeForge) { _, _ = forge.As[(forge.PRForge)](cf) }\n", want: 1},
		"assertion":       {src: pre + "func f(cf forge.CodeForge) { _, _ = cf.(forge.PRForge) }\n", want: 1},
		"bare-assertion":  {src: "package forge\nfunc f(cf CodeForge) { _, _ = cf.(PRForge) }\n", want: 1},
		"type-switch":     {src: pre + "func f(cf forge.CodeForge) {\n\tswitch cf.(type) {\n\tcase forge.LandingRef:\n\t}\n}\n", want: 1},
		"tracker-as":      {src: pre + "func f(it forge.IssueTracker) { _, _ = forge.As[forge.MergeCloser](it) }\n"},
		"other-assertion": {src: pre + "func f(x any) { _, _ = x.(forge.Issue) }\n"},
		"plain-switch":    {src: pre + "func f(x any) {\n\tswitch x.(type) {\n\tcase string:\n\t}\n}\n"},

		"exempt-func-call":  {src: resolver, exemptFunc: "ResolveCodeForge"},
		"exempt-other-func": {src: resolver + "func g(cf CodeForge) { _, _ = AsCodeForge[PRForge](cf) }\n", exemptFunc: "ResolveCodeForge", want: 1},
		"exempt-method":     {src: "package forge\nfunc (T) ResolveCodeForge(cf CodeForge) { _, _ = AsCodeForge[PRForge](cf) }\n", exemptFunc: "ResolveCodeForge", want: 1},
		"exempt-unset":      {src: resolver, want: 1},

		"alias":      {src: pre + "type P = forge.PRForge\n", want: 1},
		"defined":    {src: pre + "type P forge.PRForge\n", want: 1},
		"other-type": {src: pre + "type P forge.Issue\n"},

		"anon-assertion":        {src: pre + "func f(cf forge.CodeForge) { _, _ = cf.(interface{ RelayBundle(a, b string) error }) }\n", want: 1},
		"anon-type-switch":      {src: pre + "func f(cf forge.CodeForge) {\n\tswitch cf.(type) {\n\tcase interface{ OpenPRForBranch(string) (forge.PR, bool, error) }:\n\t}\n}\n", want: 1},
		"generic-as-capability": {src: pre + "func f(cf forge.CodeForge) { _, _ = forge.As[forge.PRForge](cf) }\n", want: 1},
		"local-generic":         {src: pre + "func f(cf forge.CodeForge) { _, _ = probe[forge.PRForge](cf) }\n", want: 1},
		"generic-multi":         {src: pre + "func f(cf forge.CodeForge) { _ = probe[string, forge.PRForge](cf) }\n", want: 1},
		"generic-anon":          {src: pre + "func f(cf forge.CodeForge) { _, _ = probe[interface{ RelayBundle(a, b string) error }](cf) }\n", want: 1},
		"anon-embedded":         {src: pre + "func f(cf forge.CodeForge) { _, _ = cf.(interface{ forge.PRForge }) }\n", want: 1},
		"anon-embedded-switch":  {src: pre + "func f(cf forge.CodeForge) {\n\tswitch cf.(type) {\n\tcase interface{ PRForge }:\n\t}\n}\n", want: 1},
		"anon-embedded-other":   {src: pre + "func f(x any) { _, _ = x.(interface{ forge.Issue }) }\n"},
		"anon-unrelated":        {src: pre + "func f(x any) { _, _ = x.(interface{ Close() error }) }\n"},

		"as-named-embed":     {src: pre + "type prish interface{ forge.PRForge }\nfunc f(cf forge.CodeForge) { _, _ = forge.AsCodeForge[prish](cf) }\n", want: 2},
		"as-named-method":    {src: pre + "type prish interface{ OpenPRForBranch(string) (forge.PR, bool, error) }\nfunc f(cf forge.CodeForge) { _, _ = forge.AsCodeForge[prish](cf) }\n", want: 2},
		"as-unrelated-local": {src: pre + "type local struct{}\nfunc f(cf forge.CodeForge) { _, _ = forge.AsCodeForge[local](cf) }\n", want: 1},
		"named-embed":        {src: pre + "type prish interface{ forge.PRForge }\n", want: 1},
		"named-method":       {src: pre + "type prish interface{ OpenPRForBranch(string) (forge.PR, bool, error) }\n", want: 1},
		"named-unrelated":    {src: pre + "type closer interface{ Close() error }\n"},
		"own-declaration":    {src: "package forge\ntype PRForge interface{ OpenPRForBranch(string) (PR, bool, error) }\n"},

		"consumer-assertion":       {src: "package p\nimport \"spindrift.dev/launcher/internal/butler\"\nfunc f(cf CodeForge) { _, _ = cf.(butler.PatchForge) }\n", want: 1},
		"consumer-bare-assertion":  {src: "package butler\nfunc f(cf CodeForge) { _, _ = cf.(PatchForge) }\n", want: 1},
		"consumer-generic":         {src: pre + "func f(cf forge.CodeForge) { _, _ = probe[PatchForge](cf) }\n", want: 1},
		"consumer-anon-embedded":   {src: pre + "func f(cf forge.CodeForge) { _, _ = cf.(interface{ PatchForge }) }\n", want: 1},
		"consumer-decl-exempt":     {src: "package butler\ntype PatchForge interface{ OpenPRForBranch(string) (forge.PR, bool, error) }\n", exemptTypes: map[string]bool{"PatchForge": true}},
		"consumer-decl-not-exempt": {src: "package butler\ntype PatchForge interface{ OpenPRForBranch(string) (forge.PR, bool, error) }\n", want: 1},
		"consumer-decl-exempt-use": {src: "package butler\ntype PatchForge interface{ OpenPRForBranch(string) (forge.PR, bool, error) }\nfunc f(cf CodeForge) { _, _ = cf.(PatchForge) }\n", exemptTypes: map[string]bool{"PatchForge": true}, want: 1},
	} {
		t.Run(name, func(t *testing.T) {
			if got := capabilityProbeSites(t, name+".go", tc.src, caps, fileExemptions{tc.exemptFunc, tc.exemptTypes}); len(got) != tc.want {
				t.Errorf("got %v, want %d site(s)", got, tc.want)
			}
		})
	}
}

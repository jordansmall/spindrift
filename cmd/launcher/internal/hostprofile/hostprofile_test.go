package hostprofile

import (
	"net/http"
	"reflect"
	"regexp"
	"testing"
)

func TestMatch(t *testing.T) {
	tests := []struct {
		name   string
		url    string
		header http.Header
		want   string
	}{
		{"self-hosted path prefix", "https://repo.example.com/artifactory/api/npm/npm-remote/", nil, "artifactory"},
		{"jfrog saas host", "https://acme.jfrog.io/some/path", nil, "artifactory"},
		{"jfrog host is case-insensitive", "https://ACME.JFrog.io/", nil, "artifactory"},
		{"header only", "https://registry.example.com/npm/", http.Header{"X-Artifactory-Id": {"abc"}}, "artifactory"},
		{"unparseable url still matches header", "://bad url", http.Header{"X-Artifactory-Id": {"abc"}}, "artifactory"},
		{"first row wins over later public row", "https://index.crates.io/", http.Header{"X-Artifactory-Id": {"abc"}}, "artifactory"},
		{"crates.io apex", "https://crates.io/", nil, "crates.io"},
		{"crates.io index", "https://index.crates.io/", nil, "crates.io"},
		{"npmjs", "https://registry.npmjs.org/", nil, "registry.npmjs.org"},
		{"go proxy", "https://proxy.golang.org", nil, "proxy.golang.org"},
		{"unprofiled host", "https://registry.example.com/npm/", nil, ""},
		{"unprofiled host with unrelated header", "https://registry.example.com/npm/", http.Header{"Server": {"nginx"}}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			row, ok := Match(tt.url, tt.header)
			if tt.want == "" {
				if ok {
					t.Fatalf("Match(%q) = %q, want no match", tt.url, row.Name)
				}
				return
			}
			if !ok || row.Name != tt.want {
				t.Fatalf("Match(%q) = %q, %v; want %q", tt.url, row.Name, ok, tt.want)
			}
		})
	}
}

func TestMatchPublicDefaultsAreUnauthenticated(t *testing.T) {
	for _, u := range []string{"https://index.crates.io/", "https://registry.npmjs.org/", "https://proxy.golang.org"} {
		row, ok := Match(u, nil)
		if !ok {
			t.Fatalf("Match(%q): no match", u)
		}
		if row.AuthScheme != "" {
			t.Errorf("%s: AuthScheme = %q, want empty", row.Name, row.AuthScheme)
		}
	}
}

func TestTableAuthSchemesUseRoutesGrammar(t *testing.T) {
	grammar := regexp.MustCompile("^(bearer|basic|header:[!#$%&'*+.^_`|~0-9A-Za-z-]+)$")
	for _, row := range table {
		if row.AuthScheme != "" && !grammar.MatchString(row.AuthScheme) {
			t.Errorf("%s: AuthScheme %q is not bearer, basic or header:<Name>", row.Name, row.AuthScheme)
		}
	}
}

func TestTableNamesAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, row := range table {
		if row.Name == "" || seen[row.Name] {
			t.Errorf("row name %q is empty or duplicated", row.Name)
		}
		seen[row.Name] = true
	}
}

// A Row must stay plain data (ADR 0071): no funcs, channels, interfaces or
// unsafe pointers anywhere in its type.
func TestRowIsValueOnly(t *testing.T) {
	var walk func(path string, typ reflect.Type, seen map[reflect.Type]bool)
	walk = func(path string, typ reflect.Type, seen map[reflect.Type]bool) {
		switch typ.Kind() {
		case reflect.Func, reflect.Chan, reflect.Interface, reflect.UnsafePointer:
			t.Errorf("%s has kind %s; a Row must be value-only", path, typ.Kind())
			return
		}
		if seen[typ] {
			return
		}
		seen[typ] = true
		switch typ.Kind() {
		case reflect.Struct:
			for i := 0; i < typ.NumField(); i++ {
				f := typ.Field(i)
				walk(path+"."+f.Name, f.Type, seen)
			}
		case reflect.Slice, reflect.Array, reflect.Pointer:
			walk(path+"[]", typ.Elem(), seen)
		case reflect.Map:
			walk(path+"[key]", typ.Key(), seen)
			walk(path+"[]", typ.Elem(), seen)
		}
	}
	walk("Row", reflect.TypeOf(Row{}), map[reflect.Type]bool{})
}

func TestRowsAndMatchReturnDeepCopies(t *testing.T) {
	wantSibling := table[0].Siblings["cargo"][0]
	wantHost := table[0].Hosts[0]

	rows := Rows()
	if len(rows) != len(table) {
		t.Fatalf("Rows() has %d rows, table has %d", len(rows), len(table))
	}
	rows[0].Siblings["cargo"][0] = "edited"
	rows[0].Siblings["extra"] = []string{"x"}
	rows[0].Hosts[0] = "edited"

	m, ok := Match("https://acme.jfrog.io/artifactory/api/npm/x", nil)
	if !ok {
		t.Fatal("no match for a jfrog.io host")
	}
	m.Siblings["cargo"][0] = "edited"
	m.Hosts[0] = "edited"

	for name, got := range map[string]Row{"table": table[0], "Rows()": Rows()[0]} {
		if got.Siblings["cargo"][0] != wantSibling || got.Hosts[0] != wantHost {
			t.Errorf("%s was aliased by a returned row: siblings %q hosts %q", name, got.Siblings["cargo"][0], got.Hosts[0])
		}
		if _, ok := got.Siblings["extra"]; ok {
			t.Errorf("%s gained a Siblings key from a returned row", name)
		}
	}
}

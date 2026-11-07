package promptassembly

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

// Every FragmentRow column decodes from its literal nix-rendered JSON name, rows
// keep their source order, and a row without extraSubstVars decodes to none.
func TestLoadRegistryDecodesEveryColumn(t *testing.T) {
	const src = `[
		{"gate": "G_ONE", "fragment": "one.md", "legacyFragment": "old-one.md", "axis": "forge",
		 "shared": true, "var": "ONE_STEP", "extraSubstVars": ["A", "B"],
		 "signalChannel": "comment", "inverseOf": "G_TWO", "inline": true},
		{"gate": "G_TWO", "fragment": "two.md", "var": "TWO_STEP"}
	]`
	reg, err := LoadRegistry(strings.NewReader(src))
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}

	want := FragmentRow{
		Gate: "G_ONE", Fragment: "one.md", LegacyFragment: "old-one.md", Axis: "forge",
		Shared: true, Var: "ONE_STEP", ExtraSubstVars: []string{"A", "B"},
		SignalChannel: "comment", InverseOf: "G_TWO", Inline: true,
	}
	if len(reg.Rows) != 2 {
		t.Fatalf("len(reg.Rows) = %d, want 2", len(reg.Rows))
	}
	if !reflect.DeepEqual(reg.Rows[0], want) {
		t.Errorf("reg.Rows[0] = %+v, want %+v", reg.Rows[0], want)
	}
	second := reg.Rows[1]
	if second.Gate != "G_TWO" || second.Fragment != "two.md" || second.Var != "TWO_STEP" {
		t.Errorf("reg.Rows[1] = %+v, want G_TWO/two.md/TWO_STEP", second)
	}
	if len(second.ExtraSubstVars) != 0 {
		t.Errorf("reg.Rows[1].ExtraSubstVars = %v, want empty", second.ExtraSubstVars)
	}
}

func TestLoadRegistryMalformed(t *testing.T) {
	f, err := os.Open("testdata/malformed.json")
	if err != nil {
		t.Fatalf("open testdata/malformed.json: %v", err)
	}
	defer f.Close()

	if _, err := LoadRegistry(f); err == nil {
		t.Fatal("LoadRegistry(malformed) = nil error, want non-nil")
	}
}

func TestLoadRegistryFileMalformed(t *testing.T) {
	if _, err := LoadRegistryFile("testdata/malformed.json"); err == nil {
		t.Fatal("LoadRegistryFile(malformed) = nil error, want non-nil")
	}
}

func TestLoadRegistryFileNonexistent(t *testing.T) {
	if _, err := LoadRegistryFile("testdata/does-not-exist.json"); err == nil {
		t.Fatal("LoadRegistryFile(nonexistent) = nil error, want non-nil")
	}
}

// An empty reader hands the JSON decoder io.EOF before any token, which must
// come back as an error rather than an empty registry.
func TestLoadRegistryEmptyReader(t *testing.T) {
	if _, err := LoadRegistry(strings.NewReader("")); err == nil {
		t.Fatal("LoadRegistry(empty reader) = nil error, want non-nil")
	}
}

func TestResolvedFragmentForgeAxis(t *testing.T) {
	row := FragmentRow{Axis: "forge", Fragment: "x.md", Var: "X"}
	cases := []struct {
		name string
		env  Env
		want string
	}{
		{"pre-resolved GH wins over CodeForge", Env{ForgeBackend: "GH", CodeForge: "git"}, "github/x.md"},
		{"pre-resolved FORGEJO wins over CodeForge", Env{ForgeBackend: "FORGEJO", CodeForge: "github"}, "forgejo/x.md"},
		{"empty backend falls back to CodeForge", Env{CodeForge: "forgejo"}, "forgejo/x.md"},
		{"both empty takes the default", Env{}, "github/x.md"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := row.resolvedFragment(tc.env)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("resolvedFragment = %q, want %q", got, tc.want)
			}
		})
	}
}

// An empty signal maps to the github folder, the arm trackerGates takes for an
// empty itFiler, so a row's folder and its gate cannot disagree.
func TestSignalFolder(t *testing.T) {
	for signal, want := range map[string]string{
		"": "github", "GH": "github", "GITHUB": "github", "LOCAL": "local", "FORGEJO": "forgejo",
	} {
		if got := signalFolder(signal); got != want {
			t.Errorf("signalFolder(%q) = %q, want %q", signal, got, want)
		}
	}
}

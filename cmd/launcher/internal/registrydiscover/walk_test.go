package registrydiscover

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/ecosystem"
)

// The config file sits under a subdirectory so the test pins that the walker
// joins InTreeConfigPath onto repoDir rather than reading a flat name.
func TestExtractRows_ReadsFileNamedByRowRelativeToRepoDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "nested", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	const content = "fake config content\n"
	if err := os.WriteFile(filepath.Join(dir, "nested", "sub", "fake.cfg"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	var gotContent string
	row := ecosystem.Row{
		Name:             "fake",
		InTreeConfigPath: "nested/sub/fake.cfg",
		ConfigParser: func(c string) ([]ecosystem.Declaration, bool, error) {
			gotContent = c
			return nil, false, nil
		},
	}

	if _, _, err := extractRows(dir, []ecosystem.Row{row}); err != nil {
		t.Fatalf("extractRows: unexpected error: %v", err)
	}
	if gotContent != content {
		t.Errorf("parser received %q, want %q", gotContent, content)
	}
}

func TestExtractRows_MissingFileYieldsNothingAndDoesNotStopTheWalk(t *testing.T) {
	dir := t.TempDir()
	missing := ecosystem.Row{
		Name:             "missing",
		InTreeConfigPath: "does-not-exist.cfg",
		ConfigParser: func(c string) ([]ecosystem.Declaration, bool, error) {
			t.Fatal("ConfigParser called for a config file that does not exist")
			return nil, false, nil
		},
	}
	if err := os.WriteFile(filepath.Join(dir, "present.cfg"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	present := ecosystem.Row{
		Name:             "present",
		InTreeConfigPath: "present.cfg",
		ConfigParser: func(c string) ([]ecosystem.Declaration, bool, error) {
			return []ecosystem.Declaration{{Host: "later-row.example.com"}}, true, nil
		},
	}

	declared, notes, err := extractRows(dir, []ecosystem.Row{missing, present})
	if err != nil {
		t.Fatalf("extractRows: unexpected error: %v", err)
	}
	if len(notes) != 0 {
		t.Errorf("notes = %+v, want none", notes)
	}
	if len(declared) != 1 || declared[0].Host != "later-row.example.com" {
		t.Errorf("declared = %+v, want the later row's one declaration", declared)
	}
}

// A directory stands in for the nil-parser row's named file: if the walker read
// it anyway, os.ReadFile would fail and the test would see the error. An empty
// path joins to repoDir, also a directory, so the same check catches it.
func TestExtractRows_SkipsRowWithNoPathOrNoParser(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "a-directory"), 0o755); err != nil {
		t.Fatal(err)
	}

	noPath := ecosystem.Row{
		Name: "no-path",
		ConfigParser: func(c string) ([]ecosystem.Declaration, bool, error) {
			return []ecosystem.Declaration{{Host: "should-not-appear.example.com"}}, true, nil
		},
	}
	noParser := ecosystem.Row{
		Name:             "no-parser",
		InTreeConfigPath: "a-directory",
	}

	declared, notes, err := extractRows(dir, []ecosystem.Row{noPath, noParser})
	if err != nil {
		t.Fatalf("extractRows: unexpected error: %v", err)
	}
	if len(declared) != 0 {
		t.Errorf("declared = %+v, want none", declared)
	}
	if len(notes) != 0 {
		t.Errorf("notes = %+v, want none", notes)
	}
}

// The parser sets Ecosystem and ConfigPath to wrong values on purpose, so the
// test can see the row's own stamp win.
func TestExtractRows_StampsRowFieldsOverParserOutput(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "fake.cfg"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	row := ecosystem.Row{
		Name:             "fake",
		InTreeConfigPath: "fake.cfg",
		ConfigParser: func(c string) ([]ecosystem.Declaration, bool, error) {
			return []ecosystem.Declaration{{Ecosystem: "wrong-ecosystem", ConfigPath: "wrong/path", Host: "h.example.com"}}, true, nil
		},
	}

	declared, _, err := extractRows(dir, []ecosystem.Row{row})
	if err != nil {
		t.Fatalf("extractRows: unexpected error: %v", err)
	}
	if len(declared) != 1 {
		t.Fatalf("declared = %+v, want exactly 1", declared)
	}
	want := ecosystem.Declaration{Ecosystem: "fake", ConfigPath: "fake.cfg", Host: "h.example.com"}
	if declared[0] != want {
		t.Errorf("declared[0] = %+v, want %+v", declared[0], want)
	}
}

func TestExtractRows_NoDeclarationsProducesNoteCarryingNamedAny(t *testing.T) {
	for _, namedAny := range []bool{true, false} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "fake.cfg"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		row := ecosystem.Row{
			Name:             "fake",
			InTreeConfigPath: "fake.cfg",
			ConfigParser: func(c string) ([]ecosystem.Declaration, bool, error) {
				return nil, namedAny, nil
			},
		}

		declared, notes, err := extractRows(dir, []ecosystem.Row{row})
		if err != nil {
			t.Fatalf("extractRows: unexpected error: %v", err)
		}
		if len(declared) != 0 {
			t.Errorf("declared = %+v, want none", declared)
		}
		want := ecosystem.Note{ConfigPath: "fake.cfg", Ecosystem: "fake", Skipped: namedAny}
		if len(notes) != 1 || notes[0] != want {
			t.Errorf("namedAny=%v: notes = %+v, want [%+v]", namedAny, notes, want)
		}
	}
}

func TestExtractRows_OrderFollowsRowsThenParserOrder(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"r1.cfg", "r2.cfg", "r3.cfg", "r4.cfg"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	declaring := func(hosts ...string) ecosystem.ConfigParser {
		return func(c string) ([]ecosystem.Declaration, bool, error) {
			decls := make([]ecosystem.Declaration, len(hosts))
			for i, h := range hosts {
				decls[i] = ecosystem.Declaration{Host: h}
			}
			return decls, true, nil
		}
	}
	noting := func(skipped bool) ecosystem.ConfigParser {
		return func(c string) ([]ecosystem.Declaration, bool, error) {
			return nil, skipped, nil
		}
	}

	rows := []ecosystem.Row{
		{Name: "r1", InTreeConfigPath: "r1.cfg", ConfigParser: declaring("a", "b")},
		{Name: "r2", InTreeConfigPath: "r2.cfg", ConfigParser: noting(false)},
		{Name: "r3", InTreeConfigPath: "r3.cfg", ConfigParser: declaring("c")},
		{Name: "r4", InTreeConfigPath: "r4.cfg", ConfigParser: noting(true)},
	}

	declared, notes, err := extractRows(dir, rows)
	if err != nil {
		t.Fatalf("extractRows: unexpected error: %v", err)
	}

	var gotHosts []string
	for _, d := range declared {
		gotHosts = append(gotHosts, d.Host)
	}
	if strings.Join(gotHosts, ",") != "a,b,c" {
		t.Errorf("declared hosts = %v, want [a b c]", gotHosts)
	}

	wantNotes := []ecosystem.Note{
		{ConfigPath: "r2.cfg", Ecosystem: "r2", Skipped: false},
		{ConfigPath: "r4.cfg", Ecosystem: "r4", Skipped: true},
	}
	if len(notes) != len(wantNotes) {
		t.Fatalf("notes = %+v, want %+v", notes, wantNotes)
	}
	for i, w := range wantNotes {
		if notes[i] != w {
			t.Errorf("notes[%d] = %+v, want %+v", i, notes[i], w)
		}
	}
}

func TestExtractRows_ParserErrorAbortsTheWalk(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad.cfg"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "later.cfg"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	rows := []ecosystem.Row{
		{
			Name:             "bad",
			InTreeConfigPath: "bad.cfg",
			ConfigParser: func(c string) ([]ecosystem.Declaration, bool, error) {
				return nil, false, errTestParse
			},
		},
		{
			Name:             "later",
			InTreeConfigPath: "later.cfg",
			ConfigParser: func(c string) ([]ecosystem.Declaration, bool, error) {
				t.Fatal("ConfigParser called for a row after an earlier row's parser error")
				return nil, false, nil
			},
		},
	}

	_, _, err := extractRows(dir, rows)
	if err == nil {
		t.Fatal("extractRows: expected an error, got nil")
	}
	want := "registrydiscover: parsing bad.cfg: " + errTestParse.Error()
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
}

// A directory sits where the row expects a file, which is portable across
// platforms unlike a permission-bit trick. This is the read-error half of the
// contract TestExtractRows_ParserErrorAbortsTheWalk pins for parse errors.
func TestExtractRows_UnreadableFileAbortsTheWalk(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "not-a-file.cfg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "later.cfg"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	rows := []ecosystem.Row{
		{
			Name:             "unreadable",
			InTreeConfigPath: "not-a-file.cfg",
			ConfigParser: func(c string) ([]ecosystem.Declaration, bool, error) {
				t.Fatal("ConfigParser called for content the walker should have failed to read")
				return nil, false, nil
			},
		},
		{
			Name:             "later",
			InTreeConfigPath: "later.cfg",
			ConfigParser: func(c string) ([]ecosystem.Declaration, bool, error) {
				t.Fatal("ConfigParser called for a row after an earlier row's read error")
				return nil, false, nil
			},
		},
	}

	_, _, err := extractRows(dir, rows)
	if err == nil {
		t.Fatal("extractRows: expected an error, got nil")
	}
	if !strings.HasPrefix(err.Error(), "registrydiscover: reading not-a-file.cfg: ") {
		t.Errorf("err = %q, want it to start with the walker's reading-error prefix naming not-a-file.cfg", err.Error())
	}
}

// The walker only wraps a parser error, so this stand-in's exact type and text
// do not matter.
var errTestParse = testParseError{}

type testParseError struct{}

func (testParseError) Error() string { return "fake parser error" }

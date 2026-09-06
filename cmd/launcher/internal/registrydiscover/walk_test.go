package registrydiscover

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/ecosystem"
)

// TestExtractRows_ReadsFileNamedByRowRelativeToRepoDir verifies the walker
// reads exactly the file a row's InTreeConfigPath names, joined onto
// repoDir -- including a path nested under a subdirectory -- and hands that
// file's content to the row's own parser unchanged.
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

// TestExtractRows_MissingFileYieldsNothingAndDoesNotStopTheWalk verifies
// that a row whose config file is absent contributes no declaration and no
// note, produces no error, and does not stop the walker from reaching a
// later row.
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

// TestExtractRows_SkipsRowWithNoPathOrNoParser verifies that a row with an
// empty InTreeConfigPath, or a nil ConfigParser, is skipped without any
// read attempt -- an empty path would otherwise join to repoDir itself, and
// a directory in place of the nil-parser row's named file, would surface an
// os.ReadFile error if the walker read it anyway. Neither case may produce
// an error, a declaration, or a note.
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

// TestExtractRows_StampsRowFieldsOverParserOutput verifies that Ecosystem
// and ConfigPath on every returned Declaration always come from the row,
// even when the parser (wrongly) sets them itself -- the walker's stamp
// wins.
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

// TestExtractRows_NoDeclarationsProducesNoteCarryingNamedAny verifies that a
// parser returning no declarations produces exactly one Note carrying the
// row's own name and path, with Note.Skipped set to whatever the parser
// reported as namedAny -- checked in both the true and false case.
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

// TestExtractRows_OrderFollowsRowsThenParserOrder verifies that declarations
// and notes come back in the order of the rows slice the caller passed, and
// -- within a row that yields declarations -- in the parser's own order.
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

// TestExtractRows_ParserErrorAbortsTheWalk verifies that a parser error
// surfaces wrapped in the walker's own message naming the row's config path,
// and that the walk stops rather than continuing to a later row.
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

// TestExtractRows_UnreadableFileAbortsTheWalk covers the file-read side of
// the same contract as TestExtractRows_ParserErrorAbortsTheWalk: a config
// path that exists but cannot be read as a file (here, a directory sits
// where the row expects one -- portable across platforms, unlike a
// permission-bit trick) surfaces wrapped in the walker's reading-error
// message and stops the walk before any later row's parser runs.
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

// errTestParse is a stand-in for whatever error a real ConfigParser (e.g.
// cargo's toml.Unmarshal failure) might return -- its exact type and text
// are irrelevant to the walker, which only wraps it.
var errTestParse = testParseError{}

type testParseError struct{}

func (testParseError) Error() string { return "fake parser error" }

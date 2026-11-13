package registrydiscover

import (
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/ecosystemtest/synthetic"
	"spindrift.dev/launcher/internal/testutil"
)

const (
	spellingFirst  = "first.cfg"
	spellingSecond = "Second.cfg"
	firstDecl      = "a = https://first.example.com/repo\n"
	secondDecl     = "a = https://second.example.com/repo\n"
)

var spellingRows = []ecosystem.Strategy{synthetic.Spellings("spellings", spellingFirst, spellingSecond)}

func TestExtract_DiscoveryConfigPathsReadsFirstPresentSpelling(t *testing.T) {
	cases := []struct {
		name     string
		files    map[string]string
		wantHost string
		wantPath string
	}{
		{"only the second", map[string]string{spellingSecond: secondDecl}, "second.example.com", spellingSecond},
		{"both present", map[string]string{spellingFirst: firstDecl, spellingSecond: secondDecl}, "first.example.com", spellingFirst},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for rel, body := range tc.files {
				testutil.WriteFixture(t, dir, rel, body)
			}
			decls, notes, err := Extract(dir, spellingRows)
			if err != nil {
				t.Fatalf("Extract: %v", err)
			}
			if len(notes) != 0 || len(decls) != 1 {
				t.Fatalf("Extract = %+v, %+v, want one declaration and no notes", decls, notes)
			}
			if decls[0].Host != tc.wantHost || decls[0].ConfigPath != tc.wantPath {
				t.Errorf("declaration = %+v, want host %s from %s", decls[0], tc.wantHost, tc.wantPath)
			}
		})
	}
}

func TestExtract_DiscoveryConfigPathsNonePresentIsSilent(t *testing.T) {
	decls, notes, err := Extract(t.TempDir(), spellingRows)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(decls) != 0 || len(notes) != 0 {
		t.Errorf("Extract = %+v, %+v, want nothing", decls, notes)
	}
}

// Writing both spellings into one snapshot would let a case-insensitive host
// filesystem clobber one with the other, so only the first present is written.
func TestMaterializeRef_DiscoveryConfigPathsMaterializesFirstPresentOnly(t *testing.T) {
	cases := []struct {
		name         string
		files        map[string]string
		wantHost     string
		wantPath     string
		wantAbsentIs string
	}{
		{"only the second", map[string]string{spellingSecond: secondDecl}, "second.example.com", spellingSecond, spellingFirst},
		{"both committed", map[string]string{spellingFirst: firstDecl, spellingSecond: secondDecl}, "first.example.com", spellingFirst, spellingSecond},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bare, _ := testutil.BareRepoWithConfigs(t, "main", tc.files, nil)
			dir, cleanup, err := MaterializeRef(bare, "main", spellingRows)
			t.Cleanup(cleanup)
			if err != nil {
				t.Fatalf("MaterializeRef: %v", err)
			}
			if _, err := os.Stat(filepath.Join(dir, tc.wantPath)); err != nil {
				t.Errorf("want %s materialized: %v", tc.wantPath, err)
			}
			mustNotExist(t, filepath.Join(dir, tc.wantAbsentIs))

			decls, _, err := Extract(dir, spellingRows)
			if err != nil {
				t.Fatalf("Extract: %v", err)
			}
			if len(decls) != 1 || decls[0].Host != tc.wantHost || decls[0].ConfigPath != tc.wantPath {
				t.Errorf("Extract over snapshot = %+v, want host %s from %s", decls, tc.wantHost, tc.wantPath)
			}
		})
	}
}

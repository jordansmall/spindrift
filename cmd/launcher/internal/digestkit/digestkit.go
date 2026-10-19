// Package digestkit is the kit a records Chore's package calls from its own
// tests: Run replays a corpus of Record fixtures through a Digest Strategy's
// renderer and checks the digest text and the latest Record ID it covers
// against the goldens beside each case (ADR 0070). It takes the renderer and a
// directory, never a Chore registry, so a Chore proves itself over its own
// corpus alone.
//
// A corpus directory holds one subdirectory per case, each with a case.json
// (the Case fields other than Records), a records.json (a JSON array of
// dispatchrecord.Record), and the digest.golden.md and latest.golden.txt the
// kit compares (or rewrites under UPDATE_GOLDENS). Importing this package from
// non-test code pulls in testing; only _test.go files should do so.
package digestkit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/golden"
)

// Case is one corpus case's inputs.
type Case struct {
	// Records is decoded from records.json, not case.json.
	Records []dispatchrecord.Record `json:"-"`
	// Cursor is the prior Ledger cursor.
	Cursor string `json:"cursor"`
	// Now is the claim time.
	Now time.Time `json:"now"`
	// MinSample is the sample floor below which a row is thin.
	MinSample int `json:"min_sample"`
	// MaxBytes is the byte cap the digest is trimmed to.
	MaxBytes int `json:"max_bytes"`
	// Prior is the Chore's own prior findings, left for its renderer to decode.
	Prior json.RawMessage `json:"prior"`
	// Merged maps a PR URL to the merge commit a human landed; an absent URL
	// is unmerged.
	Merged map[string]string `json:"merged"`
}

// Render is a Digest Strategy's renderer: the digest text and the latest
// Record ID it covers. It fails its own case through t, so a malformed input
// is a test failure rather than a panic.
type Render func(t testing.TB, c Case) (text, latest string)

// Run replays every case under corpusDir through render: one subtest per case,
// comparing the digest and the latest Record ID against their goldens. A
// corpusDir with no case fails.
func Run(t *testing.T, render Render, corpusDir string) {
	t.Helper()
	update := golden.Update()
	for _, name := range caseDirs(t, corpusDir) {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(corpusDir, name)
			c, err := loadCase(dir)
			if err != nil {
				t.Fatal(err)
			}
			text, latest := render(t, c)
			if err := golden.CompareOrUpdateText(filepath.Join(dir, "digest.golden.md"), []byte(text), update); err != nil {
				t.Error(err)
			}
			if err := golden.CompareOrUpdateText(filepath.Join(dir, "latest.golden.txt"), []byte(latest+"\n"), update); err != nil {
				t.Error(err)
			}
		})
	}
}

// caseDirs lists the case subdirectories of corpusDir, failing t when there
// are none so an unpopulated or misspelled directory cannot pass vacuously.
func caseDirs(t testing.TB, corpusDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(corpusDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("read %s: %v", corpusDir, err)
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		t.Errorf("corpus %s has no case; add one at %s/<case>/case.json", corpusDir, corpusDir)
	}
	return names
}

// loadCase decodes dir's case.json and records.json strictly, so a typo'd key
// fails the case instead of silently rendering with a zero value.
func loadCase(dir string) (Case, error) {
	var c Case
	if err := decodeFile(filepath.Join(dir, "case.json"), &c); err != nil {
		return Case{}, err
	}
	if err := decodeFile(filepath.Join(dir, "records.json"), &c.Records); err != nil {
		return Case{}, err
	}
	return c, nil
}

func decodeFile(path string, into any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := DecodeStrict(b, into); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// DecodeStrict decodes b into into, rejecting unknown object keys and any
// content after the first JSON value, so a typo or a stray second object fails
// instead of silently dropping data.
func DecodeStrict(b []byte, into any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("trailing content after the JSON value")
	}
	return nil
}

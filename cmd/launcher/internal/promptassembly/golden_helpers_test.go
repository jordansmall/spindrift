package promptassembly_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// updateGoldens flips the golden helpers from comparing to overwriting. The
// name is the one `nix run .#regen-goldens` exports.
func updateGoldens() bool { return os.Getenv("UPDATE_GOLDENS") != "" }

// compareOrUpdateText diffs produced against the golden byte for byte, or
// overwrites the golden with it in update mode.
func compareOrUpdateText(goldenPath string, produced []byte, update bool) error {
	if update {
		return os.WriteFile(goldenPath, produced, 0o644)
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		return err
	}
	if !bytes.Equal(want, produced) {
		return fmt.Errorf("%s differs from the produced output: %s", goldenPath, firstDifference(want, produced))
	}
	return nil
}

// compareOrUpdateJSON is compareOrUpdateText over canonical JSON: both sides
// are projected onto project's keys (every key when empty) and canonicalised,
// so key order never causes a spurious diff and a golden written in update
// mode is always canonical.
func compareOrUpdateJSON(goldenPath string, produced []byte, update bool, project ...string) error {
	got, err := canonicalJSON(produced, project)
	if err != nil {
		return fmt.Errorf("produced output for %s: %w", goldenPath, err)
	}
	if update {
		return os.WriteFile(goldenPath, got, 0o644)
	}
	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		return err
	}
	want, err := canonicalJSON(raw, project)
	if err != nil {
		return fmt.Errorf("golden %s: %w", goldenPath, err)
	}
	if !bytes.Equal(want, got) {
		return fmt.Errorf("%s differs from the produced output: %s", goldenPath, firstDifference(want, got))
	}
	return nil
}

// removeGoldenIfUpdate deletes a golden the cell no longer produces, in update
// mode only. In compare mode it reports a golden that is still on disk.
func removeGoldenIfUpdate(goldenPath string, update bool) error {
	if update {
		if err := os.Remove(goldenPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if _, err := os.Stat(goldenPath); err == nil {
		return fmt.Errorf("%s exists but the cell produced no such output", goldenPath)
	}
	return nil
}

// canonicalJSON renders what `jq -S <filter>` prints: keys sorted, two-space
// indent, a trailing newline, and no HTML escaping. project, when non-empty,
// is jq's `{A, B}` object projection: an absent key becomes null.
func canonicalJSON(raw []byte, project []string) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}
	if len(project) > 0 {
		obj, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("cannot project keys %v out of a %T", project, v)
		}
		projected := make(map[string]any, len(project))
		for _, k := range project {
			projected[k] = obj[k]
		}
		v = projected
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// firstDifference names the first differing line, since a whole-prompt diff
// would bury the cause.
func firstDifference(want, got []byte) string {
	wl := strings.Split(string(want), "\n")
	gl := strings.Split(string(got), "\n")
	for i := 0; i < len(wl) || i < len(gl); i++ {
		var w, g string
		if i < len(wl) {
			w = wl[i]
		}
		if i < len(gl) {
			g = gl[i]
		}
		if i >= len(wl) || i >= len(gl) || w != g {
			return fmt.Sprintf("line %d: golden %q, produced %q (golden has %d lines, produced %d)", i+1, w, g, len(wl), len(gl))
		}
	}
	return "identical lines but different bytes"
}

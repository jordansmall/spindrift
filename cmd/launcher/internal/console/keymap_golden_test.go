package console

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/exp/golden"
)

// modeNames maps every Mode constant (model.go) to a stable, readable name
// for keymapFingerprint — golden.RequireEqual pins the resulting text, so a
// name here only needs to be readable and stable across runs, not to match
// any production stringer (Mode has none).
var modeNames = map[Mode]string{
	ModeList:             "ModeList",
	ModeSidebar:          "ModeSidebar",
	ModeRebuildOutput:    "ModeRebuildOutput",
	ModeHelp:             "ModeHelp",
	ModeFilterEdit:       "ModeFilterEdit",
	ModeTerminateConfirm: "ModeTerminateConfirm",
	ModeQuitConfirm:      "ModeQuitConfirm",
	ModeDetailModal:      "ModeDetailModal",
}

// modeName renders mode via modeNames, falling back to its raw int value for
// any Mode this file hasn't named yet — keeps keymapFingerprint from
// silently dropping a new mode out of the pinned text.
func modeName(mode Mode) string {
	if name, ok := modeNames[mode]; ok {
		return name
	}
	return fmt.Sprintf("Mode(%d)", int(mode))
}

// keymapFingerprint renders every keymap (keymap.go) entry's Keys and Modes,
// one line per entry, in keymap's own order — the order itself is part of
// what's pinned, since dispatchKey's lookup and renderHelp's overlay both
// depend on iterating keymap top to bottom.
func keymapFingerprint() string {
	var b strings.Builder
	for i, bind := range keymap {
		modes := make([]string, len(bind.Modes))
		for j, mode := range bind.Modes {
			modes[j] = modeName(mode)
		}
		fmt.Fprintf(&b, "[%d] Keys=%v Modes=%v\n", i, bind.Keys, modes)
	}
	return b.String()
}

// TestKeymap_Fingerprint_Golden pins every keymap entry's Keys/Modes, in
// order, against today's behaviour (issue #2361 Slice 1) — a safety net for
// the upcoming split of keymap's single 46-entry table into per-cluster
// files, so a change to any entry's key bindings, mode reach, or ordering
// shows up as a diff here even though the split itself is meant to be a
// pure reorganisation.
func TestKeymap_Fingerprint_Golden(t *testing.T) {
	golden.RequireEqual(t, []byte(keymapFingerprint()))
}

// TestRenderHelp_Golden pins renderHelp's (view.go) exact plain-text output
// — the "?" overlay it builds by walking keymap in order and concatenating
// each entry's Help field — against today's behaviour (issue #2361 Slice
// 1). renderHelp emits no styling of its own, so this needs no NO_COLOR/TERM
// split the way the style_golden_test.go golden tests do.
func TestRenderHelp_Golden(t *testing.T) {
	golden.RequireEqual(t, []byte(renderHelp()))
}

package console

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/exp/golden"
)

// Mode has no production stringer, so these names only have to stay stable
// across runs: golden.RequireEqual pins the text they produce.
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

// The raw-int fallback keeps a Mode this file has not named yet from silently
// dropping out of the pinned text.
func modeName(mode Mode) string {
	if name, ok := modeNames[mode]; ok {
		return name
	}
	return fmt.Sprintf("Mode(%d)", int(mode))
}

// keymap's own order is part of what this pins, because dispatchKey's lookup
// and renderHelp's overlay both iterate keymap top to bottom.
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

// Pins every keymap entry's Keys, Modes, and ordering (issue #2361 Slice 1).
// Splitting keymap's single 46-entry table into per-cluster files is meant to
// change none of them, so any change it does make shows up here as a diff.
func TestKeymap_Fingerprint_Golden(t *testing.T) {
	golden.RequireEqual(t, []byte(keymapFingerprint()))
}

// Pins renderHelp's (view.go) exact plain-text "?" overlay (issue #2361 Slice
// 1). renderHelp emits no styling of its own, so this needs no NO_COLOR/TERM
// split the way the style_golden_test.go golden tests do.
func TestRenderHelp_Golden(t *testing.T) {
	golden.RequireEqual(t, []byte(renderHelp()))
}

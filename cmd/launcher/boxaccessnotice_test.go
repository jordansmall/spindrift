package main

import (
	"bytes"
	"testing"

	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/inputdoc"
)

const testBoxAccessNotice = "spindrift: BOX_FORGE_AND_ISSUE_ACCESS defaults to read-write (test notice)"

func useDeprecatedDefaultDoc(t *testing.T) {
	t.Helper()
	useDoc(t, &inputdoc.Document{DeprecatedDefaults: map[string]string{
		"BOX_FORGE_AND_ISSUE_ACCESS": testBoxAccessNotice,
	}})
}

func TestWarnBoxAccessDefault(t *testing.T) {
	t.Run("defaulted read-write prints the document's message on one line", func(t *testing.T) {
		useDeprecatedDefaultDoc(t)
		var buf bytes.Buffer
		warnBoxAccessDefault(minimalValidConfig(), &buf)
		if got, want := buf.String(), testBoxAccessNotice+"\n"; got != want {
			t.Errorf("stderr = %q, want %q", got, want)
		}
	})
	t.Run("explicit flag is silent", func(t *testing.T) {
		useDeprecatedDefaultDoc(t)
		setKnob(t, "BOX_FORGE_AND_ISSUE_ACCESS", "read-write")
		var buf bytes.Buffer
		warnBoxAccessDefault(minimalValidConfig(), &buf)
		if buf.Len() != 0 {
			t.Errorf("stderr = %q, want silence for an explicit setting", buf.String())
		}
	})
	t.Run("document without the entry is silent", func(t *testing.T) {
		useDoc(t, &inputdoc.Document{})
		var buf bytes.Buffer
		warnBoxAccessDefault(minimalValidConfig(), &buf)
		if buf.Len() != 0 {
			t.Errorf("stderr = %q, want silence", buf.String())
		}
	})
	t.Run("butler kind is silent", func(t *testing.T) {
		useDeprecatedDefaultDoc(t)
		c := minimalValidConfig()
		c.dispatchKind = dispatchkind.Butler
		var buf bytes.Buffer
		warnBoxAccessDefault(c, &buf)
		if buf.Len() != 0 {
			t.Errorf("stderr = %q, want silence for a forced read-only Box", buf.String())
		}
	})
}

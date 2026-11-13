package main

import (
	"bytes"
	"testing"

	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/inputdoc"
)

func configWithBoxAccess(access string) config {
	c := minimalValidConfig()
	c.BoxForgeAndIssueAccess = access
	return c
}

func TestWarnBoxAccessReadWrite(t *testing.T) {
	t.Run("read-write prints the notice on one line", func(t *testing.T) {
		var buf bytes.Buffer
		warnBoxAccessReadWrite(configWithBoxAccess("read-write"), &buf)
		if got, want := buf.String(), inputdoc.BoxAccessReadWriteNotice+"\n"; got != want {
			t.Errorf("stderr = %q, want %q", got, want)
		}
	})
	t.Run("read-write from the document warns", func(t *testing.T) {
		useDoc(t, &inputdoc.Document{Settings: map[string]string{"BOX_FORGE_AND_ISSUE_ACCESS": "read-write"}})
		var buf bytes.Buffer
		warnBoxAccessReadWrite(loadConfig(), &buf)
		if got, want := buf.String(), inputdoc.BoxAccessReadWriteNotice+"\n"; got != want {
			t.Errorf("stderr = %q, want %q", got, want)
		}
	})
	t.Run("read-write from the flag warns", func(t *testing.T) {
		setKnob(t, "BOX_FORGE_AND_ISSUE_ACCESS", "read-write")
		var buf bytes.Buffer
		warnBoxAccessReadWrite(loadConfig(), &buf)
		if got, want := buf.String(), inputdoc.BoxAccessReadWriteNotice+"\n"; got != want {
			t.Errorf("stderr = %q, want %q", got, want)
		}
	})
	t.Run("read-only is silent", func(t *testing.T) {
		var buf bytes.Buffer
		warnBoxAccessReadWrite(configWithBoxAccess("read-only"), &buf)
		if buf.Len() != 0 {
			t.Errorf("stderr = %q, want silence", buf.String())
		}
	})
	t.Run("butler kind is silent", func(t *testing.T) {
		c := configWithBoxAccess("read-write")
		c.dispatchKind = dispatchkind.Butler
		var buf bytes.Buffer
		warnBoxAccessReadWrite(c, &buf)
		if buf.Len() != 0 {
			t.Errorf("stderr = %q, want silence for a forced read-only Box", buf.String())
		}
	})
	t.Run("a daemon-spawned child is silent", func(t *testing.T) {
		t.Setenv(inputdoc.BoxAccessNoticeDecidedEnv, "1")
		var buf bytes.Buffer
		warnBoxAccessReadWrite(configWithBoxAccess("read-write"), &buf)
		if buf.Len() != 0 {
			t.Errorf("stderr = %q, want silence: the daemon already decided", buf.String())
		}
	})
}

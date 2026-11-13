package bindregistry

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/ecosystemtest/synthetic"
)

var spellingsRewrite = []HostRewrite{{UpstreamHost: "upstream.example", LocalURL: "http://127.0.0.1:27182"}}

const spellingsOriginal = "registry = \"https://upstream.example/index/\"\n"

func TestInTreeBindings_IncludesDiscoveryConfigPathsStrategy(t *testing.T) {
	spellings := synthetic.Spellings("spellings", "first.cfg", "second.cfg")

	got := InTreeBindings([]ecosystem.Strategy{spellings, synthetic.Strategy})

	if len(got) != 2 || got[0].Name != "spellings" || got[1].Name != synthetic.Strategy.Name {
		t.Errorf("InTreeBindings = %+v, want both rows in order", got)
	}
}

func TestBoundInTreeRow(t *testing.T) {
	spellings := synthetic.Spellings("spellings", "first.cfg", "second.cfg", "third.cfg")

	t.Run("first existing spelling", func(t *testing.T) {
		dir := t.TempDir()
		writeConfig(t, dir, "second.cfg", "x\n", false)
		writeConfig(t, dir, "third.cfg", "x\n", false)
		if got := BoundInTreeRow(dir, spellings).InTreeConfigPath; got != "second.cfg" {
			t.Errorf("InTreeConfigPath = %q, want second.cfg", got)
		}
	})
	t.Run("none exists falls back to first spelling", func(t *testing.T) {
		if got := BoundInTreeRow(t.TempDir(), spellings).InTreeConfigPath; got != "first.cfg" {
			t.Errorf("InTreeConfigPath = %q, want first.cfg", got)
		}
	})
	t.Run("fixed path unchanged", func(t *testing.T) {
		dir := t.TempDir()
		writeConfig(t, dir, "first.cfg", "x\n", false)
		got := BoundInTreeRow(dir, synthetic.Strategy)
		if got.InTreeConfigPath != synthetic.Strategy.InTreeConfigPath {
			t.Errorf("InTreeConfigPath = %q, want %q", got.InTreeConfigPath, synthetic.Strategy.InTreeConfigPath)
		}
	})
	t.Run("does not mutate the shared row", func(t *testing.T) {
		BoundInTreeRow(t.TempDir(), spellings)
		if spellings.InTreeConfigPath != "" {
			t.Errorf("input row InTreeConfigPath = %q, want empty", spellings.InTreeConfigPath)
		}
	})
}

func TestApplyInTreeBinding_RewritesTheSpellingThatExists(t *testing.T) {
	dir := newTestRepo(t)
	spellings := synthetic.Spellings("spellings", "first.cfg", "second.cfg")
	writeConfig(t, dir, "second.cfg", spellingsOriginal, true)

	row := BoundInTreeRow(dir, spellings)
	outcome, err := ApplyInTreeBinding(dir, row, spellingsRewrite)
	if err != nil {
		t.Fatalf("ApplyInTreeBinding: %v", err)
	}
	if outcome != ApplyApplied {
		t.Fatalf("outcome = %v, want %v", outcome, ApplyApplied)
	}
	got, err := os.ReadFile(filepath.Join(dir, "second.cfg"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "upstream.example") {
		t.Errorf("second.cfg still names the upstream host: %s", got)
	}
}

func TestRevertInTreeBindings_CoversEverySpelling(t *testing.T) {
	dir := newTestRepo(t)
	spellings := synthetic.Spellings("spellings", "first.cfg", "second.cfg", "third.cfg")
	writeConfig(t, dir, "second.cfg", spellingsOriginal, true)

	if _, err := ApplyInTreeBinding(dir, BoundInTreeRow(dir, spellings), spellingsRewrite); err != nil {
		t.Fatalf("ApplyInTreeBinding: %v", err)
	}

	// An agent creating an earlier spelling mid-run must not strand second.cfg.
	writeConfig(t, dir, "first.cfg", "x\n", false)

	var out bytes.Buffer
	if failed := RevertInTreeBindings(dir, []ecosystem.Strategy{spellings}, "who", &out); failed {
		t.Fatalf("RevertInTreeBindings failed: %s", out.String())
	}
	got, err := os.ReadFile(filepath.Join(dir, "second.cfg"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != spellingsOriginal {
		t.Errorf("second.cfg = %q, want %q", got, spellingsOriginal)
	}
	if skipWorktreeSet(t, dir, "second.cfg") {
		t.Error("skip-worktree bit still set after revert")
	}
	if want := "in-tree spellings config second.cfg restored"; !strings.Contains(out.String(), want) {
		t.Errorf("output = %q, want it to contain %q", out.String(), want)
	}
	if strings.Contains(out.String(), "first.cfg") || strings.Contains(out.String(), "third.cfg") {
		t.Errorf("output names an absent spelling: %q", out.String())
	}
}

func TestBoundInTreeRow_SkipsADirectorySpelling(t *testing.T) {
	spellings := synthetic.Spellings("spellings", "first.cfg", "second.cfg")
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "first.cfg"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, dir, "second.cfg", "x\n", false)
	if got := BoundInTreeRow(dir, spellings).InTreeConfigPath; got != "second.cfg" {
		t.Errorf("InTreeConfigPath = %q, want second.cfg", got)
	}
}

// On a case-insensitive filesystem os.Stat("first.cfg") finds First.cfg, so
// only an exact-name match keeps the binding on the tracked spelling.
func TestBoundInTreeRow_IgnoresACaseVariant(t *testing.T) {
	spellings := synthetic.Spellings("spellings", "first.cfg", "First.cfg")
	dir := t.TempDir()
	writeConfig(t, dir, "First.cfg", "x\n", false)
	if got := BoundInTreeRow(dir, spellings).InTreeConfigPath; got != "First.cfg" {
		t.Errorf("InTreeConfigPath = %q, want First.cfg", got)
	}
}

func TestApplyInTreeBinding_RejectsAnUnboundRow(t *testing.T) {
	dir := newTestRepo(t)
	spellings := synthetic.Spellings("spellings", "first.cfg")
	writeConfig(t, dir, "first.cfg", spellingsOriginal, true)

	_, err := ApplyInTreeBinding(dir, spellings, spellingsRewrite)
	if err == nil || !strings.Contains(err.Error(), "BoundInTreeRow") {
		t.Fatalf("err = %v, want one pointing at BoundInTreeRow", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "first.cfg")); string(got) != spellingsOriginal {
		t.Errorf("first.cfg changed: %q", got)
	}
}

func TestRevertInTreeBindings_LeavesAnUnboundSpellingsEdits(t *testing.T) {
	dir := newTestRepo(t)
	spellings := synthetic.Spellings("spellings", "first.cfg", "second.cfg")
	writeConfig(t, dir, "first.cfg", spellingsOriginal, true)
	writeConfig(t, dir, "second.cfg", spellingsOriginal, true)

	if _, err := ApplyInTreeBinding(dir, BoundInTreeRow(dir, spellings), spellingsRewrite); err != nil {
		t.Fatalf("ApplyInTreeBinding: %v", err)
	}
	const agentEdit = "agent edit\n"
	if err := os.WriteFile(filepath.Join(dir, "second.cfg"), []byte(agentEdit), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if failed := RevertInTreeBindings(dir, []ecosystem.Strategy{spellings}, "who", &out); failed {
		t.Fatalf("RevertInTreeBindings failed: %s", out.String())
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "first.cfg")); string(got) != spellingsOriginal {
		t.Errorf("first.cfg = %q, want %q", got, spellingsOriginal)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "second.cfg")); string(got) != agentEdit {
		t.Errorf("second.cfg = %q, want the agent's edit kept", got)
	}
}

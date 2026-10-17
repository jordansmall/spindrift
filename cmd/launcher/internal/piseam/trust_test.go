//go:build integration

package piseam

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// systemPrompt returns the system message of a chat-completions request; pi
// sends it as role "developer" for a reasoning model.
func systemPrompt(req map[string]any) string {
	msgs, _ := req["messages"].([]any)
	for _, m := range msgs {
		if mm, _ := m.(map[string]any); mm["role"] == "system" || mm["role"] == "developer" {
			return fmt.Sprint(mm["content"])
		}
	}
	return ""
}

// seedFiles returns a seedWork func writing the given work-relative files.
func seedFiles(t *testing.T, files map[string]string) func(work string) {
	return func(work string) {
		t.Helper()
		for path, body := range files {
			full := filepath.Join(work, path)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestPiNoApproveProjectTrust(t *testing.T) {
	const (
		projectMarker = "PROJECT-APPEND-MARKER"
		extMarkerFile = "PROJECT-EXTENSION-LOADED"
	)
	project := map[string]string{
		".pi/APPEND_SYSTEM.md":  projectMarker,
		".pi/settings.json":     `{"defaultThinkingLevel":"high"}`,
		".pi/skills/s/SKILL.md": "---\nname: s\ndescription: project-skill-marker\n---\nbody",
		// Loading the extension has a side effect, so a trusted run proves pi
		// discovers it and an untrusted one proves it never ran.
		".pi/extensions/marker.ts": `import { writeFileSync } from "node:fs";
export default function () {
	writeFileSync("` + extMarkerFile + `", "loaded");
}
`,
	}
	// No --thinking: the level must come from settings, so only trust decides it.
	opts := piOpts{args: []string{"--model", stubModelRef}, reasoning: true}

	t.Run("no-approve ignores project resources", func(t *testing.T) {
		s := newStub(t, textReply("ok"))
		dirs := newPiDirs(t)
		o := opts
		o.dirs = &dirs
		r := runPiWith(t, s, o, "hi", seedFiles(t, project))
		if r.err != nil || r.stderr != "" || len(s.Requests()) != 1 {
			t.Fatalf("pi exit = %v, stderr = %q, %d requests, want a clean single-request run", r.err, r.stderr, len(s.Requests()))
		}
		req := s.Requests()[0]
		sys := systemPrompt(req)
		if sys == "" {
			t.Fatal("request has no system message, so the absences below prove nothing")
		}
		for _, absent := range []string{projectMarker, "project-skill-marker"} {
			if strings.Contains(sys, absent) {
				t.Errorf("system prompt contains project-local %q despite --no-approve", absent)
			}
		}
		// Pi's default with no settings; the project's "high" is ignored.
		if got := req["reasoning_effort"]; got != "medium" {
			t.Errorf("reasoning_effort = %v, want medium (project settings ignored)", got)
		}
		if _, err := os.Stat(filepath.Join(dirs.work, extMarkerFile)); err == nil {
			t.Error("project extension ran despite --no-approve")
		}
	})

	// Control: the same fixture is seen once trusted, so the absences above
	// are the flag's doing and not a fixture that pi never reads.
	t.Run("approve loads project resources", func(t *testing.T) {
		s := newStub(t, textReply("ok"))
		dirs := newPiDirs(t)
		o := opts
		o.approve = true
		o.dirs = &dirs
		r := runPiWith(t, s, o, "hi", seedFiles(t, project))
		if r.err != nil || len(s.Requests()) != 1 {
			t.Fatalf("pi exit = %v, stderr = %q, %d requests", r.err, r.stderr, len(s.Requests()))
		}
		req := s.Requests()[0]
		sys := systemPrompt(req)
		if !strings.Contains(sys, projectMarker) || !strings.Contains(sys, "project-skill-marker") {
			t.Errorf("trusted run lacks project resources:\n%s", sys)
		}
		if got := req["reasoning_effort"]; got != "high" {
			t.Errorf("reasoning_effort = %v, want high (from project settings)", got)
		}
		if _, err := os.Stat(filepath.Join(dirs.work, extMarkerFile)); err != nil {
			t.Errorf("trusted run did not load the project extension: %v", err)
		}
	})

	// Context files need no trust, so --no-approve still reads them.
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		t.Run("no-approve reads "+name, func(t *testing.T) {
			s := newStub(t, textReply("ok"))
			marker := "CONTEXT-MARKER-" + name
			r := runPiWith(t, s, piOpts{args: opts.args}, "hi", seedFiles(t, map[string]string{name: marker}))
			if r.err != nil || len(s.Requests()) != 1 {
				t.Fatalf("pi exit = %v, stderr = %q, %d requests", r.err, r.stderr, len(s.Requests()))
			}
			if sys := systemPrompt(s.Requests()[0]); !strings.Contains(sys, marker) {
				t.Errorf("system prompt lacks %s content %q", name, marker)
			}
		})
	}
}

func TestPiAgentDirSkillsNeedNoTrust(t *testing.T) {
	s := newStub(t, textReply("ok"))
	dirs := newPiDirs(t)
	skill := filepath.Join(dirs.agentDir, "skills", "agent-skill", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skill), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: agent-skill\ndescription: agent-dir-skill-marker\n---\nbody"
	if err := os.WriteFile(skill, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	r := runPiWith(t, s, piOpts{reasoning: true, args: []string{"--model", stubModelRef}, dirs: &dirs}, "hi")
	if r.err != nil || len(s.Requests()) != 1 {
		t.Fatalf("pi exit = %v, stderr = %q, %d requests", r.err, r.stderr, len(s.Requests()))
	}
	if sys := systemPrompt(s.Requests()[0]); !strings.Contains(sys, "agent-dir-skill-marker") {
		t.Errorf("--no-approve system prompt lacks the agent-dir skill:\n%s", sys)
	}
}

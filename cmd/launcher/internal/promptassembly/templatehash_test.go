package promptassembly

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

// varyDispatchFacts changes every per-Dispatch fact an Env carries and leaves
// the harness setup alone.
func varyDispatchFacts(e Env) Env {
	e.IssueNumber = "9001"
	e.IssueTitle = "A wholly different issue"
	e.IssueText = "different body text"
	e.Branch = "agent/issue-9001"
	e.DispatchKey = "work-9001"
	e.RunNonce = "other-nonce"
	if e.CIFailureSummary != "" {
		e.CIFailureSummary = "another failure summary"
	}
	return e
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func mustTemplateHashes(t *testing.T, e Env, reg Registry) map[string]string {
	t.Helper()
	h, err := TemplateHashes(e, reg)
	if err != nil {
		t.Fatalf("TemplateHashes: %v", err)
	}
	return h
}

func TestTemplateHashesIgnorePerDispatchFacts(t *testing.T) {
	reg := loadTestRegistry(t)

	fresh := coveredEnv()
	fresh.IssueText = "original body text"

	fixPass := coveredEnv()
	fixPass.FixPass = 1
	fixPass.CIFailureSummary = "original failure summary"

	cases := []struct {
		name  string
		env   Env
		roles []string
	}{
		{"fresh work", fresh, []string{"delta-review", "fix", "implement", "land", "review"}},
		{"fresh work without issue text", coveredEnv(), []string{"delta-review", "fix", "implement", "land", "review"}},
		{"fix pass", fixPass, []string{"legacy"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := mustTemplateHashes(t, tc.env, reg)
			b := mustTemplateHashes(t, varyDispatchFacts(tc.env), reg)
			if got := sortedKeys(a); !reflect.DeepEqual(got, tc.roles) {
				t.Errorf("roles = %v, want %v", got, tc.roles)
			}
			if !reflect.DeepEqual(a, b) {
				t.Errorf("hashes differ across Dispatches:\n a=%v\n b=%v", a, b)
			}
		})
	}

	// Without this the equal hashes above could be vacuous: the real prompt
	// does carry the per-Dispatch facts.
	r1, err := Assemble(fresh, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	r2, err := Assemble(varyDispatchFacts(fresh), reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if !strings.Contains(r1.Prompt, fresh.IssueTitle) {
		t.Errorf("assembled prompt lacks the issue title %q", fresh.IssueTitle)
	}
	if r1.Prompt == r2.Prompt {
		t.Error("assembled prompts equal across Dispatches; per-Dispatch facts not exercised")
	}
}

func TestTemplateHashesButlerIgnoreChoreFacts(t *testing.T) {
	reg := loadTestRegistry(t)
	a := butlerEnv()
	a.ChorePatchClasses = "dead-code" // presence gates the patch section, so keep it set on both sides
	b := a
	b.ChoreHead = "0123abcd"
	b.ChoreDiffRange = "aaaa..bbbb"
	b.ChoreSlice = "other/file.go"
	b.ChoreClasses = "typo"
	b.ChorePatchClasses = "typo"
	b.ChoreMaxFindings = "9"
	b.DispatchKey = "butler-other"
	b.RunNonce = "other-nonce"

	ha := mustTemplateHashes(t, a, reg)
	if got, want := sortedKeys(ha), []string{"butler"}; !reflect.DeepEqual(got, want) {
		t.Errorf("roles = %v, want %v", got, want)
	}
	if hb := mustTemplateHashes(t, b, reg); !reflect.DeepEqual(ha, hb) {
		t.Errorf("hashes differ across chore Dispatches:\n a=%v\n b=%v", ha, hb)
	}

	// CHORE_NAME selects the chore prompt: it is setup, so it must move the hash.
	c := a
	c.ChoreName = "refactor"
	if hc := mustTemplateHashes(t, c, reg); reflect.DeepEqual(ha, hc) {
		t.Error("hash unchanged across different chores")
	}
}

func TestTemplateHashesTrackSetup(t *testing.T) {
	reg := loadTestRegistry(t)
	off := coveredEnv()
	on := off
	on.FilerEnabled = true

	ha := mustTemplateHashes(t, off, reg)
	hb := mustTemplateHashes(t, on, reg)
	if ha["implement"] == hb["implement"] {
		t.Error("implement hash unchanged after enabling the Filer section")
	}
}

func TestPerDispatchVarsAreDefined(t *testing.T) {
	reg := loadTestRegistry(t)
	e := butlerEnv()
	e.CIFailureSummary = "x"
	bodies, err := assemblePromptBodies(e, reg)
	if err != nil {
		t.Fatalf("assemblePromptBodies: %v", err)
	}
	for _, name := range perDispatchVars {
		if _, ok := bodies.vars[name]; !ok {
			t.Errorf("perDispatchVars names %q, which assemblePromptBodies does not define", name)
		}
	}
}

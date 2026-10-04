package promptassembly

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

const (
	agentsPromptFilesAll = `{"scout":"scout-prompt.md","reviewer":"review-prompt.md","filer":"filer-prompt.md","worker":"worker-prompt.md"}`
	rosterScoutOnly      = `{"scout":{"description":"fixture scout description","model":"opus","prompt":"","tools":["Read","Bash","WebFetch","WebSearch","Glob","Grep"]}}`
)

// The nix-baked agents JSON template is the roster; Assemble forwards its
// members, filling each prompt from the agent's prompt file, and drops the
// reviewer because the code-owned review pass replaced it (issue #2037).
func TestAssembleAgentsJSONRosterMembership(t *testing.T) {
	reg := loadTestRegistry(t)

	cases := []struct {
		name      string
		template  string
		promptMap string
		mutate    func(*Env)
		prompts   func(t *testing.T) string
		want      []string
		wantNot   []string
		model     map[string]string
	}{
		{
			name:    "template unset yields no agents JSON",
			wantNot: []string{"scout", "reviewer", "filer", "worker"},
		},
		{
			name:     "scout alone",
			template: rosterScoutOnly,
			want:     []string{"scout"},
			wantNot:  []string{"reviewer", "filer", "worker"},
		},
		{
			name:     "model fields come from the template",
			template: `{"reviewer":{"description":"reviewer","model":"claude-opus-4-5","prompt":"","tools":["Read","Bash","WebFetch"]},"scout":{"description":"scout","model":"claude-haiku-3-5","prompt":"","tools":["Read","Bash","WebFetch","WebSearch","Glob","Grep"]}}`,
			want:     []string{"scout"},
			wantNot:  []string{"reviewer"},
			model:    map[string]string{"scout": "claude-haiku-3-5"},
		},
		{
			name:     "filer alone",
			template: `{"filer":{"description":"fixture filer's description","model":"haiku","prompt":"","tools":["Read","Bash","WebFetch"]}}`,
			mutate:   func(e *Env) { e.FilerEnabled = true },
			want:     []string{"filer"},
			wantNot:  []string{"scout", "reviewer", "worker"},
		},
		{
			name:     "reviewer dropped even when the template carries it",
			template: `{"reviewer":{"description":"fixture reviewer description","model":"haiku","prompt":"","tools":["Read","Bash","WebFetch"]},"scout":{"description":"fixture scout description","model":"opus","prompt":"","tools":["Read","Bash","WebFetch","WebSearch","Glob","Grep"]}}`,
			want:     []string{"scout"},
			wantNot:  []string{"reviewer"},
		},
		{
			name:     "scout and filer, reviewer dropped",
			template: `{"scout":{"description":"scout","model":"opus","prompt":"","tools":["Read"]},"reviewer":{"description":"reviewer","model":"opus","prompt":"","tools":["Read"]},"filer":{"description":"filer","model":"haiku","prompt":"","tools":["Read"]}}`,
			mutate:   func(e *Env) { e.FilerEnabled = true },
			want:     []string{"scout", "filer"},
			wantNot:  []string{"reviewer"},
		},
		{
			name:     "worker alone",
			template: `{"worker":{"description":"fixture worker description","model":"sonnet","prompt":"","tools":["Read","Bash","Edit","Write","Glob","Grep"]}}`,
			mutate:   func(e *Env) { e.WorkerProvisioned = true },
			want:     []string{"worker"},
			wantNot:  []string{"scout", "reviewer", "filer"},
		},
		{
			// No per-name branch: any agent named in the prompt-file map gets
			// its prompt injected (issue #264).
			name:      "a custom agent's prompt is injected generically",
			template:  `{"scout":{"description":"scout","model":"opus","prompt":"","tools":["Read"]},"auditor":{"description":"audit","model":"haiku","prompt":"","tools":["Read"]}}`,
			promptMap: `{"scout":"scout-prompt.md","auditor":"auditor-prompt.md"}`,
			prompts: func(t *testing.T) string {
				dir, _ := promptsDirExceptSubdir(t, "no-such-entry")
				if err := os.WriteFile(filepath.Join(dir, "auditor-prompt.md"), []byte("audit stub\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				return dir
			},
			want:    []string{"scout", "auditor"},
			wantNot: []string{"reviewer"},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			env := coveredEnv()
			env.AgentsJSONTemplate = tc.template
			env.AgentsPromptFiles = agentsPromptFilesAll
			if tc.promptMap != "" {
				env.AgentsPromptFiles = tc.promptMap
			}
			if tc.prompts != nil {
				env.PromptsDir = tc.prompts(t)
			}
			if tc.mutate != nil {
				tc.mutate(&env)
			}

			result, err := Assemble(env, reg)
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}
			if tc.template == "" {
				if result.AgentsJSON != "" {
					t.Fatalf("AgentsJSON = %q, want empty so no --agents flag is passed", result.AgentsJSON)
				}
				return
			}
			var agents map[string]struct {
				Model  string `json:"model"`
				Prompt string `json:"prompt"`
			}
			if err := json.Unmarshal([]byte(result.AgentsJSON), &agents); err != nil {
				t.Fatalf("unmarshal AgentsJSON: %v\n%s", err, result.AgentsJSON)
			}
			for _, name := range tc.want {
				entry, ok := agents[name]
				if !ok {
					t.Errorf("AgentsJSON lacks %q: %s", name, result.AgentsJSON)
				} else if entry.Prompt == "" {
					t.Errorf("%s.prompt is empty, want the agent's prompt file injected", name)
				}
			}
			for _, name := range tc.wantNot {
				if _, ok := agents[name]; ok {
					t.Errorf("AgentsJSON has %q, want it absent: %s", name, result.AgentsJSON)
				}
			}
			for name, model := range tc.model {
				if agents[name].Model != model {
					t.Errorf("%s.model = %q, want %q", name, agents[name].Model, model)
				}
			}
		})
	}
}

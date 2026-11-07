package promptassembly

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestReviewFanoutAgentFollowsProvisionedAgents covers issue #3447's second
// round: the baked /code-review anchor must name an agent type the run
// actually provisions. A Consumer pinning the historical four-entry roster,
// or taking issue #392's reviewModel="" opt-out, gets the skill's own
// general-purpose default instead of an agent type its session never defines.
func TestReviewFanoutAgentFollowsProvisionedAgents(t *testing.T) {
	reg, env0 := syntheticRegistry(t, []FragmentRow{
		{Gate: "CODE_REVIEW_BAKED", Fragment: "fanout.md", Var: "FANOUT_STEP", ExtraSubstVars: []string{"REVIEW_FANOUT_AGENT"}},
	}, map[string]string{
		"review-prompt.md":    "${FANOUT_STEP}\n",
		"fragments/fanout.md": "[${REVIEW_FANOUT_AGENT}]",
	})

	// The table covers both driver mechanisms: the --agents JSON driver
	// (claude) provisions by template key, and the agent-files driver
	// (opencode) by an on-disk <name>.md.
	cases := []struct {
		name string
		env  func(t *testing.T, base Env) Env
		want string
	}{
		{
			name: "agents-json-provisions-review-axis",
			env: func(t *testing.T, base Env) Env {
				env := base
				env.AgentsJSONTemplate = `{"scout":{"model":"x"},"review-axis":{"model":"y"}}`
				return env
			},
			want: "[review-axis]",
		},
		{
			name: "agents-json-omits-review-axis",
			env: func(t *testing.T, base Env) Env {
				env := base
				env.AgentsJSONTemplate = `{"scout":{"model":"x"},"worker":{"model":"y"}}`
				return env
			},
			want: "[general-purpose]",
		},
		{
			name: "agent-files-provision-review-axis",
			env: func(t *testing.T, base Env) Env {
				env := base
				dir := t.TempDir()
				writeAgentFile(t, filepath.Join(dir, "review-axis.md"), "review-axis")
				env.DriverAgentFilesDir = dir
				return env
			},
			want: "[review-axis]",
		},
		{
			name: "agent-files-omit-review-axis",
			env: func(t *testing.T, base Env) Env {
				env := base
				dir := t.TempDir()
				writeAgentFile(t, filepath.Join(dir, "scout.md"), "scout")
				env.DriverAgentFilesDir = dir
				return env
			},
			want: "[general-purpose]",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := Assemble(tc.env(t, env0), reg)
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}
			prompt := result.ReviewPromptText
			if prompt == "" {
				t.Fatal("ReviewPromptText is empty, want the rendered review prompt")
			}
			if !strings.Contains(prompt, tc.want) {
				t.Errorf("ReviewPromptText missing %q:\n%s", tc.want, prompt)
			}
			if strings.Contains(prompt, "${REVIEW_FANOUT_AGENT}") {
				t.Errorf("ReviewPromptText carries an unsubstituted ${REVIEW_FANOUT_AGENT} token:\n%s", prompt)
			}
		})
	}
}

// TestReviewFanoutAgentMalformedAgentsJSON covers the resolver's
// json.Unmarshal error path, which the Assemble-level table above cannot
// reach: Assemble rejects a malformed template with an error before any
// fragment renders. An unparseable template says nothing about what the run
// provisions, so the resolver must fall through to the fallback.
func TestReviewFanoutAgentMalformedAgentsJSON(t *testing.T) {
	cases := []struct {
		name     string
		template string
	}{
		{name: "truncated-object", template: `{"scout":{"model":"x"},"review-axis":{"model":"y"`},
		{name: "trailing-garbage", template: `{"review-axis":{"model":"y"}} oops`},
		{name: "not-an-object", template: `["review-axis"]`},
		{name: "bare-garbage", template: `review-axis`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := coveredEnv()
			env.AgentsJSONTemplate = tc.template
			if got := reviewFanoutAgentFor(env); got != reviewFanoutFallbackAgent {
				t.Errorf("reviewFanoutAgentFor(%s) = %q, want %q", tc.template, got, reviewFanoutFallbackAgent)
			}
		})
	}
}

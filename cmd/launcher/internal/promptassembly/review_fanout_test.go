package promptassembly

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReviewFanoutAgentFollowsProvisionedAgents covers issue #3447's second
// round: the baked /code-review anchor names an agent type the run actually
// provisions. A Consumer pinning an explicit roster of the historical four
// entries -- or taking issue #392's reviewModel="" opt-out, which drops the
// review-axis entry from the baked agents -- must get the skill's own
// general-purpose default in the rendered line, not an order to spawn an
// agent type its driver session never defines.
func TestReviewFanoutAgentFollowsProvisionedAgents(t *testing.T) {
	reg := loadTestRegistry(t)

	// The rendered clause per driver mechanism: the --agents JSON driver
	// (claude) provisions by template key, the agent-files driver (opencode)
	// by an on-disk <name>.md.
	cases := []struct {
		name string
		env  func(t *testing.T) Env
		want string
	}{
		{
			name: "agents-json-provisions-review-axis",
			env: func(t *testing.T) Env {
				env := orchestratorReviewEnv()
				env.AgentsJSONTemplate = `{"scout":{"model":"x"},"review-axis":{"model":"y"}}`
				return env
			},
			want: "agent type `review-axis`",
		},
		{
			name: "agents-json-omits-review-axis",
			env: func(t *testing.T) Env {
				env := orchestratorReviewEnv()
				env.AgentsJSONTemplate = `{"scout":{"model":"x"},"worker":{"model":"y"}}`
				return env
			},
			want: "agent type `general-purpose`",
		},
		{
			name: "agent-files-provision-review-axis",
			env: func(t *testing.T) Env {
				env := orchestratorReviewEnv()
				dir := t.TempDir()
				writeAgentFile(t, filepath.Join(dir, "review-axis.md"), "review-axis")
				env.DriverAgentFilesDir = dir
				return env
			},
			want: "agent type `review-axis`",
		},
		{
			name: "agent-files-omit-review-axis",
			env: func(t *testing.T) Env {
				env := orchestratorReviewEnv()
				dir := t.TempDir()
				writeAgentFile(t, filepath.Join(dir, "scout.md"), "scout")
				env.DriverAgentFilesDir = dir
				return env
			},
			want: "agent type `general-purpose`",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := Assemble(tc.env(t), reg)
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

// TestReviewFanoutAgentBakedAnchorHasNoHardcodedName pins the fragment
// itself: the anchor must interpolate the substitution variable rather than
// bake either name, since a baked literal is exactly the drift that made a
// non-provisioned agent type reachable.
func TestReviewFanoutAgentBakedAnchorHasNoHardcodedName(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(promptsDir, "fragments", "code-review-baked.md"))
	if err != nil {
		t.Fatalf("read code-review-baked.md: %v", err)
	}
	text := string(b)
	if !strings.Contains(text, "agent type `${REVIEW_FANOUT_AGENT}`") {
		t.Errorf("code-review-baked.md does not spawn as agent type `${REVIEW_FANOUT_AGENT}`:\n%s", text)
	}
	if strings.Contains(text, "agent type `"+reviewFanoutAgent+"`") ||
		strings.Contains(text, "agent type `"+reviewFanoutFallbackAgent+"`") {
		t.Errorf("code-review-baked.md hardcodes an agent-type name:\n%s", text)
	}
}

// orchestratorReviewEnv is coveredEnv in the one cell that renders
// review-prompt.md into Result.ReviewPromptText.
func orchestratorReviewEnv() Env {
	env := coveredEnv()
	env.OrchestratorEnabled = true
	env.ReviewLoopInline = false
	return env
}

// TestReviewFanoutAgentMalformedAgentsJSON covers the resolver's
// json.Unmarshal error path, which the Assemble-level table above cannot
// reach: Assemble's own agents-JSON path rejects a malformed template with
// an error before any fragment renders. An unparseable template tells the
// resolver nothing about what the run provisions, so it must fall through to
// the fallback rather than name an agent type the session may never define.
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
			env := orchestratorReviewEnv()
			env.AgentsJSONTemplate = tc.template
			if got := reviewFanoutAgentFor(env); got != reviewFanoutFallbackAgent {
				t.Errorf("reviewFanoutAgentFor(%s) = %q, want %q", tc.template, got, reviewFanoutFallbackAgent)
			}
		})
	}
}

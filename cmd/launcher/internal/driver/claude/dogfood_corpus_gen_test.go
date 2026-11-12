package claude_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/golden"
)

// dogfoodSpec describes one synthetic pass: body lines sized by a seeded RNG,
// then the single oversized tool_result lines (the tail), then the result.
type dogfoodSpec struct {
	name     string
	seed     uint64
	turns    int   // assistant/user pairs in the body
	meanSize int   // mean bytes of a body tool_result line
	tail     []int // sizes in bytes of the oversized tool_result lines
}

var dogfoodSpecs = []dogfoodSpec{
	{"large-pass", 6849, 250, 2600, []int{100_000, 200_000, 400_000, 800_000}},
	{"small-pass", 1, 60, 3000, []int{120_000}},
}

// Source-like fragments stand in for tool output; none is real log content.
var dogfoodFragments = []string{
	"func (s *Server) handle(w http.ResponseWriter, r *http.Request) {\n\tctx := r.Context()\n\tif err := s.store.Load(ctx, r.URL.Query().Get(\"id\")); err != nil {\n\t\thttp.Error(w, err.Error(), http.StatusInternalServerError)\n\t\treturn\n\t}\n}\n",
	"--- a/internal/queue/queue.go\n+++ b/internal/queue/queue.go\n@@ -41,7 +41,9 @@ func (q *Queue) Push(item Item) error {\n \tq.mu.Lock()\n \tdefer q.mu.Unlock()\n-\tq.items = append(q.items, item)\n+\tif len(q.items) >= q.cap {\n+\t\treturn ErrFull\n+\t}\n+\tq.items = append(q.items, item)\n \treturn nil\n }\n",
	"ok  \tspindrift.dev/launcher/internal/queue\t0.412s\nok  \tspindrift.dev/launcher/internal/store\t1.093s\n?   \tspindrift.dev/launcher/internal/tools\t[no test files]\n",
	"internal/queue/queue.go:57:2: declared and not used: pending\n\tpending := make(map[string]Item, len(q.items))\n\t^\n",
	"# Configuration\n\nThe `retry` block controls backoff. Set `max_attempts` to bound the loop and `base_delay` to the first wait; later waits double up to `max_delay`.\n\n",
	"  {\"id\": 4821, \"title\": \"Flaky retry test under load\", \"state\": \"open\", \"labels\": [\"bug\", \"ready-for-agent\"], \"comments\": 3},\n",
	"drwxr-xr-x 4 agent agent 4096 Oct 13 05:24 internal\n-rw-r--r-- 1 agent agent 1873 Oct 13 05:24 go.mod\n-rw-r--r-- 1 agent agent 9210 Oct 13 05:24 main.go\n",
}

// dogfoodPad returns about n bytes of source-like text.
func dogfoodPad(rng *rand.Rand, n int) string {
	var b strings.Builder
	for b.Len() < n {
		b.WriteString(dogfoodFragments[rng.IntN(len(dogfoodFragments))])
	}
	return b.String()[:n]
}

func dogfoodMarshal(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		panic(err)
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

func dogfoodID(rng *rand.Rand, prefix string) string {
	return fmt.Sprintf("%s%016x%08x", prefix, rng.Uint64(), rng.Uint32())
}

// dogfoodPass renders one pass log. The event shapes follow the real-* corpus
// cases; only the bulky fields (assistant text, tool_use input, tool_result
// content) are padded.
func dogfoodPass(spec dogfoodSpec) []byte {
	rng := rand.New(rand.NewPCG(spec.seed, spec.seed^0x9e3779b97f4a7c15))
	session := fmt.Sprintf("%08x-%04x-4%03x-a%03x-%012x", rng.Uint32(), rng.Uint32()&0xffff, rng.Uint32()&0xfff, rng.Uint32()&0xfff, rng.Uint64()&0xffffffffffff)
	const model = "claude-opus-5-5"
	var lines []string
	lines = append(lines, dogfoodMarshal(map[string]any{
		"type": "system", "subtype": "init", "cwd": "/work", "session_id": session,
		"tools": []string{"Task", "Bash", "Edit", "Read", "Write"}, "mcp_servers": []string{},
		"model": model, "permissionMode": "bypassPermissions",
		"uuid": dogfoodID(rng, ""),
	}))
	lines = append(lines, dogfoodMarshal(map[string]any{
		"type": "rate_limit_event", "uuid": dogfoodID(rng, ""), "session_id": session,
		"rate_limit_info": map[string]any{
			"status": "allowed", "resetsAt": 1791877800, "rateLimitType": "five_hour",
			"overageStatus": "rejected", "isUsingOverage": false,
		},
	}))

	toolPair := func(textLen, inputLen, resultLen int) {
		toolID := dogfoodID(rng, "toolu_")
		content := []any{map[string]any{"type": "text", "text": dogfoodPad(rng, textLen)}}
		content = append(content, map[string]any{
			"type": "tool_use", "id": toolID, "name": "Bash", "caller": map[string]any{"type": "direct"},
			"input": map[string]any{"command": "cat internal/queue/queue.go # " + dogfoodPad(rng, inputLen)},
		})
		lines = append(lines, dogfoodMarshal(map[string]any{
			"type": "assistant", "uuid": dogfoodID(rng, ""), "session_id": session, "parent_tool_use_id": nil,
			"message": map[string]any{
				"model": model, "id": dogfoodID(rng, "msg_"), "type": "message", "role": "assistant",
				"content": content, "stop_reason": nil,
				"usage": map[string]any{"input_tokens": 2, "cache_creation_input_tokens": 1753, "cache_read_input_tokens": 26990, "output_tokens": 8 + rng.IntN(400), "service_tier": "standard"},
			},
		}))
		out := dogfoodPad(rng, resultLen)
		lines = append(lines, dogfoodMarshal(map[string]any{
			"type": "user", "uuid": dogfoodID(rng, ""), "session_id": session, "parent_tool_use_id": nil,
			"message": map[string]any{"role": "user", "content": []any{map[string]any{
				"type": "tool_result", "tool_use_id": toolID, "content": out,
			}}},
			"tool_use_result": out,
		}))
	}
	// The line mean is dominated by the user line, which carries the result
	// twice (content and tool_use_result), as the CLI emits it.
	for range spec.turns {
		size := spec.meanSize/2 + rng.IntN(spec.meanSize)
		toolPair(40+rng.IntN(300), 20+rng.IntN(200), size/2)
	}
	for _, size := range spec.tail {
		toolPair(120, 60, size/2)
	}
	lines = append(lines, dogfoodMarshal(map[string]any{
		"type": "result", "subtype": "success", "is_error": false, "stop_reason": "end_turn",
		"session_id": session, "uuid": dogfoodID(rng, ""), "num_turns": spec.turns + len(spec.tail),
		"duration_ms": 86395, "duration_api_ms": 27138, "total_cost_usd": 0.2811972,
		"result": "SPINDRIFT_OUTCOME issue=6849 landing=none status=reject note=synthetic dogfood-sized pass",
		"usage": map[string]any{
			"input_tokens": 14, "cache_creation_input_tokens": 24278, "cache_read_input_tokens": 194186,
			"output_tokens": 2404, "service_tier": "standard",
		},
		"modelUsage": map[string]any{model: map[string]any{
			"inputTokens": 14, "outputTokens": 2404, "cacheReadInputTokens": 194186,
			"cacheCreationInputTokens": 24278, "costUSD": 0.2811972,
		}},
	}))
	return []byte(strings.Join(lines, "\n") + "\n")
}

// TestDogfoodCorpusRegenerates keeps testdata/dogfood byte-identical to its
// generator; UPDATE_GOLDENS=1 rewrites it.
func TestDogfoodCorpusRegenerates(t *testing.T) {
	for _, spec := range dogfoodSpecs {
		path := filepath.Join("testdata", "dogfood", spec.name, "pass.jsonl")
		if golden.Update() {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := golden.CompareOrUpdateText(path, dogfoodPass(spec), golden.Update()); err != nil {
			t.Errorf("%s: %v (regenerate with UPDATE_GOLDENS=1)", spec.name, err)
		}
	}
}

package main

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// edgeLines are the shapes where encoding/json's lenient decoding and the
// scanner port could diverge (#7107): repeated and case-variant keys, nulls,
// mistyped fields, malformed blocks and unusual escapes.
var edgeLines = []string{
	`{"type":"assistant","message":{"id":"m1","model":"claude-opus-4","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":3,"output_tokens":4,"cache_read_input_tokens":5,"cache_creation_input_tokens":6}}}`,
	`{"TYPE":"assistant","Message":{"ID":"m2","MODEL":"x","Content":[{"TYPE":"text","TEXT":"cased"}]}}`,
	`{"type":"assistant","type":"user","message":{"content":[{"type":"text","text":"a"}]},"message":{"id":"dup","content":[{"type":"tool_result","tool_use_id":"t1","content":"r"}]}}`,
	`{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]},"message":{"id":"m2"}}`,
	`{"type":"assistant","message":{"id":"a"},"message":{"model":"b","content":[{"type":"text","text":"merged"}]}}`,
	`{"type":"assistant","message":null,"timestamp":"2025-01-02T03:04:05.678Z"}`,
	`{"type":"assistant","message":{"usage":null,"content":null}}`,
	`{"type":"assistant","message":{"usage":{"cache_creation":{"ephemeral_5m_input_tokens":7,"ephemeral_1h_input_tokens":8},"input_tokens":1}}}`,
	`{"type":"assistant","message":{"usage":{"cache_creation":{"ephemeral_5m_input_tokens":7},"cache_creation":null,"cache_creation_input_tokens":9}}}`,
	`{"type":"assistant","message":{"usage":{"cache_creation":{"ephemeral_5m_input_tokens":7},"cache_creation":{"ephemeral_1h_input_tokens":2}}}}`,
	`{"type":"assistant","message":{"usage":{"input_tokens":1.5}}}`,
	`{"type":"assistant","message":{"usage":{"input_tokens":"1"},"id":"bad"}}`,
	`{"type":"assistant","message":{"usage":{"cache_creation":[1]}}}`,
	`{"type":"assistant","message":{"id":7,"content":[{"type":"text","text":"x"}]}}`,
	`{"type":"assistant","message":[1],"error":"boom"}`,
	`{"type":"assistant","message":"str"}`,
	`{"type":7,"message":{"content":[]}}`,
	`{"type":null,"message":{"content":[]}}`,
	`{"type":"assistant","parent_tool_use_id":5,"message":{"content":[]}}`,
	`{"type":"assistant","parent_tool_use_id":"nope","message":{"model":"m","content":[{"type":"text","text":"sub"}]}}`,
	`{"type":"assistant","message":{"model":"<synthetic>","content":[{"type":"text","text":"API Error: 529 overloaded"}]},"error":"rate_limit"}`,
	`{"type":"assistant","message":{"model":"<synthetic>","content":[{"type":"text","text":"x"}]},"error":""}`,
	`{"type":"assistant","message":{"model":"<synthetic>","content":[]},"error":{"a":1}}`,
	`{"type":"assistant","message":{"model":"<synthetic>","content":[]},"error":null,"error":false}`,
	`{"type":"assistant","message":{"model":null,"content":[]},"error":"e"}`,
	`{"type":"assistant","message":{"model":5,"content":[]},"error":"e"}`,
	`{"type":"assistant","message":{"content":"just a string"}}`,
	`{"type":"user","message":{"content":"a user string"}}`,
	`{"type":"assistant","message":{"content":[1,"two",null,[3],{"type":"text","text":"ok"},{"type":"text","text":5},{"type":"nope"}]}}`,
	`{"type":"assistant","message":{"content":[{"type":"text","text":"a","text":"b"},{"type":"text","text":null},{"type":"text","TEXT":"c"}]}}`,
	`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"hmm"},{"type":"thinking","thinking":null},{"type":"thinking"},{"type":"thinking","thinking":4},{"type":"thinking","thinking":"a","thinking":"b"},{"type":"text","thinking":4,"text":"stray"}]}}`,
	`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"/a/b","file_path":"/c"}},{"type":"tool_use","id":"t2","name":"Bash","input":{"command":5}},{"type":"tool_use","id":"t3","name":"Grep","input":null},{"type":"tool_use","id":"t4","name":"Glob"},{"type":"tool_use","id":"t5","name":"Edit","input":"s"},{"type":"tool_use","id":"t6","name":"Write","input":[1]},{"type":"tool_use","id":"t7","name":"Read","input":{"File_Path":"/folded"}},{"type":"tool_use","id":"t8","name":"Read","input":{"file_path":"/escaped"}}]}}`,
	`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"s1","name":"Task","input":{"subagent_type":"explorer","prompt":"p"}},{"type":"tool_use","id":"s2","name":"Agent","input":{"SUBAGENT_TYPE":"folded","subagent_type":3}},{"type":"tool_use","id":"s3","name":"Agent","input":{"subagent_type":"a","subagent_type":null}},{"type":"tool_use","id":"s4","name":"Agent","input":[1]},{"type":"tool_use","id":"","name":"Agent","input":{"subagent_type":"noid"}},{"type":"tool_use","id":"s5","name":"Agent","input":{"subagent_type":"x","subagent_type":4}}]}}`,
	`{"type":"user","parent_tool_use_id":"s1","message":{"content":[{"type":"tool_result","tool_use_id":"s1","content":"report"},{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"a"},{"type":"text","text":""},{"type":"image"},{"type":"text","text":"b"}],"is_error":true}]}}`,
	`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"a"},5]},{"type":"tool_result","tool_use_id":"t2","content":[null,{"text":null},{"text":"z","text":"y"}]},{"type":"tool_result","tool_use_id":"t3","content":[{"text":7}]},{"type":"tool_result","tool_use_id":"t4","content":null},{"type":"tool_result","tool_use_id":"t5"},{"type":"tool_result","tool_use_id":"t6","content":{"k":[1, 2]}},{"type":"tool_result","tool_use_id":"t7","content":[]},{"type":"tool_result","tool_use_id":"t8","content":12.50e1,"is_error":"yes"},{"type":"tool_result","tool_use_id":"t9","content":" sp é😀\ud800x\udc00","is_error":null}]}}`,
	`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"A\n\t\"q\" \/ é bad:` + "\xff\xfe" + `"}]}}`,
	`{"type":"user","message":{"content":[{"type":"tool_use","name":"X"},{"type":"text","text":"user text"},{"type":"thinking","thinking":"u"}]}}`,
	`{"type":"assistant","message":{"content":[{"type":"tool_result","tool_use_id":"q"}]}}`,
	`{"type":"result","subtype":"success","is_error":false,"result":"done","session_id":"sess","num_turns":3,"total_cost_usd":0.25,"duration_ms":1200,"duration_api_ms":900,"usage":{"input_tokens":10,"output_tokens":20,"cache_read_input_tokens":30,"cache_creation_input_tokens":40},"modelUsage":{"b-model":{"outputTokens":5},"a-model":{"outputTokens":6}}}`,
	`{"type":"result","result":"x","result":null,"is_error":true,"session_id":null}`,
	`{"type":"result","result":5,"num_turns":1}`,
	`{"type":"result","result":null}`,
	`{"type":"result"}`,
	`{"type":"result","result":"r","is_error":"no","num_turns":2,"total_cost_usd":1}`,
	`{"type":"result","result":"r","num_turns":"2","total_cost_usd":1,"duration_ms":5}`,
	`{"type":"result","result":"r","num_turns":2.0,"duration_ms":1e2}`,
	`{"type":"result","result":"r","total_cost_usd":"1","num_turns":2,"modelUsage":{"m":{"outputTokens":1}}}`,
	`{"type":"result","result":"r","total_cost_usd":1e400}`,
	`{"type":"result","result":"r","duration_ms":9223372036854775808}`,
	`{"type":"result","result":"r","duration_api_ms":-4,"usage":{"cache_creation":{"ephemeral_5m_input_tokens":2}}}`,
	`{"type":"result","result":"r","usage":{"input_tokens":"x"},"num_turns":1}`,
	`{"type":"result","result":"r","usage":{"input_tokens":1},"usage":null,"usage":{"output_tokens":2}}`,
	`{"type":"result","result":"r","session_id":5,"num_turns":4}`,
	`{"type":"result","result":"r","session_id":"a","SESSION_ID":"b"}`,
	`{"type":"result","result":"r","modelUsage":{"m":{"outputTokens":"x"}}}`,
	`{"type":"result","result":"r","modelUsage":{"m":null,"n":{"outputTokens":3}}}`,
	`{"type":"result","result":"r","modelUsage":{"m":5}}`,
	`{"type":"result","result":"r","modelUsage":{"m":{"outputTokens":1,"outputTokens":2},"m":{"x":1}}}`,
	`{"type":"result","result":"r","modelUsage":{"m":{"outputTokens":1}},"modelUsage":{"n":{"outputTokens":2}}}`,
	`{"type":"result","result":"r","modelUsage":{"m":{"outputTokens":1}},"modelUsage":null,"modelUsage":{"n":{}}}`,
	`{"type":"result","result":"r","modelUsage":{"m":{"outputTokens":1}},"modelUsage":[1]}`,
	`{"type":"result","result":"r","modelUsage":{"M":{"OUTPUTTOKENS":9},"m":{"outputTokens":4}},"MODELUSAGE":{"q":{}}}`,
	`{"type":"result","result":"x","modelUsage":{"m":"bad","m":{"outputTokens":1}}}`,
	`{"type":"result","result":"x","modelUsage":{"m":{"outputTokens":"x"},"m":{"outputTokens":1}}}`,
	`{"type":"result","result":"x","modelUsage":{"m":{"outputTokens":"x"}},"modelUsage":{"m":{"outputTokens":1}}}`,
	`{"type":"result","result":"x","modelUsage":{"m":"bad"},"modelUsage":null}`,
	`{"type":"result","result":"x","modelUsage":{"m":"bad"},"MODELUSAGE":{"m":{"outputTokens":1}}}`,
	`{"type":"result","result":"r","modelUsage":{"m":{"outputTokens":2},"m":{}}}`,
	`{"type":"result","result":"r","modelUsage":{}}`,
	`{"type":"result","result":"r","modelUsage":null}`,
	`{"type":"result","is_error":true,"result":"rate limit exceeded"}`,
	`{"type":"result","result":"API Error: 429 rate_limit_error"}`,
	`{"type":"result","result":"API Error: 429 rate_limit_error","is_error":true}`,
	`{"type":"system","subtype":"init"}`,
	`{"type":"system","timestamp":"2025-01-02T03:04:05Z"}`,
	`{"type":"system","timestamp":5}`,
	`{"type":"system","timestamp":null}`,
	`{"type":"system","timestamp":"nope"}`,
	`{"type":"","timestamp":"2025-01-02T03:04:05Z"}`,
	`{"timestamp":"2025-01-02T03:04:05Z"}`,
	`{"type":"system","Timestamp":"2025-01-02T03:04:05Z","timestamp":"2026-01-02T03:04:05+01:00"}`,
	`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"z","content":"\"timestamp\""}]}}`,
	`{"type":"x","message":{"content":"timestamp"}}`,
	`{"type":"spindrift_op","spindrift_op":{"op":"pass_start","role":"reviewer"}}`,
	`{"type":"spindrift_op","spindrift_op":{"op":"pass_start","role":"implementor"}}`,
	`{"type":"spindrift_op","spindrift_op":{"op":"settle","role":5}}`,
	`{"type":"spindrift_op","spindrift_op":null}`,
	`{"type":"spindrift_op","spindrift_op":{"op":"pass_start"},"type":"assistant"}`,
	`  {"type":"assistant","message":{"content":[]}}  `,
	"\t",
	``,
	`null`,
	`5`,
	`"string"`,
	`true`,
	`[]`,
	`[{"type":"assistant"}]`,
	`{}`,
	`{"type":"assistant"}`,
	`{"type":"user","message":{}}`,
	`{"type":"assistant","message":{"content":[{"type":"text","text":"rate limit exceeded: resets 6:30pm (UTC)"}]}}`,
	`{"type":"result","result":"rate limit exceeded: resets 6:30pm (UTC)","is_error":false}`,
	`{"type":"error","error":{"type":"rate_limit_error","resetsAt":1735700000}}`,
	`{"type":"assistant","message":{"content":[{"type":"text","text":"unterminated`,
	`{"type":"assistant","message":{"content":[{"type":"text","text":"x"}]}`,
	`{"type":"assistant","message":{"content":[{"type":"text","text":"x"}]}}}`,
	`{"type":"assistant",}`,
	`{"type":"assistant","message":{"content":[{"type":"text","text":"bad escape \q"}]}}`,
	`{"type":"assistant","message":{"content":[{"type":"text","text":"ctl ` + "\x01" + `"}]}}`,
	`plain text line: API Error: 529 overloaded_error`,
	`Claude AI usage limit reached|1735700000`,
	`{"type":"assistant","message":{"content":[{"type":"text","text":"nul \u0000 and 😀 and lone \uDC00"}]}}`,
	`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"k1","name":"Read","input":{"file_path":"😀"}}]}}`,
	`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Read","input":  {"file_path" : "sp aced" , "x":[1,2,{"y":null}]}  }]}}`,
	`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"a"},"input":null}]}}`,
	`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Read","input":null,"input":{"file_path":"b"}}]}}`,
	`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Read","is_error":"x","input":{"file_path":"b"}}]}}`,
	`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Read","content":[1],"input":{"file_path":"b"}}]}}`,
	`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"b"}}],"content":null}}`,
	`{"type":"assistant","message":{"content":[{"type":"text","text":"keep"}],"content":"drop"}}`,
	`{"type":"assistant","message":{"content":{"type":"text","text":"obj"}}}`,
	`{"type":"assistant","message":{"content":5}}`,
}

func deeplyNested(n int) []string {
	open := strings.Repeat(`{"a":`, n)
	return []string{
		strings.Repeat("[", n) + strings.Repeat("]", n),
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"d","name":"Read","input":` + open + `1` + strings.Repeat("}", n) + `}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"d","content":` + strings.Repeat("[", n) + strings.Repeat("]", n) + `}]}}`,
		`{"type":"result","result":` + open + `1` + strings.Repeat("}", n) + `}`,
	}
}

var epoch = time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)

// assertSameAsReference feeds lines in order through a fresh port and a fresh
// frozen reference, so state carried across lines must agree too.
func assertSameAsReference(t testing.TB, lines []string) {
	t.Helper()
	got, want := newDecoderAt(epoch), newRefDecoder(epoch)
	for i, line := range lines {
		g, w := got(line), want(line)
		if !reflect.DeepEqual(g, w) {
			t.Fatalf("line %d %q\n got: %+v\nwant: %+v", i, line, g, w)
		}
	}
}

func TestDecodeMatchesReference(t *testing.T) {
	t.Run("sequence", func(t *testing.T) { assertSameAsReference(t, edgeLines) })
	t.Run("each line fresh", func(t *testing.T) {
		for _, line := range edgeLines {
			assertSameAsReference(t, []string{line})
		}
	})
	t.Run("deep nesting", func(t *testing.T) {
		for _, n := range []int{100, 9999, 10001} {
			assertSameAsReference(t, deeplyNested(n))
		}
	})
	t.Run("truncations", func(t *testing.T) {
		for _, line := range edgeLines {
			for cut := 0; cut < len(line); cut += 1 + len(line)/40 {
				assertSameAsReference(t, []string{line[:cut]})
			}
		}
	})
	t.Run("corpus", func(t *testing.T) {
		var files []string
		_ = filepath.WalkDir("../../cmd/launcher/internal/driver/claude/testdata", func(p string, _ fs.DirEntry, err error) error {
			if err == nil && strings.HasSuffix(p, ".jsonl") {
				files = append(files, p)
			}
			return nil
		})
		if len(files) == 0 {
			t.Skip("native Strategy corpus not in this source tree")
		}
		for _, name := range files {
			f, err := os.Open(name)
			if err != nil {
				t.Fatal(err)
			}
			var lines []string
			sc := bufio.NewScanner(f)
			sc.Buffer(nil, 1<<26)
			for sc.Scan() {
				lines = append(lines, sc.Text())
			}
			f.Close()
			if err := sc.Err(); err != nil {
				t.Fatal(err)
			}
			assertSameAsReference(t, lines)
		}
	})
}

// FuzzDecodeMatchesReference decodes newline-separated lines, state carried
// across them, and fails on any event the frozen encoding/json reference
// would not produce.
func FuzzDecodeMatchesReference(f *testing.F) {
	f.Add(strings.Join(edgeLines, "\n"))
	for _, line := range edgeLines {
		f.Add(line)
	}
	f.Fuzz(func(t *testing.T, input string) {
		assertSameAsReference(t, strings.Split(input, "\n"))
	})
}

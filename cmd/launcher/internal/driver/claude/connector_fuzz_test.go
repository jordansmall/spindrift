package claude_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/driver/driverkit"
)

// connectorSeedLines cover what a hand-rolled decoder gets wrong: escapes
// (surrogate pairs, lone surrogates), invalid UTF-8, duplicate and
// case-variant keys, nulls, mistyped fields and truncation.
var connectorSeedLines = []string{
	`{"type":"assistant","message":{"id":"m1","model":"claude-x","content":[{"type":"text","text":"esc \"q\" \\ \/ \n \t é 😀 lone \ud800 \udc00 end \ud83d"}]}}`,
	`{"type":"assistant","message":{"content":[{"type":"text","text":"bad ` + "\xff\xfe\xc3" + ` utf8"}]}}`,
	"\xff" + `{"type":"assistant","message":{"content":[{"type":"text","text":"x"}]}}`,
	`{"type":"assistant","message":{"content":[{"type":"text","text":"x"}]}}` + "\xff",
	`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t` + "\xff" + `","name":"Read","input":{"file_path":"` + "\xc3(" + `"}}]}}`,
	`{"type":"assistant","type":"user","message":{"content":[{"type":"text","text":"a"}]},"message":{"id":"dup","content":[{"type":"text","text":"b"}]}}`,
	`{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]},"message":{"id":"m2"}}`,
	`{"TYPE":"assistant","Message":{"CONTENT":[{"Type":"text","TEXT":"folded"}]}}`,
	`{"type":"result","ſession_id":"kelvin","SESSION_ID":"upper","result":"r"}`,
	`{"type":"result","result":"r","Key":1,"session_id":"s","ſession_id":"x"}`,
	`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Read","input":{"File_Path":"/folded","file_path":"/exact"}},{"type":"tool_use","id":"t2","name":"Read","input":{"file_path":"/a","file_path":"/b"}}]}}`,
	`{"type":"assistant","message":null,"timestamp":"2025-01-02T03:04:05.678Z"}`,
	`{"type":"assistant","message":{"usage":null,"content":null}}`,
	`{"type":"assistant","message":{"usage":{"input_tokens":1,"cache_creation":{"ephemeral_5m_input_tokens":7},"cache_creation":null}}}`,
	`{"type":"assistant","message":{"usage":{"input_tokens":"1"},"id":"bad","content":[{"type":"text","text":5},{"type":"nope"},1,null]}}`,
	`{"type":"assistant","message":{"content":[{"type":"text","text":"a","text":"b"},{"type":"thinking","thinking":4}]},"parent_tool_use_id":5}`,
	`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"s1","name":"Task","input":{"subagent_type":"explorer"}}]}}`,
	`{"type":"user","parent_tool_use_id":"s1","message":{"content":[{"type":"tool_result","tool_use_id":"s1","content":[{"type":"text","text":"a"},5,{"type":"text","text":"b"}],"is_error":true},{"type":"tool_result","tool_use_id":"t2","content":" sp \ud800x\udc00","is_error":"yes"}]}}`,
	`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"A\n\t\"q\" ` + "\xff" + `"}]}}`,
	`{"type":"result","subtype":"success","is_error":false,"result":"done","session_id":"sess","num_turns":3,"total_cost_usd":0.25,"duration_ms":1200,"duration_api_ms":900,"usage":{"input_tokens":10,"output_tokens":20,"cache_read_input_tokens":30,"cache_creation_input_tokens":40},"modelUsage":{"b":{"outputTokens":5},"a":{"outputTokens":6}}}`,
	`{"type":"result","result":"x","result":null,"is_error":true,"session_id":null}`,
	`{"type":"result","result":"r","num_turns":"2","total_cost_usd":"1","duration_ms":1.5,"duration_api_ms":9223372036854775808,"modelUsage":{"m":{"outputTokens":"x"},"n":null,"o":5}}`,
	`{"type":"result","result":"r","total_cost_usd":1e400,"usage":{"input_tokens":"x"},"usage":null}`,
	`{"type":"result","result":"r","modelUsage":{"M":{"OUTPUTTOKENS":9},"m":{"outputTokens":4}},"MODELUSAGE":{"q":{}}}`,
	`{"type":"result","result":"x","modelUsage":{"m":"bad","m":{"outputTokens":1}}}`,
	`{"type":"result","result":"x","modelUsage":{"m":{"outputTokens":"x"},"m":{"outputTokens":1}}}`,
	`{"type":"result","result":"x","modelUsage":{"m":{"outputTokens":"x"}},"modelUsage":{"m":{"outputTokens":1}}}`,
	`{"type":"result","result":"x","modelUsage":{"m":"bad"},"modelUsage":null}`,
	`{"type":"result","result":"x","modelUsage":{"m":"bad"},"MODELUSAGE":{"m":{"outputTokens":1}}}`,
	`{"type":"result","is_error":true,"result":"rate limit exceeded"}`,
	`{"type":"error","error":{"type":"rate_limit_error","resetsAt":1735700000}}`,
	`{"type":"system","timestamp":"2025-01-02T03:04:05Z"}`,
	`{"type":"spindrift_op","spindrift_op":{"op":"pass_start","role":"reviewer"}}`,
	`{"type":"spindrift_op","spindrift_op":{"op":"settle","role":5}}`,
	`{"type":"spindrift_op","spindrift_op":{"op":"settle","pass":"x"}}`,
	`{"type":"spindrift_op","spindrift_op":{"op":"pass_start","role":"implement","pass":"2"}}`,
	`{"type":"spindrift_op","spindrift_op":{"op":"settle","pass":"x"},"spindrift_op":{"op":"settle"}}`,
	`{"type":"spindrift_op","spindrift_op":{"op":"settle"},"spindrift_op":{"op":"settle","pass":"x"}}`,
	`{"type":"spindrift_op","spindrift_op":{"op":"pass_start","role":"review"},"spindrift_op":{"op":"settle"}}`,
	`  {"type":"assistant","message":{"content":[]}}  `,
	"\t", ``, `null`, `5`, `"string"`, `[]`, `{}`,
	`plain text line: API Error: 529 overloaded_error`,
	`Claude AI usage limit reached|1735700000`,
}

// connectorSeedInputs is the seed corpus: the edge
// lines, nesting at and just past the scanner's limit, a prefix of every
// real line shape, and a head of each recorded Pass log.
func connectorSeedInputs(t testing.TB) []string {
	t.Helper()
	inputs := []string{strings.Join(connectorSeedLines, "\n")}
	inputs = append(inputs, connectorSeedLines...)
	// A mistyped pass_start still moves the role the next line is attributed to.
	inputs = append(inputs, strings.Join([]string{
		`{"type":"spindrift_op","spindrift_op":{"op":"pass_start","role":"review"}}`,
		`{"type":"spindrift_op","spindrift_op":{"op":"pass_start","role":"implement","pass":"2"}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}`,
	}, "\n"))
	for _, n := range []int{10000, 10001} {
		open := strings.Repeat(`{"a":`, n)
		inputs = append(inputs,
			strings.Repeat("[", n)+strings.Repeat("]", n),
			`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"d","name":"Read","input":`+open+`1`+strings.Repeat("}", n)+`}]}}`,
			`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"d","content":`+strings.Repeat("[", n)+strings.Repeat("]", n)+`}]}}`,
			`{"type":"result","result":`+open+`1`+strings.Repeat("}", n)+`}`,
		)
	}
	for _, line := range connectorSeedLines[:1] {
		for cut := range len(line) {
			inputs = append(inputs, line[:cut])
		}
	}
	files, _ := filepath.Glob(filepath.Join("testdata", "corpus", "*", "pass.jsonl"))
	for _, name := range files {
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		var head []string
		for _, line := range strings.Split(string(b), "\n") {
			if len(line) > 0 && len(line) <= 4096 {
				head = append(head, line)
			}
			if len(head) == 40 {
				break
			}
		}
		inputs = append(inputs, strings.Join(head, "\n"))
		// Truncations of the first long line, as a killed Box leaves them.
		for _, line := range head[:min(3, len(head))] {
			for cut := 1; cut < len(line); cut += 1 + len(line)/25 {
				inputs = append(inputs, line[:cut])
			}
		}
	}
	return inputs
}

// FuzzConnectorDecodeMatchesNative decodes the same newline-separated lines
// through the native Strategy and the compiled Connector, one decoder each so
// state carries across lines, and requires identical events. The seeds run as
// a plain test wherever SPINDRIFT_DRIVER_CONNECTOR_WASM is set, as in
// connectors/claude's conformance check.
func FuzzConnectorDecodeMatchesNative(f *testing.F) {
	wasm, err := os.ReadFile(connectorWasm(f))
	if err != nil {
		f.Fatal(err)
	}
	var c *connectorhost.Connector
	var conn driverkit.Strategy
	load := func(t testing.TB) {
		t.Helper()
		if c != nil {
			_ = c.Close(context.Background())
		}
		var err error
		if c, err = connectorhost.LoadCompiled(context.Background(), wasm, ""); err != nil {
			t.Fatal(err)
		}
		if conn, err = c.DriverStrategy(); err != nil {
			t.Fatal(err)
		}
	}
	load(f)
	f.Cleanup(func() { _ = c.Close(context.Background()) })
	for _, in := range connectorSeedInputs(f) {
		f.Add(in)
	}
	f.Fuzz(func(t *testing.T, input string) {
		// One worker shares c across inputs; a dead Connector would turn every
		// later input, the minimiser's candidates included, into a divergence.
		if c.Err() != nil {
			load(t)
		}
		native, got := claude.Strategy.NewDecoder(), conn.NewDecoder()
		for i, line := range strings.Split(input, "\n") {
			nEvs, gEvs := native(line), got(line)
			if err := c.Err(); err != nil {
				t.Fatalf("line %d %q: claude Connector died: %v", i, line, err)
			}
			g, gErr := wireForm(gEvs)
			n, nErr := wireForm(nEvs)
			if gErr != nil && nErr != nil {
				continue // both overflow the wire, e.g. a ResetAt past year 9999
			}
			if gErr != nil || nErr != nil {
				t.Fatalf("line %d %q\n connector wire error: %v\n native wire error: %v", i, line, gErr, nErr)
			}
			if !reflect.DeepEqual(g, n) {
				t.Fatalf("line %d %q\n connector: %.400v\n native:    %.400v", i, line, g, n)
			}
		}
	})
}

// wireForm is evs as the wire carries them: the JSON the Connector's events
// cross the wasm boundary in, decoded back so only values compare. Marshal
// writes an invalid UTF-8 byte as the escape \ufffd but the host's U+FFFD
// literally, and a nil and an empty slice are both [].
func wireForm(evs []driverkit.Event) (any, error) {
	b, err := json.Marshal(evs)
	if err != nil {
		return nil, err
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, err
	}
	if v == nil {
		return []any{}, nil
	}
	return v, nil
}

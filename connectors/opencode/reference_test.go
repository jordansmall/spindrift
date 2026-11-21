package main

import (
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/sdk/driver"
)

// referenceLines are lines whose decode the reference and the live decoder must
// agree on: well-formed events, and the malformed shapes json.Unmarshal rejects
// or tolerates in particular places.
var referenceLines = []string{
	``, `  `, `null`, `42`, `"x"`, `[1]`, `{`, `panic: boom`, `{}`,
	`{"type":"step_start","timestamp":1000,"part":{"messageID":"msg_1"}}`,
	`{"TYPE":"text","Timestamp":5,"PART":{"MessageID":"m","TEXT":"é\n"}}`,
	`{"type":"text","part":null}`,
	`{"type":"text","part":"x"}`,
	`{"type":"text","part":{"text":null,"messageID":null}}`,
	`{"type":"text","part":{"text":"a"},"part":{"messageID":"m"}}`,
	`{"type":"text","type":"reasoning","part":{"text":"a"}}`,
	`{"type":"text","timestamp":1.5}`,
	`{"type":"text","timestamp":1e3}`,
	`{"type":"text","timestamp":null,"part":{"text":"a"}}`,
	`{"type":"text","timestamp":99999999999999999999}`,
	`{"type":5}`,
	`{"type":"step_finish","part":{"tokens":null}}`,
	`{"type":"step_finish","part":{"tokens":{"input":1,"cache":null}}}`,
	`{"type":"step_finish","part":{"tokens":{"input":1,"cache":{"write":"x"}}}}`,
	`{"type":"step_finish","part":{"tokens":{"input":1.0}}}`,
	`{"type":"step_finish","part":{"tokens":{"input":1,"output":2,"reasoning":3,"cache":{"write":4,"read":5}},"cost":0.25,"modelID":"m"}}`,
	`{"type":"step_finish","part":{"cost":1e2,"tokens":[1]}}`,
	`{"type":"tool_use","part":{"callID":5,"tool":"bash","state":{"status":"completed","input":{"command":"ls"},"output":null}}}`,
	`{"type":"tool_use","part":{"callID":"c","tool":"bash","state":{"status":5,"input":{"command":"ls"}}}}`,
	`{"type":"tool_use","part":{"callID":"c","tool":"bash","state":{"status":"completed","input":"ls","output":{"a":1}}}}`,
	`{"type":"tool_use","part":{"callID":"c","tool":"bash","state":{"status":"error","input":{"command":"a","command":"b"},"error":{"m":"x"}}}}`,
	`{"type":"tool_use","part":{"callID":"c","tool":"write","state":{"status":"completed","input":{"filePath":7},"output":"ok"}}}`,
	`{"type":"tool_use","part":{"callID":"c","tool":"task","state":{"status":"completed","input":{"subagent_type":"r"},"output":"done"}}}`,
	`{"type":"tool_use","part":{"callID":"c","tool":"task","state":{"status":"completed","input":null}}}`,
	`{"type":"tool_use","part":{"callID":"c","tool":["x"],"state":null}}`,
	`{"type":"tool_use","part":{"callID":"c","tool":"bash","State":{"STATUS":"completed","Input":{"Command":"x"},"OUTPUT":"😀"}}}`,
	`{"type":"error"}`, `{"type":"error","error":null}`, `{"type":"error","error":"boom"}`,
	`{"type":"error","error":{ "a" : 1 }}`,
	`{"type":"error","timestamp":1790000004000,"error":{"data":{"statusCode":429,"responseHeaders":{"retry-after":"3600"}}}}`,
	`{"type":"error","timestamp":1790000004000,"error":{"data":{"responseHeaders":{"Retry-After-Ms":"250","x":null}}}}`,
	`{"type":"error","timestamp":1790000004000,"error":{"data":{"responseHeaders":{"retry-after":"Wed, 21 Oct 2037 07:28:00 GMT"}}}}`,
	`{"type":"error","timestamp":1790000004000,"error":{"data":{"responseHeaders":{"retry-after":5}}}}`,
	`{"type":"error","timestamp":1790000004000,"error":{"data":[],"responseHeaders":1}}`,
	`{"type":"error","timestamp":1790000004000,"error":{"data":{"responseHeaders":null}}}`,
	`{"type":"error","timestamp":1790000004000,"error":{"data":null}}`,
	`{"type":"error","timestamp":1790000004000,"error":{"DATA":{"RESPONSEHEADERS":{"retry-after":"9"}},"data":{"responseHeaders":{"retry-after":"8"}}}}`,
	`{"type":"error","timestamp":-5,"error":{"data":{"responseHeaders":{"retry-after":"9"}}}}`,
	`{"type":"error","error":"` + "\xff" + `"}`,
	`{"type":"error","part":{"text":5},"error":"boom"}`,
}

func TestDecodeMatchesReference(t *testing.T) {
	for _, line := range referenceLines {
		compareToReference(t, line)
	}
}

func FuzzDecodeMatchesReference(f *testing.F) {
	for _, line := range referenceLines {
		f.Add(line)
	}
	f.Fuzz(func(t *testing.T, line string) { compareToReference(t, line) })
}

func compareToReference(t *testing.T, line string) {
	t.Helper()
	if root, ok := driver.Scan(strings.TrimSpace(line)); ok {
		if _, host := driver.HostLineOf(root); host {
			return // the host decodes these itself
		}
	}
	if got, want := decode(line), refDecode(line); !reflect.DeepEqual(got, want) {
		t.Errorf("decode(%q)\n got %+v\nwant %+v", line, got, want)
	}
	if root, ok := driver.Scan(strings.TrimSpace(line)); ok {
		if got, want := isErrorEvent(root), refIsErrorEvent(line); got != want {
			t.Errorf("isErrorEvent(%q) = %v, want %v", line, got, want)
		}
	}
}

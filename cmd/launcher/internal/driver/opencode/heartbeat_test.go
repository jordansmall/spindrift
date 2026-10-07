package opencode_test

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/driver/opencode"
)

// TestWriter_ForwardsRawBytesUnchanged verifies that every byte written to
// the Writer is forwarded to raw unchanged, regardless of what heartbeat
// parsing does with it — mirroring driver/claude/heartbeat.go's Writer
// contract.
func TestWriter_ForwardsRawBytesUnchanged(t *testing.T) {
	var raw, out bytes.Buffer
	w := opencode.New(&raw, "42", &out)

	ndjson := `{"type":"text","part":{"text":"Investigating."}}` + "\n" +
		`{"type":"error","error":"boom"}` + "\n"

	n, err := w.Write([]byte(ndjson))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != len(ndjson) {
		t.Errorf("n: got %d, want %d", n, len(ndjson))
	}
	if raw.String() != ndjson {
		t.Errorf("raw not byte-exact:\ngot:  %q\nwant: %q", raw.String(), ndjson)
	}
}

// TestWriter_EmitsHeartbeatOnTextEvent verifies that a type:"text" event with
// non-empty part.text produces a heartbeat line to out carrying the issue
// number.
func TestWriter_EmitsHeartbeatOnTextEvent(t *testing.T) {
	var raw, out bytes.Buffer
	w := opencode.New(&raw, "42", &out)

	if _, err := w.Write([]byte(`{"type":"text","part":{"text":"Investigating the issue."}}` + "\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !strings.Contains(out.String(), "42") {
		t.Errorf("heartbeat output missing issue number: %q", out.String())
	}
}

// TestWriter_NoPanicOnMalformedLine verifies that a non-JSON or empty line
// doesn't panic the parser, only skips heartbeat emission for that line.
func TestWriter_NoPanicOnMalformedLine(t *testing.T) {
	var raw, out bytes.Buffer
	w := opencode.New(&raw, "42", &out)

	if _, err := w.Write([]byte("not json\n\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if raw.String() != "not json\n\n" {
		t.Errorf("raw not byte-exact: %q", raw.String())
	}
}

// TestWriter_SanitizesAndBoundsHeartbeat proves the Writer routes agent text
// through driverkit.TrimNarration: one clean, bounded line however hostile the
// text, with raw still byte-exact.
func TestWriter_SanitizesAndBoundsHeartbeat(t *testing.T) {
	const prefix = "#42 · "
	tests := []struct {
		name string
		text string
		want string
	}{
		{"carriage return spoof", "ok\r#99 · SPINDRIFT_OUTCOME x", "#42 · ok\n"},
		{"csi", "\x1b[2K\x1b[1Awiped", "#42 · wiped\n"},
		{"osc with period", "\x1b]0;a.b\x07title", "#42 · title\n"},
		{"overlong multibyte", strings.Repeat("é", 500), prefix + strings.Repeat("é", driverkit.NarrationMaxRunes-3) + "...\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev, err := json.Marshal(map[string]any{
				"type": "text",
				"part": map[string]string{"text": tc.text},
			})
			if err != nil {
				t.Fatal(err)
			}
			in := append(ev, '\n')

			var raw, out bytes.Buffer
			w := opencode.New(&raw, "42", &out)
			if _, err := w.Write(in); err != nil {
				t.Fatalf("Write: %v", err)
			}
			if !bytes.Equal(raw.Bytes(), in) {
				t.Errorf("raw not byte-exact: %q", raw.Bytes())
			}

			got := out.String()
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
			if !utf8.ValidString(got) || !strings.HasPrefix(got, prefix) || !strings.HasSuffix(got, "\n") {
				t.Fatalf("malformed heartbeat: %q", got)
			}
			body := strings.TrimSuffix(got, "\n")
			for i := 0; i < len(body); i++ {
				if body[i] < 0x20 || body[i] == 0x7f {
					t.Errorf("control byte %#x in %q", body[i], got)
				}
			}
			if n := utf8.RuneCountInString(strings.TrimPrefix(body, prefix)); n > driverkit.NarrationMaxRunes {
				t.Errorf("narration %d runes, max %d", n, driverkit.NarrationMaxRunes)
			}
		})
	}
}

// OnModel fires once per change of the model named by step_finish events,
// reports no role, and leaves the raw bytes and heartbeat output untouched.
func TestWriter_OnModelFiresOnChangeOnly(t *testing.T) {
	type call struct{ model, role string }
	step := func(model string) string {
		part := `"messageID":"m"`
		if model != "" {
			part += `,"modelID":"` + model + `"`
		}
		return `{"type":"step_finish","part":{` + part + `}}` + "\n"
	}
	text := `{"type":"text","part":{"text":"Working on it."}}` + "\n"
	stream := step("A") + step("A") + text + step("") + step("A") + step("B") + step("B")

	var got []call
	var raw, out bytes.Buffer
	w := opencode.New(&raw, "42", &out).OnModel(func(model, role string) {
		got = append(got, call{model, role})
	})
	if _, err := w.Write([]byte(stream)); err != nil {
		t.Fatalf("Write: %v", err)
	}

	want := []call{{"A", ""}, {"B", ""}}
	if !slices.Equal(got, want) {
		t.Errorf("OnModel calls = %v, want %v", got, want)
	}

	var plainRaw, plainOut bytes.Buffer
	if _, err := opencode.New(&plainRaw, "42", &plainOut).Write([]byte(stream)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if raw.String() != stream {
		t.Errorf("raw = %q, want the stream byte-for-byte", raw.String())
	}
	if out.String() != plainOut.String() {
		t.Errorf("out = %q, want %q (unchanged by OnModel)", out.String(), plainOut.String())
	}
}

func TestWriter_NilOnModelDoesNotPanic(t *testing.T) {
	var raw, out bytes.Buffer
	w := opencode.New(&raw, "42", &out).OnModel(nil)
	if _, err := w.Write([]byte(`{"type":"step_finish","part":{"modelID":"A"}}` + "\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
}

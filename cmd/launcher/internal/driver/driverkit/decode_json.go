package driverkit

import (
	"bytes"
	"encoding/json"
)

// ToolTarget returns the string value of the input field that fields names for
// tool, "" for an unknown tool, a missing field or a non-string one.
func ToolTarget(fields map[string]string, tool string, input json.RawMessage) string {
	field, ok := fields[tool]
	if !ok {
		return ""
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(input, &obj) != nil {
		return ""
	}
	var target string
	_ = json.Unmarshal(obj[field], &target)
	return target
}

// ErrorText returns a string payload unquoted, or any other payload in compact
// JSON so a marker like `"statusCode":429` has no stray whitespace. A missing
// or invalid payload yields "", which matches no marker.
func ErrorText(raw json.RawMessage) string {
	var str string
	if json.Unmarshal(raw, &str) == nil {
		return str
	}
	return CompactJSON(raw)
}

// CompactJSON returns raw with insignificant whitespace removed, or "" for an
// empty, bare null or invalid payload; a whitespace-padded null compacts to
// "null".
func CompactJSON(raw json.RawMessage) string {
	var buf bytes.Buffer
	if len(raw) == 0 || string(raw) == "null" || json.Compact(&buf, raw) != nil {
		return ""
	}
	return buf.String()
}

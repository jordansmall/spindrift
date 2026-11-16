package main

import (
	"strings"

	"spindrift.dev/sdk/driver"
)

// resultText reads a tool_result block's content field, which the Claude API
// allows to be either a bare string or an array of {"type":"text",...} blocks.
// Anything else, or an array with a mistyped text, is returned as its raw JSON.
func resultText(content driver.Value) string {
	switch content.Kind() {
	case driver.Invalid, driver.Null:
		return ""
	case driver.String:
		var s string
		content.DecodeString(&s)
		return s
	case driver.Array:
		var parts []string
		for elem := range content.Elems() {
			if k := elem.Kind(); k != driver.Object && k != driver.Null {
				return content.Raw()
			}
			text, ok := field(elem, "text", driver.Value.DecodeString)
			if !ok {
				return content.Raw()
			}
			if text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, " ")
	}
	return content.Raw()
}

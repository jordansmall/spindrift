// Package event is the published copy of the canonical event vocabulary (ADR
// 0065) for Connector authors. Core's driverkit package owns the vocabulary;
// parity_test.go fails when this copy drifts from it. Only declarations are
// copied: the readers that parse Driver output stay in core, and Event.HostOp
// (a core-internal payload) is not published.
package event

// IsProgress reports whether k shows the agent working past an earlier error:
// an error followed by such an event was recovered from. KindUsage is
// deliberately not progress: it can close the step whose error was just
// emitted.
func IsProgress(k Kind) bool {
	switch k {
	case KindStart, KindText, KindReasoning, KindToolCall, KindToolResult:
		return true
	}
	return false
}

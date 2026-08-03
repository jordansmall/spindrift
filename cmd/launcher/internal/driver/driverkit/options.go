package driverkit

// RenderOptions controls role attribution for a Driver's heartbeat-writer
// and transcript-render output. Both operations share one options value so
// an implementor that doesn't attribute roles (e.g. opencode) just ignores
// a field, not a positional argument (issue #2263).
type RenderOptions struct {
	// TopLevelRole is the role attributed to top-level (empty
	// parent_tool_use_id) messages; empty means the implementor's own
	// default (issue #2092).
	TopLevelRole string
}

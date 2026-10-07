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

	// OnModel is called with the exact model id and the role whenever the
	// (role, model) pair behind the streamed messages changes; nil means no one
	// listens. An implementor that cannot attribute a model to a message (e.g.
	// opencode) ignores it.
	OnModel func(model, role string)
}

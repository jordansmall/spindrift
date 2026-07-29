package driverkit

// ImplementorRole is the role attributed to any message with no
// parent_tool_use_id — the main agent loop, as opposed to a Task subagent.
const ImplementorRole = "implementor"

// ReviewerRole is the role attributed to a top-level orchestrator-owned
// review pass (issue #2092).
const ReviewerRole = "reviewer"

// DefaultRole is the role attributed to a Task whose input carries no (or
// empty) subagent_type, and to any message whose parent_tool_use_id does not
// match a Task ID collected so far.
const DefaultRole = "subagent"

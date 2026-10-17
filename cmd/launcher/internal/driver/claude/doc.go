// Package claude is the claude Driver's host-side half (ADR 0009, 0065). It
// owns the decoder for `claude --output-format stream-json` (one JSON object
// per line, with the type:"assistant"/"user"/"result" envelope), the transient
// and terminal error patterns, the result-event encoder, and the session flags,
// plus the few claude quirks the shared readers take as options: the classify
// options, the heartbeat tracking, and the per-model usage ordering. Claude has
// no exit resolution of its own; its ResolveExit passes the code through. The
// readers (classify, usage, transcript, heartbeat, result text) live in
// driverkit and are wired to this decoder in driver/claude.go.
package claude

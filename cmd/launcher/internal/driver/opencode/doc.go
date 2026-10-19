// Package opencode is the opencode Driver's host-side half (ADR 0009, 0065).
// It owns the decoder for `opencode run --format json` (one JSON object per
// line, no envelope), the transient and terminal error patterns, exit
// resolution, the result-event encoder, and the session flags. The readers
// (classify, usage, transcript, heartbeat, result text) live in driverkit and
// are wired to this decoder through Strategy (strategy.go), which driverkit.New
// turns into the Driver.
package opencode

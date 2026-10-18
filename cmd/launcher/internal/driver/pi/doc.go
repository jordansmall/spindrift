// Package pi is the pi Driver's host-side half (ADR 0009, 0065, 0066). It owns
// the decoder for `pi --print --mode json` (one JSON object per line, typed
// by a top-level "type"), the transient error patterns, exit resolution, the
// result-event encoder, and the session flags. The readers (classify, usage,
// transcript, heartbeat, result text) live in driverkit and are wired to this
// decoder by driverkit.New over Strategy.
package pi

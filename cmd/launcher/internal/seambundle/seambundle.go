// Package seambundle names the fixed filename a CODE_FORGE=local seam's
// code-out bundle is written under in the writable outbox mount (ADR 0033).
// Split out as its own dependency-free package (issue #1808) so driver-exec's
// tight fileset (lib/mkHarness.nix's driverExecBin) can share this one
// constant with the launcher's local Code Forge without pulling in that
// package's full import closure (forge, forge/git, ...).
package seambundle

// FileName is the bundle's fixed name — a single well-known name, since the
// outbox holds exactly one seam's bundle per dispatch.
const FileName = "seam.bundle"

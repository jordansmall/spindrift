// Package seambundle names the fixed filename a CODE_FORGE=local seam's
// code-out bundle is written under in the writable outbox mount (ADR 0033).
// It stays dependency-free (issue #1808) so driver-exec's tight fileset
// (driverExecBin in lib/mkHarness.nix) can share the constant.
package seambundle

// FileName is the bundle's fixed name. The outbox holds exactly one seam's
// bundle per dispatch, so one well-known name is enough.
const FileName = "seam.bundle"

// Package piseam is the seam that runs the locked pi binary against a local
// stub OpenAI-compatible Provider (issue #5478). Everything lives in
// integration-tagged test files; the stub scripts the model's replies so a
// test can drive pi through a plain answer, a tool call, a retried HTTP error
// and a final outcome line.
//
// pi comes from SPINDRIFT_PI_BIN when set, else from the flake's locked
// nixpkgs via nix; with neither the tests skip. Setting
// SPINDRIFT_PI_CAPTURE_DIR makes each run write pi's raw JSON event stream to
// <dir>/<TestName>.jsonl (subtest slashes become "_"; a later run in the same
// test is <TestName>.<n>.jsonl), which is how Pass logs for the driver corpus are
// captured from the exact locked version.
package piseam

package runner

import "os/exec"

// ExecCommand is the test seam for hardcoded-binary orchestration (nix, bwrap,
// sqlite3) that has no configurable CLI field to intercept.
var ExecCommand = exec.Command

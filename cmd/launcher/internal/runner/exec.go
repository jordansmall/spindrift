package runner

import "os/exec"

// execCommand is the test seam for the core nix helpers, which exec a
// hardcoded binary with no configurable CLI field to intercept. Each exec-ing
// subpackage (oci, bwrap) keeps its own.
var execCommand = exec.Command

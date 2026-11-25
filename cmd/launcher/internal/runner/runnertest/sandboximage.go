package runnertest

const (
	// SandboxImageHex is the hex of the image ID the fake runtimes report for
	// the Connector sandbox image (a fake podman may serve it bare).
	SandboxImageHex = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	// SandboxImageID is SandboxImageHex in the sha256: form the sandbox
	// policy accepts.
	SandboxImageID = "sha256:" + SandboxImageHex
)

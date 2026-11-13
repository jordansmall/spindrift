package doctor

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/connectorforge/connectorforgetest"
	"spindrift.dev/launcher/internal/forge"
)

// A Connector that does not declare "forge.branchProtection" resolves a forge
// without that surface, so the row is not applicable, never a failure, under
// every merge policy: the same outcome as a native forge lacking the API.
func TestBranchProtectionCheck_ConnectorWithoutCapabilityIsNotApplicable(t *testing.T) {
	h := connectorforgetest.NewPRVariant(t, connectorforgetest.MinimalPRWasm)
	caps := h.Capabilities()
	if caps.BranchProtectionForge != nil {
		t.Fatalf("minimal handshake resolved a BranchProtectionForge: %T", caps.BranchProtectionForge)
	}
	for _, policy := range []string{"immediate", "auto", "manual"} {
		t.Run(policy, func(t *testing.T) {
			c := BranchProtectionCheck(caps, policy, "main")
			out, err := c.Probe()
			if err != nil {
				t.Fatalf("Probe err = %v, want nil (not applicable)", err)
			}
			const want = "not applicable (code forge has no branch-protection API)"
			if got := c.SuccessMsg(out); got != want {
				t.Errorf("SuccessMsg = %q, want %q", got, want)
			}
		})
	}
}

// A Connector declaring "forge.branchProtection" answers the probe through the
// backend: protected succeeds, unprotected fails as for a native forge.
func TestBranchProtectionCheck_ConnectorWithCapabilityProbesBackend(t *testing.T) {
	h := connectorforgetest.NewPR(t)
	// doctor.Run resolves capabilities by type probe, so probe the forge itself.
	c := BranchProtectionCheck(forge.ResolveCodeForge(h.CodeForge()), "immediate", "main")

	h.Backend.SetProtected("main", true)
	out, err := c.Probe()
	if err != nil {
		t.Fatalf("protected: Probe err = %v, want nil", err)
	}
	if got := c.SuccessMsg(out); !strings.Contains(got, `"main" is protected`) {
		t.Errorf("protected: SuccessMsg = %q, want it to say main is protected", got)
	}

	h.Backend.SetProtected("main", false)
	if _, err := c.Probe(); err == nil || !strings.Contains(err.Error(), "not protected") {
		t.Errorf("unprotected: Probe err = %v, want a not-protected failure", err)
	}
}

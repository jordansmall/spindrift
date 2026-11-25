package connectorforge

import (
	"testing"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/gitexec"
)

type recordingPolicy struct{ methods []string }

func (p *recordingPolicy) AuditRefusal(method string, refusal error) error {
	p.methods = append(p.methods, method)
	return refusal
}

func TestComposeCallingRoutesCallsAndAuditThroughTheSuppliedOnes(t *testing.T) {
	// A zero Connector has no guest: any call through c.Call would fail, so a
	// passing Probe proves ComposeCalling used the supplied call.
	c := &connectorhost.Connector{Capabilities: []string{"forge", CapBranches, capProbe, CapPRs}}
	call, calls := stubGuest(t, map[string]string{
		"forge.probe": `{"repo":"o/r"}`,
		"forge.pr":    prJSON + `,"head":"other"}`,
	})
	policy := &recordingPolicy{}
	g := Git{
		WebBase: "https://github.com",
		Plumbing: func(string, gitexec.Credential) forge.CodeForge {
			return struct{ forge.CodeForge }{}
		},
	}
	cf, caps, err := ComposeCalling(c, call, policy, testTarget, g)
	if err != nil {
		t.Fatal(err)
	}
	if repo, err := cf.Probe(); err != nil || repo != "o/r" {
		t.Fatalf("Probe = (%q, %v), want (o/r, nil)", repo, err)
	}
	if len(*calls) != 1 || (*calls)[0].Method != "forge.probe" {
		t.Fatalf("supplied call saw %v, want one forge.probe", *calls)
	}
	if err := caps.PRHeadRebaser.RebasePRHead(prURL7, "agent/issue-7"); err == nil {
		t.Fatal("RebasePRHead on a foreign head succeeded")
	}
	if len(policy.methods) != 1 || policy.methods[0] != "forge.pr" {
		t.Fatalf("policy audited %v, want one forge.pr refusal", policy.methods)
	}
}

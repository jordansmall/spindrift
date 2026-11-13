package connectorforge_test

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/connectorforge/connectorforgetest"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
)

func TestComposedForgePRForgeContract(t *testing.T) {
	forgetest.RunPRForgeContract(t, connectorforgetest.NewPR(t))
}

func TestFullCapabilityForgeOffersCIRunAndBranchProtection(t *testing.T) {
	h := connectorforgetest.NewPR(t)
	caps := h.Capabilities()
	if caps.CIRunReporter == nil || caps.BranchProtectionForge == nil || caps.MergeCommitReader == nil {
		t.Fatalf("capabilities = %+v, want CIRunReporter, BranchProtectionForge and MergeCommitReader", caps)
	}

	url := h.SeedOpenPR("301")
	if !strings.HasSuffix(url, "/pull/301") {
		t.Fatalf("SeedOpenPR URL %q does not end in /pull/301; update the run URL derived from it", url)
	}
	run := strings.TrimSuffix(url, "/pull/301") + "/actions/runs/9"
	h.Backend.AddStatus(301, "build", "failure", "boom", run)
	state, got, err := caps.CIRunReporter.CheckRun(url)
	if err != nil || state != forge.StateFailure || got != run {
		t.Fatalf("CheckRun = (%q, %q, %v), want (failure, %q, nil)", state, got, err, run)
	}

	h.Backend.SetProtected("main", true)
	if ok, err := caps.BranchProtectionForge.BranchProtected("main"); err != nil || !ok {
		t.Fatalf("BranchProtected(main) = (%v, %v), want (true, nil)", ok, err)
	}
	h.Backend.SetProtected("main", false)
	if ok, err := caps.BranchProtectionForge.BranchProtected("main"); err != nil || ok {
		t.Fatalf("BranchProtected(main) = (%v, %v), want (false, nil)", ok, err)
	}

	h.Backend.SetAutoMergeAllowed(true)
	if ok, err := h.Forge().CanAutoMerge(); err != nil || !ok {
		t.Fatalf("CanAutoMerge = (%v, %v), want (true, nil)", ok, err)
	}
	if repo, err := h.CodeForge().Probe(); err != nil || repo != "o/r" {
		t.Fatalf("Probe = (%q, %v), want o/r", repo, err)
	}
}

func TestMergeLandsARealBranchAndReportsTheMergeCommit(t *testing.T) {
	h := connectorforgetest.NewPR(t)
	url := h.SeedLandablePR("302")
	if err := h.CodeForge().Merge(url); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if !h.Merged(url) || !h.Landed("302") {
		t.Errorf("merged = %v, landed = %v, want both", h.Merged(url), h.Landed("302"))
	}
	sha, err := h.Capabilities().MergeCommitReader.MergeCommit(url)
	if err != nil || sha == "" {
		t.Fatalf("MergeCommit = (%q, %v), want the merge commit", sha, err)
	}
}

func TestComposeResolvesAForgeWithoutTheUndeclaredCapabilities(t *testing.T) {
	h := connectorforgetest.NewPRVariant(t, connectorforgetest.MinimalPRWasm)
	for _, c := range []string{"forge.autoMerge", "forge.ciRun", "forge.branchProtection", "forge.probe", "forge.draftPRs"} {
		for _, have := range h.Conn.Capabilities {
			if have == c {
				t.Fatalf("handshake declares %q, want it absent: %v", c, h.Conn.Capabilities)
			}
		}
	}
	caps := h.Capabilities()
	if caps.PRForge == nil {
		t.Fatal("PRForge is nil, want the composed PR forge")
	}
	if caps.DraftPRCreator != nil || caps.BranchPusher != nil || caps.HostCanOpenPR() {
		t.Errorf("capabilities = %+v, want no DraftPRCreator or BranchPusher", caps)
	}
	if caps.CIRunReporter != nil || caps.BranchProtectionForge != nil {
		t.Errorf("capabilities = %+v, want no CIRunReporter or BranchProtectionForge", caps)
	}
	resolved := forge.ResolveCodeForge(h.CodeForge())
	if resolved.CIRunReporter != nil || resolved.BranchProtectionForge != nil {
		t.Errorf("ResolveCodeForge = %+v, want no CIRunReporter or BranchProtectionForge", resolved)
	}

	before := h.Backend.Requests()
	if ok, err := h.Forge().CanAutoMerge(); ok || err != nil {
		t.Fatalf("CanAutoMerge = (%v, %v), want (false, nil)", ok, err)
	}
	if n := h.Backend.Requests(); n != before {
		t.Errorf("CanAutoMerge sent %d backend requests, want none", n-before)
	}
	url := h.SeedOpenPR("303")
	if err := h.Forge().EnqueueAutoMerge(url); err == nil {
		t.Error("EnqueueAutoMerge succeeded without the capability")
	}
	if h.AutoMergeEnqueued(url) || h.Backend.MergeRequests() != 0 {
		t.Error("EnqueueAutoMerge reached the backend")
	}
	// Probe falls back to re-reading the remote's identity.
	if repo, err := h.CodeForge().Probe(); err != nil || repo != "o/r" {
		t.Fatalf("Probe = (%q, %v), want o/r", repo, err)
	}
}

func TestUndeclaredMergeIsRefusedUnsentAndAudited(t *testing.T) {
	h := connectorforgetest.NewPRVariant(t, connectorforgetest.UndeclaredMergeWasm)
	url := h.SeedLandablePR("304")
	h.Audit.Reset()

	err := h.CodeForge().Merge(url)
	if !errors.Is(err, connectorhost.ErrRouteRefused) {
		t.Fatalf("Merge = %v, want ErrRouteRefused", err)
	}
	if errors.Is(err, forge.ErrMergeTransient) || errors.Is(err, forge.ErrMergeConflict) || errors.Is(err, forge.ErrMergeBlockedByChecks) {
		t.Errorf("Merge = %v, want the refusal unclassified", err)
	}
	if !strings.Contains(err.Error(), "/repos/elsewhere/r/pulls/304/merge") {
		t.Errorf("Merge = %v, want it to name the undeclared path", err)
	}
	if n := h.Backend.MergeRequests(); n != 0 {
		t.Errorf("backend saw %d merge requests, want none", n)
	}
	if pr := h.Backend.PR(304); pr.Merged || pr.Closed || h.Landed("304") {
		t.Errorf("PR = %+v, landed %v, want it still open and unlanded", pr, h.Landed("304"))
	}

	var refused []connectorhost.AuditEntry
	for _, l := range strings.Split(strings.TrimSpace(h.Audit.String()), "\n") {
		var e connectorhost.AuditEntry
		if err := json.Unmarshal([]byte(l), &e); err != nil {
			t.Fatalf("audit line %q: %v", l, err)
		}
		if e.Decision == connectorhost.DecisionRefused {
			refused = append(refused, e)
		}
	}
	if len(refused) != 1 || refused[0].Path != "/api/v1/repos/elsewhere/r/pulls/304/merge" || refused[0].Method != "POST" {
		t.Fatalf("refused audit entries = %+v, want one POST for the undeclared merge path", refused)
	}
}

func TestFullCapabilityForgeHostMediatesWithCoresCredential(t *testing.T) {
	h := connectorforgetest.NewPR(t)
	caps := h.Capabilities()
	if caps.DraftPRCreator == nil || caps.BranchPusher == nil || !caps.HostCanOpenPR() {
		t.Fatalf("capabilities = %+v, want DraftPRCreator and BranchPusher (HostCanOpenPR)", caps)
	}

	src := t.TempDir()
	forgetest.Run(t, "", "clone", h.Repo.Bare, src)
	forgetest.Run(t, src, "checkout", "-b", "work")
	forgetest.WriteFile(t, filepath.Join(src, "feature.txt"), "feature\n")
	forgetest.Run(t, src, "add", "feature.txt")
	forgetest.Run(t, src, "commit", "-m", "feature")
	wantSHA := forgetest.RevParse(t, src, "work")

	// A git shim on PATH records the credential core's git ran with.
	seen := gitCredentialLog(t)

	branch := "agent/issue-9001"
	if err := caps.BranchPusher.PushBranch(src, "work", branch, "main"); err != nil {
		t.Fatalf("PushBranch: %v", err)
	}
	if got := forgetest.RevParse(t, h.Repo.Bare, "refs/heads/"+branch); got != wantSHA {
		t.Errorf("refs/heads/%s = %s, want %s", branch, got, wantSHA)
	}
	log, err := os.ReadFile(seen)
	if err != nil {
		t.Fatalf("git shim never ran with a credential: %v", err)
	}
	if !strings.Contains(string(log), h.Credential.Username+":"+h.Credential.Password) {
		t.Errorf("git ran without core's credential: %q", log)
	}
	if strings.Contains(h.Audit.String(), h.Credential.Password) {
		t.Error("the Connector's audit log carries core's git credential")
	}

	// An identical tip is a no-op push, so advance the branch before retrying.
	forgetest.WriteFile(t, filepath.Join(src, "more.txt"), "more\n")
	forgetest.Run(t, src, "add", "more.txt")
	forgetest.Run(t, src, "commit", "-m", "more")
	if err := caps.BranchPusher.PushBranch(src, "work", branch, "main"); err == nil {
		t.Error("second PushBranch onto the same branch succeeded, want create-only refusal")
	} else if strings.Contains(err.Error(), h.Credential.Password) {
		t.Errorf("PushBranch error leaks the credential: %v", err)
	}

	url, created, err := caps.DraftPRCreator.CreateDraftPR("t", "b", "main", branch)
	if err != nil || !created || url == "" {
		t.Fatalf("CreateDraftPR = (%q, %v, %v), want a created draft", url, created, err)
	}
	if !h.IsDraft(url) {
		t.Errorf("PR %s is not a draft", url)
	}
}

// gitCredentialLog puts a git shim on PATH and returns the file it appends the
// GITEXEC_USERNAME:GITEXEC_PASSWORD core's git ran with to.
func gitCredentialLog(t *testing.T) string {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	shim, seen := t.TempDir(), filepath.Join(t.TempDir(), "seen")
	script := "#!/bin/sh\nprintf '%s:%s\\n' \"$GITEXEC_USERNAME\" \"$GITEXEC_PASSWORD\" >> '" + seen + "'\nexec '" + realGit + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(shim, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shim+string(os.PathListSeparator)+os.Getenv("PATH"))
	return seen
}

func TestComposedForgeHostMediationContract(t *testing.T) {
	forgetest.RunHostMediationContract(t, connectorforgetest.NewHostMediation(t))
}

func TestReadOnlyBoxRelaysAndOpensTheDraftWithoutTheConnectorSeeingGit(t *testing.T) {
	h := connectorforgetest.NewHostMediation(t)
	caps := h.Capabilities()
	if caps.BundleRelay == nil || caps.BundleCommitSubjects == nil || caps.DraftPRCreator == nil || caps.BranchPusher == nil {
		t.Fatalf("capabilities = %+v, want BundleRelay, BundleCommitSubjects, DraftPRCreator and BranchPusher", caps)
	}
	seen := gitCredentialLog(t)
	const ref = "agent/issue-9002"
	outbox := h.SeedBundle(ref)
	h.Audit.Reset()
	before := h.Backend.Requests()

	subjects, err := caps.BundleCommitSubjects.CommitSubjects(outbox, "main", ref)
	if err != nil || len(subjects) != 1 || subjects[0] != "feature" {
		t.Fatalf("CommitSubjects = (%q, %v), want [feature]", subjects, err)
	}
	if err := caps.BundleRelay.RelayBundle(outbox, ref); err != nil {
		t.Fatalf("RelayBundle: %v", err)
	}
	if !h.BundleLanded(ref) {
		t.Fatalf("%s never reached the repository", ref)
	}
	log, err := os.ReadFile(seen)
	if err != nil {
		t.Fatalf("git shim never ran with a credential: %v", err)
	}
	if !strings.Contains(string(log), h.Credential.Username+":"+h.Credential.Password) {
		t.Errorf("relay's git ran without core's credential: %q", log)
	}
	if n := h.Backend.Requests() - before; n != 0 || h.Audit.Len() != 0 {
		t.Errorf("relay crossed the Connector: %d backend requests, audit %q, want none", n, h.Audit.String())
	}

	if _, created, err := caps.DraftPRCreator.CreateDraftPR("t", "b", "main", ref); err != nil || !created {
		t.Fatalf("CreateDraftPR = (%v, %v), want created", created, err)
	}
	var entries []connectorhost.AuditEntry
	for _, l := range strings.Split(strings.TrimSpace(h.Audit.String()), "\n") {
		var e connectorhost.AuditEntry
		if err := json.Unmarshal([]byte(l), &e); err != nil {
			t.Fatalf("audit line %q: %v", l, err)
		}
		entries = append(entries, e)
	}
	if len(entries) != 1 || entries[0].Method != "POST" || !strings.HasSuffix(entries[0].Path, "/repos/o/r/pulls") {
		t.Errorf("audit entries = %+v, want only the forge.openPR POST", entries)
	}
	if n := h.Backend.Requests() - before; n != 1 {
		t.Errorf("backend saw %d requests, want only the openPR POST", n)
	}
	if strings.Contains(h.Audit.String(), h.Credential.Password) {
		t.Error("the Connector's audit log carries core's git credential")
	}
}

func TestBundleRelayIsOfferedOnlyToAReadOnlyBoxOnADraftPRForge(t *testing.T) {
	rw := connectorforgetest.NewPR(t).Capabilities()
	if rw.BundleRelay != nil || rw.BundleCommitSubjects != nil || rw.DraftPRCreator == nil || rw.BranchPusher == nil {
		t.Errorf("read-write capabilities = %+v, want DraftPRCreator and BranchPusher and no bundle relay", rw)
	}
	ro := connectorforgetest.NewHostMediationVariant(t, connectorforgetest.MinimalPRWasm).Capabilities()
	if ro.BundleRelay != nil || ro.BundleCommitSubjects != nil || ro.DraftPRCreator != nil || ro.BranchPusher != nil {
		t.Errorf("read-only capabilities without forge.draftPRs = %+v, want none of the host-mediated surface", ro)
	}
}

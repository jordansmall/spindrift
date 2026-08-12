package forge

import (
	"errors"
	"testing"
)

// TestFake_BareFakeDoesNotSatisfyBundleCommitSubjects proves commitSubjects
// stays unexported on HostMediationFake, the same isolation relayBundle and
// createDraftPR already have (see fake_hostmediation.go's HostMediationFake
// doc comment): a bare *Fake used as a CodeForge elsewhere must never
// silently start satisfying forge.BundleCommitSubjects too.
func TestFake_BareFakeDoesNotSatisfyBundleCommitSubjects(t *testing.T) {
	f := NewFake()
	if _, ok := any(f).(BundleCommitSubjects); ok {
		t.Fatal("bare *Fake must not satisfy BundleCommitSubjects directly")
	}
}

// TestFake_AsGithubReadOnlySatisfiesBundleCommitSubjects proves
// AsGithubReadOnly()'s wrapper implements forge.BundleCommitSubjects.
func TestFake_AsGithubReadOnlySatisfiesBundleCommitSubjects(t *testing.T) {
	cf := NewFake().AsGithubReadOnly()
	if _, ok := cf.(BundleCommitSubjects); !ok {
		t.Fatal("AsGithubReadOnly() must satisfy BundleCommitSubjects")
	}
}

// TestFake_CommitSubjectsDefaultsToNilNil proves an unscripted Fake returns
// (nil, nil) by default, mirroring RelayBundle's own zero-value default when
// RelayBundleErr is unset.
func TestFake_CommitSubjectsDefaultsToNilNil(t *testing.T) {
	cf := NewFake().AsGithubReadOnly().(BundleCommitSubjects)
	subjects, err := cf.CommitSubjects("outbox", "main", "branch")
	if subjects != nil || err != nil {
		t.Fatalf("got (%v, %v), want (nil, nil)", subjects, err)
	}
}

// TestFake_CommitSubjectsResultScriptsSuccess proves CommitSubjectsResult
// scripts the returned subjects.
func TestFake_CommitSubjectsResultScriptsSuccess(t *testing.T) {
	fc := NewFake()
	fc.CommitSubjectsResult = []string{"a", "b"}
	cf := fc.AsGithubReadOnly().(BundleCommitSubjects)

	subjects, err := cf.CommitSubjects("outbox", "main", "branch")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(subjects) != 2 || subjects[0] != "a" || subjects[1] != "b" {
		t.Fatalf("got %v, want [a b]", subjects)
	}
}

// TestFake_CommitSubjectsErrScriptsFailure proves CommitSubjectsErr scripts
// the returned error and nils out the subjects.
func TestFake_CommitSubjectsErrScriptsFailure(t *testing.T) {
	fc := NewFake()
	wantErr := errors.New("boom")
	fc.CommitSubjectsErr = wantErr
	cf := fc.AsGithubReadOnly().(BundleCommitSubjects)

	subjects, err := cf.CommitSubjects("outbox", "main", "branch")
	if !errors.Is(err, wantErr) {
		t.Fatalf("got err %v, want %v", err, wantErr)
	}
	if subjects != nil {
		t.Fatalf("got subjects %v, want nil", subjects)
	}
}

// TestFake_CommitSubjectsCallsRecordsInvocations proves every call is
// recorded in order with its exact arguments.
func TestFake_CommitSubjectsCallsRecordsInvocations(t *testing.T) {
	fc := NewFake()
	cf := fc.AsGithubReadOnly().(BundleCommitSubjects)

	if _, err := cf.CommitSubjects("outbox1", "main", "branch1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := cf.CommitSubjects("outbox2", "develop", "branch2"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(fc.CommitSubjectsCalls) != 2 {
		t.Fatalf("got %d calls, want 2", len(fc.CommitSubjectsCalls))
	}
	want := []CommitSubjectsCall{
		{OutboxDir: "outbox1", Base: "main", Ref: "branch1"},
		{OutboxDir: "outbox2", Base: "develop", Ref: "branch2"},
	}
	for i, w := range want {
		if fc.CommitSubjectsCalls[i] != w {
			t.Fatalf("call %d = %+v, want %+v", i, fc.CommitSubjectsCalls[i], w)
		}
	}
}

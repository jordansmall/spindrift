package forge

import (
	"errors"
	"testing"
)

// commitSubjects stays unexported on HostMediationFake, the same isolation
// relayBundle and createDraftPR have, so a bare *Fake used as a CodeForge
// elsewhere never silently starts satisfying forge.BundleCommitSubjects. See
// HostMediationFake's doc comment in fake_hostmediation.go.
func TestFake_BareFakeDoesNotSatisfyBundleCommitSubjects(t *testing.T) {
	f := NewFake()
	if _, ok := any(f).(BundleCommitSubjects); ok {
		t.Fatal("bare *Fake must not satisfy BundleCommitSubjects directly")
	}
}

func TestFake_AsGithubReadOnlySatisfiesBundleCommitSubjects(t *testing.T) {
	cf := NewFake().AsGithubReadOnly()
	if _, ok := cf.(BundleCommitSubjects); !ok {
		t.Fatal("AsGithubReadOnly() must satisfy BundleCommitSubjects")
	}
}

// An unscripted Fake mirrors RelayBundle's own zero-value default when
// RelayBundleErr is unset.
func TestFake_CommitSubjectsDefaultsToNilNil(t *testing.T) {
	cf := NewFake().AsGithubReadOnly().(BundleCommitSubjects)
	subjects, err := cf.CommitSubjects("outbox", "main", "branch")
	if subjects != nil || err != nil {
		t.Fatalf("got (%v, %v), want (nil, nil)", subjects, err)
	}
}

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

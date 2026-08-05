package forge

import "fmt"

// RecordLandingCall records a single RecordLanding invocation.
type RecordLandingCall struct {
	Num, Landing string
}

// PostIssueCall records a single PostIssue invocation.
type PostIssueCall struct {
	Title, Body string
	Labels      []string
}

// RecordLanding implements the optional LandingRecorder surface (ADR 0029),
// recording each call for tests to assert against.
func (f *Fake) RecordLanding(num, landing string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.RecordLandingCalls = append(f.RecordLandingCalls, RecordLandingCall{num, landing})
	return f.RecordLandingErr
}

// postIssue backs the optional HostPostedIssueFiler surface (issue #2018),
// recording each call for tests to assert against. Deliberately unexported,
// the same reasoning as createDraftPR: only issueFilerTracker's own exported
// PostIssue (reachable exclusively through AsIssueFiler()) calls it, so a
// bare *Fake used as an IssueTracker in every other test never silently
// starts satisfying forge.HostPostedIssueFiler.
func (f *Fake) postIssue(title, body string, labels []string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.PostIssueCalls = append(f.PostIssueCalls, PostIssueCall{Title: title, Body: body, Labels: labels})
	if f.PostIssueErr != nil {
		return "", f.PostIssueErr
	}
	return f.PostIssueURL, nil
}

// CloseIssue implements the optional IssueCloser surface (ADR 0029), setting
// the issue's State to IssueClosed and recording the call for tests to
// assert against.
func (f *Fake) CloseIssue(num string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.CloseIssueCalls = append(f.CloseIssueCalls, num)
	if f.CloseIssueErr != nil {
		return f.CloseIssueErr
	}
	iss, ok := f.issues[num]
	if !ok {
		return fmt.Errorf("issue %s not found", num)
	}
	iss.State = IssueClosed
	f.issues[num] = iss
	return nil
}

// CloseMergedIssue implements the optional MergeCloser surface (issue
// #1892), setting the issue's State to IssueClosed and recording the call
// (separately from CloseIssueCalls) for tests to assert against.
func (f *Fake) CloseMergedIssue(num string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.CloseMergedIssueCalls = append(f.CloseMergedIssueCalls, num)
	if f.CloseMergedIssueErr != nil {
		return f.CloseMergedIssueErr
	}
	iss, ok := f.issues[num]
	if !ok {
		return fmt.Errorf("issue %s not found", num)
	}
	iss.State = IssueClosed
	f.issues[num] = iss
	return nil
}

// FlagAbandoned implements the optional AbandonedFlagger surface (ADR 0029),
// setting the issue's Abandoned field and recording the call for tests to
// assert against.
func (f *Fake) FlagAbandoned(num string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.FlagAbandonedCalls = append(f.FlagAbandonedCalls, num)
	if f.FlagAbandonedErr != nil {
		return f.FlagAbandonedErr
	}
	iss, ok := f.issues[num]
	if !ok {
		return fmt.Errorf("issue %s not found", num)
	}
	iss.Abandoned = true
	f.issues[num] = iss
	return nil
}

package forge

// HostMediationFake holds every field the optional BundleRelay, DraftPRCreator,
// HostPostedIssueFiler, LandingRef, LandingRepair, and LandingContainmentQuery
// interfaces read or write. Its backing methods stay unexported so a bare *Fake
// used as a CodeForge or IssueTracker cannot satisfy those gated interfaces by
// accident; only the wrapper types in fake.go reach them, through promotion.
type HostMediationFake struct {
	*core

	// RelayBundleErr, if non-nil, fails every RelayBundle call, scripting
	// CODE_FORGE=local's missing or malformed bundle failure (ADR 0033).
	// Only AsLocal() reaches it.
	RelayBundleErr   error
	RelayBundleCalls []RelayBundleCall

	// CreateDraftPRURL is the URL a successful CreateDraftPR returns (issue
	// #1919). Only AsGithubReadOnly() reaches it.
	CreateDraftPRURL string
	CreateDraftPRErr error
	// CreateDraftPRAdoptHead, if non-empty, is the head a CreateDraftPR call
	// adopts rather than failing (issue #2407 slice 3), returning
	// CreateDraftPRAdoptedURL with no error. Checked before
	// CreateDraftPRErr, because the real adapters only adopt a branch's
	// existing open PR after the create call itself fails with a 409.
	CreateDraftPRAdoptHead  string
	CreateDraftPRAdoptedURL string
	CreateDraftPRCalls      []CreateDraftPRCall

	// CommitSubjectsResult scripts the subjects settle's PR-intent fallback
	// (issue #2447) rebuilds a draft PR's title and body from. Only
	// AsGithubReadOnly() reaches it.
	CommitSubjectsResult []string
	CommitSubjectsErr    error
	CommitSubjectsCalls  []CommitSubjectsCall

	// PostIssueURL is the URL a successful PostIssue returns (issue #2018).
	// Only AsIssueFiler() reaches it.
	PostIssueURL        string
	PostIssueErr        error
	PostIssueCalls      []PostIssueCall
	LandingRefValue     string
	LandingRefErr       error
	LandingRefCallCount int

	// landingContainedResults scripts LandingContained per (landing, parent)
	// pair. Only AsLocal() reaches it.
	landingContainedResults map[landingParentKey]landingContainedResult
	LandingContainedCalls   []LandingContainedCall

	// integrationTipResults scripts IntegrationTip's success result per
	// parent. Only AsLocal() reaches it.
	integrationTipResults map[string]string
	IntegrationTipErr     error
	IntegrationTipCalls   []string
}

// RelayBundleCall records a single RelayBundle invocation.
type RelayBundleCall struct {
	OutboxDir, Ref string
}

// CreateDraftPRCall records a single CreateDraftPR invocation.
type CreateDraftPRCall struct {
	Title, Body, Base, Head string
}

// CommitSubjectsCall records a single CommitSubjects invocation.
type CommitSubjectsCall struct {
	OutboxDir, Base, Ref string
}

// PostIssueCall records a single PostIssue invocation.
type PostIssueCall struct {
	Title, Body string
	Labels      []string
}

// relayBundle backs the optional BundleRelay interface (ADR 0033).
func (hm *HostMediationFake) relayBundle(outboxDir, ref string) error {
	hm.mu.Lock()
	defer hm.mu.Unlock()
	hm.RelayBundleCalls = append(hm.RelayBundleCalls, RelayBundleCall{OutboxDir: outboxDir, Ref: ref})
	return hm.RelayBundleErr
}

// createDraftPR backs the optional DraftPRCreator interface (issue #1919).
func (hm *HostMediationFake) createDraftPR(title, body, base, head string) (string, bool, error) {
	hm.mu.Lock()
	defer hm.mu.Unlock()
	hm.CreateDraftPRCalls = append(hm.CreateDraftPRCalls, CreateDraftPRCall{Title: title, Body: body, Base: base, Head: head})
	if hm.CreateDraftPRAdoptHead != "" && head == hm.CreateDraftPRAdoptHead {
		return hm.CreateDraftPRAdoptedURL, false, nil
	}
	if hm.CreateDraftPRErr != nil {
		return "", false, hm.CreateDraftPRErr
	}
	return hm.CreateDraftPRURL, true, nil
}

// commitSubjects backs the optional BundleCommitSubjects interface (issue #2447).
func (hm *HostMediationFake) commitSubjects(outboxDir, base, ref string) ([]string, error) {
	hm.mu.Lock()
	defer hm.mu.Unlock()
	hm.CommitSubjectsCalls = append(hm.CommitSubjectsCalls, CommitSubjectsCall{OutboxDir: outboxDir, Base: base, Ref: ref})
	if hm.CommitSubjectsErr != nil {
		return nil, hm.CommitSubjectsErr
	}
	return hm.CommitSubjectsResult, nil
}

// landingRef backs the optional LandingRef interface (ADR 0033).
func (hm *HostMediationFake) landingRef() (string, error) {
	hm.mu.Lock()
	defer hm.mu.Unlock()
	hm.LandingRefCallCount++
	if hm.LandingRefErr != nil {
		return "", hm.LandingRefErr
	}
	return hm.LandingRefValue, nil
}

type landingParentKey struct{ landing, parent string }

type landingContainedResult struct {
	contained bool
	err       error
}

// LandingContainedCall records a single LandingContained invocation.
type LandingContainedCall struct {
	Landing, Parent string
}

// SetLandingContained scripts LandingContained's result for landing's stored
// string form paired with scope's parent. Pass an error only for a genuine
// failure; a malformed landing or a conflicting or unlanded merge is scripted
// as contained=false, err=nil.
func (hm *HostMediationFake) SetLandingContained(landing, parent string, contained bool, err error) {
	hm.mu.Lock()
	defer hm.mu.Unlock()
	if hm.landingContainedResults == nil {
		hm.landingContainedResults = map[landingParentKey]landingContainedResult{}
	}
	hm.landingContainedResults[landingParentKey{landing, parent}] = landingContainedResult{contained: contained, err: err}
}

// landingContained backs the optional LandingContainmentQuery interface (ADR
// 0029, ADR 0033, issue #1809, issue #2129, issue #2151). An unscripted pair
// defaults to contained=false, nil, the same posture as a malformed or
// not-yet-merged landing in production.
func (hm *HostMediationFake) landingContained(landing Landing, scope SeedScope) (bool, error) {
	hm.mu.Lock()
	defer hm.mu.Unlock()
	key := landingParentKey{landing.String(), scope.Parent()}
	hm.LandingContainedCalls = append(hm.LandingContainedCalls, LandingContainedCall{Landing: key.landing, Parent: key.parent})
	res := hm.landingContainedResults[key]
	return res.contained, res.err
}

// SetIntegrationTip scripts the "<branch>@<sha>" reference IntegrationTip
// returns for parent. IntegrationTipErr still overrides it on every call, the
// same precedence LandingRefErr has over LandingRefValue.
func (hm *HostMediationFake) SetIntegrationTip(parent, ref string) {
	hm.mu.Lock()
	defer hm.mu.Unlock()
	if hm.integrationTipResults == nil {
		hm.integrationTipResults = map[string]string{}
	}
	hm.integrationTipResults[parent] = ref
}

// integrationTip backs the optional LandingRepair interface (ADR 0029, ADR
// 0033, issue #1809).
func (hm *HostMediationFake) integrationTip(parent string) (string, error) {
	hm.mu.Lock()
	defer hm.mu.Unlock()
	hm.IntegrationTipCalls = append(hm.IntegrationTipCalls, parent)
	if hm.IntegrationTipErr != nil {
		return "", hm.IntegrationTipErr
	}
	return hm.integrationTipResults[parent], nil
}

// postIssue backs the optional HostPostedIssueFiler interface (issue #2018).
func (hm *HostMediationFake) postIssue(title, body string, labels []string) (string, error) {
	hm.mu.Lock()
	defer hm.mu.Unlock()
	hm.PostIssueCalls = append(hm.PostIssueCalls, PostIssueCall{Title: title, Body: body, Labels: labels})
	if hm.PostIssueErr != nil {
		return "", hm.PostIssueErr
	}
	return hm.PostIssueURL, nil
}

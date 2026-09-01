package forge

// HostMediationFake is the host-mediation-capability slice of Fake, holding
// every field the optional BundleRelay, DraftPRCreator, HostPostedIssueFiler,
// LandingRef, LandingRepair, and LandingContainmentQuery surfaces read or
// write. It embeds *core — see core's doc comment for the admission rule.
//
// Every backing method below is deliberately unexported: these must NOT be
// methods Go's type-assertion machinery can see on *Fake, or a bare *Fake used
// as a CodeForge/IssueTracker (the majority of settle/reconcile tests) would
// silently start satisfying those gated interfaces too. Only fake.go's wrapper
// types — localForge, githubReadOnlyForge, issueFilerTracker, reachable
// exclusively through AsLocal(), AsGithubReadOnly(), and AsIssueFiler() — call
// them, resolving through promotion from Fake's embedded *HostMediationFake.
//
// Each *Err field, when non-nil, is returned by every call to its method; each
// *Calls slice records invocations in order.
type HostMediationFake struct {
	*core

	// Reachable only through AsLocal(), the only wrapper implementing
	// forge.BundleRelay. Scripts CODE_FORGE=local's missing/malformed-bundle
	// failure mode (ADR 0033).
	RelayBundleErr   error
	RelayBundleCalls []RelayBundleCall

	// Reachable only through AsGithubReadOnly().
	CreateDraftPRURL string
	CreateDraftPRErr error
	// CreateDraftPRAdoptHead, if non-empty, is the head a call adopts rather
	// than failing: it returns CreateDraftPRAdoptedURL with no error even when
	// CreateDraftPRErr is set, mirroring github/forgejo catching an
	// already-exists/409 refusal and adopting the branch's existing open PR.
	// Checked before CreateDraftPRErr, since the real adapters only reach
	// adoption after the create call itself already failed.
	CreateDraftPRAdoptHead  string
	CreateDraftPRAdoptedURL string
	CreateDraftPRCalls      []CreateDraftPRCall

	// Reachable only through AsGithubReadOnly(). Scripts the commit subjects
	// settle's PR-intent-fallback path reconstructs a draft PR's title/body
	// from.
	CommitSubjectsResult []string
	CommitSubjectsErr    error
	CommitSubjectsCalls  []CommitSubjectsCall

	// Reachable only through AsIssueFiler().
	PostIssueURL   string
	PostIssueErr   error
	PostIssueCalls []PostIssueCall

	LandingRefValue     string
	LandingRefErr       error
	LandingRefCallCount int

	// landingContainedResults scripts LandingContained per (landing string,
	// parent) pair, defaulting to contained=false, nil when unscripted — the
	// same "stays open" posture production takes. Reachable only through
	// AsLocal().
	landingContainedResults map[landingParentKey]landingContainedResult
	LandingContainedCalls   []LandingContainedCall

	// Reachable only through AsLocal().
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

// relayBundle backs the optional BundleRelay surface (ADR 0033).
func (hm *HostMediationFake) relayBundle(outboxDir, ref string) error {
	hm.mu.Lock()
	defer hm.mu.Unlock()
	hm.RelayBundleCalls = append(hm.RelayBundleCalls, RelayBundleCall{OutboxDir: outboxDir, Ref: ref})
	return hm.RelayBundleErr
}

// createDraftPR backs the optional DraftPRCreator surface.
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

// commitSubjects backs the optional BundleCommitSubjects surface.
func (hm *HostMediationFake) commitSubjects(outboxDir, base, ref string) ([]string, error) {
	hm.mu.Lock()
	defer hm.mu.Unlock()
	hm.CommitSubjectsCalls = append(hm.CommitSubjectsCalls, CommitSubjectsCall{OutboxDir: outboxDir, Base: base, Ref: ref})
	if hm.CommitSubjectsErr != nil {
		return nil, hm.CommitSubjectsErr
	}
	return hm.CommitSubjectsResult, nil
}

// landingRef backs the optional LandingRef surface (ADR 0033).
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

// SetLandingContained scripts LandingContained(landing, scope)'s result for
// landing's stored-string form paired with scope's parent — contained, or a
// genuine error distinct from the normal "not contained" (malformed landing,
// conflicting/unlanded merge) outcome, which callers script as
// contained=false, err=nil instead.
func (hm *HostMediationFake) SetLandingContained(landing, parent string, contained bool, err error) {
	hm.mu.Lock()
	defer hm.mu.Unlock()
	if hm.landingContainedResults == nil {
		hm.landingContainedResults = map[landingParentKey]landingContainedResult{}
	}
	hm.landingContainedResults[landingParentKey{landing, parent}] = landingContainedResult{contained: contained, err: err}
}

// landingContained backs the optional LandingContainmentQuery surface (ADR
// 0029, ADR 0033). An unscripted (landing.String(), scope.Parent()) pair
// defaults to contained=false, nil — the same posture as a malformed or
// not-yet-merged landing in production.
func (hm *HostMediationFake) landingContained(landing Landing, scope SeedScope) (bool, error) {
	hm.mu.Lock()
	defer hm.mu.Unlock()
	key := landingParentKey{landing.String(), scope.Parent()}
	hm.LandingContainedCalls = append(hm.LandingContainedCalls, LandingContainedCall{Landing: key.landing, Parent: key.parent})
	res := hm.landingContainedResults[key]
	return res.contained, res.err
}

// SetIntegrationTip scripts IntegrationTip(parent)'s success result — the
// resolved landing-ready "<branch>@<sha>" reference. IntegrationTipErr, when
// set, takes precedence over it.
func (hm *HostMediationFake) SetIntegrationTip(parent, ref string) {
	hm.mu.Lock()
	defer hm.mu.Unlock()
	if hm.integrationTipResults == nil {
		hm.integrationTipResults = map[string]string{}
	}
	hm.integrationTipResults[parent] = ref
}

// integrationTip backs the optional LandingRepair surface (ADR 0029, ADR 0033).
func (hm *HostMediationFake) integrationTip(parent string) (string, error) {
	hm.mu.Lock()
	defer hm.mu.Unlock()
	hm.IntegrationTipCalls = append(hm.IntegrationTipCalls, parent)
	if hm.IntegrationTipErr != nil {
		return "", hm.IntegrationTipErr
	}
	return hm.integrationTipResults[parent], nil
}

// postIssue backs the optional HostPostedIssueFiler surface.
func (hm *HostMediationFake) postIssue(title, body string, labels []string) (string, error) {
	hm.mu.Lock()
	defer hm.mu.Unlock()
	hm.PostIssueCalls = append(hm.PostIssueCalls, PostIssueCall{Title: title, Body: body, Labels: labels})
	if hm.PostIssueErr != nil {
		return "", hm.PostIssueErr
	}
	return hm.PostIssueURL, nil
}

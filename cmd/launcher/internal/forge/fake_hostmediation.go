package forge

// HostMediationFake is the host-mediation-capability slice of Fake, holding
// every field the optional BundleRelay, DraftPRCreator, HostPostedIssueFiler,
// LandingRef, LandingRepair, and LandingContainmentQuery surfaces read or
// write. It embeds *core — see core's doc comment for the admission rule —
// so its methods can reach mu/etc. directly.
//
// Every backing method below (relayBundle, createDraftPR, postIssue,
// landingRef, landingContained, integrationTip) is deliberately unexported:
// these must NOT be methods Go's type-assertion machinery can see directly
// on *Fake, or a bare *Fake used as a CodeForge/IssueTracker elsewhere (the
// majority of settle/reconcile tests) would silently start satisfying the
// gated interfaces (BundleRelay, DraftPRCreator, HostPostedIssueFiler,
// LandingRef, LandingRepair, LandingContainmentQuery) too. Only the wrapper
// types in fake.go — localForge, githubReadOnlyForge, issueFilerTracker,
// reachable exclusively through AsLocal(), AsGithubReadOnly(), and
// AsIssueFiler() — call these methods by name, resolving them through
// promotion from Fake's embedded *HostMediationFake.
type HostMediationFake struct {
	// *core is the shared substrate promoted through to HostMediationFake —
	// see core's doc comment for the admission rule.
	*core

	// RelayBundleErr, if non-nil, is returned by every RelayBundle call —
	// scripts CODE_FORGE=local's missing/malformed-bundle failure mode (ADR
	// 0033). Only reachable through AsLocal(), the only wrapper implementing
	// forge.BundleRelay.
	RelayBundleErr error
	// RelayBundleCalls records all RelayBundle invocations in order.
	RelayBundleCalls []RelayBundleCall

	// CreateDraftPRURL is returned by every CreateDraftPR call on success —
	// scripts the URL of the draft PR the github read-only adapter opens
	// host-side (issue #1919). Only reachable through AsGithubReadOnly().
	CreateDraftPRURL string
	// CreateDraftPRErr, if non-nil, is returned by every CreateDraftPR call,
	// unless head == CreateDraftPRAdoptHead (checked first).
	CreateDraftPRErr error
	// CreateDraftPRAdoptHead, if non-empty, is the head a CreateDraftPR call
	// adopts rather than failing (issue #2407 slice 3): a call for this
	// exact head returns CreateDraftPRAdoptedURL with no error, even when
	// CreateDraftPRErr is set, mirroring github/forgejo's CreateDraftPR
	// catching an already-exists/409 create refusal and adopting the
	// branch's existing open PR via OpenPRForBranch instead of surfacing
	// it. Checked before CreateDraftPRErr, since the real adapters only
	// ever reach adoption after the create call itself already failed.
	CreateDraftPRAdoptHead string
	// CreateDraftPRAdoptedURL is returned instead of CreateDraftPRErr when
	// head == CreateDraftPRAdoptHead.
	CreateDraftPRAdoptedURL string
	// CreateDraftPRCalls records all CreateDraftPR invocations in order.
	CreateDraftPRCalls []CreateDraftPRCall

	// CommitSubjectsResult is returned by every CommitSubjects call on
	// success — scripts the commit subjects settle's PR-intent-fallback path
	// (issue #2447) reconstructs a draft PR's title/body from. Only
	// reachable through AsGithubReadOnly().
	CommitSubjectsResult []string
	// CommitSubjectsErr, if non-nil, is returned by every CommitSubjects
	// call.
	CommitSubjectsErr error
	// CommitSubjectsCalls records all CommitSubjects invocations in order.
	CommitSubjectsCalls []CommitSubjectsCall

	// PostIssueURL is returned by every PostIssue call on success — scripts
	// the URL of the issue the Launcher files host-side (issue #2018). Only
	// reachable through AsIssueFiler().
	PostIssueURL string
	// PostIssueErr, if non-nil, is returned by every PostIssue call.
	PostIssueErr error
	// PostIssueCalls records all PostIssue invocations in order.
	PostIssueCalls []PostIssueCall
	// LandingRefValue is returned by LandingRef on success.
	LandingRefValue string
	// LandingRefErr, if non-nil, is returned by every LandingRef call.
	LandingRefErr error
	// LandingRefCallCount counts every LandingRef invocation.
	LandingRefCallCount int

	// landingContainedResults scripts LandingContained's result per
	// (landing string, parent) pair, defaulting to contained=false, nil when
	// unscripted — the same "stays open" default the three predecessors this
	// issue collapsed used. Only reachable through AsLocal(), the only
	// wrapper implementing forge.LandingContainmentQuery.
	landingContainedResults map[landingParentKey]landingContainedResult
	// LandingContainedCalls records every LandingContained invocation in
	// order.
	LandingContainedCalls []LandingContainedCall

	// integrationTipResults scripts IntegrationTip's success result per
	// parent. Only reachable through AsLocal().
	integrationTipResults map[string]string
	// IntegrationTipErr, if non-nil, is returned by every IntegrationTip call.
	IntegrationTipErr error
	// IntegrationTipCalls records every IntegrationTip invocation in order.
	IntegrationTipCalls []string
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

// relayBundle backs the optional BundleRelay surface (ADR 0033), recording
// each call for tests to assert against. Deliberately unexported: unlike
// Merge/Rebase/RecordLanding, this must NOT be a method Go's type-assertion
// machinery can see directly on *Fake, or every settle test constructing
// Settle with a bare *Fake as its CodeForge (the github/git-flow majority)
// would silently start satisfying forge.BundleRelay too. Only localForge's
// own exported RelayBundle (reachable exclusively through AsLocal()) calls
// this.
func (hm *HostMediationFake) relayBundle(outboxDir, ref string) error {
	hm.mu.Lock()
	defer hm.mu.Unlock()
	hm.RelayBundleCalls = append(hm.RelayBundleCalls, RelayBundleCall{OutboxDir: outboxDir, Ref: ref})
	return hm.RelayBundleErr
}

// createDraftPR backs the optional DraftPRCreator surface (issue #1919),
// recording each call for tests to assert against. Deliberately unexported,
// the same reasoning as relayBundle: only githubReadOnlyForge's own exported
// CreateDraftPR (reachable exclusively through AsGithubReadOnly()) calls it,
// so a bare *Fake used as a github-shaped CodeForge in every other settle
// test never silently starts satisfying forge.DraftPRCreator.
func (hm *HostMediationFake) createDraftPR(title, body, base, head string) (string, error) {
	hm.mu.Lock()
	defer hm.mu.Unlock()
	hm.CreateDraftPRCalls = append(hm.CreateDraftPRCalls, CreateDraftPRCall{Title: title, Body: body, Base: base, Head: head})
	if hm.CreateDraftPRAdoptHead != "" && head == hm.CreateDraftPRAdoptHead {
		return hm.CreateDraftPRAdoptedURL, nil
	}
	if hm.CreateDraftPRErr != nil {
		return "", hm.CreateDraftPRErr
	}
	return hm.CreateDraftPRURL, nil
}

// commitSubjects backs the optional BundleCommitSubjects surface (issue
// #2447), recording each call for tests to assert against. Deliberately
// unexported, the same reasoning as relayBundle: only githubReadOnlyForge's
// own exported CommitSubjects (reachable exclusively through
// AsGithubReadOnly()) calls it, so a bare *Fake used as a github-shaped
// CodeForge in every other settle test never silently starts satisfying
// forge.BundleCommitSubjects.
func (hm *HostMediationFake) commitSubjects(outboxDir, base, ref string) ([]string, error) {
	hm.mu.Lock()
	defer hm.mu.Unlock()
	hm.CommitSubjectsCalls = append(hm.CommitSubjectsCalls, CommitSubjectsCall{OutboxDir: outboxDir, Base: base, Ref: ref})
	if hm.CommitSubjectsErr != nil {
		return nil, hm.CommitSubjectsErr
	}
	return hm.CommitSubjectsResult, nil
}

// landingRef backs the optional LandingRef surface (ADR 0033), the same
// AsLocal()-only restriction as relayBundle above, for the same reason.
func (hm *HostMediationFake) landingRef() (string, error) {
	hm.mu.Lock()
	defer hm.mu.Unlock()
	hm.LandingRefCallCount++
	if hm.LandingRefErr != nil {
		return "", hm.LandingRefErr
	}
	return hm.LandingRefValue, nil
}

// landingParentKey keys landingContainedResults on the (landing string,
// parent) pair LandingContained's scope.Parent() takes alongside landing's
// own String() form.
type landingParentKey struct{ landing, parent string }

// landingContainedResult scripts a single SetLandingContained entry.
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
// 0029, ADR 0033, issue #1809, issue #2129, issue #2151), the same
// AsLocal()-only restriction as relayBundle above. An unscripted
// (landing.String(), scope.Parent()) pair defaults to contained=false, nil —
// the same posture as a malformed or not-yet-merged landing in production.
func (hm *HostMediationFake) landingContained(landing Landing, scope SeedScope) (bool, error) {
	hm.mu.Lock()
	defer hm.mu.Unlock()
	key := landingParentKey{landing.String(), scope.Parent()}
	hm.LandingContainedCalls = append(hm.LandingContainedCalls, LandingContainedCall{Landing: key.landing, Parent: key.parent})
	res := hm.landingContainedResults[key]
	return res.contained, res.err
}

// SetIntegrationTip scripts IntegrationTip(parent)'s success result — the
// resolved landing-ready "<branch>@<sha>" reference IntegrationTipErr's
// precedence overrides for every call, mirroring LandingRefErr over
// LandingRefValue.
func (hm *HostMediationFake) SetIntegrationTip(parent, ref string) {
	hm.mu.Lock()
	defer hm.mu.Unlock()
	if hm.integrationTipResults == nil {
		hm.integrationTipResults = map[string]string{}
	}
	hm.integrationTipResults[parent] = ref
}

// integrationTip backs the optional LandingRepair surface (ADR 0029, ADR
// 0033, issue #1809), the same AsLocal()-only restriction as relayBundle
// above.
func (hm *HostMediationFake) integrationTip(parent string) (string, error) {
	hm.mu.Lock()
	defer hm.mu.Unlock()
	hm.IntegrationTipCalls = append(hm.IntegrationTipCalls, parent)
	if hm.IntegrationTipErr != nil {
		return "", hm.IntegrationTipErr
	}
	return hm.integrationTipResults[parent], nil
}

// postIssue backs the optional HostPostedIssueFiler surface (issue #2018),
// recording each call for tests to assert against. Deliberately unexported,
// the same reasoning as createDraftPR: only issueFilerTracker's own exported
// PostIssue (reachable exclusively through AsIssueFiler()) calls it, so a
// bare *Fake used as an IssueTracker in every other test never silently
// starts satisfying forge.HostPostedIssueFiler.
func (hm *HostMediationFake) postIssue(title, body string, labels []string) (string, error) {
	hm.mu.Lock()
	defer hm.mu.Unlock()
	hm.PostIssueCalls = append(hm.PostIssueCalls, PostIssueCall{Title: title, Body: body, Labels: labels})
	if hm.PostIssueErr != nil {
		return "", hm.PostIssueErr
	}
	return hm.PostIssueURL, nil
}

package forge

// RelayBundleCall records a single RelayBundle invocation.
type RelayBundleCall struct {
	OutboxDir, Ref string
}

// CreateDraftPRCall records a single CreateDraftPR invocation.
type CreateDraftPRCall struct {
	Title, Body, Base, Head string
}

// relayBundle backs the optional BundleRelay surface (ADR 0033), recording
// each call for tests to assert against. Deliberately unexported: unlike
// Merge/Rebase/RecordLanding, this must NOT be a method Go's type-assertion
// machinery can see directly on *Fake, or every settle test constructing
// Settle with a bare *Fake as its CodeForge (the github/git-flow majority)
// would silently start satisfying forge.BundleRelay too. Only localForge's
// own exported RelayBundle (reachable exclusively through AsLocal()) calls
// this.
func (f *Fake) relayBundle(outboxDir, ref string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.RelayBundleCalls = append(f.RelayBundleCalls, RelayBundleCall{OutboxDir: outboxDir, Ref: ref})
	return f.RelayBundleErr
}

// createDraftPR backs the optional DraftPRCreator surface (issue #1919),
// recording each call for tests to assert against. Deliberately unexported,
// the same reasoning as relayBundle: only githubReadOnlyForge's own exported
// CreateDraftPR (reachable exclusively through AsGithubReadOnly()) calls it,
// so a bare *Fake used as a github-shaped CodeForge in every other settle
// test never silently starts satisfying forge.DraftPRCreator.
func (f *Fake) createDraftPR(title, body, base, head string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.CreateDraftPRCalls = append(f.CreateDraftPRCalls, CreateDraftPRCall{Title: title, Body: body, Base: base, Head: head})
	if f.CreateDraftPRErr != nil {
		return "", f.CreateDraftPRErr
	}
	return f.CreateDraftPRURL, nil
}

// landingRef backs the optional LandingRef surface (ADR 0033), the same
// AsLocal()-only restriction as relayBundle above, for the same reason.
func (f *Fake) landingRef() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.LandingRefCallCount++
	if f.LandingRefErr != nil {
		return "", f.LandingRefErr
	}
	return f.LandingRefValue, nil
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
func (f *Fake) SetLandingContained(landing, parent string, contained bool, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.landingContainedResults == nil {
		f.landingContainedResults = map[landingParentKey]landingContainedResult{}
	}
	f.landingContainedResults[landingParentKey{landing, parent}] = landingContainedResult{contained: contained, err: err}
}

// landingContained backs the optional LandingContainmentQuery surface (ADR
// 0029, ADR 0033, issue #1809, issue #2129, issue #2151), the same
// AsLocal()-only restriction as relayBundle above. An unscripted
// (landing.String(), scope.Parent()) pair defaults to contained=false, nil —
// the same posture as a malformed or not-yet-merged landing in production.
func (f *Fake) landingContained(landing Landing, scope SeedScope) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := landingParentKey{landing.String(), scope.Parent()}
	f.LandingContainedCalls = append(f.LandingContainedCalls, LandingContainedCall{Landing: key.landing, Parent: key.parent})
	res := f.landingContainedResults[key]
	return res.contained, res.err
}

// SetIntegrationTip scripts IntegrationTip(parent)'s success result — the
// resolved landing-ready "<branch>@<sha>" reference IntegrationTipErr's
// precedence overrides for every call, mirroring LandingRefErr over
// LandingRefValue.
func (f *Fake) SetIntegrationTip(parent, ref string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.integrationTipResults == nil {
		f.integrationTipResults = map[string]string{}
	}
	f.integrationTipResults[parent] = ref
}

// integrationTip backs the optional LandingRepair surface (ADR 0029, ADR
// 0033, issue #1809), the same AsLocal()-only restriction as relayBundle
// above.
func (f *Fake) integrationTip(parent string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.IntegrationTipCalls = append(f.IntegrationTipCalls, parent)
	if f.IntegrationTipErr != nil {
		return "", f.IntegrationTipErr
	}
	return f.integrationTipResults[parent], nil
}

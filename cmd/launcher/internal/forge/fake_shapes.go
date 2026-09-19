package forge

// issueFilerTracker adds HostPostedIssueFiler to a Fake (issue #2028, issue
// #1964). It stays gated behind AsIssueFiler so a bare *Fake used as an
// IssueTracker elsewhere never silently satisfies HostPostedIssueFiler too.
type issueFilerTracker struct {
	IssueTracker
	f *Fake
}

// AsIssueFiler returns f wrapped so it satisfies IssueTracker and HostPostedIssueFiler.
func (f *Fake) AsIssueFiler() IssueTracker { return issueFilerTracker{IssueTracker: f, f: f} }

func (i issueFilerTracker) PostIssue(title, body string, labels []string) (string, error) {
	return i.f.postIssue(title, body, labels)
}

var _ HostPostedIssueFiler = issueFilerTracker{}

// noLandingIssueTracker hides a Fake's RecordLanding and CloseIssue so a
// type assertion against either reports absence, matching the github and
// jira adapters, which implement neither (ADR 0029).
type noLandingIssueTracker struct{ IssueTracker }

// IsGithubTracker implements the optional GithubTracker marker (issue
// #2341) so this shape still reads as github-shaped to the settle tests
// built on AsNoLandingRecorder, which expect an injected Closes #N
// reference. *Fake has no such method to promote.
func (noLandingIssueTracker) IsGithubTracker() bool { return true }

// AsNoLandingRecorder returns f in the github adapter's shape: GithubTracker, but no LandingRecorder or IssueCloser.
func (f *Fake) AsNoLandingRecorder() IssueTracker { return noLandingIssueTracker{f} }

// localShapedIssueTracker matches the real local adapter (issue #1892):
// IssueCloser but never MergeCloser, even when paired with a PRForge Code
// Forge, since ISSUE_TRACKER=local + CODE_FORGE=github is a valid pairing.
type localShapedIssueTracker struct {
	IssueTracker
	f *Fake
}

func (l localShapedIssueTracker) RecordLanding(num, landing string) error {
	return l.f.RecordLanding(num, landing)
}

// RecordLandingPass promotes the same optional method RecordLanding does
// (issue #2983): the real local adapter implements both on one *LocalTracker.
func (l localShapedIssueTracker) RecordLandingPass(num string, pass int, kind string) error {
	return l.f.RecordLandingPass(num, pass, kind)
}

func (l localShapedIssueTracker) CloseIssue(num string) error {
	return l.f.CloseIssue(num)
}

// AsLocalShaped returns f in the local adapter's shape: LandingRecorder, LandingPassRecorder, and IssueCloser, but no MergeCloser.
func (f *Fake) AsLocalShaped() IssueTracker {
	return localShapedIssueTracker{IssueTracker: f, f: f}
}

// localIssueFilerTracker is the real local adapter's actual combined shape
// (issue #2592). Neither existing double covers it: AsLocalShaped never
// gained PostIssue, and AsIssueFiler embeds the IssueTracker interface value
// rather than *Fake, so it promotes neither RecordLanding nor CloseIssue.
type localIssueFilerTracker struct {
	IssueTracker
	f *Fake
}

func (l localIssueFilerTracker) RecordLanding(num, landing string) error {
	return l.f.RecordLanding(num, landing)
}

// RecordLandingPass promotes the same optional method RecordLanding does
// (issue #2983): the real local adapter implements both on one *LocalTracker.
func (l localIssueFilerTracker) RecordLandingPass(num string, pass int, kind string) error {
	return l.f.RecordLandingPass(num, pass, kind)
}

func (l localIssueFilerTracker) CloseIssue(num string) error {
	return l.f.CloseIssue(num)
}

func (l localIssueFilerTracker) PostIssue(title, body string, labels []string) (string, error) {
	return l.f.postIssue(title, body, labels)
}

// AsLocalIssueFiler returns f in the local adapter's combined shape, AsLocalShaped plus HostPostedIssueFiler (issue #2592).
func (f *Fake) AsLocalIssueFiler() IssueTracker {
	return localIssueFilerTracker{IssueTracker: f, f: f}
}

var _ HostPostedIssueFiler = localIssueFilerTracker{}
var _ LandingRecorder = localIssueFilerTracker{}
var _ LandingPassRecorder = localIssueFilerTracker{}
var _ IssueCloser = localIssueFilerTracker{}

// forgejoShapedIssueTracker adds MergeCloser while hiding RecordLanding and
// CloseIssue by embedding the IssueTracker interface value rather than
// *Fake. It omits GithubTracker because forgejo issue numbers are foreign to
// GitHub's Closes-keyword namespace (issue #2341).
type forgejoShapedIssueTracker struct {
	IssueTracker
	f *Fake
}

func (fs forgejoShapedIssueTracker) CloseMergedIssue(num string) error {
	return fs.f.CloseMergedIssue(num)
}

// AsForgejoShaped returns f in the forgejo adapter's shape: MergeCloser, but no LandingRecorder, IssueCloser, or GithubTracker.
func (f *Fake) AsForgejoShaped() IssueTracker {
	return forgejoShapedIssueTracker{IssueTracker: f, f: f}
}

// seamListedIssueTracker adds the local adapter's SeamLister (ADR 0033),
// kept separate so localShapedIssueTracker's callers do not gain it too.
type seamListedIssueTracker struct {
	IssueTracker
	f *Fake
}

func (s seamListedIssueTracker) AllIssues() ([]Issue, error) { return s.f.allIssues() }

// AsSeamListed returns f in the local adapter's grouping shape: IssueTracker plus SeamLister.
func (f *Fake) AsSeamListed() IssueTracker {
	return seamListedIssueTracker{IssueTracker: f, f: f}
}

var _ SeamLister = seamListedIssueTracker{}

// fullyPaginatedIssueTracker matches the forgejo and jira adapters, which
// wrap their list calls in a page-walking loop. A bare *Fake stays single-page.
type fullyPaginatedIssueTracker struct{ IssueTracker }

func (fullyPaginatedIssueTracker) WalksAllPages() bool { return true }

// AsFullyPaginated returns f in the forgejo and jira adapters' shape: IssueTracker plus FullyPaginated.
func (f *Fake) AsFullyPaginated() IssueTracker {
	return fullyPaginatedIssueTracker{IssueTracker: f}
}

var _ FullyPaginated = fullyPaginatedIssueTracker{}

// pushOnlyForge hides a Fake's PRForge methods so a type assertion against
// PRForge reports absence, giving tests the git adapter's push-only shape.
type pushOnlyForge struct{ f *Fake }

// AsPushOnly returns f wrapped so it satisfies CodeForge but not PRForge.
func (f *Fake) AsPushOnly() CodeForge { return pushOnlyForge{f} }

func (p pushOnlyForge) AgentBranch(num string) string            { return p.f.AgentBranch(num) }
func (p pushOnlyForge) Merge(url string) error                   { return p.f.Merge(url) }
func (p pushOnlyForge) Rebase(url string) error                  { return p.f.Rebase(url) }
func (p pushOnlyForge) Probe() (string, error)                   { return p.f.Probe() }
func (p pushOnlyForge) BranchExists(branch string) (bool, error) { return p.f.BranchExists(branch) }

var _ CodeForge = pushOnlyForge{}

// localForge adds the BundleRelay and LandingRef methods CODE_FORGE=local's
// adapter implements (ADR 0033) to the push-only shape.
type localForge struct{ pushOnlyForge }

// AsLocal returns f in the local adapter's shape: CodeForge, BundleRelay, and LandingRef, but not PRForge.
func (f *Fake) AsLocal() CodeForge { return localForge{pushOnlyForge{f}} }

func (l localForge) RelayBundle(outboxDir, ref string) error { return l.f.relayBundle(outboxDir, ref) }
func (l localForge) LandingRef() (string, error)             { return l.f.landingRef() }
func (l localForge) IntegrationTip(parent string) (string, error) {
	return l.f.integrationTip(parent)
}
func (l localForge) LandingContained(landing Landing, scope SeedScope) (bool, error) {
	return l.f.landingContained(landing, scope)
}

var _ CodeForge = localForge{}
var _ BundleRelay = localForge{}

// githubReadOnlyForge matches github.readOnlyCodeForge (issue #1919): the
// CodeForge and PRForge methods *Fake already exposes, plus BundleRelay and
// DraftPRCreator. Unlike localForge and pushOnlyForge, nothing needs hiding.
type githubReadOnlyForge struct{ *Fake }

// AsGithubReadOnly returns f in the github read-only adapter's shape: CodeForge, PRForge, BundleRelay, and DraftPRCreator.
func (f *Fake) AsGithubReadOnly() CodeForge { return githubReadOnlyForge{f} }

func (g githubReadOnlyForge) RelayBundle(outboxDir, ref string) error {
	return g.Fake.relayBundle(outboxDir, ref)
}

func (g githubReadOnlyForge) CreateDraftPR(title, body, base, head string) (string, bool, error) {
	return g.Fake.createDraftPR(title, body, base, head)
}

func (g githubReadOnlyForge) CommitSubjects(outboxDir, base, ref string) ([]string, error) {
	return g.Fake.commitSubjects(outboxDir, base, ref)
}

var _ CodeForge = githubReadOnlyForge{}
var _ PRForge = githubReadOnlyForge{}
var _ BundleRelay = githubReadOnlyForge{}
var _ DraftPRCreator = githubReadOnlyForge{}
var _ BundleCommitSubjects = githubReadOnlyForge{}
var _ BranchProtectionForge = githubReadOnlyForge{}
var _ LandingRef = localForge{}
var _ LandingRepair = localForge{}
var _ LandingContainmentQuery = localForge{}

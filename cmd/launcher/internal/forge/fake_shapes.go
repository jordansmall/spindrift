package forge

// The wrappers below each pin a Fake to one real adapter's interface shape,
// so type assertions in tests see exactly the optional surfaces that adapter
// implements. Two mechanics recur: a wrapper embeds the IssueTracker
// *interface value* rather than *Fake when it needs to hide methods that
// exist on *Fake but aren't part of IssueTracker, and every optional surface
// stays gated behind an As* constructor so a bare *Fake never silently
// starts satisfying it.

// issueFilerTracker is IssueTracker plus HostPostedIssueFiler, the shape
// github and forgejo satisfy directly on their own trackers.
type issueFilerTracker struct {
	IssueTracker
	f *Fake
}

// AsIssueFiler wraps f as an issueFilerTracker.
func (f *Fake) AsIssueFiler() IssueTracker { return issueFilerTracker{IssueTracker: f, f: f} }

func (i issueFilerTracker) PostIssue(title, body string, labels []string) (string, error) {
	return i.f.postIssue(title, body, labels)
}

var _ HostPostedIssueFiler = issueFilerTracker{}

// noLandingIssueTracker is the core IssueTracker surface only, hiding
// RecordLanding and CloseIssue — a github/jira adapter's shape (ADR 0029):
// neither implements the optional local-only write surfaces.
type noLandingIssueTracker struct{ IssueTracker }

// IsGithubTracker marks this shape github-shaped, which gates the Closes #N
// injection settle's tests expect. Only the real github execClient carries
// the method, so it can't be promoted from the embedded IssueTracker.
func (noLandingIssueTracker) IsGithubTracker() bool { return true }

// AsNoLandingRecorder wraps f as a noLandingIssueTracker.
func (f *Fake) AsNoLandingRecorder() IssueTracker { return noLandingIssueTracker{f} }

// localShapedIssueTracker is IssueTracker plus the local-only write
// surfaces (LandingRecorder, IssueCloser) but not MergeCloser: local
// implements the reconcile-owned closed: axis but never settle's
// merge-driven backstop, even when paired with a PRForge-implementing Code
// Forge (ISSUE_TRACKER=local + CODE_FORGE=github is a valid combination).
type localShapedIssueTracker struct {
	IssueTracker
	f *Fake
}

func (l localShapedIssueTracker) RecordLanding(num, landing string) error {
	return l.f.RecordLanding(num, landing)
}

func (l localShapedIssueTracker) CloseIssue(num string) error {
	return l.f.CloseIssue(num)
}

// AsLocalShaped wraps f as a localShapedIssueTracker.
func (f *Fake) AsLocalShaped() IssueTracker {
	return localShapedIssueTracker{IssueTracker: f, f: f}
}

// localIssueFilerTracker is LandingRecorder + IssueCloser +
// HostPostedIssueFiler — the real local adapter's combined shape, where
// RecordLanding and PostIssue live on the same *localTracker. Needed to
// exercise ResearchSettle's local branch (r.landing != nil) together with
// issue filing, which neither AsLocalShaped nor AsIssueFiler can do alone.
type localIssueFilerTracker struct {
	IssueTracker
	f *Fake
}

func (l localIssueFilerTracker) RecordLanding(num, landing string) error {
	return l.f.RecordLanding(num, landing)
}

func (l localIssueFilerTracker) CloseIssue(num string) error {
	return l.f.CloseIssue(num)
}

func (l localIssueFilerTracker) PostIssue(title, body string, labels []string) (string, error) {
	return l.f.postIssue(title, body, labels)
}

// AsLocalIssueFiler wraps f as a localIssueFilerTracker.
func (f *Fake) AsLocalIssueFiler() IssueTracker {
	return localIssueFilerTracker{IssueTracker: f, f: f}
}

var _ HostPostedIssueFiler = localIssueFilerTracker{}
var _ LandingRecorder = localIssueFilerTracker{}
var _ IssueCloser = localIssueFilerTracker{}

// forgejoShapedIssueTracker is IssueTracker plus MergeCloser, hiding
// LandingRecorder and IssueCloser. It deliberately does NOT implement
// GithubTracker: forgejo issue numbers are foreign to GitHub's
// Closes-keyword namespace.
type forgejoShapedIssueTracker struct {
	IssueTracker
	f *Fake
}

func (fs forgejoShapedIssueTracker) CloseMergedIssue(num string) error {
	return fs.f.CloseMergedIssue(num)
}

// AsForgejoShaped wraps f as a forgejoShapedIssueTracker.
func (f *Fake) AsForgejoShaped() IssueTracker {
	return forgejoShapedIssueTracker{IssueTracker: f, f: f}
}

// seamListedIssueTracker is IssueTracker plus AllIssues — the local
// adapter's SeamLister surface (ADR 0033). Kept separate from
// localShapedIssueTracker so that shape's callers don't pick up SeamLister
// as a side effect.
type seamListedIssueTracker struct {
	IssueTracker
	f *Fake
}

func (s seamListedIssueTracker) AllIssues() ([]Issue, error) { return s.f.allIssues() }

// AsSeamListed wraps f as a seamListedIssueTracker.
func (f *Fake) AsSeamListed() IssueTracker {
	return seamListedIssueTracker{IssueTracker: f, f: f}
}

var _ SeamLister = seamListedIssueTracker{}

// fullyPaginatedIssueTracker is IssueTracker plus WalksAllPages — the
// forgejo/jira shape. A bare *Fake stays single-page, like github's gh-exec
// adapter.
type fullyPaginatedIssueTracker struct{ IssueTracker }

func (fullyPaginatedIssueTracker) WalksAllPages() bool { return true }

// AsFullyPaginated wraps f as a fullyPaginatedIssueTracker.
func (f *Fake) AsFullyPaginated() IssueTracker {
	return fullyPaginatedIssueTracker{IssueTracker: f}
}

var _ FullyPaginated = fullyPaginatedIssueTracker{}

// pushOnlyForge is the core CodeForge surface only, hiding the Fake's
// PRForge methods — the git adapter's shape.
type pushOnlyForge struct{ f *Fake }

// AsPushOnly returns f wrapped so it satisfies CodeForge but not PRForge.
func (f *Fake) AsPushOnly() CodeForge { return pushOnlyForge{f} }

func (p pushOnlyForge) AgentBranch(num string) string            { return p.f.AgentBranch(num) }
func (p pushOnlyForge) Merge(url string) error                   { return p.f.Merge(url) }
func (p pushOnlyForge) Rebase(url string) error                  { return p.f.Rebase(url) }
func (p pushOnlyForge) Probe() (string, error)                   { return p.f.Probe() }
func (p pushOnlyForge) BranchExists(branch string) (bool, error) { return p.f.BranchExists(branch) }

var _ CodeForge = pushOnlyForge{}

// localForge is the push-only CodeForge surface plus the BundleRelay and
// LandingRef hooks CODE_FORGE=local's adapter implements (ADR 0033).
type localForge struct{ pushOnlyForge }

// AsLocal wraps f as a localForge.
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

// githubReadOnlyForge is the github read-only adapter's shape: the full
// CodeForge+PRForge surface the Fake already exposes (nothing is hidden
// here) plus the host-mediated BundleRelay and DraftPRCreator hooks.
type githubReadOnlyForge struct{ *Fake }

// AsGithubReadOnly wraps f as a githubReadOnlyForge.
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

package forge

// issueFilerTracker adapts a Fake to expose IssueTracker plus
// HostPostedIssueFiler, the same isolation githubReadOnlyForge's own
// DraftPRCreator staging uses — github and forgejo satisfy the interface
// directly on their own IssueTracker (issue #2028, issue #1964), but the
// Fake stays gated behind AsIssueFiler() so a bare *Fake used as an
// IssueTracker elsewhere never silently starts satisfying it too.
type issueFilerTracker struct {
	IssueTracker
	f *Fake
}

// AsIssueFiler returns f wrapped so it satisfies IssueTracker and
// HostPostedIssueFiler.
func (f *Fake) AsIssueFiler() IssueTracker { return issueFilerTracker{IssueTracker: f, f: f} }

func (i issueFilerTracker) PostIssue(title, body string, labels []string) (string, error) {
	return i.f.postIssue(title, body, labels)
}

var _ HostPostedIssueFiler = issueFilerTracker{}

// noLandingIssueTracker adapts a Fake to expose only the core IssueTracker
// surface, hiding both its RecordLanding and CloseIssue methods so a type
// assertion against either reports absence — the IssueTracker analogue of
// pushOnlyForge, matching a github/jira adapter's shape (ADR 0029): neither
// implements the optional local-only write surfaces.
type noLandingIssueTracker struct{ IssueTracker }

// IsGithubTracker implements the optional GithubTracker marker (issue
// #2341) so noLandingIssueTracker keeps standing in for "github-shaped"
// across the ~40 settle tests already built on AsNoLandingRecorder() —
// those tests expect a Closes #N reference to be injected, the github-only
// behavior GithubTracker now gates. *Fake itself has no such method (only
// the real github execClient does), so it must be added here explicitly
// rather than promoted through the embedded IssueTracker.
func (noLandingIssueTracker) IsGithubTracker() bool { return true }

// AsNoLandingRecorder returns f wrapped so it satisfies IssueTracker and
// GithubTracker (the github adapter's shape) but neither LandingRecorder
// nor IssueCloser.
func (f *Fake) AsNoLandingRecorder() IssueTracker { return noLandingIssueTracker{f} }

// localShapedIssueTracker adapts a Fake to expose IssueTracker plus the
// local-only write surfaces (LandingRecorder, IssueCloser) but hides
// MergeCloser — the real local adapter's shape (issue #1892): local
// implements the reconcile-owned closed: axis (IssueCloser) but never
// settle's merge-driven backstop (MergeCloser, implemented by github and
// forgejo), even when paired with a PRForge-implementing Code Forge
// (ISSUE_TRACKER=local + CODE_FORGE=github is a valid independent
// combination, main.go's newIssueTracker/newCodeForge).
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

// AsLocalShaped returns f wrapped so it satisfies IssueTracker,
// LandingRecorder, and IssueCloser — the local adapter's shape — but not
// MergeCloser, which only github and forgejo implement.
func (f *Fake) AsLocalShaped() IssueTracker {
	return localShapedIssueTracker{IssueTracker: f, f: f}
}

// localIssueFilerTracker adapts a Fake to expose IssueTracker plus both
// LandingRecorder and IssueCloser (localShapedIssueTracker's local-only
// write surfaces) together with HostPostedIssueFiler (issueFilerTracker's
// host-mediated filing surface) — the real local adapter's actual combined
// shape (local.go: RecordLanding and PostIssue are both implemented on the
// same *localTracker). Neither AsLocalShaped nor AsIssueFiler alone can
// stand in for it: AsLocalShaped promotes RecordLanding/CloseIssue but never
// gained PostIssue, and AsIssueFiler embeds the IssueTracker interface value
// rather than *Fake, so it doesn't promote RecordLanding/CloseIssue at all.
// That left no Fake shape able to exercise ResearchSettle's local branch
// (r.landing != nil) together with issue filing via
// fileIssueIntentsDetailed (issue #2592) — this double closes that gap.
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

// AsLocalIssueFiler returns f wrapped so it satisfies IssueTracker,
// LandingRecorder, IssueCloser, and HostPostedIssueFiler — the real local
// adapter's combined shape (issue #2592) — but not MergeCloser, which only
// github and forgejo implement.
func (f *Fake) AsLocalIssueFiler() IssueTracker {
	return localIssueFilerTracker{IssueTracker: f, f: f}
}

var _ HostPostedIssueFiler = localIssueFilerTracker{}
var _ LandingRecorder = localIssueFilerTracker{}
var _ IssueCloser = localIssueFilerTracker{}

// forgejoShapedIssueTracker adapts a Fake to expose IssueTracker plus
// MergeCloser — one surface the real forgejo adapter implements (see
// forgejo.go) — but hides LandingRecorder and IssueCloser (embedding the
// IssueTracker interface value, not *Fake directly, the same trick
// noLandingIssueTracker uses, since RecordLanding/CloseIssue are methods on
// *Fake but not part of the IssueTracker interface) and does NOT implement
// GithubTracker — forgejo issue numbers are foreign to GitHub's
// Closes-keyword namespace (issue #2341), the exact gap this double closes
// for ensureClosesReference's test coverage.
type forgejoShapedIssueTracker struct {
	IssueTracker
	f *Fake
}

func (fs forgejoShapedIssueTracker) CloseMergedIssue(num string) error {
	return fs.f.CloseMergedIssue(num)
}

// AsForgejoShaped returns f wrapped so it satisfies IssueTracker and
// MergeCloser — the real forgejo adapter's shape — but not LandingRecorder,
// IssueCloser, or GithubTracker.
func (f *Fake) AsForgejoShaped() IssueTracker {
	return forgejoShapedIssueTracker{IssueTracker: f, f: f}
}

// pushOnlyForge adapts a Fake to expose only the core CodeForge surface,
// hiding its PRForge methods so a type assertion against it reports absence
// — the git adapter's shape, for tests that need to exercise push-only-forge
// behavior without a removed PushOnly() flag.
type pushOnlyForge struct{ f *Fake }

// AsPushOnly returns f wrapped so it satisfies CodeForge but not PRForge.
func (f *Fake) AsPushOnly() CodeForge { return pushOnlyForge{f} }

func (p pushOnlyForge) AgentBranch(num string) string            { return p.f.AgentBranch(num) }
func (p pushOnlyForge) Merge(url string) error                   { return p.f.Merge(url) }
func (p pushOnlyForge) Rebase(url string) error                  { return p.f.Rebase(url) }
func (p pushOnlyForge) Probe() (string, error)                   { return p.f.Probe() }
func (p pushOnlyForge) BranchExists(branch string) (bool, error) { return p.f.BranchExists(branch) }

var _ CodeForge = pushOnlyForge{}

// localForge adapts a Fake to expose the push-only CodeForge surface plus
// the BundleRelay and LandingRef hooks CODE_FORGE=local's adapter implements
// (ADR 0033) — the Fake analogue of local.localCodeForge's real wrapper.
type localForge struct{ pushOnlyForge }

// AsLocal returns f wrapped so it satisfies CodeForge, BundleRelay, and
// LandingRef, but not PRForge — the local adapter's shape.
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

// githubReadOnlyForge adapts a Fake to the github read-only adapter's shape
// (issue #1919): the full CodeForge+PRForge surface *Fake already exposes
// directly (unlike localForge/pushOnlyForge, nothing here needs hiding) plus
// BundleRelay and DraftPRCreator — mirroring github.readOnlyCodeForge, which
// wraps execClient (already PRForge-shaped) with the same two host-mediated
// hooks.
type githubReadOnlyForge struct{ *Fake }

// AsGithubReadOnly returns f wrapped so it satisfies CodeForge, PRForge,
// BundleRelay, and DraftPRCreator — the github read-only adapter's shape.
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
var _ LandingRef = localForge{}
var _ LandingRepair = localForge{}
var _ LandingContainmentQuery = localForge{}

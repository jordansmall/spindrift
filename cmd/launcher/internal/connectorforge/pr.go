package connectorforge

import (
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"spindrift.dev/launcher/internal/connectorcall"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/bundlerelay"
)

// prForge is a PR-capable Code Forge composed from a Connector: the Connector
// answers every PR question and performs every PR write, while the policy (URL
// pinning, answer validation, merge-failure classification) stays here. It
// implements none of the optional surfaces itself: composePR stacks one layer
// per declared surface, so a type probe finds exactly the declared ones.
type prForge struct {
	*reads
	push      *pushOnly
	autoMerge bool
}

// composePR wraps the validated push-only pieces p as a PR forge for a Connector
// advertising caps, pinning PR URLs under webHost and webPath. It adds no relay
// layer: Compose does, since NewReader's forge has no clone to relay into.
func composePR(p *pushOnly, caps []string, webHost, webPath string) forge.CodeForge {
	p.rd.webHost, p.rd.webPath = webHost, webPath
	f := &prForge{
		reads:     p.rd,
		push:      p,
		autoMerge: slices.Contains(caps, capAutoMerge),
	}
	var cf forge.CodeForge = f
	if slices.Contains(caps, capCIRun) {
		cf = ciRunLayer{cf, f}
	}
	if slices.Contains(caps, capBranchProtection) {
		cf = protectionLayer{cf, f}
	}
	if slices.Contains(caps, capDraftPRs) {
		cf = draftPRLayer{cf, f, p}
	}
	return cf
}

// A layer adds one optional surface over the forge it wraps. Embedding
// forge.CodeForge hides everything the wrapped forge offers beyond the base
// interface, so each layer unwraps for forge.AsCodeForge to find the rest:
// probe a composed forge with AsCodeForge or ResolveCodeForge, never a bare
// type assertion.
type (
	ciRunLayer struct {
		forge.CodeForge
		f *prForge
	}
	protectionLayer struct {
		forge.CodeForge
		f *prForge
	}
	// draftPRLayer is the host-mediated PR surface: CreateDraftPR over the
	// Connector's forge.openPR and PushBranch through core's own git.
	draftPRLayer struct {
		forge.CodeForge
		f *prForge
		p *pushOnly
	}
	// relayLayer is the Box's hand-off: core relays the bundle and
	// reads its commit subjects in its own git, so the Connector never sees
	// either.
	relayLayer struct {
		forge.CodeForge
		p *pushOnly
	}
)

func (l ciRunLayer) UnwrapCodeForge() forge.CodeForge      { return l.CodeForge }
func (l protectionLayer) UnwrapCodeForge() forge.CodeForge { return l.CodeForge }
func (l draftPRLayer) UnwrapCodeForge() forge.CodeForge    { return l.CodeForge }
func (l relayLayer) UnwrapCodeForge() forge.CodeForge      { return l.CodeForge }

func (l relayLayer) RelayBundle(outboxDir, ref string) error {
	return bundlerelay.Relay("connector forge", outboxDir, ref, l.p.cred, l.p.cloneURL, l.p.relayClone("relay bundle"))
}

func (l relayLayer) CommitSubjects(outboxDir, base, ref string) ([]string, error) {
	return bundlerelay.CommitSubjects("connector forge", outboxDir, base, ref, l.p.relayClone("commit subjects"))
}

func (l ciRunLayer) CheckRun(prURL string) (forge.RollupState, string, error) {
	return l.f.checkRun(prURL)
}

func (l protectionLayer) BranchProtected(branch string) (bool, error) {
	return l.f.branchProtected(branch)
}

func (l draftPRLayer) CreateDraftPR(title, body, base, head string) (string, bool, error) {
	return l.f.createDraftPR(title, body, base, head)
}

func (l draftPRLayer) PushBranch(srcDir, localRef, branch, base string) error {
	return l.p.pushBranch(srcDir, localRef, branch, base)
}

func (f *prForge) checkRun(prURL string) (forge.RollupState, string, error) {
	state, err := f.CheckState(prURL)
	if err != nil {
		return "", "", err
	}
	var res wireRunLink
	if err := f.numbered("forge.ciRun", prURL, &res); err != nil {
		return "", "", err
	}
	return state, res.URL, nil
}

func (f *prForge) branchProtected(branch string) (bool, error) {
	var res struct {
		Protected bool `json:"protected"`
	}
	if err := f.call(context.Background(), "forge.branchProtected", branchParams{f.target, branch}, &res); err != nil {
		return false, err
	}
	return res.Protected, nil
}

// numbered resolves prURL and calls method with the PR's number, decoding into
// result.
func (f *prForge) numbered(method, prURL string, result any) error {
	n, err := f.prNumber(prURL)
	if err != nil {
		return err
	}
	return f.call(context.Background(), method, numberedParams{f.target, n}, result)
}

func (f *prForge) Mergeable(prURL string) (forge.MergeableState, error) {
	w, _, err := f.pr(prURL)
	if err != nil {
		return "", err
	}
	// Fail closed: an unrecognised value must never read as mergeable.
	switch s := forge.MergeableState(w.Mergeable); s {
	case forge.MergeableMergeable, forge.MergeableConflicting, forge.MergeableUnknown:
		return s, nil
	}
	return "", fmt.Errorf("%w: PR %s has mergeable state %q", ErrAnswer, w.Number, w.Mergeable)
}

func (f *prForge) HeadCommitSHA(prURL string) (string, error) {
	w, _, err := f.pr(prURL)
	if err != nil {
		return "", err
	}
	if w.HeadSHA == "" {
		return "", fmt.Errorf("%w: PR %s has no head commit", ErrAnswer, w.Number)
	}
	return w.HeadSHA, nil
}

func (f *prForge) MergeCommit(prURL string) (string, error) {
	w, _, err := f.pr(prURL)
	if err != nil {
		return "", err
	}
	if w.MergeCommit != "" && !fullHexCommit.MatchString(w.MergeCommit) {
		return "", fmt.Errorf("%w: PR %s merge commit %q is not a full hex commit id", ErrAnswer, w.Number, w.MergeCommit)
	}
	return w.MergeCommit, nil
}

// fullHexCommit matches a lowercase SHA-1 or SHA-256 object id, the form git
// prints; "" (no merge commit) is accepted separately by the caller.
var fullHexCommit = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

func (f *prForge) NeedsUpdate(prURL string) (bool, error) {
	var res wireBehind
	if err := f.numbered("forge.behind", prURL, &res); err != nil {
		return false, err
	}
	return res.BehindBy > 0, nil
}

func (f *prForge) ListPRFiles(prURL string) ([]string, error) {
	n, err := f.prNumber(prURL)
	if err != nil {
		return nil, err
	}
	var res wireFiles
	if err := f.call(context.Background(), "forge.prFiles", numberedParams{f.target, n}, &res); err != nil {
		return nil, err
	}
	return *res.Files, nil
}

// cleanRepoPath reports whether p is a non-empty, relative path in path.Clean
// form (so "./x" cannot dodge a guard glob) with no ".." segment and no
// control character.
func cleanRepoPath(p string) bool {
	return p != "" && !strings.HasPrefix(p, "/") && path.Clean(p) == p &&
		!slices.Contains(strings.Split(p, "/"), "..") &&
		strings.IndexFunc(p, unicode.IsControl) < 0
}

func (f *prForge) setDraft(prURL string, draft bool) error {
	n, err := f.prNumber(prURL)
	if err != nil {
		return err
	}
	p := struct {
		Target Target `json:"target"`
		Number string `json:"number"`
		Draft  bool   `json:"draft"`
	}{f.target, n, draft}
	return f.call(context.Background(), "forge.setDraft", p, nil)
}

func (f *prForge) MarkReady(prURL string) error {
	err := f.setDraft(prURL, false)
	if err != nil && connectorhost.ErrorClass(err) == connectorhost.Transient {
		return fmt.Errorf("%w: %w", err, forge.ErrMarkReadyTransient)
	}
	return err
}

func (f *prForge) MarkDraft(prURL string) error { return f.setDraft(prURL, true) }

func (f *prForge) CommentPR(prURL, body string) error {
	n, err := f.prNumber(prURL)
	if err != nil {
		return err
	}
	p := struct {
		Target Target `json:"target"`
		Number string `json:"number"`
		Body   string `json:"body"`
	}{f.target, n, body}
	return f.call(context.Background(), "forge.comment", p, nil)
}

// CanAutoMerge is false without a call when the Connector does not declare
// "forge.autoMerge".
func (f *prForge) CanAutoMerge() (bool, error) {
	if !f.autoMerge {
		return false, nil
	}
	return f.autoMergeAllowed()
}

// autoMergeAllowed is the read itself, for callers that checked the capability.
func (f *prForge) autoMergeAllowed() (bool, error) {
	var res struct {
		Allowed bool `json:"allowed"`
	}
	p := struct {
		Target Target `json:"target"`
	}{f.target}
	if err := f.call(context.Background(), "forge.autoMergeAllowed", p, &res); err != nil {
		return false, err
	}
	return res.Allowed, nil
}

func (f *prForge) EnqueueAutoMerge(prURL string) error {
	if !f.autoMerge {
		return fmt.Errorf("connector forge: auto-merge is unavailable: the connector does not declare %q", capAutoMerge)
	}
	return f.numbered("forge.enqueueAutoMerge", prURL, nil)
}

// Merge asks the Connector to merge the PR. A transient failure is reported
// as forge.ErrMergeTransient for the Lander's merge loop to retry (the
// classified call never retries a merge). Only a NotMergeable refusal is
// classified by re-reading the PR; every other failure is returned as is.
func (f *prForge) Merge(prURL string) error {
	err := f.numbered("forge.merge", prURL, nil)
	switch connectorhost.ErrorClass(err) {
	case connectorhost.Transient:
		return fmt.Errorf("%w: %w", forge.ErrMergeTransient, err)
	case connectorhost.NotMergeable:
		return f.classifyMergeFailure(prURL, err)
	default:
		return err
	}
}

// mergeRecheckDelay is how long classifyMergeFailure waits before re-reading a
// PR classed conflicting. A forge may recompute mergeability asynchronously
// after a push and report a conflict while it runs (Forgejo reports false, not
// unknown), so a refusal straight after a rebase can read as a conflict that is
// not there. A Connector cannot sleep in the guest, so the pause lives in core.
const mergeRecheckDelay = 5 * time.Second

// classifyMergeFailure classifies a merge failure from the PR's state; one
// classed a conflict is re-read once after mergeRecheckDelay and classified
// from that second read.
func (f *prForge) classifyMergeFailure(prURL string, cause error) error {
	err := f.classifyFromPR(prURL, cause)
	if !errors.Is(err, forge.ErrMergeConflict) {
		return err
	}
	f.push.clock.Sleep(mergeRecheckDelay)
	return f.classifyFromPR(prURL, cause)
}

// classifyFromPR tells a conflict from a PR merely blocked by checks from the
// PR's own mergeable state. A PR that already merged landed by another path
// (nil); a draft is an error with no sentinel; so is any state that cannot be
// told apart.
func (f *prForge) classifyFromPR(prURL string, cause error) error {
	w, state, err := f.pr(prURL)
	if err != nil {
		return fmt.Errorf("connector forge: merge %s: %w (PR lookup failed: %w)", prURL, cause, err)
	}
	switch {
	case state == forge.PRMerged:
		return nil
	case state == forge.PRClosed:
		return fmt.Errorf("connector forge: merge %s: %w (PR is closed)", prURL, cause)
	case w.Draft:
		return fmt.Errorf("connector forge: merge %s: %w (PR is a draft)", prURL, cause)
	}
	if sentinel, ok := forge.ClassifyMergeFailure(forge.MergeableState(w.Mergeable)); ok {
		return fmt.Errorf("%w: %w", sentinel, cause)
	}
	return fmt.Errorf("connector forge: merge %s: %w (mergeable state %q undetermined)", prURL, cause, w.Mergeable)
}

// Rebase is refused: the PR's head comes from the guest, so rebasing and
// force-pushing it unpinned could rewrite main or release with core's
// credential. Callers use RebasePRHead (issue #6961).
func (f *prForge) Rebase(prURL string) error {
	return fmt.Errorf("%w: an unpinned rebase of %s is refused, the head comes from the guest; use RebasePRHead", ErrAnswer, prURL)
}

// RebasePRHead rebases the PR's head branch through core's git plumbing, pinned
// to head, the agent branch core derived: the PR's head comes from the guest, so
// a different one is refused before any git runs (issue #6961).
func (f *prForge) RebasePRHead(prURL, head string) error {
	if head == "" {
		return fmt.Errorf("%w: RebasePRHead called with no expected head", ErrAnswer)
	}
	if f.push.git == nil {
		return ErrNoGit
	}
	w, _, err := f.pr(prURL)
	if err != nil {
		return err
	}
	if w.Head == "" {
		return fmt.Errorf("%w: PR %s has no head branch", ErrAnswer, w.Number)
	}
	if w.Head != head {
		return f.push.conn.AuditRefusal("forge.pr", fmt.Errorf("%w: PR %s head %q is not the agent branch %q", ErrAnswer, w.Number, w.Head, head))
	}
	return f.push.git.Rebase(w.Head)
}

// createDraftPR opens a draft PR. The Connector has no duplicate class, so when
// the create fails and head already has an open PR, that one is adopted
// (created false): a retried hand-off must not fail on its own earlier PR. A
// refused answer (ErrAnswer) or core bug (connectorcall.ErrCoreBug) is
// returned, never adopted.
func (f *prForge) createDraftPR(title, body, base, head string) (string, bool, error) {
	p := struct {
		Target Target `json:"target"`
		PR     struct {
			Title string `json:"title"`
			Body  string `json:"body"`
			Base  string `json:"base"`
			Head  string `json:"head"`
			Draft bool   `json:"draft"`
		} `json:"pr"`
	}{Target: f.target}
	p.PR.Title, p.PR.Body, p.PR.Base, p.PR.Head, p.PR.Draft = title, body, base, head, true
	var w wirePR
	err := f.call(context.Background(), "forge.openPR", p, &w)
	if err == nil {
		return w.URL, true, nil
	}
	if errors.Is(err, ErrAnswer) || errors.Is(err, connectorcall.ErrCoreBug) {
		return "", false, err
	}
	if pr, ok, lookup := f.OpenPRForBranch(head); lookup == nil && ok {
		return pr.URL, false, nil
	}
	return "", false, err
}

// Probe goes through forge.probe, like the push-only forge.
func (f *prForge) Probe() (string, error) { return f.push.Probe() }

func (f *prForge) BranchExists(branch string) (bool, error) { return f.push.BranchExists(branch) }

func (f *prForge) DeleteBranch(branch, base string) error { return f.push.DeleteBranch(branch, base) }

// HeadOf reads the head branch of the PR at prURL through the Connector, and
// whether that head lives in another repository than the base. forge.PRForge
// carries no accessor for either, so this reaches the composed forge's own PR
// read. The unwrap is hand-rolled because forge.AsCodeForge outside the
// resolver trips TestNoCapabilityProbeOutsideResolver.
func HeadOf(cf forge.CodeForge, prURL string) (head string, crossRepo bool, err error) {
	for cf != nil {
		if f, ok := cf.(*prForge); ok {
			w, _, err := f.pr(prURL)
			if err != nil {
				return "", false, err
			}
			if w.Head == "" {
				return "", false, fmt.Errorf("%w: PR %s has no head branch", ErrAnswer, w.Number)
			}
			return w.Head, w.CrossRepo, nil
		}
		u, ok := cf.(forge.CodeForgeUnwrapper)
		if !ok {
			break
		}
		cf = u.UnwrapCodeForge()
	}
	return "", false, errors.New("connector forge: not a Connector PR forge")
}

// SeedMirror clones the repo bare through core's git, as the push-only forge does.
func (f *prForge) SeedMirror(dir string) error { return f.push.SeedMirror(dir) }

var _ forge.MirrorSeeder = (*prForge)(nil)

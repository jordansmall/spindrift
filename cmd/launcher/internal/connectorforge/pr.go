package connectorforge

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/bundlerelay"
)

// prForge is a PR-capable Code Forge composed from a Connector: the Connector
// answers every PR question and performs every PR write, while the policy (URL
// pinning, answer validation, merge-failure classification) stays here. It
// builds on reads for the five PR reads, and on pushOnly for the branch and git
// pieces. It implements none of the optional surfaces (CIRunReporter,
// BranchProtectionForge, DraftPRCreator and BranchPusher) as interface
// methods: composePR stacks one layer per declared surface over it, so a type
// probe finds exactly the declared ones.
type prForge struct {
	*reads
	push      *pushOnly
	autoMerge bool
	hasProbe  bool
}

// composePR wraps the validated push-only pieces p as a PR forge for a Connector
// advertising caps, pinning PR URLs to webHost. relay adds the read-only Box's
// bundle relay over the draft surface (see Git.ReadOnlyBox).
func composePR(p *pushOnly, caps []string, webHost string, relay bool) forge.CodeForge {
	f := &prForge{
		reads:     &reads{call: p.call, target: p.target, webHost: webHost},
		push:      p,
		autoMerge: slices.Contains(caps, capAutoMerge),
		hasProbe:  slices.Contains(caps, capProbe),
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
		// Settle reads BundleRelay as "the Box handed off a bundle", so only a
		// read-only Box is offered it.
		if relay {
			cf = relayLayer{cf, p}
		}
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
	// relayLayer is the read-only Box's hand-off: core relays the bundle and
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
	return bundlerelay.Relay("connector forge", outboxDir, ref, l.p.cred, l.p.relayClone("relay bundle"))
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
	var res struct {
		URL string `json:"url"`
	}
	if err := f.numbered("forge.ciRun", prURL, &res); err != nil {
		return "", "", err
	}
	return state, f.ownRunLink(res.URL), nil
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
	return w.MergeCommit, err
}

func (f *prForge) NeedsUpdate(prURL string) (bool, error) {
	var res struct {
		BehindBy int `json:"behindBy"`
	}
	if err := f.numbered("forge.behind", prURL, &res); err != nil {
		return false, err
	}
	if res.BehindBy < 0 {
		return false, fmt.Errorf("%w: behindBy %d is negative", ErrAnswer, res.BehindBy)
	}
	return res.BehindBy > 0, nil
}

func (f *prForge) ListPRFiles(prURL string) ([]string, error) {
	var res struct {
		Files []string `json:"files"`
	}
	if err := f.numbered("forge.prFiles", prURL, &res); err != nil {
		return nil, err
	}
	return res.Files, nil
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

func (f *prForge) MarkReady(prURL string) error { return f.setDraft(prURL, false) }
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

// Merge asks the Connector to merge the PR. A transient failure is not retried
// here (classedCall never retries a merge) but reported as
// forge.ErrMergeTransient, which the Lander's merge loop retries. A rate limit,
// an auth failure or a refused route is returned as is; every other failure is
// classified by re-reading the PR (see classifyMergeFailure).
func (f *prForge) Merge(prURL string) error {
	err := f.numbered("forge.merge", prURL, nil)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, forge.ErrRateLimit), errors.Is(err, forge.ErrAuthFailure):
		return err
	case errors.Is(err, connectorhost.ErrRouteRefused):
		// The host closed the Connector for an undeclared request, so a re-read
		// could only fail and bury the refusal.
		return err
	case connectorhost.ErrorClass(err) == connectorhost.Transient:
		return fmt.Errorf("%w: %w", forge.ErrMergeTransient, err)
	}
	return f.classifyMergeFailure(prURL, err)
}

// classifyMergeFailure tells a conflict from a PR merely blocked by checks from
// the PR's own mergeable state. A PR that already merged landed by another path
// (nil); a draft is an error with no sentinel; so is any state that cannot be
// told apart.
func (f *prForge) classifyMergeFailure(prURL string, cause error) error {
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

// Rebase rebases the PR's head branch through core's git plumbing.
func (f *prForge) Rebase(prURL string) error {
	w, _, err := f.pr(prURL)
	if err != nil {
		return err
	}
	if w.Head == "" {
		return fmt.Errorf("%w: PR %s has no head branch", ErrAnswer, w.Number)
	}
	return f.push.git.Rebase(w.Head)
}

// createDraftPR opens a draft PR. The Connector has no duplicate class, so when
// the create fails and head already has an open PR, that one is adopted
// (created false): a retried hand-off must not fail on its own earlier PR.
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
		if _, err = f.check(w); err == nil {
			return w.URL, true, nil
		}
		return "", false, err
	}
	if pr, ok, lookup := f.OpenPRForBranch(head); lookup == nil && ok {
		return pr.URL, false, nil
	}
	return "", false, err
}

// Probe asks the Connector for the repository's name when it declares
// "forge.probe", and otherwise re-reads the remote's identity.
func (f *prForge) Probe() (string, error) {
	if !f.hasProbe {
		return f.push.Probe()
	}
	var res struct {
		Repo string `json:"repo"`
	}
	p := struct {
		Target Target `json:"target"`
	}{f.target}
	if err := f.call(context.Background(), "forge.probe", p, &res); err != nil {
		return "", err
	}
	if res.Repo == "" {
		return "", fmt.Errorf("%w: probe named no repository", ErrAnswer)
	}
	return res.Repo, nil
}

func (f *prForge) BranchExists(branch string) (bool, error) { return f.push.BranchExists(branch) }

func (f *prForge) DeleteBranch(branch, base string) error { return f.push.DeleteBranch(branch, base) }

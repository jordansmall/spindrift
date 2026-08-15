package settle

import (
	"errors"
	"fmt"
	"strings"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
)

// TextSource records where Open's returned PR title/body actually came from.
type TextSource int

const (
	// TextSourceUnknown is TextSource's zero value: it's what Open and
	// reconstructPRText's error-path returns carry, since none of the three
	// real sources below ever resolved. Keeping it distinct from
	// TextSourceIntent (rather than letting the zero value alias it) avoids
	// an error return being misread as "text came from the box's own
	// PR-intent line", which never happened on an error path.
	TextSourceUnknown TextSource = iota
	TextSourceIntent
	TextSourceReconstructed
	TextSourceDefault
)

// Fallback selects Open's behavior when the Box's own PR-intent line is
// missing or malformed.
type Fallback int

const (
	FallbackNone Fallback = iota
	FallbackReconstruct
	FallbackDefault
)

// ErrNoPRIntent is Open's sentinel for FallbackNone (and FallbackReconstruct
// when reconstruction also fails) -- callers use errors.Is to distinguish
// this from a genuine relay/create failure.
var ErrNoPRIntent = errors.New("settle: no usable PR-intent line found")

// errRelayBundle wraps Open's RelayBundle-failure return, letting callers
// distinguish it from a draft-PR-create failure via errors.Is instead of
// matching mediation.go's own error-message text (issue #2501 review).
var errRelayBundle = errors.New("settle: relay bundle failed")

// errCreateDraftPR wraps Open's CreateDraftPR-failure return, the
// errCreateDraftPR analog to errRelayBundle above.
var errCreateDraftPR = errors.New("settle: draft PR create failed")

// Mediation coordinates the host-mediated relay-then-create-PR hand-off
// shared by settle's four call sites (pr_intent.go, adopt_relayed.go): relay
// a Box's finished branch out of the outbox, resolve a PR title/body, ensure
// a "Closes #N" reference, and create-or-adopt a draft PR.
type Mediation struct {
	cf         forge.CodeForge
	it         forge.IssueTracker
	outboxDir  func(num string) string
	baseBranch string

	br  forge.BundleRelay
	dpc forge.DraftPRCreator
	bcs forge.BundleCommitSubjects
}

// NewMediation builds a Mediation over cf/it, discovering cf's optional
// forge.BundleRelay, forge.DraftPRCreator, and forge.BundleCommitSubjects
// capabilities via type assertion (standard optional-interface pattern).
func NewMediation(cf forge.CodeForge, it forge.IssueTracker, outboxDir func(num string) string, baseBranch string) *Mediation {
	m := &Mediation{cf: cf, it: it, outboxDir: outboxDir, baseBranch: baseBranch}
	m.br, _ = cf.(forge.BundleRelay)
	m.dpc, _ = cf.(forge.DraftPRCreator)
	m.bcs, _ = cf.(forge.BundleCommitSubjects)
	return m
}

// RequiredCapabilityError reports the missing capability, if any, that
// BOX_FORGE_AND_ISSUE_ACCESS=read-only's startup capability gate requires of
// codeForgeName's Code Forge: forge.BundleRelay unconditionally, and — only
// when prShaped (the forge also implements forge.PRForge) —
// forge.DraftPRCreator and forge.BundleCommitSubjects. Returns nil when every
// required capability is present.
func (m *Mediation) RequiredCapabilityError(codeForgeName string, prShaped bool) error {
	return RequiredCapabilityError(m.cf, codeForgeName, prShaped)
}

// RequiredCapabilityError is Mediation.RequiredCapabilityError's
// construction-free form: main.go's startup capability gate only needs to
// ask whether cf implements the right capabilities, not build a full
// Mediation (which also wants an IssueTracker, outbox resolver, and base
// branch it would never use for this question).
func RequiredCapabilityError(cf forge.CodeForge, codeForgeName string, prShaped bool) error {
	br, _ := cf.(forge.BundleRelay)
	dpc, _ := cf.(forge.DraftPRCreator)
	bcs, _ := cf.(forge.BundleCommitSubjects)

	if br == nil {
		return fmt.Errorf("BOX_FORGE_AND_ISSUE_ACCESS=read-only: the selected CODE_FORGE=%q does not implement bundle-relay (forge.BundleRelay) for the Box's finished branch hand-off; only CODE_FORGE=local implements it today", codeForgeName)
	}
	if prShaped && dpc == nil {
		return fmt.Errorf("BOX_FORGE_AND_ISSUE_ACCESS=read-only: the selected CODE_FORGE=%q does not implement host-side draft-PR-create (forge.DraftPRCreator); not yet available on CODE_FORGE=github", codeForgeName)
	}
	if prShaped && bcs == nil {
		return fmt.Errorf("BOX_FORGE_AND_ISSUE_ACCESS=read-only: the selected CODE_FORGE=%q does not implement commit-subjects (forge.BundleCommitSubjects), the reconstructed-PR fallback the host-mediated hand-off needs when a Box leaves no usable PR-intent line", codeForgeName)
	}
	return nil
}

// Open relays num's finished branch out of the outbox and opens (or adopts)
// a draft PR on it, resolving title/body from result's PR-intent line, or —
// per fallback — a reconstructed-from-commits or issue-derived default when
// that line is missing or malformed. Returns the resolved PR URL, whether a
// fresh PR was created (as opposed to an existing one adopted), and which
// source the returned title/body actually came from.
func (m *Mediation) Open(num, branch string, result dispatch.Result, fallback Fallback) (url string, created bool, source TextSource, err error) {
	if m.br == nil {
		return "", false, TextSourceUnknown, errors.New("settle: Code Forge does not implement forge.BundleRelay")
	}
	if m.dpc == nil {
		return "", false, TextSourceUnknown, errors.New("settle: Code Forge does not implement forge.DraftPRCreator")
	}
	if m.outboxDir == nil {
		return "", false, TextSourceUnknown, errors.New("settle: Config.OutboxDir is unset but the Code Forge implements forge.BundleRelay")
	}

	if err := m.br.RelayBundle(m.outboxDir(num), branch); err != nil {
		return "", false, TextSourceUnknown, fmt.Errorf("%w: %w", errRelayBundle, err)
	}

	title, body, foundIntent := parsePRIntent(result)
	source = TextSourceIntent
	if !foundIntent {
		switch fallback {
		case FallbackReconstruct:
			var rerr error
			title, body, rerr = m.reconstructPRText(num, branch)
			if rerr != nil {
				return "", false, TextSourceUnknown, fmt.Errorf("%w: reconstructing from the relayed branch's commits also failed: %w", ErrNoPRIntent, rerr)
			}
			source = TextSourceReconstructed
		case FallbackDefault:
			title, body = m.defaultAdoptPRText(num)
			source = TextSourceDefault
		default: // FallbackNone
			return "", false, TextSourceUnknown, ErrNoPRIntent
		}
	}
	body = ensureClosesReference(body, num, m.it)

	url, created, err = m.dpc.CreateDraftPR(title, body, m.baseBranch, branch)
	if err != nil {
		return "", false, TextSourceUnknown, fmt.Errorf("%w: %w", errCreateDraftPR, err)
	}
	return url, created, source, nil
}

// reconstructPRText builds a title/body for num's already-relayed branch
// from its own commits via m.bcs, when no usable PR-intent line survived —
// see hostMediateDraftPR's doc comment (pr_intent.go) for the full
// reasoning.
func (m *Mediation) reconstructPRText(num, branch string) (title, body string, err error) {
	if m.bcs == nil {
		return "", "", errors.New("settle: Code Forge does not implement forge.BundleCommitSubjects")
	}
	subjects, err := m.bcs.CommitSubjects(m.outboxDir(num), m.baseBranch, branch)
	if err != nil {
		return "", "", err
	}
	if len(subjects) == 0 {
		return "", "", errors.New("relayed branch carries no commits to reconstruct a PR description from")
	}

	var b strings.Builder
	b.WriteString("Reconstructed host-side: the box's log carried no usable PR-intent line, so this description was derived from the relayed branch's own commits.\n\nCommits:\n")
	for _, subject := range subjects {
		b.WriteString("- ")
		b.WriteString(defuseClosingKeywords(subject))
		b.WriteString("\n")
	}
	// subjects[0] (the title) is returned raw, not run through
	// defuseClosingKeywords: GitHub's closing-keyword auto-close scanner only
	// ever scans a PR's body, never its title, so defusing the title would
	// only visibly mangle it with no corresponding safety benefit — see
	// defuseClosingKeywords's own doc comment (pr_intent.go) for the
	// body-side hazard this guards against.
	return subjects[0], strings.TrimRight(b.String(), "\n"), nil
}

// defaultAdoptPRText builds the fallback title/body Open uses under
// FallbackDefault when the box's log carried no usable PR-intent line: the
// title prefers the underlying issue's own title (falling back to a generic
// "Adopt agent work for #N" when the issue lookup fails or its title is
// blank), and the body explains that this PR was auto-adopted host-side
// because the box self-reported success but its outcome line was missing or
// degraded to the synthetic backstop (ADR 0036/0039).
func (m *Mediation) defaultAdoptPRText(num string) (title, body string) {
	title = fmt.Sprintf("Adopt agent work for #%s", num)
	if iss, err := m.it.Issue(num); err == nil && strings.TrimSpace(iss.Title) != "" {
		title = iss.Title
	}
	body = "Auto-adopted PR for the relayed agent branch: the run's driver self-reported success but its outcome line was missing or degraded to the synthetic backstop (ADR 0036/0039); this PR was opened host-side from the relayed outbox bundle."
	return title, body
}

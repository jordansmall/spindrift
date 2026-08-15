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
	TextSourceIntent TextSource = iota
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
	if m.br == nil {
		return fmt.Errorf("BOX_FORGE_AND_ISSUE_ACCESS=read-only: the selected CODE_FORGE=%q does not implement bundle-relay (forge.BundleRelay) for the Box's finished branch hand-off; only CODE_FORGE=local implements it today", codeForgeName)
	}
	if prShaped && m.dpc == nil {
		return fmt.Errorf("BOX_FORGE_AND_ISSUE_ACCESS=read-only: the selected CODE_FORGE=%q does not implement host-side draft-PR-create (forge.DraftPRCreator); not yet available on CODE_FORGE=github", codeForgeName)
	}
	if prShaped && m.bcs == nil {
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
		return "", false, 0, errors.New("settle: Code Forge does not implement forge.BundleRelay")
	}
	if m.dpc == nil {
		return "", false, 0, errors.New("settle: Code Forge does not implement forge.DraftPRCreator")
	}
	if m.outboxDir == nil {
		return "", false, 0, errors.New("settle: Config.OutboxDir is unset but the Code Forge implements forge.BundleRelay")
	}

	if err := m.br.RelayBundle(m.outboxDir(num), branch); err != nil {
		return "", false, 0, fmt.Errorf("relay bundle: %w", err)
	}

	title, body, foundIntent := parsePRIntent(result)
	source = TextSourceIntent
	if !foundIntent {
		switch fallback {
		case FallbackReconstruct:
			var rerr error
			title, body, rerr = m.reconstructPRText(num, branch)
			if rerr != nil {
				return "", false, 0, fmt.Errorf("%w: no usable PR-intent line found in the box's log; reconstructing from the relayed branch's commits also failed: %v", ErrNoPRIntent, rerr)
			}
			source = TextSourceReconstructed
		case FallbackDefault:
			title, body = m.defaultAdoptPRText(num)
			source = TextSourceDefault
		default: // FallbackNone
			return "", false, 0, ErrNoPRIntent
		}
	}
	body = ensureClosesReference(body, num, m.it)

	url, created, err = m.dpc.CreateDraftPR(title, body, m.baseBranch, branch)
	if err != nil {
		return "", false, 0, fmt.Errorf("draft PR create: %w", err)
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
	return subjects[0], strings.TrimRight(b.String(), "\n"), nil
}

// defaultAdoptPRText builds the fallback title/body Open uses under
// FallbackDefault when the box's log carried no usable PR-intent line — see
// (*Settle).defaultAdoptPRText (adopt_relayed.go) for the full reasoning.
func (m *Mediation) defaultAdoptPRText(num string) (title, body string) {
	title = fmt.Sprintf("Adopt agent work for #%s", num)
	if iss, err := m.it.Issue(num); err == nil && strings.TrimSpace(iss.Title) != "" {
		title = iss.Title
	}
	body = "Auto-adopted PR for the relayed agent branch: the run's driver self-reported success but its outcome line was missing or degraded to the synthetic backstop (ADR 0036/0039); this PR was opened host-side from the relayed outbox bundle."
	return title, body
}

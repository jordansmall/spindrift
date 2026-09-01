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
	// TextSourceUnknown is the zero value, carried by Open's error returns.
	// It is deliberately distinct from TextSourceIntent so an error return
	// can't be misread as "text came from the box's own PR-intent line".
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
// when reconstruction also fails). blockHandoff posts its text verbatim to
// operators, so it deliberately carries no "settle:" package prefix.
var ErrNoPRIntent = errors.New("no usable PR-intent line found in the box's log")

// errRelayBundle and errCreateDraftPR let callers tell Open's two failure
// modes apart with errors.Is instead of matching error-message text.
var errRelayBundle = errors.New("relay bundle failed")

var errCreateDraftPR = errors.New("draft PR create failed")

// Mediation coordinates the host-mediated relay-then-create-PR hand-off
// shared by settle's four call sites (pr_intent.go, adopt_relayed.go): relay
// a Box's finished branch out of the outbox, resolve a PR title/body, ensure
// a "Closes #N" reference, and create-or-adopt a draft PR.
type Mediation struct {
	it         forge.IssueTracker
	outboxDir  func(num string) string
	baseBranch string

	br  forge.BundleRelay
	dpc forge.DraftPRCreator
	bcs forge.BundleCommitSubjects
}

// mediationFor resolves num's own Code Forge (s.cfForNum) and builds the
// branch and Mediation every host-mediated hand-off call site shares.
func (s *Settle) mediationFor(num string) (branch string, m *Mediation) {
	cf := s.cfForNum(num)
	// cf is resolved fresh per num (ADR 0033's per-issue/per-parent wiring
	// under CODE_FORGE=local), so the capabilities must be re-resolved here:
	// s.cfg.Capabilities was resolved once by the read tier against
	// whichever cf New was constructed with, and carries the wrong per-issue
	// state for every other num. Only the descriptors (config-time, never
	// per-issue) are safe to reuse from it.
	caps := forge.ResolveCapabilities(cf, s.it, s.cfg.Capabilities.ForgeDescriptor, s.cfg.Capabilities.TrackerDescriptor)
	return cf.AgentBranch(num), NewMediation(caps, s.it, s.cfg.OutboxDir, s.cfg.BaseBranch)
}

// NewMediation builds a Mediation from caps' BundleRelay, DraftPRCreator,
// and BundleCommitSubjects fields.
func NewMediation(caps forge.Capabilities, it forge.IssueTracker, outboxDir func(num string) string, baseBranch string) *Mediation {
	m := &Mediation{it: it, outboxDir: outboxDir, baseBranch: baseBranch}
	m.br, m.dpc, m.bcs = caps.BundleRelay, caps.DraftPRCreator, caps.BundleCommitSubjects
	return m
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
// from its own commits when no usable PR-intent line survived — see
// hostMediateDraftPR (pr_intent.go) for the reasoning.
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
	// The title is returned raw, not defused: GitHub's closing-keyword
	// auto-close scanner reads only a PR's body, so defusing the title would
	// mangle it for no safety benefit (see defuseClosingKeywords).
	return subjects[0], strings.TrimRight(b.String(), "\n"), nil
}

// defaultAdoptPRText builds Open's FallbackDefault title/body. The title
// prefers the issue's own title, falling back to "Adopt agent work for #N"
// when the lookup fails or the title is blank.
func (m *Mediation) defaultAdoptPRText(num string) (title, body string) {
	title = fmt.Sprintf("Adopt agent work for #%s", num)
	if iss, err := m.it.Issue(num); err == nil && strings.TrimSpace(iss.Title) != "" {
		title = iss.Title
	}
	body = "Auto-adopted PR for the relayed agent branch: the run's driver self-reported success but its outcome line was missing or degraded to the synthetic backstop (ADR 0036/0039); this PR was opened host-side from the relayed outbox bundle."
	return title, body
}

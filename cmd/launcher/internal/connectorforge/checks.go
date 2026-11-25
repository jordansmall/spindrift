package connectorforge

import (
	"fmt"

	"spindrift.dev/launcher/internal/connectorcall"
	"spindrift.dev/launcher/internal/connectorprotocol"
	"spindrift.dev/launcher/internal/forge"
)

// The result shapes the check bodies read, named where a method's answer is
// not already one of connectorforge.go's wire types.
type (
	wirePRList struct {
		PRs []wirePR `json:"prs"`
	}
	wireBehind struct {
		BehindBy int `json:"behindBy"`
	}
	wireFiles struct {
		Files *[]string `json:"files"`
	}
	wireRunLink struct {
		URL string `json:"url"`
	}
	wireProbe struct {
		Repo string `json:"repo"`
	}
	tokenGrantParams struct {
		Target      Target `json:"target"`
		TokenPrefix string `json:"tokenPrefix"`
	}
)

// forgeChecks is the forge axis's whole registry: every call of p, and of the
// reads p.rd, runs through it. The bodies read p and p.rd when they run, so
// the web host and path composePR pins after construction reach them.
func forgeChecks(p *pushOnly) connectorcall.Checks {
	t := p.rd
	return connectorcall.Checks{
		connectorprotocol.CheckDecode:      connectorcall.Decode,
		connectorprotocol.CheckPrShape:     t.prShapeBody,
		connectorprotocol.CheckPrAnswer:    connectorcall.Typed(t.prAnswerBody),
		connectorprotocol.CheckRollupState: connectorcall.Typed(func(_ any, ro *wireRollup) error { return checkRollupState(*ro) }),
		connectorprotocol.CheckRunLink: connectorcall.Typed(func(_ any, r *wireRunLink) error {
			r.URL = t.ownRunLink(r.URL)
			return nil
		}),
		connectorprotocol.CheckBehindCount: connectorcall.Typed(func(_ any, r *wireBehind) error {
			if r.BehindBy < 0 {
				return fmt.Errorf("%w: behindBy %d is negative", ErrAnswer, r.BehindBy)
			}
			return nil
		}),
		connectorprotocol.CheckPrFiles: connectorcall.Typed(checkPRFiles),
		connectorprotocol.CheckProbeRepo: connectorcall.Typed(func(_ any, r *wireProbe) error {
			if r.Repo == "" {
				return fmt.Errorf("%w: probe named no repository", ErrAnswer)
			}
			return nil
		}),
		connectorprotocol.CheckTokenGrant: connectorcall.Typed(func(_ tokenGrantParams, g *wireGrant) error {
			if err := checkGrant(*g); err != nil {
				return err
			}
			g.Reason = cleanReason(g.Reason)
			return nil
		}),
	}
}

// prShapeBody checks the PR forge.openPR answered or each PR forge.prsForBranch
// listed.
func (t *reads) prShapeBody(_, result any) error {
	switch r := result.(type) {
	case *wirePR:
		return t.checkPR(*r)
	case *wirePRList:
		for _, w := range r.PRs {
			if err := t.checkPR(w); err != nil {
				return err
			}
		}
		return nil
	}
	return fmt.Errorf("connectorforge: prShape takes a PR or a PR list, got %T: %w", result, connectorcall.ErrCoreBug)
}

func (t *reads) prAnswerBody(p numberedParams, w *wirePR) error {
	if err := t.checkPR(*w); err != nil {
		return err
	}
	if w.Number != p.Number {
		return fmt.Errorf("%w: asked for PR %s, got %s", ErrAnswer, p.Number, w.Number)
	}
	return nil
}

func checkRollupState(ro wireRollup) error {
	switch forge.RollupState(ro.State) {
	case "", forge.StateSuccess, forge.StatePending, forge.StateExpected, forge.StateFailure, forge.StateError:
		return nil
	}
	return fmt.Errorf("%w: unknown rollup state %q", ErrAnswer, ro.State)
}

func checkPRFiles(p numberedParams, res *wireFiles) error {
	// The merge guard reads this list, so an absent, empty or malformed one must
	// not decode to "no guarded path touched": every PR changes at least a file.
	if res.Files == nil {
		return fmt.Errorf("%w: PR %s file list is missing", ErrAnswer, p.Number)
	}
	if len(*res.Files) == 0 {
		return fmt.Errorf("%w: PR %s file list has no entries", ErrAnswer, p.Number)
	}
	for _, f := range *res.Files {
		if !cleanRepoPath(f) {
			return fmt.Errorf("%w: PR %s file list entry %q is not a clean repository-relative path", ErrAnswer, p.Number, f)
		}
	}
	return nil
}

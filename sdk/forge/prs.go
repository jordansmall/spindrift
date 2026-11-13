package forge

import (
	"encoding/json"

	"spindrift.dev/sdk/connector"
)

// The capability names of the PR primitives. CapPRs is declared by every
// PR-capable Connector; the rest are optional and declared only when the
// Connector sets the funcs behind them. Core keeps its own copies; a parity
// test pins them.
const (
	CapPRs              = "forge.prs"
	CapAutoMerge        = "forge.autoMerge"
	CapCIRun            = "forge.ciRun"
	CapBranchProtection = "forge.branchProtection"
	CapProbe            = "forge.probe"
)

// NewPR is the pull request a Connector opens. Base is the branch it merges
// into and Head the branch it merges from.
type NewPR struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	Base  string `json:"base"`
	Head  string `json:"head"`
	Draft bool   `json:"draft"`
}

// PRs is everything a Connector-backed Code Forge needs of the forge itself
// for pull requests; core derives the rest. Reading a PR stays on Reader.
type PRs struct {
	// Open opens a pull request and returns it.
	Open func(t Target, p NewPR) (PR, error)

	// Behind returns how many commits the PR's base has that its head lacks.
	Behind func(t Target, number string) (int, error)

	// Files returns every path the PR changes.
	Files func(t Target, number string) ([]string, error)

	// SetDraft marks the PR as a draft or ready for review.
	SetDraft func(t Target, number string, draft bool) error

	// Comment posts body on the PR.
	Comment func(t Target, number, body string) error

	// Merge merges the PR now.
	Merge func(t Target, number string) error

	// AutoMergeAllowed and EnqueueAutoMerge are optional and set together;
	// they declare CapAutoMerge. AutoMergeAllowed reports whether the
	// repository permits auto-merge and EnqueueAutoMerge asks the forge to
	// merge the PR once its requirements are met.
	AutoMergeAllowed func(t Target) (bool, error)
	EnqueueAutoMerge func(t Target, number string) error

	// CIRun is optional and declares CapCIRun. It returns the URL of the CI
	// run for the PR's head commit, "" when there is none yet.
	CIRun func(t Target, number string) (string, error)

	// BranchProtected is optional and declares CapBranchProtection. It
	// reports whether branch has protection rules.
	BranchProtected func(t Target, branch string) (bool, error)

	// Probe is optional and declares CapProbe. It returns the repository's
	// full name, proving the forge answers with the Connector's credential.
	Probe func(t Target) (string, error)
}

// RegisterPRs exports p as the Connector's PR primitives. Call it from
// init(). It panics if a required func is unset or only one of the
// auto-merge pair is.
func RegisterPRs(p PRs) {
	caps, methods := prHandlers(p)
	connector.Register(caps...)
	for name, fn := range methods {
		connector.Handle(name, fn)
	}
}

// prHandlers builds the capability list and method table for p.
func prHandlers(p PRs) ([]string, map[string]method) {
	switch {
	case p.Open == nil:
		panic("forge: PRs.Open is required")
	case p.Behind == nil:
		panic("forge: PRs.Behind is required")
	case p.Files == nil:
		panic("forge: PRs.Files is required")
	case p.SetDraft == nil:
		panic("forge: PRs.SetDraft is required")
	case p.Comment == nil:
		panic("forge: PRs.Comment is required")
	case p.Merge == nil:
		panic("forge: PRs.Merge is required")
	case (p.AutoMergeAllowed == nil) != (p.EnqueueAutoMerge == nil):
		panic("forge: PRs.AutoMergeAllowed and PRs.EnqueueAutoMerge are set together or not at all")
	}

	// targeted decodes the {target} params of the repository-wide calls.
	targeted := func(params json.RawMessage) (Target, error) {
		var q struct {
			Target Target `json:"target"`
		}
		err := unmarshal(params, &q)
		return q.Target, err
	}
	// done runs a call with no result, answering an empty object, not null.
	done := func(err error) (any, error) {
		if err != nil {
			return nil, err
		}
		return struct{}{}, nil
	}
	m := map[string]method{
		"forge.openPR": func(params json.RawMessage) (any, error) {
			var q struct {
				Target Target `json:"target"`
				PR     NewPR  `json:"pr"`
			}
			if err := unmarshal(params, &q); err != nil {
				return nil, err
			}
			return p.Open(q.Target, q.PR)
		},
		"forge.behind": func(params json.RawMessage) (any, error) {
			t, n, err := numbered(params)
			if err != nil {
				return nil, err
			}
			by, err := p.Behind(t, n)
			if err != nil {
				return nil, err
			}
			return struct {
				BehindBy int `json:"behindBy"`
			}{by}, nil
		},
		"forge.prFiles": func(params json.RawMessage) (any, error) {
			t, n, err := numbered(params)
			if err != nil {
				return nil, err
			}
			files, err := p.Files(t, n)
			if err != nil {
				return nil, err
			}
			if files == nil {
				files = []string{} // encode [] rather than null
			}
			return struct {
				Files []string `json:"files"`
			}{files}, nil
		},
		"forge.setDraft": func(params json.RawMessage) (any, error) {
			var q struct {
				Target Target `json:"target"`
				Number string `json:"number"`
				Draft  bool   `json:"draft"`
			}
			if err := unmarshal(params, &q); err != nil {
				return nil, err
			}
			return done(p.SetDraft(q.Target, q.Number, q.Draft))
		},
		"forge.comment": func(params json.RawMessage) (any, error) {
			var q struct {
				Target Target `json:"target"`
				Number string `json:"number"`
				Body   string `json:"body"`
			}
			if err := unmarshal(params, &q); err != nil {
				return nil, err
			}
			return done(p.Comment(q.Target, q.Number, q.Body))
		},
		"forge.merge": func(params json.RawMessage) (any, error) {
			t, n, err := numbered(params)
			if err != nil {
				return nil, err
			}
			return done(p.Merge(t, n))
		},
	}
	caps := []string{CapPRs}

	if p.AutoMergeAllowed != nil {
		m["forge.autoMergeAllowed"] = func(params json.RawMessage) (any, error) {
			t, err := targeted(params)
			if err != nil {
				return nil, err
			}
			allowed, err := p.AutoMergeAllowed(t)
			if err != nil {
				return nil, err
			}
			return struct {
				Allowed bool `json:"allowed"`
			}{allowed}, nil
		}
		m["forge.enqueueAutoMerge"] = func(params json.RawMessage) (any, error) {
			t, n, err := numbered(params)
			if err != nil {
				return nil, err
			}
			return done(p.EnqueueAutoMerge(t, n))
		}
		caps = append(caps, CapAutoMerge)
	}
	if p.CIRun != nil {
		m["forge.ciRun"] = func(params json.RawMessage) (any, error) {
			t, n, err := numbered(params)
			if err != nil {
				return nil, err
			}
			url, err := p.CIRun(t, n)
			if err != nil {
				return nil, err
			}
			return struct {
				URL string `json:"url"`
			}{url}, nil
		}
		caps = append(caps, CapCIRun)
	}
	if p.BranchProtected != nil {
		m["forge.branchProtected"] = func(params json.RawMessage) (any, error) {
			var q struct {
				Target Target `json:"target"`
				Branch string `json:"branch"`
			}
			if err := unmarshal(params, &q); err != nil {
				return nil, err
			}
			protected, err := p.BranchProtected(q.Target, q.Branch)
			if err != nil {
				return nil, err
			}
			return struct {
				Protected bool `json:"protected"`
			}{protected}, nil
		}
		caps = append(caps, CapBranchProtection)
	}
	if p.Probe != nil {
		m["forge.probe"] = func(params json.RawMessage) (any, error) {
			t, err := targeted(params)
			if err != nil {
				return nil, err
			}
			repo, err := p.Probe(t)
			if err != nil {
				return nil, err
			}
			return struct {
				Repo string `json:"repo"`
			}{repo}, nil
		}
		caps = append(caps, CapProbe)
	}
	return caps, m
}

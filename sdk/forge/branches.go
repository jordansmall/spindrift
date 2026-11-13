package forge

import (
	"encoding/json"

	"spindrift.dev/sdk/connector"
)

// The capability names a forge Connector advertises in its handshake. Core
// keeps its own copies; a parity test pins them.
const (
	CapBranches = "forge.branches"
	CapPushOnly = "forge.pushOnly"
)

// Remote is a repository's git identity. CloneURL is what core's git plumbing
// pushes to and WebURL is the repository's web page. Neither may carry
// credentials: core refuses one that does, and one naming a host outside the
// Connector's declared routes.
type Remote struct {
	CloneURL string `json:"cloneUrl"`
	WebURL   string `json:"webUrl"`
}

// Branches is everything a Connector-backed Code Forge needs of the forge
// itself for branches; core does every git operation against Remote.
type Branches struct {
	// Remote returns the repository's clone and web URLs.
	Remote func(t Target) (Remote, error)

	// BranchExists reports whether branch exists on the forge.
	BranchExists func(t Target, branch string) (bool, error)

	// DeleteBranch deletes branch from the forge.
	DeleteBranch func(t Target, branch string) error

	// PushOnly declares a forge with no pull-request concept, so core composes
	// a push-only Code Forge from it.
	PushOnly bool
}

// RegisterBranches exports b as the Connector's branch primitives. Call it
// from init(). It panics if any func is unset.
func RegisterBranches(b Branches) {
	caps, methods := branchHandlers(b)
	connector.Register(caps...)
	for name, fn := range methods {
		connector.Handle(name, fn)
	}
}

// branchHandlers builds the capability list and method table for b.
func branchHandlers(b Branches) ([]string, map[string]method) {
	switch {
	case b.Remote == nil:
		panic("forge: Branches.Remote is required")
	case b.BranchExists == nil:
		panic("forge: Branches.BranchExists is required")
	case b.DeleteBranch == nil:
		panic("forge: Branches.DeleteBranch is required")
	}

	// branched decodes the {target, branch} params of the per-branch calls.
	branched := func(params json.RawMessage) (Target, string, error) {
		var p struct {
			Target Target `json:"target"`
			Branch string `json:"branch"`
		}
		err := unmarshal(params, &p)
		return p.Target, p.Branch, err
	}
	m := map[string]method{
		"forge.remote": func(params json.RawMessage) (any, error) {
			var p struct {
				Target Target `json:"target"`
			}
			if err := unmarshal(params, &p); err != nil {
				return nil, err
			}
			return b.Remote(p.Target)
		},
		"forge.branchExists": func(params json.RawMessage) (any, error) {
			t, branch, err := branched(params)
			if err != nil {
				return nil, err
			}
			exists, err := b.BranchExists(t, branch)
			if err != nil {
				return nil, err
			}
			return struct {
				Exists bool `json:"exists"`
			}{exists}, nil
		},
		"forge.deleteBranch": func(params json.RawMessage) (any, error) {
			t, branch, err := branched(params)
			if err != nil {
				return nil, err
			}
			if err := b.DeleteBranch(t, branch); err != nil {
				return nil, err
			}
			return struct{}{}, nil // an empty object, not null
		},
	}
	caps := []string{CapBranches}
	if b.PushOnly {
		caps = append(caps, CapPushOnly)
	}
	return caps, m
}

package forge

import (
	"fmt"
	"os"
)

// NativeDepsReader is the optional IssueTracker capability for a tracker with
// native dependency relationships, which DepsOf prefers over the issue body.
type NativeDepsReader interface {
	NativeDepsOf(num string) ([]string, error)
}

// BodyGrammar names how a tracker's raw issue body declares dependencies and
// touched paths. Backends only declare it; core parses.
type BodyGrammar int

const (
	// BodyGrammarIssueRefs reads the whole body for "#N" refs.
	BodyGrammarIssueRefs BodyGrammar = iota
	// BodyGrammarLocal reads only the description above "## Comments", and
	// blockers are filename slugs.
	BodyGrammarLocal
	// BodyGrammarNoDeps declares touches only: native links are authoritative,
	// so DepsOf never falls back to the body and returns a native error as-is.
	BodyGrammarNoDeps
)

// BodyGrammarer is the optional capability a tracker implements to declare a
// non-default BodyGrammar.
type BodyGrammarer interface {
	BodyGrammar() BodyGrammar
}

func grammarOf(it IssueTracker) BodyGrammar {
	if g, ok := it.(BodyGrammarer); ok {
		return g.BodyGrammar()
	}
	return BodyGrammarIssueRefs
}

// DepsOf returns issue num's dependencies: the tracker's native relationships
// when it has any, otherwise those parsed from the issue body. The two are
// never merged. A native lookup failure warns and falls back to the body.
func DepsOf(it IssueTracker, num string) ([]Dependency, error) {
	grammar := grammarOf(it)
	if nr, ok := it.(NativeDepsReader); ok {
		deps, err := nr.NativeDepsOf(num)
		if grammar == BodyGrammarNoDeps {
			if err != nil {
				return nil, err
			}
			return WithSource(deps, DepSourceNative), nil
		}
		if err == nil && len(deps) > 0 {
			return WithSource(deps, DepSourceNative), nil
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "WARNING: native dependency lookup for issue %s failed (%v); falling back to body parsing\n", num, err)
		}
	}
	if grammar == BodyGrammarNoDeps {
		return nil, nil
	}
	iss, err := it.Issue(num)
	if err != nil {
		return nil, err
	}
	if grammar == BodyGrammarLocal {
		return WithSource(ParseBlockerSlugs(iss.Body), DepSourceBody), nil
	}
	return WithSource(ParseBlockerRefs(iss.Body), DepSourceBody), nil
}

// TouchesOf returns the touch-set declared in issue num's body. Under the
// local grammar only the description counts, so host-written comments never
// add touches.
func TouchesOf(it IssueTracker, num string) ([]string, error) {
	iss, err := it.Issue(num)
	if err != nil {
		return nil, err
	}
	body := iss.Body
	if grammarOf(it) == BodyGrammarLocal {
		body = Description(body)
	}
	return ParseTouchPaths(body), nil
}

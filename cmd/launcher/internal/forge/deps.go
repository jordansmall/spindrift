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
	// BodyGrammarNativeDepsOnly declares touches only: native links are
	// authoritative, so DepsOf never falls back to the body and returns a native
	// error as-is.
	BodyGrammarNativeDepsOnly
)

// BodyGrammarer is the optional capability a tracker implements to declare a
// non-default BodyGrammar.
type BodyGrammarer interface {
	BodyGrammar() BodyGrammar
}

// grammarRules is what core does differently per BodyGrammar.
type grammarRules struct {
	nativeOnly bool                  // native links are the only dependency source; a native error passes through
	descOnly   bool                  // only the description above "## Comments" counts for touches
	parseDeps  func(string) []string // body dependency parser; nil when nativeOnly
}

var grammars = map[BodyGrammar]grammarRules{
	BodyGrammarIssueRefs:      {parseDeps: ParseBlockerRefs},
	BodyGrammarLocal:          {descOnly: true, parseDeps: ParseBlockerSlugs},
	BodyGrammarNativeDepsOnly: {nativeOnly: true},
}

func rulesOf(it IssueTracker) grammarRules {
	if g, ok := As[BodyGrammarer](it); ok {
		return grammars[g.BodyGrammar()]
	}
	return grammars[BodyGrammarIssueRefs]
}

// DepsOf returns issue num's dependencies, each tagged with its source: the
// tracker's native relationships (NativeDepsReader) when it has any, otherwise
// those parsed from the Issue body. The two are never merged. A native lookup
// failure warns and falls back to the body, except under
// BodyGrammarNativeDepsOnly, which returns the error unchanged and never reads
// the body.
//
// It looks up NativeDepsReader and BodyGrammarer on the bare tracker rather than
// reading Capabilities: its callers (waves, console) hold no Capabilities.
func DepsOf(it IssueTracker, num string) ([]Dependency, error) {
	rules := rulesOf(it)
	if nr, ok := As[NativeDepsReader](it); ok {
		deps, err := nr.NativeDepsOf(num)
		if rules.nativeOnly {
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
	if rules.nativeOnly {
		return nil, nil
	}
	iss, err := it.Issue(num)
	if err != nil {
		return nil, err
	}
	return WithSource(rules.parseDeps(iss.Body), DepSourceBody), nil
}

// TouchesOf returns the path globs issue num's body names as the files its
// work will touch (a "## Touches" section), which the wave engine's overlap
// gate consumes; an issue with none returns nil, nil. Under the local grammar
// only the description counts, so host-written comments never add touches.
func TouchesOf(it IssueTracker, num string) ([]string, error) {
	iss, err := it.Issue(num)
	if err != nil {
		return nil, err
	}
	body := iss.Body
	if rulesOf(it).descOnly {
		body = Description(body)
	}
	return ParseTouchPaths(body), nil
}

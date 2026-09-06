package registryvocab

import "net/url"

// RewriteOutcome distinguishes why a RewriteRow's Rewrite did or didn't
// rewrite a body -- specifically, it lets the caller tell the deliberate
// skip, RewriteSkippedForeignHost (an edited value named a host other than
// the route's own match-host), apart from RewriteNone (nothing recognizable
// to rewrite at all). Both non-applied outcomes are logged by the caller --
// RewriteNone's line names only the row, since there's no edited value to
// name (issue #3175's blocking review finding: a matched-but-unrewritten
// body was previously undiagnosable).
type RewriteOutcome int

const (
	RewriteNone RewriteOutcome = iota
	RewriteSkippedForeignHost
	RewriteApplied
)

// RewriteContext bundles the per-route facts a RewriteRow's Rewrite needs
// about the request that produced the response it's rewriting: the route's
// own match-host (to decide whether an edited value is rewritable at all),
// the Forwarder address to point a rewritten value at, and the route's
// prefix to re-insert ahead of the value's path.
type RewriteContext struct {
	MatchHost string
	Forwarder *url.URL
	Prefix    string
}

// RewriteEdit is one edited value: before, after, and the route-relative
// subtree the edit's target was found under. LearnedPath is set only on
// RewriteApplied; "/" is used for "no base segment" instead of "" because
// PathSet.Admits' HasPrefix(cleaned, sub+"/") branch would treat "" as
// admit-everything too -- "/" is the normalized, self-documenting sentinel,
// not an unwidened one.
//
// An edit with an empty To is the row declining that one value -- e.g. a
// packument holds many tarball URLs, and one naming a CDN rather than the
// route's own match-host is left alone. It's still reportable in a
// RewriteApplied result's Edits alongside the edits actually applied, since
// a body can hold both at once; the caller logs it as a skip and learns
// nothing from it (an unset LearnedPath here must never reach
// learnRewriteBase, or "" would normalize to "/" and admit the whole host).
type RewriteEdit struct {
	From        string
	To          string
	LearnedPath string
}

// RewriteResult is what a RewriteRow's Rewrite reports back: the (possibly
// untouched) body, the edits made for the caller's log lines, and the
// outcome that decides whether those edits apply.
type RewriteResult struct {
	Body    []byte
	Edits   []RewriteEdit
	Outcome RewriteOutcome
}

// RewriteRow binds one request shape -- ecosystem, HTTP method, and a
// path-shape matcher -- to a response-body rewriter. A shape with no
// matching row is never inspected at all, not even to sniff its
// Content-Type -- issue #2854's wrong-media-type defect (content-sniffing to
// detect JSON) is excluded by construction, not by a runtime guard.
type RewriteRow struct {
	Name      string // shape name, used only in log lines (e.g. "cargo config.json")
	Ecosystem string // an ecosystem.Table row's Name; matched against the route's tagged subtrees
	Method    string // e.g. http.MethodGet; must be body-bearing -- the proxy runs a matching row against a HEAD response's empty body too
	Matches   func(routeRelativePath, base string) bool
	Rewrite   func(body []byte, rc RewriteContext) RewriteResult
}

// JoinBase joins a route-relative subtree base with a row-relative path.
// base == "/" is the sentinel for "no base segment at all" (mirrors
// PathSet's own root-subtree convention), so it combines with rel as rel
// itself, not "//config.json" -- a row matcher that always prepended base
// literally would double the leading slash for every host-rooted subtree,
// which is the common case (an index with no path prefix at all).
func JoinBase(base, rel string) string {
	if base == "/" {
		return rel
	}
	return base + rel
}

package registryvocab

import (
	"errors"
	"io"
	"net/url"
)

// RewriteOutcome tells the caller why a RewriteRow's Rewrite did or didn't
// rewrite a body. RewriteSkippedForeignHost is a deliberate skip, where an
// edited value named a host other than the route's own match-host;
// RewriteNone means there was nothing recognizable to rewrite, so the
// caller's log line can name only the row (issue #3175).
type RewriteOutcome int

const (
	RewriteNone RewriteOutcome = iota
	RewriteSkippedForeignHost
	RewriteApplied
)

// RewriteContext carries the per-route facts a RewriteRow's Rewrite needs
// about the request that produced the response it is rewriting.
type RewriteContext struct {
	MatchHost string
	Forwarder *url.URL
	// Prefix is the route prefix a rewriter re-inserts ahead of the value's
	// path; a rewritten value that omits it does not route back.
	Prefix string
}

// RewriteEdit is one edited value. An empty To means the row declined this
// value, for example a packument tarball URL naming a CDN rather than the
// route's own match-host; the caller logs that as a skip and learns nothing
// from it. A RewriteApplied result's Edits can hold declined and applied
// edits together, because one body can carry both.
type RewriteEdit struct {
	From string
	To   string
	// LearnedPath is the route-relative subtree the edit's target was found
	// under, set only on RewriteApplied. "/" means "no base segment": an
	// empty string must never reach learnRewriteBase, because it normalizes
	// to "/" and PathSet.Admits' HasPrefix(cleaned, sub+"/") branch then
	// admits the whole host.
	LearnedPath string
}

// RewriteResult is what a RewriteRow's Rewrite reports back. Outcome decides
// whether Edits apply; Body may be untouched.
type RewriteResult struct {
	Body    []byte
	Edits   []RewriteEdit
	Outcome RewriteOutcome
}

// ErrRewriteAbandoned is what a RewriteStream returns when it stopped
// interpreting the body partway (malformed JSON, or one token larger than its
// window) and relayed everything from that point on byte-identical. It is
// returned, wrapped with the reason, only after the whole remainder of src
// reached dst, so the client still holds a complete body. Any other non-nil
// error from a RewriteStream is an I/O failure on src or dst.
var ErrRewriteAbandoned = errors.New("rewrite abandoned; remainder relayed unchanged")

// RewriteRow binds one request shape (ecosystem, HTTP method, path-shape
// matcher) to a response-body rewriter. A shape with no matching row is never
// inspected at all, not even to sniff its Content-Type, so issue #2854's
// wrong-media-type defect is excluded by construction rather than by a
// runtime guard.
type RewriteRow struct {
	Name      string // shape name, used only in log lines (e.g. "cargo config.json")
	Ecosystem string // an ecosystem.Table row's Name; matched against the route's tagged subtrees
	Method    string // must be body-bearing; the proxy runs a matching row against a HEAD response's empty body too
	Matches   func(routeRelativePath, base string) bool
	// Exactly one of Rewrite and RewriteStream is set.
	Rewrite func(body []byte, rc RewriteContext) RewriteResult
	// RewriteStream, set instead of Rewrite, rewrites src into dst as it
	// reads, for a document too large to buffer whole (issue #5003). onEdit
	// runs for each edited value (declined ones too, with an empty To) before
	// the edited bytes are written to dst, so whatever the caller learns from
	// an edit is in place before a client can read the value it admits.
	RewriteStream func(dst io.Writer, src io.Reader, rc RewriteContext, onEdit func(RewriteEdit)) error
}

// JoinBase joins a route-relative subtree base with a row-relative path.
// base == "/" is the sentinel for "no base segment at all", so it combines
// with rel as rel itself; prepending it literally would yield
// "//config.json" for every host-rooted subtree, which is the common case.
func JoinBase(base, rel string) string {
	if base == "/" {
		return rel
	}
	return base + rel
}

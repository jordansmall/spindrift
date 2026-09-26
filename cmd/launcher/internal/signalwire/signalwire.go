// Package signalwire is the Signal socket's wire contract (ADR 0052, issue
// #3724): the kinds, the request and reply shapes, and the byte limits both
// ends of the socket agree on.
//
// It imports nothing outside the standard library, deliberately. Go links a
// whole package, so a Box binary reaching for these shapes where they used to
// live -- beside the listener in internal/signalsocket -- would link
// internal/doctor, and behind it internal/runner, internal/forge and the
// container backend, into the Box. internal/registrymanifest is the same move
// for the registry proxy's Box-facing contract.
package signalwire

// Kind is the closed set of signal kinds. A Kind names the route the handler
// serves, so it is a receipt field, never a request field.
type Kind string

const (
	KindComment     Kind = "comment"
	KindPRIntent    Kind = "pr-intent"
	KindIssueIntent Kind = "issue-intent"
)

// UsageRoute is the diagnostics-only path segment a client-side usage
// failure (a bad flag, a missing required one) posts to: "/K/usage" for a
// kind K the client named, or "/usage" when it could not name one at all
// (missing or unrecognised kind word). The route buffers nothing and is
// never consumed at settle -- it exists only to be logged (ADR 0052).
const UsageRoute = "usage"

// UsagePath returns the diagnostics-only usage route for kind, or the
// kindless "/usage" route when kind is empty. Both the Box client and the
// host handler call this so the two ends never drift on the path shape.
func UsagePath(kind string) string {
	if kind == "" {
		return "/" + UsageRoute
	}
	return "/" + kind + "/" + UsageRoute
}

// MaxUsageReasonBytes bounds a usage report's Reason. A flag package error
// line is well under this; the limit exists so the diagnostics-only route
// stays bounded like every other request (ADR 0052).
const MaxUsageReasonBytes = 1024

// UsageReport is a client-side usage failure's content -- a `driver-exec
// signal` CLI/flag usage failure (unknown flag, missing required flag),
// never a token-usage figure; that is dispatch.UsageReport / internal/usage,
// an unrelated concept sharing only the word. The kind is the route, never a
// request field, so this carries only the reason.
type UsageReport struct {
	Reason string `json:"reason"`
}

// MaxBodyBytes bounds a signal's body. This package owns the limit
// deliberately: internal/forge's text bound governs the opposite
// direction (text carried into the Box through execve), so coupling the
// two would tie an argv constraint to a socket one.
const MaxBodyBytes = 64 * 1024

// MaxRequestBytes bounds a request body before it is decoded. It sits well
// above MaxBodyBytes because a legal max-size body field does not serialise to
// MaxBodyBytes bytes: the JSON envelope adds braces, keys and quotes, and
// string escaping can turn one content byte into six (a control byte encodes
// as a six-character \u escape). The slack carries that worst case, so the
// buffer's own limit -- which reports the precise "oversize" fault -- stays
// the binding one for any body a Box could legitimately send.
const MaxRequestBytes = 6*MaxBodyBytes + 64*1024

// SecretHeader is the signal socket's own TCP bearer header. It is
// deliberately NOT registrymanifest.TCPSecretHeader: separate listeners,
// separate credentials (ADR 0052), so compromising one gate never opens the
// other and either listener can be deleted without disturbing the other.
const SecretHeader = "X-Spindrift-Signal-Secret"

// Comment is a comment signal's content.
type Comment struct {
	Body string `json:"body"`
}

// PRIntent is a pull-request-intent signal's content.
type PRIntent struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// IssueIntent is an issue-intent signal's content. Type is the closed
// doctor.FindingTypeLabels vocabulary; the host still picks labels, but
// DedupTerms is the Box's to supply (issue #3609) -- only the Box knows the
// finding's site.
type IssueIntent struct {
	Title      string   `json:"title"`
	Body       string   `json:"body"`
	Type       string   `json:"type"`
	DedupTerms []string `json:"dedupTerms,omitempty"`
}

// Receipt is an accept reply's payload.
type Receipt struct {
	Kind     Kind   `json:"kind"`
	Bytes    int    `json:"bytes"`
	Hash     string `json:"hash"`
	Sequence int    `json:"sequence"`
}

// Reject is a reject reply's payload plus the HTTP status the handler answers
// with. Reason stays one lower-case line that names no path, token, issue or
// field value: it crosses back into the Box, which must learn what to fix and
// nothing about the host.
type Reject struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
	Code   int    `json:"-"`
}

// Status reports what the buffer has accepted so far: kinds and hashes, never
// content.
type Status struct {
	Comment      *Receipt  `json:"comment,omitempty"`
	PRIntent     *Receipt  `json:"prIntent,omitempty"`
	IssueIntents []Receipt `json:"issueIntents,omitempty"`
}

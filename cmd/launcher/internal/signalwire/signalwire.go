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
// doctor.FindingTypeLabels vocabulary; unlike settle's log-scanned shape it
// carries no labels and no dedup terms, because the host picks those.
type IssueIntent struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	Type  string `json:"type"`
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

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

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"
)

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
// above MaxBodyBytes because a legal max-size field does not serialise to
// MaxBodyBytes bytes: the JSON envelope adds braces, keys and quotes, and
// string escaping can turn one content byte into six (a control byte encodes
// as a six-character \u escape). Sized for two worst-case-escaped fields, not
// one -- an issue-intent request can carry both a max-size body and a
// max-size patch (ADR 0057, issue #4072) at once, so a single field's
// six-times slack is not enough headroom. The slack carries that worst case,
// so the buffer's own per-field limit -- which reports the precise "oversize"
// fault naming the offending field -- stays the binding one for any request a
// Box could legitimately send. The trade-off: every request kind, patch field
// or not, may now buffer up to this doubled bound before decoding.
const MaxRequestBytes = 12*MaxBodyBytes + 64*1024

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

// IssueIntent is an issue-intent finding's one wire shape, used end to end:
// driver-exec's client builds it, the socket buffers it, and settle files
// it. Type is the closed doctor.FindingTypeLabels vocabulary; the host
// still picks labels, but DedupTerms is the Box's to supply (issue #3609)
// -- only the Box knows the finding's site. Class and Concurrence are issue
// #3880's auto-promotion gate: Class is the Box's own claim about the
// finding's kind, checked against the host's allow-list only at settle, and
// Concurrence is the in-Box reviewer subagent's one-line agreement, empty
// when it dissented or never ran. Neither can promote anything by itself --
// the host holds the allow-list and the daily budget, and the Box cannot
// change either. Patch (ADR 0057, issue #4072) is an optional unified diff of
// modification hunks only, no binary content; the host alone decides
// whether it is ever applied, and only for a finding whose Class is on the
// host's own patch allow-list -- the Box merely carries it.
type IssueIntent struct {
	Title       string   `json:"title"`
	Body        string   `json:"body"`
	Type        string   `json:"type"`
	DedupTerms  []string `json:"dedupTerms,omitempty"`
	Class       string   `json:"class,omitempty"`
	Concurrence string   `json:"concurrence,omitempty"`
	Patch       string   `json:"patch,omitempty"`
}

// PruneBlankDedupTerms returns i with every whitespace-only DedupTerms entry
// removed. It is the one place that rule lives -- Validate calls it before
// building its field list, and the socket calls it before computing
// Bytes/Hash, so the two carriers cannot drift on which terms survive.
func (i IssueIntent) PruneBlankDedupTerms() IssueIntent {
	i.DedupTerms = slices.DeleteFunc(slices.Clone(i.DedupTerms), func(t string) bool {
		return strings.TrimSpace(t) == ""
	})
	return i
}

// Validate checks the rules every carrier shares: non-blank title and body,
// well-formed UTF-8 within MaxBodyBytes for every field set, then a legal
// Class slug. Type stays optional here because the log carrier lets the Box
// omit it; the socket requires it itself, via ValidateTypeRequired.
func (i IssueIntent) Validate() *Reject {
	return i.validate(false)
}

// ValidateTypeRequired is Validate plus a non-blank Type, for the socket
// route where Type is mandatory. Folding the check into the same field list
// -- rather than checking it separately, before or after -- keeps the
// "wrong in two ways always reports the same fault" contract CheckFields
// gives every other field.
func (i IssueIntent) ValidateTypeRequired() *Reject {
	return i.validate(true)
}

func (i IssueIntent) validate(typeRequired bool) *Reject {
	i = i.PruneBlankDedupTerms()
	fs := []Field{{"title", i.Title}, {"body", i.Body}}
	if i.Type != "" || typeRequired {
		fs = append(fs, Field{"type", i.Type})
	}
	for idx, term := range i.DedupTerms {
		fs = append(fs, Field{fmt.Sprintf("dedupTerms[%d]", idx), term})
	}
	if i.Class != "" {
		fs = append(fs, Field{"class", i.Class})
	}
	if i.Concurrence != "" {
		fs = append(fs, Field{"concurrence", i.Concurrence})
	}
	if i.Patch != "" {
		fs = append(fs, Field{"patch", i.Patch})
	}
	if rej := CheckFields(fs...); rej != nil {
		return rej
	}
	if i.Class != "" && !ValidClass(i.Class) {
		return &Reject{Status: "invalid_class", Reason: "class " + ClassRule, Code: http.StatusBadRequest}
	}
	if i.Patch != "" {
		if err := ValidateUnifiedDiff(i.Patch); err != nil {
			return &Reject{Status: "invalid_patch", Reason: err.Error(), Code: http.StatusBadRequest}
		}
	}
	return nil
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

// Field is one content field: its wire name and its value. Every field is
// bounded by MaxBodyBytes -- title and type included, not just body --
// because the alternative is a per-field guess: without it a title is held
// only by MaxRequestBytes, thirteen times the limit any tracker accepts.
type Field struct {
	Name  string
	Value string
}

// CheckFields runs the content checks every carrier of a wire shape shares,
// in their pinned order -- empty, then UTF-8, then size -- across every
// field, so a signal wrong in two ways always reports the same fault.
func CheckFields(fs ...Field) *Reject {
	for _, f := range fs {
		if strings.TrimSpace(f.Value) == "" {
			return &Reject{Status: "empty", Reason: f.Name + " is empty", Code: http.StatusBadRequest}
		}
	}
	for _, f := range fs {
		if !utf8.ValidString(f.Value) {
			return InvalidUTF8Reject(f.Name)
		}
	}
	for _, f := range fs {
		if len(f.Value) > MaxBodyBytes {
			return &Reject{
				Status: "oversize",
				Reason: fmt.Sprintf("%s exceeds the %d-byte limit", f.Name, MaxBodyBytes),
				Code:   http.StatusRequestEntityTooLarge,
			}
		}
	}
	return nil
}

// InvalidUTF8Reject is the package's one invalid_utf8 reject: the caller
// names the offending field or, for a raw request body, a fixed token.
func InvalidUTF8Reject(what string) *Reject {
	return &Reject{Status: "invalid_utf8", Reason: what + " is not valid utf-8", Code: http.StatusBadRequest}
}

// Status reports what the buffer has accepted so far: kinds and hashes, never
// content.
type Status struct {
	Comment      *Receipt  `json:"comment,omitempty"`
	PRIntent     *Receipt  `json:"prIntent,omitempty"`
	IssueIntents []Receipt `json:"issueIntents,omitempty"`
}

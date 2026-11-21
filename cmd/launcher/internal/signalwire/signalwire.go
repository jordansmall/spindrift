// Package signalwire is the Signal socket's wire contract (ADR 0052, issue
// #3724): the kinds, the request and reply shapes, and the byte limits both
// ends of the socket agree on.
//
// It imports only the standard library, deliberately: the Box binary links
// it, and living beside the listener in internal/signalsocket would drag
// internal/doctor, internal/runner and internal/forge into the Box.
// internal/registrymanifest does the same for the registry proxy.
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
// failure posts to. It buffers nothing and is only logged (ADR 0052).
const UsageRoute = "usage"

// UsagePath returns the usage route for kind, or "/usage" when the client
// could not name a kind. Both ends call it so the path shape cannot drift.
func UsagePath(kind string) string {
	if kind == "" {
		return "/" + UsageRoute
	}
	return "/" + kind + "/" + UsageRoute
}

// MaxUsageReasonBytes bounds a usage report's Reason, so the diagnostics
// route stays bounded like every other request (ADR 0052).
const MaxUsageReasonBytes = 1024

// UsageReport is a `driver-exec signal` flag-usage failure, unrelated to
// token usage (dispatch.UsageReport). The kind is the route, so this carries
// only the reason.
type UsageReport struct {
	Reason string `json:"reason"`
}

// MaxBodyBytes bounds a signal's body. It is separate from internal/forge's
// text bound, an argv (execve) constraint on text flowing into the Box.
const MaxBodyBytes = 64 * 1024

// MaxRequestBytes bounds a request body before decoding. JSON escaping can
// turn one content byte into six (\u escape), and an issue-intent can carry a
// max-size body and a max-size patch at once (ADR 0057, issue #4072), so it is
// sized for two worst-case fields. That keeps the per-field "oversize" reject,
// which names the field, the binding limit for any legitimate request. The
// cost: every request kind may buffer up to this doubled bound.
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

// Validate reports why p cannot be accepted, or nil. Dispatch joins the PR
// text as title+"\n\n"+body and settle cuts the title at the first newline,
// so a multi-line title accepted here would be split there (issue #5004).
func (p PRIntent) Validate() *Reject {
	if rej := CheckFields(Field{"title", p.Title}, Field{"body", p.Body}); rej != nil {
		return rej
	}
	if strings.ContainsAny(p.Title, "\r\n") {
		return &Reject{Status: "multiline_title", Reason: "title must be a single line", Code: http.StatusBadRequest}
	}
	return nil
}

// IssueIntent is an issue-intent finding's one wire shape, end to end:
// driver-exec builds it, the socket buffers it, settle files it. Type is the
// closed doctor.FindingTypeLabels vocabulary; DedupTerms is the Box's to
// supply since only it knows the finding's site (issue #3609). Class (the
// Box's own claim, checked against the host's allow-list only at settle) and
// Concurrence (the in-Box reviewer's one-line agreement, empty when it
// dissented or never ran) feed issue #3880's auto-promotion gate, but the host
// holds the allow-list and daily budget, so neither promotes anything alone.
// Cites (digest anchors) and Metric (a slug) are a tuning finding's evidence
// (issue #4952): shape-checked on every intent, but only driver-exec requires
// them, when CHORE_INPUT is set. Patch (ADR 0057, issue #4072) is an optional
// unified diff of modification hunks only, no binary content; the host applies
// it only for a Class on its own patch allow-list.
type IssueIntent struct {
	Title       string   `json:"title"`
	Body        string   `json:"body"`
	Type        string   `json:"type"`
	DedupTerms  []string `json:"dedupTerms,omitempty"`
	Cites       []string `json:"cites,omitempty"`
	Metric      string   `json:"metric,omitempty"`
	Class       string   `json:"class,omitempty"`
	Concurrence string   `json:"concurrence,omitempty"`
	Patch       string   `json:"patch,omitempty"`
}

// PruneBlankDedupTerms returns i with every whitespace-only DedupTerms entry
// removed. Validate and the socket's Bytes/Hash both use it, so the carriers
// cannot drift on which terms survive.
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
// route. Type joins the same field list so CheckFields' fault order holds.
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
	for idx, cite := range i.Cites {
		fs = append(fs, Field{fmt.Sprintf("cites[%d]", idx), cite})
	}
	if i.Metric != "" {
		fs = append(fs, Field{"metric", i.Metric})
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
	// The metric slug deliberately shares the class grammar (ClassRule).
	if i.Metric != "" && !ValidClass(i.Metric) {
		return &Reject{Status: "invalid_metric", Reason: "metric " + ClassRule, Code: http.StatusBadRequest}
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

// Field is one content field. Every field, title and type included, is
// bounded by MaxBodyBytes; otherwise a title is held only by MaxRequestBytes,
// thirteen times what any tracker accepts.
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

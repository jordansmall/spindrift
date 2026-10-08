// Package dispatchkey declares one Dispatch key sum type (issue #3988): a
// Dispatch is keyed by exactly one of a tracker issue number or a butler
// Chore name (ADR 0056), never both. report and daemon both need this type —
// daemon must not pull dispatch's heavy graph (see internal/dispatchkind's
// doc for the same leaf rationale) — so it lives here rather than in
// internal/dispatch, which imports internal/report and would cycle.
package dispatchkey

import (
	"fmt"
	"strings"
)

// Key identifies a Dispatch: either a tracker issue number or a butler
// Chore name. Fields are unexported so no caller can construct a value with
// both set, or with neither.
type Key struct {
	issue string
	chore string
}

// Set is the keys a live daemon child holds. It is shared read-only with
// whoever receives it: the receiver must not mutate or retain it. A nil Set
// is empty.
type Set map[Key]bool

// Issue builds a Key for a tracker-issue-keyed Dispatch.
func Issue(number string) Key { return Key{issue: number} }

// Chore builds a Key for a butler Chore-keyed Dispatch (ADR 0056).
func Chore(name string) Key { return Key{chore: name} }

// ChorePrefix leads the rendered form of every Chore Key.
const ChorePrefix = "butler-"

// IsChoreKey reports whether s is a rendered Chore Key, as opposed to a bare
// issue number.
func IsChoreKey(s string) bool { return strings.HasPrefix(s, ChorePrefix) }

// String renders the key: the bare issue number, or ChorePrefix + chore name.
// buildBoxEnv forwards it to the Box as DISPATCH_KEY, which the box program
// (cmd/launcher/box) and butler-prompt.md's OUTCOME line both read.
func (k Key) String() string {
	if k.chore != "" {
		return ChorePrefix + k.chore
	}
	return k.issue
}

// IsChore reports whether this Key is a butler Chore key.
func (k Key) IsChore() bool { return k.chore != "" }

// IsZero reports whether this Key has neither field set.
func (k Key) IsZero() bool { return k.issue == "" && k.chore == "" }

// Fields returns the one set field and "" for the other, for wire encoding.
func (k Key) Fields() (issue, chore string) { return k.issue, k.chore }

// Parse is the inverse of Fields: exactly one of issue, chore must be
// non-empty. It does structural checking only; content validation (digit
// runs, chore name rules) stays with callers (daemon's validIssue/validChore).
func Parse(issue, chore string) (Key, error) {
	switch {
	case issue != "" && chore != "":
		return Key{}, fmt.Errorf("dispatchkey: both issue %q and chore %q set", issue, chore)
	case issue == "" && chore == "":
		return Key{}, fmt.Errorf("dispatchkey: neither issue nor chore set")
	case chore != "":
		return Chore(chore), nil
	default:
		return Issue(issue), nil
	}
}

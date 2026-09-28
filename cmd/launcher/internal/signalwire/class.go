package signalwire

import "regexp"

// classSlugRE and MaxClassLen are the one Chore class grammar (issue #3986),
// shared by the host's configured allow-list and the Box's IssueIntent.Class.
// The Box's Class is interpolated unescaped into host-authored notes (issue
// #3880), so anything wider -- in shape or length -- would let the Box inject
// its own formatting rather than merely name a class.
var classSlugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// MaxClassLen is the length bound ClassRule and ValidClass enforce.
const MaxClassLen = 40

// ClassRule describes ValidClass for error messages; keep the two in step.
const ClassRule = "must be a lowercase slug of letters, digits, and '-', not starting with '-', at most 40 characters"

// ValidClass reports whether s is a legal Chore class slug.
func ValidClass(s string) bool {
	return len(s) <= MaxClassLen && classSlugRE.MatchString(s)
}

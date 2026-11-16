package driver

import (
	"strings"
)

// The attribution roles core stamps on events. A Driver's decoder uses them to
// attribute the events it emits.
const (
	// ImplementorRole attributes a top-level event in an implement, fix or
	// land pass.
	ImplementorRole = "implementor"
	// ReviewerRole attributes a top-level event in a review or delta-review
	// pass.
	ReviewerRole = "reviewer"
	// DefaultRole attributes an event whose subagent parent is unknown.
	DefaultRole = "subagent"
)

// passRoles mirrors core's pass-to-role table; parity_test.go pins it.
var passRoles = map[string]string{
	"review":       ReviewerRole,
	"delta-review": ReviewerRole,
	"implement":    ImplementorRole,
	"fix":          ImplementorRole,
	"land":         ImplementorRole,
}

// HostLine reports whether line is a host-written spindrift_op line, and, for a
// pass_start op, the attribution role the pass starts ("" for any other op or
// a pass with no role). The host passes these lines to a Decoder so its state
// can follow passes; it decodes their meaning itself and discards the
// Decoder's output for them.
func HostLine(line string) (passRole string, ok bool) {
	v, ok := Scan(strings.TrimSpace(line))
	if !ok {
		return "", false
	}
	return HostLineOf(v)
}

// HostLineOf is HostLine for a line already scanned, so a Decoder that has
// scanned its line needn't scan it again. It reads the root as encoding/json
// would a struct{Type string; SpindriftOp *struct{Op, Role string}}: the last
// "type" wins, a null "spindrift_op" resets the payload, an object merges into
// the payload so far, and any mistyped field fails the line.
func HostLineOf(v Value) (passRole string, ok bool) {
	if v.Kind() != Object { // null decodes to the zero struct, which is no host line
		return "", false
	}
	var typ, op, role string
	var hasOp bool
	for k, m := range v.Members() {
		switch {
		case KeyMatches(k, "type"):
			if !m.DecodeString(&typ) {
				return "", false
			}
		case KeyMatches(k, "spindrift_op"):
			switch m.Kind() {
			case Null:
				hasOp, op, role = false, "", ""
			case Object:
				hasOp = true
				for pk, pv := range m.Members() {
					switch {
					case KeyMatches(pk, "op"):
						if !pv.DecodeString(&op) {
							return "", false
						}
					case KeyMatches(pk, "role"):
						if !pv.DecodeString(&role) {
							return "", false
						}
					}
				}
			default:
				return "", false
			}
		}
	}
	if typ != "spindrift_op" || !hasOp {
		return "", false
	}
	if op != "pass_start" {
		return "", true
	}
	return passRoles[role], true
}

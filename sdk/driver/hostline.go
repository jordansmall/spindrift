package driver

import (
	"encoding/json"
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
	var l struct {
		Type        string `json:"type"`
		SpindriftOp *struct {
			Op   string `json:"op"`
			Role string `json:"role"`
		} `json:"spindrift_op"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(line)), &l) != nil || l.Type != "spindrift_op" || l.SpindriftOp == nil {
		return "", false
	}
	if l.SpindriftOp.Op != "pass_start" {
		return "", true
	}
	return passRoles[l.SpindriftOp.Role], true
}

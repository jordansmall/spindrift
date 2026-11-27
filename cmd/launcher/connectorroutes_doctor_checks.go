package main

import (
	"fmt"
	"strings"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/rowcredential"
)

// connectorRoutesChecks builds two Advisory rows per network Connector row the
// configured run reads through: the Route table it holds, and the diff of its
// grant (Routes, credential variable, authorization scheme, base, Git remote)
// against the Consumer's accepted baseline (ADR 0073, issue #6806). A row the
// run does not read through is no concern of this run's doctor (issue #7383).
// A widening is a finding for the operator to review, never a configuration
// fault, so the rows reach the report half only.
func connectorRoutesChecks(c config) []doctor.Check {
	var checks []doctor.Check
	for _, row := range c.connectors.Rows() {
		if !rowcredential.Reads(row, c.CodeForge, c.IssueTracker) {
			continue
		}
		checks = append(checks, connectorRoutesCheck(row), connectorRoutesWidenedCheck(row))
	}
	return checks
}

func connectorRoutesCheck(row backendrows.Row) doctor.Check {
	name := "connector-routes[" + row.Name + "]"
	return doctor.Check{
		Name: name,
		Tier: doctor.Advisory,
		Probe: func() (any, error) {
			return joinRoutes(row.Routes, ", ", ""), nil
		},
		SuccessMsg: func(output any) string {
			credential := "credential " + row.CredentialEnv
			if row.CredentialKnob != "" {
				credential = fmt.Sprintf("credential knob %s (env %s)", row.CredentialKnob, row.CredentialEnv)
			}
			if row.BaseEnv != "" {
				credential += "; host from " + row.BaseEnv
			}
			return fmt.Sprintf("%s (%s; routes: %s)", name, credential, output)
		},
	}
}

func connectorRoutesWidenedCheck(row backendrows.Row) doctor.Check {
	name := "connector-routes-widened[" + row.Name + "]"
	return doctor.Check{
		Name:   name,
		Tier:   doctor.Advisory,
		Remedy: `review the widened grant, then run "spindrift connectors accept" in the Consumer flake's root and commit the spindrift-connector-routes.json diff (ADR 0073)`,
		Probe: func() (any, error) {
			var parts []string
			if added := row.Widening(); len(added) > 0 {
				parts = append(parts, "routes:\n"+joinRoutes(added, "\n", "+ "))
			}
			for _, ch := range row.GrantChanges() {
				parts = append(parts, fmt.Sprintf("%s: %s (accepted: %s)", ch.Part, ch.Declared, ch.Accepted))
			}
			if len(parts) == 0 {
				return nil, nil
			}
			return nil, fmt.Errorf("grant beyond the accepted baseline:\n%s", strings.Join(parts, "\n"))
		},
		SuccessMsg: func(any) string {
			return name + " (within the accepted baseline)"
		},
	}
}

// joinRoutes renders each route as "METHOD host path" behind prefix, joined by sep.
func joinRoutes(routes []connectorhost.Route, sep, prefix string) string {
	lines := make([]string, len(routes))
	for i, r := range routes {
		lines[i] = prefix + r.String()
	}
	return strings.Join(lines, sep)
}

package main

import (
	"fmt"
	"strings"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/doctor"
)

// connectorRoutesChecks builds two Advisory rows per network Connector row of
// the input document: the Route table it holds, and the diff of that table
// against the Consumer's accepted baseline (ADR 0073, issue #6806). A widening
// is a finding for the operator to review, never a configuration fault, so the
// rows reach the report half only.
func connectorRoutesChecks() []doctor.Check {
	var checks []doctor.Check
	for _, row := range backendrows.All() {
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
			return fmt.Sprintf("%s (%s; routes: %s)", name, credential, output)
		},
	}
}

func connectorRoutesWidenedCheck(row backendrows.Row) doctor.Check {
	name := "connector-routes-widened[" + row.Name + "]"
	return doctor.Check{
		Name:   name,
		Tier:   doctor.Advisory,
		Remedy: fmt.Sprintf(`review the added routes, then add them under %q in the Consumer flake's spindrift-connector-routes.json (ADR 0073)`, row.Name),
		Probe: func() (any, error) {
			added := row.Widening()
			if len(added) == 0 {
				return nil, nil
			}
			return nil, fmt.Errorf("routes beyond the accepted table:\n%s", joinRoutes(added, "\n", "+ "))
		},
		SuccessMsg: func(any) string {
			return name + " (within the accepted table)"
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

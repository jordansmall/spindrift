package main

import (
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"

	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/hostprofile"
	"spindrift.dev/launcher/internal/registrypathset"
	"spindrift.dev/launcher/internal/registryroutes"
	"spindrift.dev/launcher/internal/registryvocab"
)

// registryRouteProfileCheckName keeps the row's Name and its SuccessMsg closure
// from drifting apart on a rename.
const registryRouteProfileCheckName = "registry-route-profile"

// profileMatcher is hostprofile.Match's shape. The row takes it as a parameter
// so a test can supply a profile that differs from the shipped table.
type profileMatcher func(declaredURL string, header http.Header) (hostprofile.Row, bool)

// registryRouteProfileCheckForRoutes builds the advisory row warning where a
// route departs from its Host profile (ADR 0071), or nil when the Target repo
// does not resolve. It reads the same snapshot as the drift row.
func registryRouteProfileCheckForRoutes(c config, routes []registryroutes.Route, rows []ecosystem.Strategy) []doctor.Check {
	src, ok := resolveTargetRepoSource(c)
	if !ok {
		return nil
	}
	return []doctor.Check{registryRouteProfileCheckFor(src, routes, rows, hostprofile.Match)}
}

// registryRouteProfileCheckFor builds the row for an already-resolved source,
// so a test can hand it a fixture dir directly.
func registryRouteProfileCheckFor(src targetRepoSource, routes []registryroutes.Route, rows []ecosystem.Strategy, match profileMatcher) doctor.Check {
	return registryRouteProfileRow(routes, rows, match, func() ([]registrypathset.HostPathSet, error) {
		if src.ref != "" {
			return registrypathset.DeriveFromGitRef(src.dir, src.ref, rows)
		}
		return registrypathset.Derive(src.dir, rows)
	})
}

// registryRouteProfileRow builds the profile row shared by both sources, which
// differ only in the derive they supply.
func registryRouteProfileRow(routes []registryroutes.Route, rows []ecosystem.Strategy, match profileMatcher, derive func() ([]registrypathset.HostPathSet, error)) doctor.Check {
	return doctor.Check{
		Name:   registryRouteProfileCheckName,
		Tier:   doctor.Advisory,
		Remedy: "a Host profile is advisory knowledge, not enforcement; if the departure is not deliberate, edit the route's auth-scheme or add the missing path to its allow list",
		Probe: func() (any, error) {
			sets, err := derive()
			if err != nil {
				return nil, fmt.Errorf("%w: %w", err, doctor.ErrDegraded)
			}
			findings := routeProfileFindings(routes, rows, match, sets)
			if len(findings) > 0 {
				return nil, errors.New(strings.Join(findings, "; "))
			}
			return "no departures", nil
		},
		SuccessMsg: func(output any) string {
			return fmt.Sprintf("%s (%s)", registryRouteProfileCheckName, output)
		},
	}
}

// routeProfileFindings makes no network request: match always gets a nil
// header, so only the host-pattern and declared-URL-path signals can hit.
func routeProfileFindings(routes []registryroutes.Route, rows []ecosystem.Strategy, match profileMatcher, sets []registrypathset.HostPathSet) []string {
	byHost := make(map[string]registrypathset.HostPathSet, len(sets))
	for _, s := range sets {
		byHost[s.Host] = s
	}

	var findings []string
	seen := map[string]bool{}
	add := func(f string) {
		if !seen[f] {
			seen[f] = true
			findings = append(findings, f)
		}
	}
	for _, r := range routes {
		// A route the Target repo never declares resolves to a zero Route: it
		// serves no ecosystem here but is still matched by host for the scheme
		// rule.
		resolved, _ := registrypathset.ApplyToRoute(r.ProxyRoute(), byHost, rows)

		var prof hostprofile.Row
		var ok bool
		for _, sub := range resolved.EnforcedSubtrees {
			if prof, ok = match(resolved.Upstream+sub.Path, nil); ok {
				break
			}
		}
		if !ok {
			prof, ok = match("https://"+r.MatchHost, nil)
		}
		if !ok {
			continue
		}

		if prof.AuthScheme != "" && r.AuthScheme != prof.AuthScheme {
			add(fmt.Sprintf("route %q uses auth-scheme %s, profile %s expects %s", r.MatchHost, r.AuthScheme, prof.Name, prof.AuthScheme))
		}
		admitted := registryvocab.PathSet(resolved.EnforcedPaths)
		for _, sub := range resolved.EnforcedSubtrees {
			for _, s := range prof.Siblings[sub.Ecosystem] {
				if p := path.Join(sub.Path, s); !admitted.Admits(p) {
					add(fmt.Sprintf("route %q does not admit %s's %s sibling path %s; add it to allow", r.MatchHost, prof.Name, sub.Ecosystem, p))
				}
			}
		}
	}
	return findings
}

package registrydiscover

// UncoveredHosts runs Extract against repoDir and returns each declared
// host (deduped and normalized exactly as Discover's own loop does: via
// hostOnly, first-occurrence order) that matches none of covered --
// typically a configured routes file's own route MatchHost values, each
// normalized through the same hostOnly before comparison. Reusing Extract
// and hostOnly here, rather than a second host-enumeration pass, is what
// keeps `spindrift doctor`'s drift row and `spindrift registry discover`
// consistent by construction (issue #3144 slice 2 AC5): both read the same
// declared hosts off the same repo tree the same way.
//
// Coverage is hostOnly-normalized equality, the same comparison
// registryroutes.Parse uses to reject two routes declaring one host --
// registryproxy routes requests by the path prefix AssignPrefixes derives
// from MatchHost at synthesis time, so declared-host equality is the only
// coverage semantics there is.
func UncoveredHosts(repoDir string, covered []string) ([]string, error) {
	declared, _, err := Extract(repoDir)
	if err != nil {
		return nil, err
	}

	coveredSet := make(map[string]bool, len(covered))
	for _, h := range covered {
		coveredSet[hostOnly(h)] = true
	}

	var uncovered []string
	seen := make(map[string]bool, len(declared))
	for _, d := range declared {
		host := hostOnly(d.Host)
		if seen[host] {
			continue
		}
		seen[host] = true
		if !coveredSet[host] {
			uncovered = append(uncovered, host)
		}
	}
	return uncovered, nil
}

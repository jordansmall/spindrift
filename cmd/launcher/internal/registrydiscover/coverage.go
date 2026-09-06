package registrydiscover

import "spindrift.dev/launcher/internal/registryvocab"

// UncoveredHosts runs Extract against repoDir and returns each declared
// host (deduped and normalized exactly as Discover's own loop does: via
// registryvocab.HostKey, first-occurrence order) that matches none of
// covered -- typically a configured routes file's own route MatchHost
// values, each normalized through the same registryvocab.HostKey before
// comparison. Reusing Extract and registryvocab.HostKey here, rather than a
// second host-enumeration pass, keeps `spindrift doctor`'s drift row and
// `spindrift registry discover` consistent by construction (issue #3144
// slice 2 AC5): both read the same declared hosts off the same repo tree the
// same way.
//
// Coverage is registryvocab.HostKey-normalized equality, the same comparison
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
		coveredSet[registryvocab.HostKey(h)] = true
	}

	var uncovered []string
	seen := make(map[string]bool, len(declared))
	for _, d := range declared {
		host := registryvocab.HostKey(d.Host)
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

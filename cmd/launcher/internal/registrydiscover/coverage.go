package registrydiscover

import (
	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/registryvocab"
)

// UncoveredHosts returns each host declared under repoDir that matches none of
// covered, deduped in first-occurrence order. Both sides normalize through
// registryvocab.HostKey so doctor and `registry discover` agree (issue #3144).
// registryproxy routes by MatchHost, so comparing hosts is the whole of
// coverage.
func UncoveredHosts(repoDir string, covered []string, rows []ecosystem.Strategy) ([]string, error) {
	declared, _, err := Extract(repoDir, rows)
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

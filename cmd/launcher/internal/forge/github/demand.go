package github

import (
	"time"

	"spindrift.dev/launcher/internal/forge"
)

var _ forge.DemandCounter = (*execClient)(nil)

// demandProbeInterval is how often the daemon re-counts GitHub demand. The
// Connector answers an unchanged page with a conditional request that costs no
// primary rate limit, but a changed one costs a request, so the cadence stays
// relaxed.
const demandProbeInterval = 60 * time.Second

// CountReady implements forge.DemandCounter through the Connector, which owns
// the conditional-request cache; fresh makes it skip the validator, forcing a
// full read after a count the child contradicted.
func (e *execClient) CountReady(fresh bool) (int, error) {
	row, err := routeRow()
	if err != nil {
		return 0, err
	}
	return e.countReady(row, fresh)
}

// ProbeInterval implements forge.DemandCounter.
func (e *execClient) ProbeInterval() time.Duration { return demandProbeInterval }

package butler

import (
	"fmt"
	"os"
	"time"

	"spindrift.dev/launcher/internal/ledger"
)

// dayRoom is the day's shared promotion budget, re-walked from the Ledger
// each remaining call rather than carried as a value computed at run start:
// a promotion whose done commit this run landed between run start and
// settle must still count. A hosted Ledger's view is the run's mirror
// (issue #3995), so a rival's promotions since run start go uncounted; like
// ADR 0056's other budgets this is a soft cap, not a hard one.
type dayRoom struct {
	perDay int
	chores []string
	zone   *time.Location
}

// newDayRoom builds a dayRoom from policy's budget fields, so the Runner and
// its tests cannot drift apart.
func newDayRoom(policy Policy) dayRoom {
	return dayRoom{perDay: policy.MaxPromotionsPerDay, chores: policy.choreNames(), zone: policy.Zone}
}

// remaining sums today's totals across d.chores in d.zone from backend. A
// DayTotalsAll error fails closed (0 room, a warning to stderr) rather than
// promoting on a total it could not compute.
func (d dayRoom) remaining(backend ledger.Backend, now time.Time) int {
	totals, err := ledger.DayTotalsAll(backend, d.chores, now.In(d.zone))
	if err != nil {
		fmt.Fprintf(os.Stderr, "butler: promotion room: %s\n", err)
		return 0
	}
	remaining := d.perDay - totals.Promoted
	if remaining < 0 {
		remaining = 0
	}
	return remaining
}

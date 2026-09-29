package butler

import (
	"fmt"
	"os"
	"time"

	"spindrift.dev/launcher/internal/ledger"
)

// dayRoom is the day's shared promotion budget, re-walked fresh from the
// Ledger each remaining call rather than carried as a stale snapshot: a
// promotion whose done commit landed between run start and settle must
// still count. Like ADR 0056's other budgets this is a soft cap, not a hard
// one -- two runs settling at the same moment can each read the same total
// and both spend it.
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

// remaining takes its own fresh ledger.Snapshot of backend (rather than
// reusing any snapshot a caller already holds), since it must see
// promotions that landed since that earlier snapshot was taken, then sums
// today's totals across d.chores in d.zone. A Snapshot or DayTotalsAll
// error fails closed (0 room, a warning to stderr) rather than promoting on
// a total it could not compute.
func (d dayRoom) remaining(backend ledger.Backend, now time.Time) int {
	var totals ledger.Totals
	snap, err := ledger.Snapshot(backend)
	if err == nil {
		totals, err = ledger.DayTotalsAll(snap, d.chores, now.In(d.zone))
	}
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

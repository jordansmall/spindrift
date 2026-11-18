package github

import (
	"strconv"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

func seedReadyRange(h *connectorHarness, first, n int) {
	for i := first; i < first+n; i++ {
		num := strconv.Itoa(i)
		h.SeedIssue(forge.Issue{Number: num, Title: num, Labels: []string{testLabels.Dispatchable}})
	}
}

// listReads returns the issues-list requests the backend has served since the
// last call, so each assertion sees only its own poll.
func (b *restBackend) listReads() []listRead {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := b.lists
	b.lists = nil
	return out
}

func countVia(t *testing.T, c *execClient, fresh bool) int {
	t.Helper()
	n, err := c.CountReady(fresh)
	if err != nil {
		t.Fatalf("CountReady(%v): %v", fresh, err)
	}
	return n
}

// Under a github row the daemon's demand count keeps the native adapter's
// protections: an unchanged poll is one conditional request answered 304, and
// fresh forces a full read.
func TestConnector_CountReadyUnchangedPollIs304(t *testing.T) {
	h, c, calls := routedHarness(t)
	seedReadyRange(h, 1, 3)

	if got := countVia(t, c, false); got != 3 {
		t.Fatalf("first count = %d, want 3", got)
	}
	reads := h.backend.listReads()
	if len(reads) != 1 || reads[0].ifNoneMatch != "" || reads[0].status != 200 {
		t.Fatalf("first poll reads = %+v, want one unconditional 200", reads)
	}
	for _, want := range []string{"sort=updated", "direction=desc", "per_page=100", "state=open", "labels=" + testLabels.Dispatchable} {
		if !strings.Contains(reads[0].query, want) {
			t.Errorf("query %q lacks %q", reads[0].query, want)
		}
	}

	if got := countVia(t, c, false); got != 3 {
		t.Fatalf("second count = %d, want the cached 3", got)
	}
	reads = h.backend.listReads()
	if len(reads) != 1 || reads[0].ifNoneMatch == "" || reads[0].status != 304 || !strings.Contains(reads[0].query, "page=1") {
		t.Fatalf("second poll reads = %+v, want one conditional page-1 request answered 304", reads)
	}

	if got := countVia(t, c, true); got != 3 {
		t.Fatalf("fresh count = %d, want 3", got)
	}
	reads = h.backend.listReads()
	if len(reads) != 1 || reads[0].ifNoneMatch != "" || reads[0].status != 200 {
		t.Fatalf("fresh poll reads = %+v, want one unconditional 200", reads)
	}

	if n := calls(); n != 0 {
		t.Errorf("the routed count ran gh %d times", n)
	}
	if h.backend.unexpected != nil {
		t.Errorf("unexpected backend requests: %v", h.backend.unexpected)
	}
}

// Relabelling an old issue bumps its updated time, so the update-sorted page 1
// changes and a non-fresh poll sees the new count instead of a 304.
func TestConnector_CountReadyRelabelledOldIssueIsCounted(t *testing.T) {
	h, c, _ := routedHarness(t)
	h.SeedIssue(forge.Issue{Number: "1", Title: "old"})
	seedReadyRange(h, 2, 2)
	if got := countVia(t, c, false); got != 2 {
		t.Fatalf("count = %d, want 2", got)
	}
	h.backend.listReads()

	h.mutate("1", func(i *restIssue) { i.labels = []string{testLabels.Dispatchable} })

	if got := countVia(t, c, false); got != 3 {
		t.Fatalf("count after relabel = %d, want 3", got)
	}
	reads := h.backend.listReads()
	if len(reads) != 1 || reads[0].ifNoneMatch == "" || reads[0].status != 200 {
		t.Fatalf("reads = %+v, want one conditional request answered 200 with the new page", reads)
	}
}

// Core reloads a Connector after a failed call, which empties the guest's ETag
// cache: the next poll is a full read, not a 304.
func TestConnector_CountReadyReloadDropsTheCachedETag(t *testing.T) {
	h, c, _ := routedHarness(t)
	seedReadyRange(h, 1, 2)
	countVia(t, c, false)
	h.backend.listReads()

	closeSharedConnectors()

	if got := countVia(t, c, false); got != 2 {
		t.Fatalf("count after reload = %d, want 2", got)
	}
	reads := h.backend.listReads()
	if len(reads) != 1 || reads[0].ifNoneMatch != "" || reads[0].status != 200 {
		t.Fatalf("reads after reload = %+v, want one unconditional 200", reads)
	}
}

// Past the cap the walk stops: 130 ready issues take page 1 only, and the
// count is clamped to forge.ResultPageLimit.
func TestConnector_CountReadyStopsAtTheCap(t *testing.T) {
	h, c, _ := routedHarness(t)
	seedReadyRange(h, 1, forge.ResultPageLimit+30)

	if got := countVia(t, c, false); got != forge.ResultPageLimit {
		t.Fatalf("count = %d, want %d", got, forge.ResultPageLimit)
	}
	if reads := h.backend.listReads(); len(reads) != 1 {
		t.Fatalf("reads = %+v, want page 1 only once the cap is met", reads)
	}
}

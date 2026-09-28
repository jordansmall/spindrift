package forgejo_test

import (
	"net/http"
	"strconv"
)

// forgejoServerPageCap mirrors Forgejo's default [api] MAX_RESPONSE_ITEMS
// cap (issue #3978). Stock Forgejo clamps whatever limit a client requests
// down to this value, so a fake that always honored a larger client limit
// would never exercise the walk-until-empty-page behavior it's meant to
// fake.
const forgejoServerPageCap = 50

// windowPage reads page (default 1) and limit (default len(items)) from r's
// query, clamps limit at forgejoServerPageCap the way stock Forgejo does,
// and returns the resulting window into items. Out of range returns a
// non-nil empty slice, matching Forgejo's own past-the-end response.
func windowPage[T any](r *http.Request, items []T) []T {
	page := 1
	if p := r.URL.Query().Get("page"); p != "" {
		if v, err := strconv.Atoi(p); err == nil && v > 0 {
			page = v
		}
	}
	limit := len(items)
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 {
			limit = v
		}
	}
	if limit > forgejoServerPageCap {
		limit = forgejoServerPageCap
	}
	if limit <= 0 {
		return []T{}
	}
	start := (page - 1) * limit
	if start >= len(items) {
		return []T{}
	}
	end := start + limit
	if end > len(items) {
		end = len(items)
	}
	return items[start:end]
}

// serveCappedNumbered renders a fake list endpoint serving items numbered
// 1..total, windowed by windowPage so every page is capped at
// forgejoServerPageCap regardless of the client's requested limit, then an
// empty page. render(start, count) produces a non-empty window's JSON.
func serveCappedNumbered(w http.ResponseWriter, r *http.Request, total int, render func(start, count int) string) {
	nums := make([]int, total)
	for i := range nums {
		nums[i] = i + 1
	}
	windowed := windowPage(r, nums)
	if len(windowed) == 0 {
		w.Write([]byte(`[]`))
		return
	}
	w.Write([]byte(render(windowed[0], len(windowed))))
}

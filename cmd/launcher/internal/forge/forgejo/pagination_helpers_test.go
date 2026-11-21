package forgejo_test

import (
	"net/http"
	"strconv"
)

// forgejoServerPageCap mirrors Forgejo's default [api] MAX_RESPONSE_ITEMS
// (issue #3978). Stock Forgejo clamps any requested limit to it; a fake
// honoring a larger limit would never exercise the page walk.
const forgejoServerPageCap = 50

// windowPage returns the window of items r's page and limit query select,
// clamped as stock Forgejo does. Past the end is a non-nil empty slice, as in
// Forgejo.
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

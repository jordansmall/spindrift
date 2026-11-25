package connector

// Pages walks a paged listing from cursor first: fetch reads one page,
// collecting its items itself, and returns the next cursor and whether there is
// one — the Connector's own next-page rule (a Link header, a total count, a
// cursor). fetch runs at most limit times; a listing with more pages fails with
// an error naming limit. An error from fetch ends the walk and is returned
// unchanged.
func Pages[C any](limit int, first C, fetch func(cursor C) (next C, more bool, err error)) error {
	if limit < 1 {
		return Errorf(Permanent, "connector: page limit %d is below 1", limit)
	}
	cursor := first
	for range limit {
		next, more, err := fetch(cursor)
		if err != nil {
			return err
		}
		if !more {
			return nil
		}
		cursor = next
	}
	return Errorf(Permanent, "connector: listing has more pages than the limit of %d pages", limit)
}

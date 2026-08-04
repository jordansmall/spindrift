package forge

// WalkPages repeatedly invokes fetch for page 1, 2, 3, ... until fetch
// reports the walk is done, so a caller performs one request per page and
// decides its own last-page signal without hand-rolling the loop itself.
func WalkPages(fetch func(page int) (done bool, err error)) error {
	for page := 1; ; page++ {
		done, err := fetch(page)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
	}
}

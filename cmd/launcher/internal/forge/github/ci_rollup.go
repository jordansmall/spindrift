package github

// FailureDetail renders the PR's failing checks into a bounded report, or ""
// when none are failing. The fetch is best-effort: a non-nil error means the
// detail is unavailable, and callers should proceed without it.
func (e *execClient) FailureDetail(url string) (string, error) {
	row, err := routeRow(e.connectors)
	if err != nil {
		return "", err
	}
	return e.failureDetail(row, url)
}

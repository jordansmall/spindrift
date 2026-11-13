//go:build !wasip1

package connector

func hostHTTPRequest(_, _ []byte) (uint64, error) { return 0, ErrNoHost }

func hostResponseHeader(_, _ []byte) (uint64, error) { return 0, ErrNoHost }

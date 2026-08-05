package forge

import (
	"errors"
	"testing"
)

func TestIsTransientForgeError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"HTTP 502", errors.New("HTTP 502: Bad Gateway (https://api.github.com/...)"), true},
		{"HTTP 500", errors.New("HTTP 500: Internal Server Error"), true},
		{"i/o timeout", errors.New("dial tcp: i/o timeout"), true},
		{"connection reset", errors.New("connection reset by peer"), true},
		{"ETIMEDOUT", errors.New("ETIMEDOUT"), true},
		{"no such host", errors.New("no such host"), true},
		{"genuine non-transient error", errors.New("gh pr list: exit status 1: no pull requests found"), false},
		{"HTTP 404 not transient", errors.New("HTTP 404: Not Found"), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTransientForgeError(tc.err); got != tc.want {
				t.Errorf("isTransientForgeError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

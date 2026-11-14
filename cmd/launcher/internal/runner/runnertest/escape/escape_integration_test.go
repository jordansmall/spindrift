//go:build integration

package escape

import "testing"

func TestEscapeControlReachesWhatTheSandboxMustNot(t *testing.T) {
	host, skip, err := HostDir()
	if err != nil {
		t.Fatal(err)
	}
	if skip != "" {
		t.Skip(skip)
	}
	Control(t, host)
}

package github

import (
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/seamtest"
)

func TestMain(m *testing.M) {
	// The primary-row contract tests run this binary as their gh; seamtest.Main
	// would answer a name "gh" with its own scripted fake instead.
	if filepath.Base(os.Args[0]) == "gh" && os.Getenv(fakeGHAPIEnv) != "" {
		os.Exit(fakeGHMain(os.Args[1:]))
	}
	seamtest.Main(m)
}

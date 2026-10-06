package runner

import (
	"os"
	"testing"
)

// ociAdapter.Run reaps the temp dir that other packages' tests share when
// `go test ./...` runs them in parallel; point it somewhere private (issue
// #4658). TMPDIR itself stays put: lengthening it pushes the socket tests'
// paths past the unix socket limit.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "spindrift-runner-test-")
	if err != nil {
		panic(err)
	}
	rebaseTempRoot = func() string { return dir }
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

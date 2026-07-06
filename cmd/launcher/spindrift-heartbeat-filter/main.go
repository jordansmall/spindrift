// Command spindrift-heartbeat-filter reads stream-json from stdin, forwards
// all bytes unchanged to stdout (preserving the launcher's byte-exact capture
// channel), and writes coarse heartbeat lines to a file so a human can
// tail -f the file instead of reading raw NDJSON.
//
// Usage: spindrift-heartbeat-filter -n ISSUE_NUMBER -f /tmp/heartbeat.log
//
// This is the in-box companion to the launcher-side heartbeat.Writer (#182).
// It reuses the same heartbeat package so the output format is identical.
// Decoupling mechanism: raw stream-json stays on stdout (captured by the
// launcher into logs/issue-N.log); cleaned heartbeat goes to -f (default
// /tmp/heartbeat.log inside the box). To view heartbeat in a running box:
//
//	podman exec <box> tail -f /tmp/heartbeat.log
//
// or shell in and tail -f the file directly.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"spindrift.dev/launcher/internal/heartbeat"
)

func main() {
	issue := flag.String("n", os.Getenv("ISSUE_NUMBER"), "issue number (default: $ISSUE_NUMBER)")
	filePath := flag.String("f", "/tmp/heartbeat.log", "path to write heartbeat lines")
	flag.Parse()

	if *issue == "" {
		*issue = "0"
	}
	if err := run(*issue, *filePath, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "spindrift-heartbeat-filter:", err)
		os.Exit(1)
	}
}

// run reads stream-json from in, copies all bytes to out unchanged, and writes
// throttled heartbeat lines to the file at filePath.
func run(issue, filePath string, in io.Reader, out io.Writer) error {
	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open heartbeat file: %w", err)
	}
	defer f.Close()
	w := heartbeat.New(out, issue, f, heartbeat.DefaultThrottle)
	_, err = io.Copy(w, in)
	return err
}

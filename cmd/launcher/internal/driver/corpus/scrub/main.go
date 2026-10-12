// Command scrub redacts credential-shaped strings from a Pass log so it can
// join the Driver corpus. Run it as
// `cd cmd/launcher && go run ./internal/driver/corpus/scrub [in [out]]`;
// in defaults to stdin and out to stdout ("-" also means the standard stream).
// Per-pattern redaction counts go to stderr, never the secrets themselves.
package main

import (
	"fmt"
	"io"
	"os"
	"sort"

	"spindrift.dev/launcher/internal/driver/corpus"
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "scrub:", err)
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) > 2 {
		return fmt.Errorf("usage: scrub [in [out]]")
	}
	in, err := readInput(args, stdin)
	if err != nil {
		return err
	}

	counts := map[string]int{}
	for _, f := range corpus.Find(in) {
		counts[f.Pattern]++
	}
	names := make([]string, 0, len(counts))
	for n := range counts {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(stderr, "redacted %d %s\n", counts[n], n)
	}

	out := corpus.Scrub(in)
	if len(args) == 2 && args[1] != "-" {
		return os.WriteFile(args[1], out, 0o644)
	}
	_, err = stdout.Write(out)
	return err
}

func readInput(args []string, stdin io.Reader) ([]byte, error) {
	if len(args) == 0 || args[0] == "-" {
		return io.ReadAll(stdin)
	}
	return os.ReadFile(args[0])
}

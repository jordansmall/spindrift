// Prints the comment-free token stream of the Go file on stdin, one token
// per line, for ci-changes.sh (issue #5158): two files with the same stream
// differ only in comments and non-newline whitespace. Tokens, not an AST
// dump: some AST positions carry meaning (a type alias's `=`).
//
// Exits non-zero when the file does not parse, imports "C" (a cgo preamble
// is a comment the compiler reads), or carries a build constraint, which
// moving a blank line can switch off without touching the constraint itself.
package main

import (
	"bufio"
	"fmt"
	"go/parser"
	"go/scanner"
	"go/token"
	"io"
	"os"
	"strconv"
	"strings"
)

func main() {
	if err := run(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "ci-go-tokens:", err)
		os.Exit(1)
	}
}

func run(in io.Reader, out io.Writer) error {
	src, err := io.ReadAll(in)
	if err != nil {
		return err
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, parser.ImportsOnly|parser.ParseComments)
	if err != nil {
		return err
	}
	for _, cg := range f.Comments {
		if cg.Pos() > f.Package {
			break
		}
		for _, c := range cg.List {
			if strings.HasPrefix(c.Text, "//go:build") || strings.HasPrefix(c.Text, "// +build") {
				return fmt.Errorf("build constraint %q", c.Text)
			}
		}
	}
	for _, imp := range f.Imports {
		if path, _ := strconv.Unquote(imp.Path.Value); path == "C" {
			return fmt.Errorf("imports \"C\"")
		}
	}
	if _, err := parser.ParseFile(fset, "", src, parser.SkipObjectResolution); err != nil {
		return err
	}

	var s scanner.Scanner
	var scanErr error
	s.Init(fset.AddFile("", -1, len(src)), src, func(_ token.Position, msg string) {
		if scanErr == nil {
			scanErr = fmt.Errorf("scan: %s", msg)
		}
	}, 0)
	w := bufio.NewWriter(out)
	for {
		_, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		fmt.Fprintf(w, "%s\t%q\n", tok, lit)
	}
	if scanErr != nil {
		return scanErr
	}
	return w.Flush()
}

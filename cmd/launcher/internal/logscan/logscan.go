// Package logscan scans Box logs line by line for the launcher. Every caller
// goes through ForEachLine so the 4 MiB buffer and the oversized-line handling
// live in one place.
package logscan

import (
	"bufio"
	"errors"
	"io"
	"os"
)

// Policy controls what ForEachLine does with a line longer than bufSize.
type Policy int

const (
	// SkipOversized discards an oversized line: fn sees no part of it.
	SkipOversized Policy = iota
	// ChunkOversized invokes fn once per buffer-sized chunk, so a marker inside
	// a large blob is still seen, at the cost of missing a match that straddles
	// a chunk boundary.
	ChunkOversized
)

const bufSize = 4 * 1024 * 1024

// ForEachLine opens path and invokes fn once per line, applying policy to any
// line longer than bufSize. It returns the os.Open error unchanged, so a caller
// can test it with errors.Is(err, os.ErrNotExist).
func ForEachLine(path string, policy Policy, fn func(line string)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	br := bufio.NewReaderSize(f, bufSize)
	for {
		line, isPrefix, err := br.ReadLine()
		if err == nil && (!isPrefix || policy == ChunkOversized) {
			fn(string(line))
		}
		for isPrefix && err == nil {
			var chunk []byte
			chunk, isPrefix, err = br.ReadLine()
			if err == nil && policy == ChunkOversized {
				fn(string(chunk))
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

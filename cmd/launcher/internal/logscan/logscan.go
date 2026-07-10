// Package logscan is the single shared line-by-line log scanner for the
// launcher. Every caller that scans a Box log for markers or events goes
// through ForEachLine so the 4 MiB buffer and oversized-line handling live
// in exactly one place instead of being copy-pasted per caller.
package logscan

import (
	"bufio"
	"errors"
	"io"
	"os"
)

// Policy controls what ForEachLine does with a line longer than its 4 MiB
// scan buffer.
type Policy int

const (
	// SkipOversized discards an oversized line entirely: fn is not called
	// for any part of it.
	SkipOversized Policy = iota
	// ChunkOversized invokes fn once per buffer-sized chunk of an oversized
	// line, so a marker inside a large blob (e.g. JSON) is still seen, at
	// the cost of possibly splitting a match across a chunk boundary.
	ChunkOversized
)

// bufSize is the scan buffer shared by every logscan caller.
const bufSize = 4 * 1024 * 1024

// ForEachLine opens path and invokes fn once per line, per policy for any
// line exceeding bufSize. Returns the os.Open error unchanged (check with
// errors.Is(err, os.ErrNotExist) for a missing file) or any other read error
// from the underlying file.
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

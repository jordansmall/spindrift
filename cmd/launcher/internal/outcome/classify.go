package outcome

import (
	"bufio"
	"errors"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Class describes whether a non-zero agent exit is retryable or not.
type Class string

const (
	// Transient exits are retryable infrastructure failures — the agent never
	// got a fair chance (rate limit, API overload, network blip).
	Transient Class = "transient"
	// Terminal exits are genuine task failures — the agent ran but produced
	// no valid result, or encountered an unrecoverable error.
	Terminal Class = "terminal"
)

// Reason identifies the specific cause of a classified exit.
type Reason string

const (
	RateLimit  Reason = "rateLimit"  // Claude API 429 rate limit
	Overloaded Reason = "overloaded" // Claude API 529 / overloaded_error
	Network    Reason = "network"    // transient network failure
	TaskFailed Reason = "taskFailed" // agent ran but produced no valid result
)

// Classification is the result of Classify.
type Classification struct {
	Class   Class
	Reason  Reason
	ResetAt *time.Time // non-nil only for RateLimit with a known reset time
}

// resetsAtRe matches the JSON field "resetsAt":UNIX_TIMESTAMP (integer).
var resetsAtRe = regexp.MustCompile(`"resetsAt"\s*:\s*(\d+)`)

// transientPatterns lists log-line substrings that mark a transient failure.
// The first match in the ordered list wins when multiple markers appear.
var transientPatterns = []struct {
	substr string
	reason Reason
}{
	{"429", RateLimit},
	{"rate_limit_error", RateLimit},
	{"529", Overloaded},
	{"overloaded_error", Overloaded},
	{"Overloaded", Overloaded},
	{"connection refused", Network},
	{"connection reset", Network},
	{"dial tcp", Network},
	{"net/http: request canceled", Network},
	{"context deadline exceeded", Network},
	{"no such host", Network},
}

// scanResult accumulates everything Classify needs from one pass over the log.
type scanResult struct {
	reason   Reason
	found    bool
	resetsAt *time.Time
}

// Classify scans the box log at logPath and, combined with the container exit
// code, returns a Classification describing whether the failure is transient
// (retryable) or terminal (genuine).
//
// When the log contains a 429 rate-limit marker with a "resetsAt" field, the
// returned Classification carries a non-nil ResetAt so callers can hold until
// the known reset time.
//
// A missing log file is treated as terminal/taskFailed. Lines larger than the
// 4 MiB scan buffer are skipped, matching the same resilience contract as
// LastInLog.
func Classify(logPath string, exitCode int) (Classification, error) {
	f, err := os.Open(logPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Classification{Class: Terminal, Reason: TaskFailed}, nil
		}
		return Classification{}, err
	}
	defer f.Close()

	sr, err := scanLog(f)
	if err != nil {
		return Classification{}, err
	}

	if !sr.found {
		return Classification{Class: Terminal, Reason: TaskFailed}, nil
	}

	cl := Classification{Class: Transient, Reason: sr.reason}
	if sr.reason == RateLimit {
		cl.ResetAt = sr.resetsAt
	}
	return cl, nil
}

// scanLog reads r line by line (skipping oversized lines) and returns a
// scanResult with the first transient reason found and any resetsAt extracted
// from anywhere in the log.
func scanLog(r io.Reader) (scanResult, error) {
	const bufSize = 4 * 1024 * 1024
	br := bufio.NewReaderSize(r, bufSize)

	var sr scanResult

	processChunk := func(chunk string) {
		if !sr.found {
			if reason, ok := matchTransient(chunk); ok {
				sr.found = true
				sr.reason = reason
			}
		}
		if sr.resetsAt == nil {
			if t := extractResetsAt(chunk); t != nil {
				sr.resetsAt = t
			}
		}
	}

	for {
		line, isPrefix, err := br.ReadLine()
		processChunk(string(line))
		if isPrefix {
			for isPrefix {
				var chunk []byte
				chunk, isPrefix, err = br.ReadLine()
				processChunk(string(chunk))
				if err != nil {
					break
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return scanResult{}, err
		}
	}

	return sr, nil
}

// matchTransient checks whether line contains a known transient marker.
// Returns the first matching reason in pattern order.
func matchTransient(line string) (Reason, bool) {
	for _, p := range transientPatterns {
		if strings.Contains(line, p.substr) {
			return p.reason, true
		}
	}
	return "", false
}

// extractResetsAt parses the first "resetsAt":UNIX_TIMESTAMP occurrence in
// content and returns a UTC time, or nil if none is found or the value is
// unparseable.
func extractResetsAt(content string) *time.Time {
	m := resetsAtRe.FindStringSubmatch(content)
	if m == nil {
		return nil
	}
	secs, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return nil
	}
	t := time.Unix(secs, 0).UTC()
	return &t
}

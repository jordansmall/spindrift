package outcome

import (
	"errors"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/logscan"
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
// Patterns are deliberately specific to avoid matching ordinary log content
// (issue numbers, byte counts, port numbers, etc. containing digit sequences).
// The first match in the ordered list wins when multiple markers appear.
var transientPatterns = []struct {
	substr string
	reason Reason
}{
	// Structured API error types — highest specificity, check first.
	{"rate_limit_error", RateLimit},
	{"overloaded_error", Overloaded},
	{"usage_limit_reached", RateLimit},
	// HTTP status phrase patterns — specific enough to avoid false positives.
	{"429 Too Many Requests", RateLimit},
	{"529 Overloaded", Overloaded},
	// Claude plain-text error messages.
	{"Claude Code usage limit reached", RateLimit},
	{"Overloaded", Overloaded},
	// Network-level failures logged by the Go HTTP client or stdlib.
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

// Classify scans the box log at logPath and returns a Classification
// describing whether the failure is transient (retryable) or terminal
// (genuine).
//
// When the log contains a 429 rate-limit marker with a "resetsAt" field, the
// returned Classification carries a non-nil ResetAt so callers can hold until
// the known reset time.
//
// A missing log file is treated as terminal/taskFailed. Lines larger than the
// 4 MiB scan buffer are processed in chunks, matching the same resilience
// contract as LastInLog.
func Classify(logPath string) (Classification, error) {
	sr, err := scanLog(logPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Classification{Class: Terminal, Reason: TaskFailed}, nil
		}
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

// scanLog reads logPath line by line and returns a scanResult with the first
// transient reason found and any resetsAt timestamp extracted from anywhere in
// the log. Oversized lines (> 4 MiB) are processed in chunks rather than
// skipped, so markers in large JSON blobs are still detected.
func scanLog(logPath string) (scanResult, error) {
	var sr scanResult
	err := logscan.ForEachLine(logPath, logscan.ChunkOversized, func(chunk string) {
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
	})
	if err != nil {
		return scanResult{}, err
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

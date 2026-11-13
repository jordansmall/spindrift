package connector

import "fmt"

// Class is the canonical error class a Connector reports with a failure. Core
// decides on retries from the class alone, so it must describe the failure and
// not the primitive that raised it.
type Class string

const (
	// Transient is a failure a retry may clear: a timeout, a 5xx, a dropped
	// connection.
	Transient Class = "transient"
	// RateLimited is a refusal for sending too much; core holds on its own
	// schedule rather than retrying at once.
	RateLimited Class = "rate-limited"
	// NotFound is a subject that does not exist.
	NotFound Class = "not-found"
	// Auth is a credential the remote refused or that lacks standing.
	Auth Class = "auth"
	// Permanent is every other failure; a retry cannot help. It is the class
	// of an error that carries none.
	Permanent Class = "permanent"
)

// Error is a handler error that carries its Class. Return one, or wrap one
// with %w, and the response reports its class.
type Error struct {
	Class Class
	Err   error
}

func (e *Error) Error() string { return e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

// Errorf returns an *Error of class c with a formatted message; %w in format
// wraps as in fmt.Errorf.
func Errorf(c Class, format string, args ...any) error {
	return &Error{Class: c, Err: fmt.Errorf(format, args...)}
}

// ClassForStatus maps an HTTP status to a Class, for Connectors backed by an
// HTTP API.
func ClassForStatus(status int) Class {
	switch {
	case status == 408 || status >= 500:
		return Transient
	case status == 429:
		return RateLimited
	case status == 404:
		return NotFound
	case status == 401 || status == 403:
		return Auth
	}
	return Permanent
}

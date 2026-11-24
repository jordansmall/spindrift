package connector

import "fmt"

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

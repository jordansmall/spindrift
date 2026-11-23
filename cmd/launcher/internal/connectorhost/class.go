package connectorhost

import (
	"errors"

	"spindrift.dev/launcher/internal/driver/driverkit"
)

// maxErrorText bounds the Connector-supplied error text core prints, records in
// the Connector's log and may post to a PR; the response cap alone is megabytes.
const maxErrorText = 256

// Class is the canonical error class a Connector reports with a failed call,
// on every primitive of both axes. Core decides on retries from it.
type Class string

const (
	// Transient may clear on a retry: a timeout, a 5xx, a dropped connection.
	Transient Class = "transient"
	// RateLimited is a refusal for sending too much; core's rate-limit hold
	// handles it, not a retry loop.
	RateLimited Class = "rate-limited"
	// NotFound is a subject that does not exist.
	NotFound Class = "not-found"
	// Auth is a credential the remote refused or that lacks standing.
	Auth Class = "auth"
	// NotMergeable is a merge the forge refused because the pull request is not
	// mergeable as it stands: a conflict, a missing required check or review, or
	// a draft, closed, or merged state. Core re-reads the PR on this class only.
	NotMergeable Class = "not-mergeable"
	// Permanent is every other failure, and the class of any answer that names
	// none or a class core does not know: core never retries what it cannot read.
	Permanent Class = "permanent"
)

// CallError is a failure the Connector itself reported. A Call failing this way
// matches ErrCall and holds a *CallError; ErrorClass reads its class.
type CallError struct {
	Class   Class
	Message string
}

func (e *CallError) Error() string { return e.Message }

// ErrorClass returns the class of a Connector-reported failure in err's chain,
// or "" when there is none, as for a host-side failure such as ErrBudget.
func ErrorClass(err error) Class {
	var ce *CallError
	if errors.As(err, &ce) {
		return ce.Class
	}
	return ""
}

// callError builds the *CallError for a response that carries an error.
func (r *response) callError() *CallError {
	c := Class(r.Class)
	switch c {
	case Transient, RateLimited, NotFound, Auth, NotMergeable, Permanent:
	default:
		c = Permanent
	}
	return &CallError{Class: c, Message: capText(*r.Error)}
}

// capText caps and sanitises guest text that a core-built error carries.
func capText(s string) string { return driverkit.CapLine(s, maxErrorText) }

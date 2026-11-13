package connectorhost

import "errors"

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
	case Transient, RateLimited, NotFound, Auth, Permanent:
	default:
		c = Permanent
	}
	return &CallError{Class: c, Message: *r.Error}
}

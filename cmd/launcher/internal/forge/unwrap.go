package forge

// Unwrapper is implemented by an IssueTracker that wraps another one, in the
// manner of errors.Unwrap.
type Unwrapper interface {
	Unwrap() IssueTracker
}

// As returns the outermost layer of it, following Unwrap, that implements T.
//
// An optional capability on an IssueTracker must be looked up with As, never a
// bare type assertion: embedding the IssueTracker interface hides every
// capability of the wrapped tracker, so a wrapper's own implementation
// shadows the wrapped tracker's and everything else shows through.
func As[T any](it IssueTracker) (T, bool) {
	for it != nil {
		if v, ok := it.(T); ok {
			return v, true
		}
		u, ok := it.(Unwrapper)
		if !ok {
			break
		}
		it = u.Unwrap()
	}
	var zero T
	return zero, false
}

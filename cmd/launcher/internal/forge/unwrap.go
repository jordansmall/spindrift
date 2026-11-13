package forge

// Unwrapper is implemented by an IssueTracker that wraps another one, in the
// manner of errors.Unwrap.
type Unwrapper interface {
	Unwrap() IssueTracker
}

// CapabilityHider is implemented by a wrapper that answers some capabilities
// itself or not at all: As stops at it for those instead of unwrapping.
type CapabilityHider interface {
	// Hides reports whether As must not look past this layer for the capability
	// capability points at, e.g. (*DemandCounter)(nil).
	Hides(capability any) bool
}

// As returns the outermost layer of it, following Unwrap, that implements T,
// stopping at a CapabilityHider that hides T.
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
		if h, ok := it.(CapabilityHider); ok && h.Hides((*T)(nil)) {
			break
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

// CodeForgeUnwrapper is implemented by a CodeForge that wraps another one, in
// the manner of errors.Unwrap.
type CodeForgeUnwrapper interface {
	UnwrapCodeForge() CodeForge
}

// AsCodeForge returns the outermost layer of cf, following UnwrapCodeForge,
// that implements T.
//
// An optional capability on a CodeForge must be looked up with AsCodeForge,
// never a bare type assertion: embedding the CodeForge interface hides every
// capability of the wrapped forge, so a wrapper's own implementation shadows
// the wrapped forge's and everything else shows through.
func AsCodeForge[T any](cf CodeForge) (T, bool) {
	for cf != nil {
		if v, ok := cf.(T); ok {
			return v, true
		}
		u, ok := cf.(CodeForgeUnwrapper)
		if !ok {
			break
		}
		cf = u.UnwrapCodeForge()
	}
	var zero T
	return zero, false
}

package connector

import (
	"fmt"
	"net/url"
	"strings"
)

// PathSegment escapes s as one URL path segment, for building a Request URL
// from a name the Connector did not write (a branch, a project key). It refuses
// what cannot stay one segment or that the host would refuse: "", "." and "..",
// and any s holding "/" or ";" (the host refuses path parameters).
//
// The result is canonical: url.Parse of it keeps no RawPath, which the host
// requires. That is why it is not url.PathEscape, which writes "," as "%2C" and
// so leaves a RawPath behind; here "," stays literal.
func PathSegment(s string) (string, error) {
	switch {
	case s == "" || s == "." || s == "..":
		return "", fmt.Errorf("connector: %q is not a usable path segment", s)
	case strings.ContainsAny(s, "/;"):
		return "", fmt.Errorf("connector: path segment %q holds a \"/\" or \";\"", s)
	}
	return (&url.URL{Path: s}).EscapedPath(), nil
}

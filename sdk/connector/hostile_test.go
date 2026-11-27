//go:build !wasip1

package connector

import "testing"

func TestPathSegmentKeepsEveryHostileInputInsideTheRoute(t *testing.T) {
	host := NewFakeHost([]Route{{Method: "GET", Host: "h.example", Path: "/r"}})
	for _, in := range HostileInputs() {
		seg, err := PathSegment(in)
		if err != nil {
			continue
		}
		req := Request{Method: "GET", URL: "https://h.example/r/" + seg}
		if len(req.URL) > MaxHTTPURL {
			continue // HTTPRequest refuses it before the host sees it
		}
		if err := host.Admit(req); err != nil {
			t.Errorf("PathSegment(%q) = %q: %v", in, seg, err)
		}
	}
}

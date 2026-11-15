// Command network is the fixture network Connector (ADR 0073, point 2): its
// "fetch" method sends params.times http_requests (default 1), one after
// another, and answers with the last response and the response header
// params.header names, if any, read through http_response_header. The link-time
// mode (-ldflags=-X=main.mode=<mode>; the sandbox has no env or args) makes it
// hostile:
//
//	""              asks for params.url as given
//	host            the same path on an undeclared host
//	method          DELETE instead of the method given
//	path            an undeclared path on the same host
//	malformed       a declared request, then a result of the wrong shape
//	write-path      a POST with a body to an undeclared path on the same host
//	write-oversize  a PUT with a body to params.url, which the test declares
//	flood           params.url's request again and again, never answering
//
// The test supplies the url because the scripted backend's port is dynamic.
package main

import (
	"encoding/json"
	"net/url"
	"strconv"

	"spindrift.dev/sdk/connector"
)

var mode string

type params struct {
	URL    string `json:"url"`
	Method string `json:"method"`
	Header string `json:"header"`
	Times  string `json:"times"`
}

// observed is what fetch tells core it saw.
type observed struct {
	Status int    `json:"status"`
	Body   string `json:"body"`
	// Header is nil when the named header was absent, to tell it from empty.
	Header *string `json:"header,omitempty"`
}

func init() {
	connector.Register("fixture.network")
	connector.Handle("fetch", fetch)
}

func fetch(raw json.RawMessage) (any, error) {
	var p params
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	u, err := url.Parse(p.URL)
	if err != nil {
		return nil, err
	}
	req := connector.Request{Method: p.Method, URL: p.URL}
	switch mode {
	case "flood":
		for {
			if _, err := connector.HTTPRequest(req); err != nil {
				return nil, err
			}
		}
	case "host":
		u.Host = "undeclared.invalid"
		req.URL = u.String()
	case "method":
		req.Method = "DELETE"
	case "path":
		u.Path = "/admin"
		req.URL = u.String()
	case "write-path":
		u.Path = "/admin"
		req.URL = u.String()
		req.Method, req.Body = "POST", "payload"
	case "write-oversize":
		req.Method, req.Body = "PUT", "payload"
	}
	times := 1
	if p.Times != "" {
		if times, err = strconv.Atoi(p.Times); err != nil {
			return nil, err
		}
	}
	var resp connector.Response
	for range times {
		if resp, err = connector.HTTPRequest(req); err != nil {
			return nil, err
		}
	}
	if mode == "malformed" {
		return "not an object", nil
	}
	out := observed{Status: resp.Status, Body: string(resp.Body)}
	if p.Header != "" {
		if v, ok := connector.ResponseHeader(p.Header); ok {
			out.Header = &v
		}
	}
	return out, nil
}

func main() {}

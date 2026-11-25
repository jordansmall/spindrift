package connector

import (
	"encoding/json"
	"strings"
)

// SendJSON sends req through HTTPRequest with in, when non-nil, JSON-encoded as
// its body, and decodes a 2xx answer into out, when non-nil. It asks for JSON
// with Accept, and declares a body with Content-Type, unless req.Headers
// already sets that header; req.Headers is not modified.
//
// A failed send is Transient, which HTTPRequest's own error does not say, even
// when retrying cannot help (the host could not resolve the request's
// credential); a non-2xx status is an error of ClassForStatus's class, and an
// answer that does not decode is Permanent. Whenever an answer arrived the
// Response is returned too, so a caller that reads a status itself (a 404
// meaning "no", a 409 meaning "not mergeable") can inspect it beside the error.
//
// SendJSON, PathSegment and Pages are conveniences, not controls: they run in
// the guest, and the host enforces the Route table and every other guarantee
// regardless of whether a Connector uses them.
func SendJSON(req Request, in, out any) (Response, error) {
	headers := make(map[string]string, len(req.Headers)+2)
	for k, v := range req.Headers {
		headers[k] = v
	}
	setDefault := func(name, value string) {
		for k := range headers {
			if strings.EqualFold(k, name) {
				return
			}
		}
		headers[name] = value
	}
	setDefault("Accept", "application/json")
	if in != nil {
		body, err := json.Marshal(in)
		if err != nil {
			return Response{}, Errorf(Permanent, "%s %s: encode: %w", req.Method, req.URL, err)
		}
		req.Body = string(body)
		setDefault("Content-Type", "application/json")
	}
	req.Headers = headers

	resp, err := HTTPRequest(req)
	if err != nil {
		return Response{}, Errorf(Transient, "%s %s: %w", req.Method, req.URL, err)
	}
	if resp.Status < 200 || resp.Status > 299 {
		return resp, Errorf(ClassForStatus(resp.Status), "%s %s: status %d", req.Method, req.URL, resp.Status)
	}
	if out != nil {
		if err := json.Unmarshal(resp.Body, out); err != nil {
			return resp, Errorf(Permanent, "%s %s: decode: %w", req.Method, req.URL, err)
		}
	}
	return resp, nil
}

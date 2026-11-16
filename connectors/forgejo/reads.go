package main

import (
	"encoding/json"
	"fmt"

	"spindrift.dev/sdk/connector"
)

// send is the HTTP transport; tests swap it because connector.HTTPRequest
// fails outside wasip1.
var send = connector.HTTPRequest

// request sends one request to api+path. The host attaches the credential, so
// none is set here. The status is the caller's to judge.
func request(method, api, path string, body []byte) (connector.Response, error) {
	headers := map[string]string{"Accept": "application/json"}
	if body != nil {
		headers["Content-Type"] = "application/json"
	}
	return send(connector.Request{Method: method, URL: api + path, Headers: headers, Body: string(body)})
}

// statusError is a non-2xx answer, carrying the class core retries on.
func statusError(method, path string, status int) error {
	return connector.Errorf(connector.ClassForStatus(status), "%s %s: status %d", method, path, status)
}

func is2xx(status int) bool { return status >= 200 && status <= 299 }

// call sends the request and decodes a 2xx JSON body into out.
func call(method, api, path string, body []byte, out any) error {
	resp, err := request(method, api, path, body)
	if err != nil {
		return err
	}
	if !is2xx(resp.Status) {
		return statusError(method, path, resp.Status)
	}
	if err := json.Unmarshal(resp.Body, out); err != nil {
		return fmt.Errorf("%s %s: decode: %w", method, path, err)
	}
	return nil
}

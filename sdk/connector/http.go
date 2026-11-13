package connector

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// MaxHTTPResponse is the largest response body the host hands a guest; a
// bigger one aborts the call and closes the Connector. It mirrors the host's
// maxHTTPResponse (cmd/launcher/internal/connectorhost/network.go);
// parity_test.go pins the two.
const MaxHTTPResponse = 4 << 20

// ErrNoHost is returned by HTTPRequest outside a wasip1 build, where there is
// no host to serve the call.
var ErrNoHost = errors.New("connector: no host to serve http_request outside wasip1")

// Request is one outbound HTTP request. The host admits it only if a declared
// route matches, and attaches the credential itself; the Connector never holds
// it, and a header the Connector sets cannot override it. It mirrors the host's
// httpRequest wire shape; parity_test.go pins the two.
type Request struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
}

// Response is the answer to a Request that came back from the server.
type Response struct {
	Status int
	Body   []byte
}

// respBuf is the one buffer the host writes responses into. A fresh
// MaxHTTPResponse buffer per call is never reclaimed by the guest's heap and
// exhausts the host's memory cap after about 120 requests. Reuse is safe
// because a guest runs one call at a time and the host call does not yield;
// Body is copied out so a caller holding an earlier Response is never
// overwritten.
var respBuf []byte

// hostCall is the http_request Host call, swapped by tests. It writes the
// answer into resp and returns the host's status<<32 | n.
var hostCall = hostHTTPRequest

// HTTPRequest sends req through the host's http_request Host call, available
// to a network Connector only. An error means the request did not complete (the
// transport failed, the credential could not be resolved, or the body could not
// be read) and is the Connector's to handle; its text is a fixed reason, the
// detail staying in the host's audit log. A request the host refuses, or a
// response over MaxHTTPResponse, does not return: the host aborts the call and
// closes the Connector.
func HTTPRequest(req Request) (Response, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return Response{}, fmt.Errorf("connector: encode request: %w", err)
	}
	if respBuf == nil {
		respBuf = make([]byte, MaxHTTPResponse)
	}
	buf := respBuf
	packed, err := hostCall(raw, buf)
	if err != nil {
		return Response{}, err
	}
	status, n := int(packed>>32), int(uint32(packed))
	if n > len(buf) {
		return Response{}, fmt.Errorf("connector: host reported %d response bytes for a %d byte buffer", n, len(buf))
	}
	if status == 0 {
		return Response{}, errors.New(string(buf[:n]))
	}
	return Response{Status: status, Body: bytes.Clone(buf[:n])}, nil
}

// headerBufStart is ResponseHeader's first buffer; a longer value costs one
// more host call, which is repeatable because the host holds the response.
const headerBufStart = 1024

// headerCall is the http_response_header Host call, swapped by tests. It writes
// as much of the value as fits into out and returns found<<32 | n, found being
// 1 when the header is present and n the value's full length.
var headerCall = hostResponseHeader

// ResponseHeader returns the named header of the response to this Connector's
// most recent HTTPRequest, joining repeated values with ", ". The name is
// case-insensitive. ok is false when the header is absent, when that request
// did not complete, and outside a host that serves the call; an empty value is
// present. Set-Cookie is never served. The call is imported only by a Connector
// that uses it, so one built against an older host fails to link, not to call.
func ResponseHeader(name string) (value string, ok bool) {
	buf := make([]byte, headerBufStart)
	for range 2 {
		packed, err := headerCall([]byte(name), buf)
		if err != nil || packed>>32 == 0 {
			return "", false
		}
		n := int(uint32(packed))
		if n <= len(buf) {
			return string(buf[:n]), true
		}
		buf = make([]byte, n)
	}
	return "", false
}

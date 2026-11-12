// Package signalclient is the Box-side client of the Signal socket (ADR 0052,
// issue #3724): endpoint resolution from the environment, the unix/TCP
// transport, and the reply decoding that driver-exec's signal verb and the
// in-process box binary share. The wire shapes live in signalwire; this
// package imports only that and the standard library, so it stays cheap to
// link into a Box binary.
package signalclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/signalwire"
)

const (
	// EndpointEnv names the Signal socket, in one of two forms:
	// "unix:///abs/path.sock" or "http://127.0.0.1:PORT".
	EndpointEnv = "SIGNAL_SOCKET_ENDPOINT"
	// SecretEnv carries the TCP form's bearer secret. Like
	// REGISTRY_PROXY_TCP_SECRET it is an environment variable rather than a
	// flag: argv is world-readable through /proc.
	SecretEnv = "SIGNAL_SOCKET_SECRET"

	unixScheme = "unix://"
	httpScheme = "http://"

	// unixHost is the authority unix-transport requests are addressed to. The
	// dialer ignores it -- there is no host to resolve -- but http.NewRequest
	// still needs a well-formed URL.
	unixHost = "http://signal-socket"

	// Timeout bounds one request end to end. The peer is a local listener
	// buffering in memory, so anything slower than this is a wedged socket
	// rather than a slow signal.
	Timeout = 30 * time.Second

	// maxReplyBytes bounds the reply read. Every reply is a receipt, a reject,
	// a status summary or a capped issue snapshot (which fits even after
	// worst-case JSON escaping). A longer reply is an error: cutting it
	// silently would hand a decoder half a document.
	maxReplyBytes = 1 << 20
)

// Target resolves the socket from the environment: an HTTP client for its
// transport, the base URL to address, and the secret to present (empty on the
// unix form, whose gate is the socket file's permissions).
func Target() (*http.Client, string, string, error) {
	endpoint := os.Getenv(EndpointEnv)
	client, base, err := New(endpoint)
	if err != nil {
		return nil, "", "", err
	}
	if !strings.HasPrefix(endpoint, httpScheme) {
		return client, base, "", nil
	}
	secret := os.Getenv(SecretEnv)
	if secret == "" {
		// Refused here rather than on the wire: a TCP request without the
		// header can only ever earn a 401, and the empty attempt would be one
		// more thing in the log to explain.
		return nil, "", "", fmt.Errorf("%s is required for an %s endpoint", SecretEnv, httpScheme)
	}
	return client, base, secret, nil
}

// New builds the client for endpoint and the base URL to address it at.
func New(endpoint string) (*http.Client, string, error) {
	switch {
	case strings.HasPrefix(endpoint, unixScheme):
		path := strings.TrimPrefix(endpoint, unixScheme)
		if path == "" {
			return nil, "", fmt.Errorf("%s names no socket path: %s", EndpointEnv, endpoint)
		}
		transport := &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", path)
			},
		}
		return &http.Client{Timeout: Timeout, Transport: transport}, unixHost, nil
	case strings.HasPrefix(endpoint, httpScheme):
		return &http.Client{Timeout: Timeout}, strings.TrimSuffix(endpoint, "/"), nil
	case endpoint == "":
		return nil, "", fmt.Errorf("%s is required", EndpointEnv)
	default:
		return nil, "", fmt.Errorf("%s must be %s<path> or %s<host:port>, got: %s", EndpointEnv, unixScheme, httpScheme, endpoint)
	}
}

// Do sends req, presenting secret when non-empty, and returns the status code
// and the (bounded) reply body.
func Do(client *http.Client, req *http.Request, secret string) (int, []byte, error) {
	if secret != "" {
		req.Header.Set(signalwire.SecretHeader, secret)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("reach the signal socket: %w", err)
	}
	defer resp.Body.Close()
	reply, err := io.ReadAll(io.LimitReader(resp.Body, maxReplyBytes+1))
	if err != nil {
		return 0, nil, fmt.Errorf("read the signal socket's reply: %w", err)
	}
	if len(reply) > maxReplyBytes {
		return 0, nil, fmt.Errorf("the signal socket's reply exceeds the %d-byte reply limit", maxReplyBytes)
	}
	return resp.StatusCode, reply, nil
}

// DecodeReject reads the handler's own Reject payload -- every reply the
// signal socket itself produces, including the TCP gate's 401, is this
// {status, reason} shape now that the gate rejects through the same path as
// every other route. The fallback below is for a reply from something that
// is not this socket at all (a proxy, a misrouted endpoint), which owes no
// such shape.
func DecodeReject(code int, reply []byte) signalwire.Reject {
	var rej signalwire.Reject
	if err := json.Unmarshal(reply, &rej); err == nil && rej.Reason != "" {
		return rej
	}
	reason, _, _ := strings.Cut(strings.TrimSpace(string(reply)), "\n")
	if reason == "" {
		reason = "the signal socket refused the request"
	}
	return signalwire.Reject{Status: fmt.Sprintf("http_%d", code), Reason: reason}
}

// FetchStatus is the transport shared by driver-exec's "status" verb and by
// markergate's socket-carrier PR-intent presence check (issue #3726): GET
// base+"/status" against an already-resolved client/secret and decode the
// reply. A non-2xx reply comes back as a non-nil *signalwire.Reject rather
// than an error, so a caller that wants CLI-style "rejected (status): reason"
// text and one that wants a plain error (Status) each render it their own way
// from the same parsed value.
func FetchStatus(client *http.Client, base, secret string) (signalwire.Status, *signalwire.Reject, error) {
	req, err := http.NewRequest(http.MethodGet, base+"/status", nil)
	if err != nil {
		return signalwire.Status{}, nil, err
	}
	code, reply, err := Do(client, req, secret)
	if err != nil {
		return signalwire.Status{}, nil, err
	}
	if code < 200 || code > 299 {
		rej := DecodeReject(code, reply)
		return signalwire.Status{}, &rej, nil
	}
	var st signalwire.Status
	if err := json.Unmarshal(reply, &st); err != nil {
		return signalwire.Status{}, nil, fmt.Errorf("decode status: %w", err)
	}
	return st, nil, nil
}

// Status is markergate.NudgeConfig.SignalStatus / ResolveConfig.SignalStatus
// (issue #3726): it resolves the socket target from the environment itself, so
// a caller supplies nothing but this function, never a client or secret. ADR
// 0007 / issue #2511 keeps the decision in markergate and the transport here.
func Status() (signalwire.Status, error) {
	client, base, secret, err := Target()
	if err != nil {
		return signalwire.Status{}, err
	}
	st, rej, err := FetchStatus(client, base, secret)
	if err != nil {
		return signalwire.Status{}, err
	}
	if rej != nil {
		return signalwire.Status{}, fmt.Errorf("signal status rejected (%s): %s", rej.Status, rej.Reason)
	}
	return st, nil
}

// FetchIssue is FetchStatus for the issue read verb: GET base+"/issue" with
// ref as the query value. The ref goes through url.Values because it can carry
// '#', '&' and '/', any of which would otherwise cut or split the query.
func FetchIssue(client *http.Client, base, secret, ref string) (signalwire.IssueSnapshot, *signalwire.Reject, error) {
	q := url.Values{"ref": {ref}}
	req, err := http.NewRequest(http.MethodGet, base+"/"+signalwire.IssueRoute+"?"+q.Encode(), nil)
	if err != nil {
		return signalwire.IssueSnapshot{}, nil, err
	}
	code, reply, err := Do(client, req, secret)
	if err != nil {
		return signalwire.IssueSnapshot{}, nil, err
	}
	if code < 200 || code > 299 {
		rej := DecodeReject(code, reply)
		return signalwire.IssueSnapshot{}, &rej, nil
	}
	var snap signalwire.IssueSnapshot
	if err := json.Unmarshal(reply, &snap); err != nil {
		return signalwire.IssueSnapshot{}, nil, fmt.Errorf("decode issue: %w", err)
	}
	return snap, nil, nil
}

// FetchIssueSearch is FetchIssue for the issue-search verb: GET
// base+"/issue-search" with text and state always sent and label only when
// set. The free text goes through url.Values because it can carry '&', '#'
// and tracker qualifiers like repo:x/y, which must stay one value.
func FetchIssueSearch(client *http.Client, base, secret, text, state, label string) (signalwire.IssueSearchReply, *signalwire.Reject, error) {
	q := url.Values{"text": {text}, "state": {state}}
	if label != "" {
		q.Set("label", label)
	}
	req, err := http.NewRequest(http.MethodGet, base+"/"+signalwire.IssueSearchRoute+"?"+q.Encode(), nil)
	if err != nil {
		return signalwire.IssueSearchReply{}, nil, err
	}
	code, reply, err := Do(client, req, secret)
	if err != nil {
		return signalwire.IssueSearchReply{}, nil, err
	}
	if code < 200 || code > 299 {
		rej := DecodeReject(code, reply)
		return signalwire.IssueSearchReply{}, &rej, nil
	}
	var res signalwire.IssueSearchReply
	if err := json.Unmarshal(reply, &res); err != nil {
		return signalwire.IssueSearchReply{}, nil, fmt.Errorf("decode issue search: %w", err)
	}
	return res, nil, nil
}

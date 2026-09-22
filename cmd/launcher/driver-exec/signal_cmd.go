package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/signalwire"
)

const (
	// signalEndpointEnv names the Signal socket, in one of two forms:
	// "unix:///abs/path.sock" or "http://127.0.0.1:PORT".
	signalEndpointEnv = "SIGNAL_SOCKET_ENDPOINT"
	// signalSecretEnv carries the TCP form's bearer secret. Like
	// REGISTRY_PROXY_TCP_SECRET above it is an environment variable rather
	// than a flag: argv is world-readable through /proc.
	signalSecretEnv = "SIGNAL_SOCKET_SECRET"

	signalUnixScheme = "unix://"
	signalHTTPScheme = "http://"

	// signalUnixHost is the authority unix-transport requests are addressed
	// to. The dialer ignores it -- there is no host to resolve -- but
	// http.NewRequest still needs a well-formed URL.
	signalUnixHost = "http://signal-socket"

	// signalTimeout bounds one request end to end. The peer is a local
	// listener buffering in memory, so anything slower than this is a wedged
	// socket rather than a slow signal.
	signalTimeout = 30 * time.Second

	// signalMaxReplyBytes bounds the reply read. Every reply is a receipt, a
	// reject or a status summary, all tiny.
	signalMaxReplyBytes = 1 << 20
)

// isSignalInvocation reports whether args selects the signal subcommand, which
// is a verb rather than a top-level flag.
func isSignalInvocation(args []string) bool {
	return len(args) > 0 && args[0] == "signal"
}

// runSignal posts one signal to the Box's Signal socket (ADR 0052, issue
// #3724): `signal comment|pr-intent|issue-intent|status`. The kind is the
// route, so it is a word rather than a flag, and no flag names a destination
// -- an issue number, label, branch or target is unrepresentable here, not
// merely ignored.
//
// Bodies arrive on stdin or via -body-file, never on argv: a 64 KiB Markdown
// body would blow the argv cap, and argv is world-readable through /proc.
// -title and -type are short closed-vocabulary fields and stay flags.
func runSignal(args []string, stdin io.Reader, stdout io.Writer) int {
	if len(args) == 0 {
		return signalUsage(stdout)
	}
	kind, rest := args[0], args[1:]

	switch kind {
	case "comment":
		fs := signalFlagSet(kind, stdout)
		bodyFile := fs.String("body-file", "", "file holding the body; empty or - reads stdin")
		if err := fs.Parse(rest); err != nil {
			return 1
		}
		client, base, secret, err := signalTarget()
		if err != nil {
			return signalFail(stdout, err)
		}
		body, err := readSignalBody(*bodyFile, stdin)
		if err != nil {
			return signalFail(stdout, err)
		}
		return postSignal(stdout, client, base, secret, kind, signalBody{"body": rawString(body)})

	case "pr-intent":
		fs := signalFlagSet(kind, stdout)
		title := fs.String("title", "", "pull-request title (required)")
		bodyFile := fs.String("body-file", "", "file holding the body; empty or - reads stdin")
		if err := fs.Parse(rest); err != nil {
			return 1
		}
		if *title == "" {
			return signalFail(stdout, errors.New("pr-intent: -title is required"))
		}
		client, base, secret, err := signalTarget()
		if err != nil {
			return signalFail(stdout, err)
		}
		body, err := readSignalBody(*bodyFile, stdin)
		if err != nil {
			return signalFail(stdout, err)
		}
		return postSignal(stdout, client, base, secret, kind, signalBody{"title": rawString(*title), "body": rawString(body)})

	case "issue-intent":
		fs := signalFlagSet(kind, stdout)
		title := fs.String("title", "", "issue title (required)")
		issueType := fs.String("type", "", "issue type: bug, enhancement or chore (required)")
		bodyFile := fs.String("body-file", "", "file holding the body; empty or - reads stdin")
		if err := fs.Parse(rest); err != nil {
			return 1
		}
		if *title == "" {
			return signalFail(stdout, errors.New("issue-intent: -title is required"))
		}
		if *issueType == "" {
			return signalFail(stdout, errors.New("issue-intent: -type is required"))
		}
		client, base, secret, err := signalTarget()
		if err != nil {
			return signalFail(stdout, err)
		}
		body, err := readSignalBody(*bodyFile, stdin)
		if err != nil {
			return signalFail(stdout, err)
		}
		return postSignal(stdout, client, base, secret, kind, signalBody{"title": rawString(*title), "body": rawString(body), "type": rawString(*issueType)})

	case "status":
		fs := signalFlagSet(kind, stdout)
		if err := fs.Parse(rest); err != nil {
			return 1
		}
		client, base, secret, err := signalTarget()
		if err != nil {
			return signalFail(stdout, err)
		}
		return getSignalStatus(stdout, client, base, secret)
	}

	return signalUsage(stdout)
}

func signalFlagSet(kind string, stdout io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("signal "+kind, flag.ContinueOnError)
	fs.SetOutput(stdout)
	return fs
}

func signalUsage(stdout io.Writer) int {
	fmt.Fprintln(stdout, "driver-exec signal: want one of comment, pr-intent, issue-intent, status")
	return 1
}

func signalFail(stdout io.Writer, err error) int {
	fmt.Fprintln(stdout, "driver-exec signal:", err)
	return 1
}

// signalTarget resolves the socket from the environment: an HTTP client for
// its transport, the base URL to address, and the secret to present (empty on
// the unix form, whose gate is the socket file's permissions).
func signalTarget() (*http.Client, string, string, error) {
	endpoint := os.Getenv(signalEndpointEnv)
	client, base, err := signalClient(endpoint)
	if err != nil {
		return nil, "", "", err
	}
	if !strings.HasPrefix(endpoint, signalHTTPScheme) {
		return client, base, "", nil
	}
	secret := os.Getenv(signalSecretEnv)
	if secret == "" {
		// Refused here rather than on the wire: a TCP request without the
		// header can only ever earn a 401, and the empty attempt would be one
		// more thing in the log to explain.
		return nil, "", "", fmt.Errorf("%s is required for an %s endpoint", signalSecretEnv, signalHTTPScheme)
	}
	return client, base, secret, nil
}

// signalClient builds the client for endpoint and the base URL to address it
// at.
func signalClient(endpoint string) (*http.Client, string, error) {
	switch {
	case strings.HasPrefix(endpoint, signalUnixScheme):
		path := strings.TrimPrefix(endpoint, signalUnixScheme)
		if path == "" {
			return nil, "", fmt.Errorf("%s names no socket path: %s", signalEndpointEnv, endpoint)
		}
		transport := &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", path)
			},
		}
		return &http.Client{Timeout: signalTimeout, Transport: transport}, signalUnixHost, nil
	case strings.HasPrefix(endpoint, signalHTTPScheme):
		return &http.Client{Timeout: signalTimeout}, strings.TrimSuffix(endpoint, "/"), nil
	case endpoint == "":
		return nil, "", fmt.Errorf("%s is required", signalEndpointEnv)
	default:
		return nil, "", fmt.Errorf("%s must be %s<path> or %s<host:port>, got: %s", signalEndpointEnv, signalUnixScheme, signalHTTPScheme, endpoint)
	}
}

// readSignalBody reads the body from bodyFile, or from stdin when it is empty
// or "-". Both sources are bounded the same way (readLimitedBody), so a
// 1 GiB body-file errors exactly like over-limit stdin instead of landing in
// Box memory.
func readSignalBody(bodyFile string, stdin io.Reader) (string, error) {
	if bodyFile == "" || bodyFile == "-" {
		return readLimitedBody(stdin, "stdin")
	}
	f, err := os.Open(bodyFile)
	if err != nil {
		return "", fmt.Errorf("read body file: %w", err)
	}
	defer f.Close()
	return readLimitedBody(f, "body file")
}

// readLimitedBody reads at most one byte past signalwire.MaxRequestBytes so
// an over-limit source is caught by length, not by silently truncating to
// the cap and letting the reject reason describe bytes the agent never sent.
func readLimitedBody(r io.Reader, source string) (string, error) {
	b, err := io.ReadAll(io.LimitReader(r, signalwire.MaxRequestBytes+1))
	if err != nil {
		return "", fmt.Errorf("read %s: %w", source, err)
	}
	if len(b) > signalwire.MaxRequestBytes {
		return "", fmt.Errorf("%s: %d-byte request limit exceeded", source, signalwire.MaxRequestBytes)
	}
	return string(b), nil
}

// signalBody is one request's content fields under their wire names. The
// socket validates the bytes on receipt (ADR 0052), so the verb carries them
// unaltered rather than deciding anything about them itself.
type signalBody map[string]rawString

// rawString marshals byte for byte, where encoding/json's own string encoder
// substitutes U+FFFD for invalid UTF-8. Laundering the bytes here would hand
// the socket a request the agent never wrote -- and one it would accept.
type rawString string

func (s rawString) MarshalJSON() ([]byte, error) {
	out := make([]byte, 0, len(s)+2)
	out = append(out, '"')
	// Byte-wise on purpose: range over a string decodes UTF-8 and yields
	// U+FFFD for an invalid byte, laundering exactly the bytes this type
	// exists to transmit unchanged so the socket can reject them.
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '"' || c == '\\':
			out = append(out, '\\', c)
		case c < 0x20:
			out = append(out, fmt.Sprintf(`\u%04x`, c)...)
		default:
			out = append(out, c)
		}
	}
	return append(out, '"'), nil
}

func printReject(stdout io.Writer, kind string, rej signalwire.Reject) int {
	fmt.Fprintf(stdout, "signal %s rejected (%s): %s\n", kind, rej.Status, rej.Reason)
	return 1
}

func postSignal(stdout io.Writer, client *http.Client, base, secret, kind string, payload any) int {
	body, err := json.Marshal(payload)
	if err != nil {
		return signalFail(stdout, fmt.Errorf("encode %s signal: %w", kind, err))
	}
	req, err := http.NewRequest(http.MethodPost, base+"/"+kind, bytes.NewReader(body))
	if err != nil {
		return signalFail(stdout, err)
	}
	req.Header.Set("Content-Type", "application/json")

	code, reply, err := doSignal(client, req, secret)
	if err != nil {
		return signalFail(stdout, err)
	}
	if code < 200 || code > 299 {
		return printReject(stdout, kind, decodeReject(code, reply))
	}

	var rec signalwire.Receipt
	if err := json.Unmarshal(reply, &rec); err != nil {
		return signalFail(stdout, fmt.Errorf("decode %s receipt: %w", kind, err))
	}
	fmt.Fprintf(stdout, "signal %s accepted: %d bytes, %s, sequence %d\n", rec.Kind, rec.Bytes, rec.Hash, rec.Sequence)
	return 0
}

// fetchSignalStatus is the transport shared by the "status" verb below and by
// markergate's socket-carrier PR-intent presence check (issue #3726,
// markergate_cmd.go's signalStatusFunc): GET base+"/status" against an
// already-resolved client/secret and decode the reply. A non-2xx reply comes
// back as a non-nil *signalwire.Reject rather than an error, so a caller that
// wants CLI-style "rejected (status): reason" text (getSignalStatus) and one
// that wants a plain error (markergate's closure) each render it their own
// way from the same parsed value.
func fetchSignalStatus(client *http.Client, base, secret string) (signalwire.Status, *signalwire.Reject, error) {
	req, err := http.NewRequest(http.MethodGet, base+"/status", nil)
	if err != nil {
		return signalwire.Status{}, nil, err
	}
	code, reply, err := doSignal(client, req, secret)
	if err != nil {
		return signalwire.Status{}, nil, err
	}
	if code < 200 || code > 299 {
		rej := decodeReject(code, reply)
		return signalwire.Status{}, &rej, nil
	}
	var st signalwire.Status
	if err := json.Unmarshal(reply, &st); err != nil {
		return signalwire.Status{}, nil, fmt.Errorf("decode status: %w", err)
	}
	return st, nil, nil
}

// signalStatusFunc is markergate_cmd.go's markergate.NudgeConfig.SignalStatus
// / ResolveConfig.SignalStatus closure (issue #3726): it resolves the socket
// target itself, so markergate's caller supplies nothing but this function,
// never a client or secret. ADR 0007 / issue #2511 keeps the decision in
// markergate and the transport here.
func signalStatusFunc() (signalwire.Status, error) {
	client, base, secret, err := signalTarget()
	if err != nil {
		return signalwire.Status{}, err
	}
	st, rej, err := fetchSignalStatus(client, base, secret)
	if err != nil {
		return signalwire.Status{}, err
	}
	if rej != nil {
		return signalwire.Status{}, fmt.Errorf("signal status rejected (%s): %s", rej.Status, rej.Reason)
	}
	return st, nil
}

func getSignalStatus(stdout io.Writer, client *http.Client, base, secret string) int {
	st, rej, err := fetchSignalStatus(client, base, secret)
	if err != nil {
		return signalFail(stdout, err)
	}
	if rej != nil {
		return printReject(stdout, "status", *rej)
	}
	// Declaration order, not accept order: a stable listing lets an agent
	// diff two status calls.
	receipts := make([]signalwire.Receipt, 0, 2+len(st.IssueIntents))
	if st.Comment != nil {
		receipts = append(receipts, *st.Comment)
	}
	if st.PRIntent != nil {
		receipts = append(receipts, *st.PRIntent)
	}
	receipts = append(receipts, st.IssueIntents...)
	if len(receipts) == 0 {
		fmt.Fprintln(stdout, "no signals accepted")
		return 0
	}
	for _, r := range receipts {
		fmt.Fprintf(stdout, "%s %d bytes, %s, sequence %d\n", r.Kind, r.Bytes, r.Hash, r.Sequence)
	}
	return 0
}

func doSignal(client *http.Client, req *http.Request, secret string) (int, []byte, error) {
	if secret != "" {
		req.Header.Set(signalwire.SecretHeader, secret)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("reach the signal socket: %w", err)
	}
	defer resp.Body.Close()
	reply, err := io.ReadAll(io.LimitReader(resp.Body, signalMaxReplyBytes))
	if err != nil {
		return 0, nil, fmt.Errorf("read the signal socket's reply: %w", err)
	}
	return resp.StatusCode, reply, nil
}

// decodeReject reads the handler's own Reject payload -- every reply the
// signal socket itself produces, including the TCP gate's 401, is this
// {status, reason} shape now that the gate rejects through the same path as
// every other route. The fallback below is for a reply from something that
// is not this socket at all (a proxy, a misrouted endpoint), which owes no
// such shape.
func decodeReject(code int, reply []byte) signalwire.Reject {
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

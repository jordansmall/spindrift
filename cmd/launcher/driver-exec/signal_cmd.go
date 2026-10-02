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
	"reflect"
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

	// signalUsageReportTimeout bounds a usage report. The invocation has
	// already failed, so a wedged socket must not hold it for the full
	// signalTimeout before the caller sees its rejected line.
	signalUsageReportTimeout = 5 * time.Second

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
		return signalUsageFailure(stdout, "", "want one of comment, pr-intent, issue-intent, status")
	}
	kind, rest := args[0], args[1:]

	switch kind {
	case "comment":
		fs := signalFlagSet(kind, stdout)
		bodyFile := fs.String("body-file", "", "file holding the body; empty or - reads stdin")
		if !parseSignalFlags(fs, rest, kind, stdout) {
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
		return postSignal(stdout, client, base, secret, kind, rawFields(signalwire.Comment{Body: body}))

	case "pr-intent":
		fs := signalFlagSet(kind, stdout)
		title := fs.String("title", "", "pull-request title (required)")
		bodyFile := fs.String("body-file", "", "file holding the body; empty or - reads stdin")
		if !parseSignalFlags(fs, rest, kind, stdout) {
			return 1
		}
		if *title == "" {
			return signalUsageFailure(stdout, kind, "-title is required")
		}
		client, base, secret, err := signalTarget()
		if err != nil {
			return signalFail(stdout, err)
		}
		body, err := readSignalBody(*bodyFile, stdin)
		if err != nil {
			return signalFail(stdout, err)
		}
		return postSignal(stdout, client, base, secret, kind, rawFields(signalwire.PRIntent{Title: *title, Body: body}))

	case "issue-intent":
		fs := signalFlagSet(kind, stdout)
		title := fs.String("title", "", "issue title (required)")
		issueType := fs.String("type", "", "issue type: bug, enhancement or chore (required)")
		bodyFile := fs.String("body-file", "", "file holding the body; empty or - reads stdin")
		var dedup stringSliceFlag
		fs.Var(&dedup, "dedup", "site key for dedup, e.g. path/to/file.go:Symbol; repeat for more than one")
		class := fs.String("class", "", "promotion-candidate class (issue #3880); omit unless the finding is on CHORE_CLASSES")
		concurrence := fs.String("concurrence", "", "the reviewer subagent's one-line agreement (issue #3880); omit on dissent or if it never ran")
		patchFile := fs.String("patch-file", "", "file holding a unified diff (ADR 0057, issue #4072); omit unless the finding's class is on the host's CHORE_PATCH_CLASSES")
		if !parseSignalFlags(fs, rest, kind, stdout) {
			return 1
		}
		if *title == "" {
			return signalUsageFailure(stdout, kind, "-title is required")
		}
		if *issueType == "" {
			return signalUsageFailure(stdout, kind, "-type is required")
		}
		client, base, secret, err := signalTarget()
		if err != nil {
			return signalFail(stdout, err)
		}
		body, err := readSignalBody(*bodyFile, stdin)
		if err != nil {
			return signalFail(stdout, err)
		}
		var patch string
		if *patchFile != "" {
			if patch, err = readLimitedFile(*patchFile, "patch file"); err != nil {
				return signalFail(stdout, err)
			}
		}
		// dedupTerms/class/concurrence/patch stay omitempty on the struct: a
		// term-less, class-less, patch-less call's wire body must stay
		// byte-identical to what it was before these fields existed.
		return postSignal(stdout, client, base, secret, kind, rawFields(signalwire.IssueIntent{
			Title:       *title,
			Body:        body,
			Type:        *issueType,
			DedupTerms:  dedup,
			Class:       *class,
			Concurrence: *concurrence,
			Patch:       patch,
		}))

	case "status":
		fs := signalFlagSet(kind, stdout)
		if !parseSignalFlags(fs, rest, kind, stdout) {
			return 1
		}
		client, base, secret, err := signalTarget()
		if err != nil {
			return signalFail(stdout, err)
		}
		return getSignalStatus(stdout, client, base, secret)
	}

	return signalUsageFailure(stdout, "", "unknown signal kind; want one of comment, pr-intent, issue-intent, status")
}

// parseSignalFlags parses rest with fs and routes a parse failure through the
// usage-failure path (report + final rejected line) -- except an explicit
// -h/-help, which is a request, not a failure, so it keeps the plain dump and
// exit 1 with no report. On ok=false the caller must return 1 immediately;
// fs has already written its own error and usage dump to stdout before either
// path adds anything further.
func parseSignalFlags(fs *flag.FlagSet, rest []string, kind string, stdout io.Writer) (ok bool) {
	if err := fs.Parse(rest); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return false
		}
		signalUsageFailure(stdout, kind, err.Error())
		return false
	}
	return true
}

func signalFlagSet(kind string, stdout io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("signal "+kind, flag.ContinueOnError)
	fs.SetOutput(stdout)
	return fs
}

// signalUsageFailure handles a client-side usage failure (issue #3867): it
// reports the failure to the launcher over the socket's diagnostics-only
// route -- best-effort, silently discarding any error -- and only then prints
// the final "signal ... rejected (usage): reason" line, so the report always
// runs before the line a caller may be tailing rather than after it.
//
// kind is both the socket path and what the printed line names: "" means
// kindless (route /usage, line "signal rejected (usage): reason"), and
// anything else routes /<kind>/usage with line "signal <kind> rejected
// (usage): reason". An unrecognised kind word always calls this with "" --
// safe to name in the fixed reason text, but never sent over the wire or
// echoed in the line.
func signalUsageFailure(stdout io.Writer, kind, reason string) int {
	reportSignalUsage(kind, reason)
	if kind == "" {
		fmt.Fprintf(stdout, "signal rejected (usage): %s\n", reason)
		return 1
	}
	return printReject(stdout, kind, signalwire.Reject{Status: "usage", Reason: reason})
}

// reportSignalUsage posts a client-side usage failure to the socket's
// diagnostics-only route. It is best-effort and silent on every failure: a
// report must never mask the original usage error or change its exit code.
func reportSignalUsage(kind, reason string) {
	client, base, secret, err := signalTarget()
	if err != nil {
		return
	}
	// The invocation has already failed, so a wedged socket must not hold it
	// for the full signalTimeout.
	reportClient := *client
	reportClient.Timeout = signalUsageReportTimeout

	// The host refuses an over-limit or invalid-UTF-8 reason outright, so the
	// wire copy is cut to fit; the printed line keeps the reason in full.
	if len(reason) > signalwire.MaxUsageReasonBytes {
		reason = reason[:signalwire.MaxUsageReasonBytes]
	}
	body, err := json.Marshal(signalwire.UsageReport{Reason: strings.ToValidUTF8(reason, "")})
	if err != nil {
		return
	}
	req, err := http.NewRequest(http.MethodPost, base+signalwire.UsagePath(kind), bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	_, _, _ = doSignal(&reportClient, req, secret)
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
// or "-". Both sources are bounded the same way (readLimited), so a
// 1 GiB body-file errors exactly like over-limit stdin instead of landing in
// Box memory.
func readSignalBody(bodyFile string, stdin io.Reader) (string, error) {
	if bodyFile == "" || bodyFile == "-" {
		return readLimited(stdin, "stdin")
	}
	return readLimitedFile(bodyFile, "body file")
}

// readLimitedFile reads path through readLimited, so every file-sourced
// field (body or patch) is bounded before it reaches the socket.
func readLimitedFile(path, source string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", source, err)
	}
	defer f.Close()
	return readLimited(f, source)
}

// readLimited reads at most one byte past signalwire.MaxRequestBytes so
// an over-limit source is caught by length, not by silently truncating to
// the cap and letting the reject reason describe bytes the agent never sent.
func readLimited(r io.Reader, source string) (string, error) {
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
// unaltered rather than deciding anything about them itself. The value type
// is json.Marshaler rather than rawString directly so a field like
// dedupTerms, an array rather than a single string, can share the map.
type signalBody map[string]json.Marshaler

// rawFields builds a signalBody from a signalwire wire struct, keyed by its
// json tags with encoding/json's omitempty rule, so the verb posts the same
// shape the socket and settle decode rather than a hand-built copy of it.
// Wire structs carry only string and []string fields; anything else panics.
func rawFields(v any) signalBody {
	rv := reflect.ValueOf(v)
	rt := rv.Type()
	out := make(signalBody, rt.NumField())
	for i := 0; i < rt.NumField(); i++ {
		name, opts, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ",")
		fv := rv.Field(i)
		// Length checks live inside the cases so an unsupported kind reaches
		// the panic below, not reflect's own Len() panic.
		switch x := fv.Interface().(type) {
		case string:
			if opts == "omitempty" && len(x) == 0 {
				continue
			}
			out[name] = rawString(x)
		case []string:
			if opts == "omitempty" && len(x) == 0 {
				continue
			}
			out[name] = rawStringSlice(x)
		default:
			panic(fmt.Sprintf("rawFields: %s.%s is %T, not string or []string", rt.Name(), rt.Field(i).Name, x))
		}
	}
	return out
}

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

// rawStringSlice marshals as a JSON array of rawString elements, so
// dedupTerms preserves each term's bytes the same way a single rawString
// field does.
type rawStringSlice []string

func (s rawStringSlice) MarshalJSON() ([]byte, error) {
	elems := make([]rawString, len(s))
	for i, v := range s {
		elems[i] = rawString(v)
	}
	return json.Marshal(elems)
}

// stringSliceFlag is a repeatable flag.Value: each -flag occurrence appends
// rather than replacing the previous one.
type stringSliceFlag []string

func (f *stringSliceFlag) String() string {
	if f == nil {
		return ""
	}
	return strings.Join(*f, ",")
}

func (f *stringSliceFlag) Set(v string) error {
	*f = append(*f, v)
	return nil
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

package connectorhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"spindrift.dev/launcher/internal/registryvocab"
)

// maxHTTPResponse caps the response body http_request hands a guest, so a
// hostile or buggy upstream cannot make the host buffer without bound.
const maxHTTPResponse = 4 << 20

var (
	// ErrRouteTable marks a Route table that does not validate: a path that
	// breaks the registry-route rules, a method that is not an uppercase token,
	// or a host that is not a bare host[:port].
	ErrRouteTable = errors.New("connector route table invalid")
	// ErrRouteRefused marks an http_request the host refused without sending: no
	// route admits its method, host and path, or the request is malformed or
	// would carry the credential somewhere a route cannot vouch for (plaintext,
	// userinfo, a non-canonical or escaped path).
	ErrRouteRefused = errors.New("connector http request refused")
	// ErrHTTPSend marks an admitted request that did not complete: the
	// credential could not be resolved, or the transport failed before or while
	// the response body arrived.
	ErrHTTPSend = errors.New("connector http request failed")
	// ErrResponseTooLarge marks a response body over the guest's response buffer
	// or maxHTTPResponse, whichever is smaller. A FollowRedirect route is exempt:
	// it keeps the body's tail instead.
	ErrResponseTooLarge = errors.New("connector http response too large")
	// ErrAudit marks a request the audit log could not record. The answer is
	// withheld and the call fails closed: an unaudited request must not succeed.
	ErrAudit = errors.New("connector audit log write failed")
)

// Route admits one slice of a network Connector's traffic: an exact uppercase
// Method, an exact host[:port] (case-insensitive, the port significant), and a
// Path subtree root in registryvocab.PathSet form. A route does not restrict the
// query string: any query is forwarded to an admitted path, and the audit log
// records it.
//
// FollowRedirect lets a GET route follow one redirect hop to a host the table
// cannot name (a pre-signed blob URL). The hop is sent uncredentialed and
// audited with its target host, asks for the body's tail with a Range suffix,
// and is bounded to half the call's remaining budget (failing non-fatally past
// it); String shows the opt-in, so a Route-table diff does too. A body over the
// response cap keeps its tail rather than failing the call: that error is
// fatal to the Connector, and the tail of a blob such as a job log is the
// useful part.
type Route struct {
	Method         string `json:"method"`
	Host           string `json:"host"`
	Path           string `json:"path"`
	FollowRedirect bool   `json:"followRedirect,omitempty"`
}

func (r Route) String() string {
	s := r.Method + " " + r.Host + " " + r.Path
	if r.FollowRedirect {
		s += " (follows redirect)"
	}
	return s
}

// ValidateRoutes checks every route, naming the first that fails; the error
// matches ErrRouteTable, and the path reason (registryvocab.ErrPattern*) when
// the path is at fault.
func ValidateRoutes(routes []Route) error {
	for _, r := range routes {
		if err := r.validate(); err != nil {
			return fmt.Errorf("%w: route %q: %w", ErrRouteTable, r.String(), err)
		}
	}
	return nil
}

func (r Route) validate() error {
	if !registryvocab.IsValidHeaderFieldName(r.Method) || r.Method != strings.ToUpper(r.Method) {
		return errors.New("method is not an uppercase HTTP token")
	}
	u, err := url.Parse("https://" + r.Host)
	if err != nil || u.Host != r.Host || u.Hostname() == "" || strings.HasSuffix(r.Host, ":") ||
		u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("host is not a bare host[:port]")
	}
	if r.FollowRedirect && r.Method != http.MethodGet {
		return errors.New("followRedirect is allowed on GET only")
	}
	return registryvocab.CheckPathPattern(r.Path)
}

// Decision is what the host did with a request.
type Decision string

// The decisions an AuditEntry records.
const (
	DecisionSent    Decision = "sent"
	DecisionRefused Decision = "refused"
	DecisionFailed  Decision = "failed"
)

// AuditEntry is one line of the network audit log: what a Connector asked for
// and what the host did with it. Route is empty when none matched; Status is 0
// when the request got no response, else the server's status even if the call
// then failed. Size counts the body bytes read: for an oversized response
// (DecisionFailed) that is the cap, min(response buffer, maxHTTPResponse), plus
// one, though none reach the guest. A FollowRedirect route's oversized body is
// DecisionSent with Size its full length; Reason notes only the tail was kept.
// A request has one entry; any failed call of a network Connector whose failure
// no Host call already audited gets a further DecisionFailed one.
type AuditEntry struct {
	Connector string   `json:"connector"`
	Route     string   `json:"route"`
	Method    string   `json:"method"`
	Host      string   `json:"host"`
	Path      string   `json:"path"`
	Query     string   `json:"query,omitempty"`
	Status    int      `json:"status"`
	Size      int64    `json:"size"`
	Decision  Decision `json:"decision"`
	Reason    string   `json:"reason,omitempty"`
	// Shadow marks an entry a sampled shadow Connector recorded beside the
	// native backend; it is omitted otherwise, so other entries are unchanged.
	Shadow bool `json:"shadow,omitempty"`
	// RateLimit is RateLimitPrimary or RateLimitSecondary when a 403 or 429 carries a
	// rate-limit signal, else empty; see rateLimitKind.
	RateLimit string `json:"rate_limit,omitempty"`
	// RetryAfter is the response's Retry-After value verbatim, on a 403 or 429.
	RetryAfter string `json:"retry_after,omitempty"`
	// RedirectHost is the host of the one hop a FollowRedirect route followed.
	RedirectHost string `json:"redirect_host,omitempty"`
}

// The wire values of AuditEntry.RateLimit; docs/reference.md documents them.
const (
	RateLimitPrimary   = "primary"
	RateLimitSecondary = "secondary"
)

// rateLimitKind classifies a 403 or 429 response from GitHub's documented
// rate-limit signals: x-ratelimit-remaining of 0 is the primary limit, a
// retry-after header or a "secondary rate limit" body is the secondary one. It
// is read host-side because the guest sees only status and body. A 429 with
// neither is still secondary (it means too many requests on any server); a
// plain 403 is not a limit, so Status alone tells it apart.
func rateLimitKind(status int, h http.Header, body []byte) (kind, retryAfter string) {
	if status != http.StatusForbidden && status != http.StatusTooManyRequests {
		return "", ""
	}
	retryAfter = h.Get("Retry-After")
	switch {
	case h.Get("X-RateLimit-Remaining") == "0":
		kind = RateLimitPrimary
	case status == http.StatusTooManyRequests || retryAfter != "" || bytes.Contains(bytes.ToLower(body), []byte("secondary rate limit")):
		kind = RateLimitSecondary
	}
	return kind, retryAfter
}

// AuditLog writes AuditEntry records as JSON lines. Each record is one Write
// under the mutex, so concurrent Connectors never interleave within a line.
type AuditLog struct {
	mu sync.Mutex
	w  io.Writer
}

// NewAuditLog returns a log writing to w.
func NewAuditLog(w io.Writer) *AuditLog { return &AuditLog{w: w} }

// Record appends e as one line and returns the marshal or write failure. A
// caller already failing may ignore it; one about to succeed must not, or the
// request goes unaudited.
func (l *AuditLog) Record(e AuditEntry) error {
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	l.mu.Lock()
	defer l.mu.Unlock()
	_, err = l.w.Write(line)
	return err
}

// recordFailure audits d for a caller already failing, so the write error has
// nowhere to go.
func (n *Network) recordFailure(d Decision, err error) {
	_ = n.record(AuditEntry{Connector: n.Name, Decision: d, Reason: err.Error()})
}

// record stamps e with the Network's Shadow mark, writes it, then hands it to
// Observe, outside the audit log's mutex.
func (n *Network) record(e AuditEntry) error {
	e.Shadow = n.Shadow
	err := n.Audit.Record(e)
	if n.Observe != nil {
		n.Observe(e)
	}
	return err
}

// Network is the host side of a network Connector's single Host call,
// http_request.
type Network struct {
	// Name is the Connector's name, in errors and the audit log.
	Name string
	// Routes is the only traffic the Connector may send; see ValidateRoutes.
	Routes []Route
	// Authorize attaches the credential to an admitted request, after the
	// guest's headers are copied so it wins over any the guest set.
	Authorize func(*http.Request) error
	// Client is the transport. A redirect is never followed except the one hop
	// of a FollowRedirect route; CheckRedirect is set on a copy, so the
	// caller's client is untouched.
	Client *http.Client
	// Audit receives one entry per request; required.
	Audit *AuditLog
	// Shadow stamps every audit entry as a shadow Connector's.
	Shadow bool
	// Observe, when non-nil, is called with each entry after it is recorded.
	Observe func(AuditEntry)
}

// httpRequest is the wire form of a guest's http_request.
type httpRequest struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
}

// tailChunk is readTail's read size.
const tailChunk = 32 << 10

// redirectHopShare: a followed redirect hop gets 1/redirectHopShare of the
// caller's remaining deadline, so a slow blob host fails non-fatally before
// the call budget expires.
const redirectHopShare = 2

// readTail reads r to EOF keeping only its last limit bytes, in memory bounded
// by 2*limit plus one tailChunk, and returns them with the total bytes read.
func readTail(r io.Reader, limit int) (tail []byte, total int64, err error) {
	chunk := make([]byte, tailChunk)
	for {
		n, rerr := r.Read(chunk)
		tail = append(tail, chunk[:n]...)
		total += int64(n)
		if len(tail) >= 2*limit {
			tail = append(tail[:0], tail[len(tail)-limit:]...)
		}
		if rerr != nil {
			if len(tail) > limit {
				tail = tail[len(tail)-limit:]
			}
			if rerr == io.EOF {
				rerr = nil
			}
			return tail, total, rerr
		}
	}
}

// contentRangeTotal parses the complete length from "bytes a-b/total"; ok is
// false for a malformed header or an unknown ("*") total.
func contentRangeTotal(h string) (total int64, ok bool) {
	rest, found := strings.CutPrefix(h, "bytes ")
	if !found {
		return 0, false
	}
	_, t, found := strings.Cut(rest, "/")
	if !found {
		return 0, false
	}
	total, err := strconv.ParseInt(t, 10, 64)
	return total, err == nil && total >= 0
}

// do runs one http_request: match, authorize, send, audit. One audit entry is
// recorded whatever the outcome; if that write fails the answer is withheld.
func (n *Network) do(ctx context.Context, raw []byte, maxBody int) (status int, body []byte, err error) {
	entry := AuditEntry{Connector: n.Name}
	defer func() {
		entry.Status = status
		switch {
		case err == nil:
			entry.Decision = DecisionSent
		case errors.Is(err, ErrRouteRefused):
			entry.Decision = DecisionRefused
		default:
			entry.Decision = DecisionFailed
		}
		if err != nil {
			entry.Reason = err.Error()
		}
		if aerr := n.record(entry); aerr != nil && err == nil {
			status, body, err = 0, nil, fmt.Errorf("%w: connector %q: %v", ErrAudit, n.Name, aerr)
		}
	}()

	refuse := func(format string, args ...any) error {
		return fmt.Errorf("%w: connector %q: %s", ErrRouteRefused, n.Name, fmt.Sprintf(format, args...))
	}

	var in httpRequest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if derr := dec.Decode(&in); derr != nil {
		return 0, nil, refuse("malformed request: %v", derr)
	}
	if dec.More() {
		return 0, nil, refuse("malformed request: trailing data after the request object")
	}
	entry.Method = in.Method
	u, perr := url.Parse(in.URL)
	if perr != nil {
		return 0, nil, refuse("%s: unparseable url", in.Method)
	}
	entry.Host, entry.Path, entry.Query = u.Host, u.Path, u.RawQuery
	if u.Scheme != "https" || u.User != nil || u.Fragment != "" {
		return 0, nil, refuse("%s %s%s: only https urls without userinfo or fragment are allowed", in.Method, u.Host, u.Path)
	}
	// Admits cleans the path, so a non-canonical or escaped form must be refused
	// first: the server would resolve it differently from the way it was matched.
	canonical := strings.HasPrefix(u.Path, "/") && path.Clean(u.Path) == u.Path && u.RawPath == ""
	var matched *Route
	if canonical {
		for i, r := range n.Routes {
			if r.Method == in.Method && strings.EqualFold(r.Host, u.Host) && (registryvocab.PathSet{r.Path}).Admits(u.Path) {
				matched = &n.Routes[i]
				break
			}
		}
	}
	if matched == nil {
		return 0, nil, refuse("no route admits %s %s%s", in.Method, u.Host, u.Path)
	}
	entry.Route = matched.String()

	req, rerr := http.NewRequestWithContext(ctx, in.Method, u.String(), strings.NewReader(in.Body))
	if rerr != nil {
		return 0, nil, fmt.Errorf("%w: connector %q: %v", ErrHTTPSend, n.Name, rerr)
	}
	for k, v := range in.Headers {
		req.Header.Set(k, v)
	}
	if n.Authorize != nil {
		if aerr := n.Authorize(req); aerr != nil {
			return 0, nil, fmt.Errorf("%w: connector %q: authorize: %v", ErrHTTPSend, n.Name, aerr)
		}
	}

	client := http.Client{}
	if n.Client != nil {
		client = *n.Client
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, serr := client.Do(req)
	if serr != nil {
		return 0, nil, fmt.Errorf("%w: connector %q: %v", ErrHTTPSend, n.Name, serr)
	}
	if matched.FollowRedirect && resp.StatusCode >= 300 && resp.StatusCode < 400 {
		loc, lerr := resp.Location()
		if lerr == nil {
			status = resp.StatusCode
			resp.Body.Close()
			if loc.Scheme != "https" || loc.User != nil || loc.Host == "" {
				return status, nil, refuse("redirect from %s%s: only https locations without userinfo are followed", u.Host, u.Path)
			}
			entry.RedirectHost = loc.Host
			// A fresh request: the guest's headers and the credential must not leave the admitted host.
			// The hop's budget covers its body read; expiring ctx instead would
			// kill the Connector rather than fail with a plain ErrHTTPSend.
			hopCtx := ctx
			if dl, ok := ctx.Deadline(); ok {
				var cancel context.CancelFunc
				hopCtx, cancel = context.WithTimeout(ctx, time.Until(dl)/redirectHopShare)
				defer cancel()
			}
			hop, herr := http.NewRequestWithContext(hopCtx, http.MethodGet, loc.String(), nil)
			if herr != nil {
				return status, nil, fmt.Errorf("%w: connector %q: %v", ErrHTTPSend, n.Name, herr)
			}
			// A server honouring the suffix range sends only the tail; one that
			// ignores it sends the whole body, which readTail trims. A suffix
			// range on a zero-length blob can draw a 416 from Azure-style hosts;
			// harmless, as an empty log yields no excerpt either way.
			hop.Header.Set("Range", fmt.Sprintf("bytes=-%d", maxBody))
			resp, serr = client.Do(hop)
			if serr != nil {
				return status, nil, fmt.Errorf("%w: connector %q: redirect to %s: %v", ErrHTTPSend, n.Name, loc.Host, serr)
			}
		} else {
			entry.Reason = "redirect not followed: " + lerr.Error()
		}
	}
	defer resp.Body.Close()
	status = resp.StatusCode
	var data []byte
	var rderr error
	if matched.FollowRedirect {
		var total int64
		data, total, rderr = readTail(resp.Body, maxBody)
		// A 206 carries only the tail; its Content-Range total is the real size.
		if resp.StatusCode == http.StatusPartialContent {
			if full, ok := contentRangeTotal(resp.Header.Get("Content-Range")); ok {
				total = full
			}
		}
		entry.Size = total
		if total > int64(maxBody) {
			entry.Reason = fmt.Sprintf("body over %d bytes; kept its last %d", maxBody, maxBody)
		}
	} else {
		data, rderr = io.ReadAll(io.LimitReader(resp.Body, int64(maxBody)+1))
		entry.Size = int64(len(data))
	}
	entry.RateLimit, entry.RetryAfter = rateLimitKind(status, resp.Header, data)
	if rderr != nil {
		return status, nil, fmt.Errorf("%w: connector %q: reading response: %v", ErrHTTPSend, n.Name, rderr)
	}
	if len(data) > maxBody {
		return status, nil, fmt.Errorf("%w: connector %q: over %d bytes", ErrResponseTooLarge, n.Name, maxBody)
	}
	return status, data, nil
}

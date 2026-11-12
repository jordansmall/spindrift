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
	"strings"
	"sync"

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
	// or maxHTTPResponse, whichever is smaller.
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
type Route struct {
	Method, Host, Path string
}

func (r Route) String() string { return r.Method + " " + r.Host + " " + r.Path }

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
// one, though none reach the guest.
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
	_ = n.Audit.Record(AuditEntry{Connector: n.Name, Decision: d, Reason: err.Error()})
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
	// Client is the transport. A redirect is never followed, on a copy, so the
	// caller's client is untouched.
	Client *http.Client
	// Audit receives one entry per request; required.
	Audit *AuditLog
}

// httpRequest is the wire form of a guest's http_request.
type httpRequest struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
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
		if aerr := n.Audit.Record(entry); aerr != nil && err == nil {
			status, body, err = 0, nil, fmt.Errorf("%w: connector %q: %v", ErrAudit, n.Name, aerr)
		}
	}()

	refuse := func(format string, args ...any) error {
		return fmt.Errorf("%w: connector %q: %s", ErrRouteRefused, n.Name, fmt.Sprintf(format, args...))
	}

	var in httpRequest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if derr := dec.Decode(&in); derr != nil || dec.More() {
		return 0, nil, refuse("malformed request")
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
	defer resp.Body.Close()
	status = resp.StatusCode
	data, rderr := io.ReadAll(io.LimitReader(resp.Body, int64(maxBody)+1))
	entry.Size = int64(len(data))
	if rderr != nil {
		return status, nil, fmt.Errorf("%w: connector %q: reading response: %v", ErrHTTPSend, n.Name, rderr)
	}
	if len(data) > maxBody {
		return status, nil, fmt.Errorf("%w: connector %q: over %d bytes", ErrResponseTooLarge, n.Name, maxBody)
	}
	return status, data, nil
}

package connectorhost

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"spindrift.dev/launcher/internal/registryvocab"
)

// maxHTTPResponse caps the response body http_request hands a guest, so a
// hostile or buggy upstream cannot make the host buffer without bound.
const maxHTTPResponse = 4 << 20

// httpRequestTimeout bounds one http_request end to end: the send, a followed
// redirect hop, and the body read. The call budget pauses while the host
// serves a request, so this is all that stops a stalled upstream; it is
// generous enough for a slow GitHub.
const httpRequestTimeout = 60 * time.Second

// maxHTTPURL caps a guest's request URL before it is parsed or routed, so an
// oversized one is neither logged nor echoed.
const maxHTTPURL = 8 << 10

// httpMethods is the fixed set an http_request may use and a Route may
// declare. lib/connector-routes.nix mirrors it.
var httpMethods = []string{
	http.MethodGet, http.MethodHead, http.MethodPost,
	http.MethodPut, http.MethodPatch, http.MethodDelete,
}

var errMethodNotAllowed = errors.New("method is not one of " + strings.Join(httpMethods, ", "))

var (
	// ErrRouteTable marks a Route table that does not validate: a path that
	// breaks the registry-route rules, a method outside the fixed set,
	// or a host that is not a bare host[:port].
	ErrRouteTable = errors.New("connector route table invalid")
	// ErrRouteWildcardRoot marks a Route path whose first segment is "*": it
	// would admit every path on the host, the blanket root ADR 0047 bans.
	ErrRouteWildcardRoot = errors.New(`a leading "*" segment would blanket-authorize the whole host`)
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

// fixedRequestHeaders is what a guest may send on any route: content
// negotiation and caching. A route declares anything else in RequestHeaders.
var fixedRequestHeaders = []string{
	"Accept", "Accept-Language", "Cache-Control", "Content-Type",
	"If-Match", "If-Modified-Since", "If-None-Match", "If-Unmodified-Since",
}

// fixedResponseHeaders is what a guest may read on any route; a route declares
// anything else in ResponseHeaders.
var fixedResponseHeaders = []string{
	"Content-Type", "Content-Length", "Content-Range", "Date", "ETag",
	"Last-Modified", "Link", "Location", "Retry-After",
}

// neverRequestHeader reports a request header no route may declare: the host
// owns the credential and the connection's identity, so a guest never sets
// them. name is canonical. lib/connector-routes.nix mirrors it.
func neverRequestHeader(name string) bool {
	switch name {
	case "Authorization", "Cookie", "Host", "Forwarded", "Via", "X-Real-Ip":
		return true
	}
	return strings.HasPrefix(name, "X-Forwarded-") || strings.HasPrefix(name, "Proxy-")
}

// neverResponseHeader reports a response header no route may declare: a
// cookie is a credential the host keeps. name is canonical.
// lib/connector-routes.nix mirrors it.
func neverResponseHeader(name string) bool {
	return name == "Set-Cookie" || name == "Set-Cookie2"
}

// validHeaderName reports whether s is an RFC 9110 token.
func validHeaderName(s string) bool {
	return s != "" && !strings.ContainsFunc(s, func(r rune) bool {
		return r >= 0x80 || !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", r))
	})
}

// checkDeclaredHeaders judges one route's declared header names; never says
// whether a canonical name is undeclarable.
func checkDeclaredHeaders(field string, names []string, never func(string) bool) error {
	seen := map[string]bool{}
	for _, n := range names {
		c := http.CanonicalHeaderKey(n)
		switch {
		case !validHeaderName(n):
			return fmt.Errorf("%s: %q is not a valid header name", field, n)
		case never(c):
			return fmt.Errorf("%s: %q can never be declared", field, n)
		case seen[c]:
			return fmt.Errorf("%s: %q is declared twice", field, n)
		}
		seen[c] = true
	}
	return nil
}

var documentName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

// checkDeclaredDocuments validates a route's Documents, naming the first
// problem in name order. A nil map is an unconstrained route; a non-nil empty
// one is a mistake, so JSON "documents":{} is refused.
func checkDeclaredDocuments(method string, docs map[string]string) error {
	if docs == nil {
		return nil
	}
	if method != http.MethodPost {
		return errors.New("documents are allowed on POST only")
	}
	if len(docs) == 0 {
		return errors.New("documents: must declare at least one")
	}
	owner := make(map[string]string, len(docs))
	for _, n := range slices.Sorted(maps.Keys(docs)) {
		switch text := docs[n]; {
		case !documentName.MatchString(n):
			return fmt.Errorf("documents: %q is not a valid name", n)
		case text == "":
			return fmt.Errorf("documents: %q has no text", n)
		case owner[text] != "":
			return fmt.Errorf("documents: %q and %q declare the same text", owner[text], n)
		default:
			owner[text] = n
		}
	}
	return nil
}

// Route admits one slice of a network Connector's traffic: an exact uppercase
// Method, an exact host[:port] (case-insensitive, the port significant), and a
// Path subtree root in registryvocab.PathSet form, where a `*` segment matches
// exactly one request segment (`/jobs/*/logs`). A route does not restrict the
// query string: any query is forwarded to an admitted path, and the audit log
// records it.
//
// FollowRedirect lets a GET route follow one redirect hop to a host the table
// cannot name (a pre-signed blob URL). The hop is sent uncredentialed, audited
// with its target host, asks for the body's tail with a Range suffix, and gets
// half the request's remaining time (failing non-fatally past it); String
// shows the opt-in, so a Route-table diff does too. A body over the response
// cap keeps its tail rather than failing the call, which would be fatal to the
// Connector: the tail of a blob such as a job log is the useful part.
//
// RequestHeaders and ResponseHeaders declare the headers a guest may send and
// read beyond the fixed sets (fixedRequestHeaders, fixedResponseHeaders);
// anything else is refused on the way out and absent on the way back. String
// shows them, so a Route-table diff does too. validate rejects a name that
// could carry the credential or the host's identity (neverRequestHeader,
// neverResponseHeader), one that is not a valid header token, and one declared
// twice.
//
// Documents names the GraphQL query texts a POST route's body may carry, each
// under a name the audit reports; the match is on text, with variables free.
// String shows the names, so a Route-table diff does too. validate rejects an
// empty set, a name outside [A-Za-z][A-Za-z0-9_]*, an empty text, two names
// sharing a text, and any method but POST.
type Route struct {
	Method          string            `json:"method"`
	Host            string            `json:"host"`
	Path            string            `json:"path"`
	FollowRedirect  bool              `json:"followRedirect,omitempty"`
	RequestHeaders  []string          `json:"requestHeaders,omitempty"`
	ResponseHeaders []string          `json:"responseHeaders,omitempty"`
	Documents       map[string]string `json:"documents,omitempty"`

	// NamedByKnob marks a host an Operator knob supplied ({base}), which may
	// resolve anywhere; every other host is a literal the Connector declared
	// and must resolve to a public address (checkedDial). The zero value is
	// the checked one, and the tag keeps a Connector's own table from setting
	// it.
	NamedByKnob bool `json:"-"`
}

func (r Route) String() string {
	s := r.Method + " " + r.Host + " " + r.Path
	if r.FollowRedirect {
		s += " (follows redirect)"
	}
	if len(r.RequestHeaders) > 0 {
		s += " (sends " + strings.Join(sortedNames(r.RequestHeaders), ", ") + ")"
	}
	if len(r.ResponseHeaders) > 0 {
		s += " (reads " + strings.Join(sortedNames(r.ResponseHeaders), ", ") + ")"
	}
	if len(r.Documents) > 0 {
		// The text's hash, so an edit under an unchanged name still shows.
		var docs []string
		for _, name := range slices.Sorted(maps.Keys(r.Documents)) {
			sum := sha256.Sum256([]byte(r.Documents[name]))
			docs = append(docs, name+"@"+hex.EncodeToString(sum[:documentHashBytes]))
		}
		s += " (documents " + strings.Join(docs, ", ") + ")"
	}
	return s
}

// documentHashBytes is how many leading bytes of a document's SHA-256 Route.String
// shows: eight hex digits, the width docs/reference.md states.
const documentHashBytes = 4

// sortedNames returns names canonicalised and sorted, so declarations that
// differ only in case render the same.
func sortedNames(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = http.CanonicalHeaderKey(n)
	}
	slices.Sort(out)
	return out
}

// declares reports whether names holds canonical, compared canonically.
func declares(names []string, canonical string) bool {
	return slices.ContainsFunc(names, func(d string) bool { return http.CanonicalHeaderKey(d) == canonical })
}

// sendable reports whether a guest may send the request header name (canonical)
// on this route: the fixed set or a declared extra.
func (r Route) sendable(name string) bool {
	return slices.Contains(fixedRequestHeaders, name) || declares(r.RequestHeaders, name)
}

// ValidateRoutes checks every route, naming the first that fails; the error
// matches ErrRouteTable, and the path reason (registryvocab.ErrPattern* or
// ErrRouteWildcardRoot) when the path is at fault.
func ValidateRoutes(routes []Route) error {
	for _, r := range routes {
		if err := r.validate(); err != nil {
			return fmt.Errorf("%w: route %q: %w", ErrRouteTable, r.String(), err)
		}
	}
	return nil
}

func (r Route) validate() error {
	if !slices.Contains(httpMethods, r.Method) {
		return errMethodNotAllowed
	}
	u, err := url.Parse("https://" + r.Host)
	if err != nil || u.Host != r.Host || u.Hostname() == "" || strings.HasSuffix(r.Host, ":") ||
		u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("host is not a bare host[:port]")
	}
	if r.FollowRedirect && r.Method != http.MethodGet {
		return errors.New("followRedirect is allowed on GET only")
	}
	if err := checkDeclaredHeaders("requestHeaders", r.RequestHeaders, neverRequestHeader); err != nil {
		return err
	}
	if err := checkDeclaredHeaders("responseHeaders", r.ResponseHeaders, neverResponseHeader); err != nil {
		return err
	}
	if err := checkDeclaredDocuments(r.Method, r.Documents); err != nil {
		return err
	}
	if err := registryvocab.CheckPathPattern(r.Path); err != nil {
		return err
	}
	return CheckWildcardRoot(r.Path)
}

// CheckWildcardRoot refuses a declared Route path whose first segment is "*".
// A caller that prefixes the path (a {base} route) must ask before it does:
// "/jira/*" no longer leads with the wildcard but admits the whole subtree.
func CheckWildcardRoot(path string) error {
	if path == "/*" || strings.HasPrefix(path, "/*/") {
		return ErrRouteWildcardRoot
	}
	return nil
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
	// RateLimit is RateLimitPrimary or RateLimitSecondary when a 403 or 429 carries a
	// rate-limit signal, else empty; see rateLimitKind.
	RateLimit string `json:"rate_limit,omitempty"`
	// RetryAfter is the response's Retry-After value verbatim, on a 403 or 429.
	RetryAfter string `json:"retry_after,omitempty"`
	// RedirectHost is the host of the one hop a FollowRedirect route followed.
	RedirectHost string `json:"redirect_host,omitempty"`
	// RedirectStatus is the 3xx the hop followed or was refused on; Status is
	// the hop's answer, which would otherwise hide it.
	RedirectStatus int `json:"redirect_status,omitempty"`
	// Headers names, sorted and canonical, every header the host set on the
	// request (the guest's plus the credential); never a value. It omits what
	// the transport adds (User-Agent, Accept-Encoding, Content-Length) and the
	// redirect hop's Range. Empty when nothing was sent.
	Headers []string `json:"headers,omitempty"`
	// BodySHA256 is the hex SHA-256 of the request body the guest supplied,
	// recorded for every request that decoded, sent or refused, so a refusal
	// can be matched to the exact bytes. Empty whenever no body was decoded:
	// a malformed request, and the lines from networkCalls.refuse,
	// recordFailure and AuditRefusal, which are written without a decoded
	// request.
	BodySHA256 string `json:"body_sha256,omitempty"`
	// Document names the declared GraphQL document the body matched, on a
	// route that declares Documents.
	Document string `json:"document,omitempty"`
	// AddressCheck says how a literal Route host's address was checked: one of
	// the AddressCheck* values, empty for a host an Operator knob named.
	AddressCheck string `json:"address_check,omitempty"`
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

// record writes e to the audit log.
func (n *Network) record(e AuditEntry) error {
	return n.Audit.Record(e)
}

// AuditRefusal audits core refusing the Connector's answer to method, as
// opposed to an http_request, and returns refusal. A failed audit write, or a
// Connector without a network grant (so no audit log), also matches ErrAudit:
// an unaudited refusal must not pass for an audited one. refusal's text is
// written to the audit log, so keep secrets out of it.
func (c *Connector) AuditRefusal(method string, refusal error) error {
	if c.network == nil {
		return errors.Join(refusal, fmt.Errorf("%w: connector has no audit log", ErrAudit))
	}
	n := c.network
	if err := n.record(AuditEntry{Connector: n.Name, Decision: DecisionRefused, Reason: method + ": " + refusal.Error()}); err != nil {
		return errors.Join(refusal, fmt.Errorf("%w: connector %q: %v", ErrAudit, n.Name, err))
	}
	return refusal
}

// Network is the host side of a network Connector's Host calls: http_request
// and the http_response_header read of its answer.
type Network struct {
	// Name is the Connector's name, in errors and the audit log.
	Name string
	// Routes is the only traffic the Connector may send; see ValidateRoutes.
	Routes []Route
	// Authorize attaches the credential to an admitted request, after the
	// guest's headers are copied so it wins over any the guest set. Its error
	// must name what failed (an unset variable, say) and never the credential:
	// it reaches Call's error and the audit entry.
	Authorize func(*http.Request) error
	// Client is the transport. A redirect is never followed except the one hop
	// of a FollowRedirect route; CheckRedirect is set on a copy, so the
	// caller's client is untouched.
	Client *http.Client
	// PublicForTest replaces the public-address classifier and LookupForTest the
	// system resolver, for a harness that dials a local test server through a
	// literal host. A production binary that sets either fails to load (checkSeams).
	PublicForTest func(netip.Addr) bool
	LookupForTest func(ctx context.Context, host string) ([]netip.Addr, error)
	// checked is Client's transport swapped for one that dials only public
	// addresses; set at load, nil when Client's transport is not an *http.Transport.
	checked *http.Client
	// Audit receives one entry per request; required.
	Audit *AuditLog
	// requestTimeout bounds one http_request; zero means httpRequestTimeout.
	requestTimeout time.Duration
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
// request's remaining time, so a slow blob host fails non-fatally before
// the request timeout or the caller's deadline cuts the whole request.
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

// maxBodyKeyEcho bounds an unknown body key quoted in a refusal, so a hostile
// body cannot bloat the audit line. Twin: sdk/connector/fakehost.go, pinned by
// nix/checks/graphql-query-twin-parity.nix.
const maxBodyKeyEcho = 256

// graphQLQuery returns the "query" text of a GraphQL POST body, or says why
// the body is not the plain {"query","variables","operationName"} object.
// It is deliberately strict because the server's JSON parser is not Go's: Go
// matches keys case-insensitively and keeps the last duplicate, so a lenient
// decode could judge one query while the server runs another. Any key
// spelling, duplicate or trailing data that could be read two ways is
// refused. The text is compared as is; there is no GraphQL parser here.
func graphQLQuery(body string) (string, error) {
	errShape := errors.New(`body is not a JSON object with a string "query"`)
	dec := json.NewDecoder(strings.NewReader(body))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return "", errShape
	}
	var query string
	seen := map[string]bool{}
	for dec.More() {
		tok, err := dec.Token()
		key, ok := tok.(string)
		if err != nil || !ok {
			return "", errShape
		}
		if key != "query" && key != "variables" && key != "operationName" {
			if len(key) > maxBodyKeyEcho {
				key = key[:maxBodyKeyEcho] + "..."
			}
			return "", fmt.Errorf("body key %q is not allowed", key)
		}
		if seen[key] {
			return "", fmt.Errorf("body key %q is duplicated", key)
		}
		seen[key] = true
		if key == "query" {
			err = dec.Decode(&query)
		} else {
			var skip json.RawMessage
			err = dec.Decode(&skip)
		}
		if err != nil {
			return "", errShape
		}
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return "", errShape
	}
	if _, err := dec.Token(); err != io.EOF {
		return "", errors.New("body has trailing data after the JSON object")
	}
	if !seen["query"] {
		return "", errShape
	}
	return query, nil
}

// matchDocument returns the first name, in sorted order, whose document is
// query, or "" when none is. Twin: sdk/connector/fakehost.go, pinned by
// nix/checks/graphql-query-twin-parity.nix.
func matchDocument(docs map[string]string, query string) string {
	for _, name := range slices.Sorted(maps.Keys(docs)) {
		if docs[name] == query {
			return name
		}
	}
	return ""
}

// do runs one http_request: match, authorize, send, audit. One audit entry is
// recorded whatever the outcome; if that write fails the answer is withheld.
func (n *Network) do(ctx context.Context, raw []byte, maxBody int) (status int, body []byte, hdr http.Header, err error) {
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
			status, body, hdr, err = 0, nil, nil, fmt.Errorf("%w: connector %q: %v", ErrAudit, n.Name, aerr)
		}
	}()

	refuse := func(format string, args ...any) error {
		return fmt.Errorf("%w: connector %q: %s", ErrRouteRefused, n.Name, fmt.Sprintf(format, args...))
	}

	var in httpRequest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if derr := dec.Decode(&in); derr != nil {
		return 0, nil, nil, refuse("malformed request: %v", derr)
	}
	if dec.More() {
		return 0, nil, nil, refuse("malformed request: trailing data after the request object")
	}
	sum := sha256.Sum256([]byte(in.Body))
	entry.BodySHA256 = hex.EncodeToString(sum[:])
	if len(in.URL) > maxHTTPURL {
		return 0, nil, nil, refuse("url is %d bytes, over the %d-byte limit", len(in.URL), maxHTTPURL)
	}
	if !slices.Contains(httpMethods, in.Method) {
		return 0, nil, nil, refuse("%v", errMethodNotAllowed)
	}
	entry.Method = in.Method
	u, perr := url.Parse(in.URL)
	if perr != nil {
		return 0, nil, nil, refuse("%s: unparseable url", in.Method)
	}
	entry.Host, entry.Path, entry.Query = u.Host, u.Path, u.RawQuery
	if u.Scheme != "https" || u.User != nil || u.Fragment != "" {
		return 0, nil, nil, refuse("%s %s%s: only https urls without userinfo or fragment are allowed", in.Method, u.Host, u.Path)
	}
	// Admits cleans the path, so a non-canonical or escaped form must be refused
	// first: the server would resolve it differently from the way it was matched.
	canonical := strings.HasPrefix(u.Path, "/") && path.Clean(u.Path) == u.Path && u.RawPath == ""
	var matched *Route
	if canonical {
		for i, r := range n.Routes {
			if r.Method == in.Method && strings.EqualFold(r.Host, u.Host) && routeAdmits(r.Path, u.Path) {
				matched = &n.Routes[i]
				break
			}
		}
	}
	if matched == nil {
		return 0, nil, nil, refuse("no route admits %s %s%s", in.Method, u.Host, u.Path)
	}
	entry.Route = matched.String()

	if matched.Documents != nil {
		// A GraphQL server may also read ?query= on a POST, and would run a
		// document the host never matched.
		if u.RawQuery != "" {
			return 0, nil, nil, refuse("%s %s%s: a route that declares documents admits no query string", in.Method, u.Host, u.Path)
		}
		query, qerr := graphQLQuery(in.Body)
		if qerr != nil {
			return 0, nil, nil, refuse("%s %s%s: %v", in.Method, u.Host, u.Path, qerr)
		}
		entry.Document = matchDocument(matched.Documents, query)
		if entry.Document == "" {
			return 0, nil, nil, refuse("%s %s%s: query is not a document the route declares", in.Method, u.Host, u.Path)
		}
	}

	// Sorted, so the refusal names the same header whatever the map order.
	for _, k := range slices.Sorted(maps.Keys(in.Headers)) {
		c := http.CanonicalHeaderKey(k)
		if !matched.sendable(c) {
			// The name is the guest's; bound what a refusal echoes of it.
			name := k
			if len(name) > maxHeaderName {
				name = name[:maxHeaderName] + "..."
			}
			return 0, nil, nil, refuse("%s %s%s: request header %q is not declared by the route", in.Method, u.Host, u.Path, name)
		}
	}

	timeout := cmp.Or(n.requestTimeout, httpRequestTimeout)
	timeoutCause := fmt.Errorf("no response within %s", timeout)
	ctx, cancelTimeout := context.WithTimeoutCause(ctx, timeout, timeoutCause)
	defer cancelTimeout()
	// sendFailed names the clock that ended the request, the timeout above or
	// the budget's http_request time cap, when one did; context.DeadlineExceeded
	// alone would not say which ran out. A context ended with no cause of its
	// own, and any pre-send failure, passes through unchanged.
	sendFailed := func(what string, cause error) error {
		if c := context.Cause(ctx); ctx.Err() != nil && c != ctx.Err() {
			cause = c
		}
		if what != "" {
			what += ": "
		}
		return fmt.Errorf("%w: connector %q: %s%v", ErrHTTPSend, n.Name, what, cause)
	}

	req, rerr := http.NewRequestWithContext(ctx, in.Method, u.String(), strings.NewReader(in.Body))
	if rerr != nil {
		return 0, nil, nil, sendFailed("", rerr)
	}
	for k, v := range in.Headers {
		req.Header.Set(k, v)
	}
	if n.Authorize != nil {
		if aerr := n.Authorize(req); aerr != nil {
			return 0, nil, nil, sendFailed("authorize", aerr)
		}
	}
	entry.Headers = slices.Sorted(maps.Keys(req.Header))

	client := http.Client{}
	if n.Client != nil {
		client = *n.Client
	}
	if !matched.NamedByKnob {
		var checked *http.Client
		checked, entry.AddressCheck = n.literalClient(req)
		if checked != nil {
			client = *checked
		}
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, serr := client.Do(req)
	if serr != nil {
		var refused *notPublicError
		if errors.As(serr, &refused) {
			entry.AddressCheck = AddressCheckRefused
			return 0, nil, nil, refuse("%s %s%s: %v", in.Method, u.Host, u.Path, refused)
		}
		return 0, nil, nil, sendFailed("", serr)
	}
	if matched.FollowRedirect && resp.StatusCode >= 300 && resp.StatusCode < 400 {
		loc, lerr := resp.Location()
		if lerr == nil {
			status = resp.StatusCode
			resp.Body.Close()
			if loc.Scheme != "https" || loc.User != nil || loc.Host == "" {
				return status, nil, nil, refuse("redirect from %s%s: only https locations without userinfo are followed", u.Host, u.Path)
			}
			entry.RedirectHost = loc.Host
			entry.RedirectStatus = status
			// A fresh request: the guest's headers and the credential must not
			// leave the admitted host. The hop's share covers its body read; the
			// caller's deadline expiring instead would kill the Connector rather
			// than fail with a plain ErrHTTPSend.
			hopCtx := ctx
			if dl, ok := ctx.Deadline(); ok {
				var cancel context.CancelFunc
				hopCtx, cancel = context.WithTimeout(ctx, time.Until(dl)/redirectHopShare)
				defer cancel()
			}
			// The client's and url.Parse's *url.Error quote the hop URL, whose
			// signed query is a secret; the inner error names the failure
			// without it.
			hopFailed := func(err error) error {
				var uerr *url.Error
				if errors.As(err, &uerr) {
					err = uerr.Err
				}
				return sendFailed("redirect to "+loc.Host, err)
			}
			hop, herr := http.NewRequestWithContext(hopCtx, http.MethodGet, loc.String(), nil)
			if herr != nil {
				return status, nil, nil, hopFailed(herr)
			}
			// A server honouring the suffix range sends only the tail; one that
			// ignores it sends the whole body, which readTail trims. A suffix
			// range on a zero-length blob can draw a 416 from Azure-style hosts;
			// harmless, as an empty log yields no excerpt either way.
			hop.Header.Set("Range", fmt.Sprintf("bytes=-%d", maxBody))
			// The hop is always address-checked, even from a knob-named route: a
			// server the Operator trusts must not redirect into the private
			// network.
			hopper := client
			checked, check := n.literalClient(hop)
			if checked != nil {
				hopper = *checked
				hopper.CheckRedirect = client.CheckRedirect
			}
			entry.AddressCheck = check
			resp, serr = hopper.Do(hop)
			if serr != nil {
				var refused *notPublicError
				if errors.As(serr, &refused) {
					entry.AddressCheck = AddressCheckRefused
					return status, nil, nil, refuse("redirect from %s%s: %v", u.Host, u.Path, refused)
				}
				return status, nil, nil, hopFailed(serr)
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
		return status, nil, nil, sendFailed("reading response", rderr)
	}
	if len(data) > maxBody {
		return status, nil, nil, fmt.Errorf("%w: connector %q: over %d bytes", ErrResponseTooLarge, n.Name, maxBody)
	}
	return status, data, matched.readable(resp.Header), nil
}

// readable is the part of h a guest may read on this route: the fixed set and
// the route's declared extras. The host's own reads (rateLimitKind) use the
// whole header.
func (r Route) readable(h http.Header) http.Header {
	out := http.Header{}
	for _, name := range slices.Concat(fixedResponseHeaders, r.ResponseHeaders) {
		c := http.CanonicalHeaderKey(name)
		if v, ok := h[c]; ok {
			out[c] = slices.Clone(v)
		}
	}
	return out
}

// routeAdmits is registryvocab.PathSet's subtree test for one Route path, plus
// the `*` segment: it matches exactly one non-empty request segment, and
// whatever follows the pattern stays a subtree, so `/jobs/*/logs` admits
// `/jobs/7/logs` and below but not `/jobs/7`. Twin: sdk/connector/fakehost.go.
func routeAdmits(pattern, requestPath string) bool {
	pat := strings.Split(strings.TrimPrefix(pattern, "/"), "/")
	if !slices.Contains(pat, "*") {
		return registryvocab.PathSet{pattern}.Admits(requestPath)
	}
	if strings.Contains(requestPath, ";") {
		return false
	}
	cleaned := path.Clean(requestPath)
	if !strings.HasPrefix(cleaned, "/") {
		return false
	}
	segs := strings.Split(cleaned[1:], "/")
	if len(segs) < len(pat) {
		return false
	}
	for i, p := range pat {
		if segs[i] == "" || (p != "*" && p != segs[i]) {
			return false
		}
	}
	return true
}

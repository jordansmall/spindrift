package connectorhost_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/connectorhost"
)

const (
	docViewer = `query { viewer { login } }`
	docRepo   = `query($o:String!){ repository(owner:$o) { id } }`
)

func docsRoute(b *backend) connectorhost.Route {
	return connectorhost.Route{Method: "POST", Host: b.host(), Path: "/graphql",
		Documents: map[string]string{"Viewer": docViewer, "Repo": docRepo}}
}

// postBody wraps a POST body into the guest's http_request wire form.
func postBody(t *testing.T, b *backend, body string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"method": "POST", "url": b.URL + "/graphql", "body": body})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestNetworkSendsADeclaredDocumentWithAnyVariables(t *testing.T) {
	var got string
	b := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		got = string(data)
	})
	var audit bytes.Buffer
	n := netFor(b, &audit, docsRoute(b))
	body := `{"query":"query($o:String!){ repository(owner:$o) { id } }","variables":{"o":"anything","x":[1,2]},"operationName":"Repo"}`
	if _, _, err := n.Do(context.Background(), postBody(t, b, body), 1<<10); err != nil {
		t.Fatal(err)
	}
	if got != body {
		t.Errorf("server saw %q, want %q", got, body)
	}
	lines := auditLines(t, &audit)
	if len(lines) != 1 || lines[0].Decision != connectorhost.DecisionSent || lines[0].Document != "Repo" || lines[0].BodySHA256 != sha(body) {
		t.Errorf("audit = %+v, want sent, document Repo, body_sha256 %s", lines, sha(body))
	}
}

func TestNetworkRefusesAnythingButADeclaredDocument(t *testing.T) {
	for name, body := range map[string]string{
		"undeclared":          `{"query":"mutation { deleteEverything }"}`,
		"altered text":        `{"query":"query  { viewer { login } }"}`,
		"trailing space":      `{"query":"query { viewer { login } } "}`,
		"empty query":         `{"query":""}`,
		"missing query":       `{"variables":{}}`,
		"null query":          `{"query":null}`,
		"non-string query":    `{"query":["query { viewer { login } }"]}`,
		"duplicate, evil 2nd": `{"query":"query { viewer { login } }","query":"mutation { x }"}`,
		"duplicate, evil 1st": `{"query":"mutation { x }","query":"query { viewer { login } }"}`,
		"duplicate variables": `{"query":"query { viewer { login } }","variables":{},"variables":{}}`,
		"case-variant key":    `{"Query":"query { viewer { login } }"}`,
		"case-variant extra":  `{"query":"query { viewer { login } }","QUERY":"mutation { x }"}`,
		"extra key":           `{"query":"query { viewer { login } }","extensions":{"persisted":1}}`,
		"trailing data":       `{"query":"query { viewer { login } }"} {"query":"mutation { x }"}`,
		"trailing garbage":    `{"query":"query { viewer { login } }"}x`,
		"array":               `[{"query":"query { viewer { login } }"}]`,
		"string":              `"query { viewer { login } }"`,
		"not json":            `query { viewer { login } }`,
		"empty body":          ``,
		"truncated":           `{"query":"query { viewer { login } }"`,
	} {
		t.Run(name, func(t *testing.T) {
			b := newBackend(t, func(http.ResponseWriter, *http.Request) {})
			var audit bytes.Buffer
			n := netFor(b, &audit, docsRoute(b))
			_, _, err := n.Do(context.Background(), postBody(t, b, body), 1<<10)
			if !errors.Is(err, connectorhost.ErrRouteRefused) {
				t.Fatalf("err = %v, want ErrRouteRefused", err)
			}
			if strings.Contains(err.Error(), "deleteEverything") || strings.Contains(err.Error(), "mutation") {
				t.Errorf("refusal echoes the guest's query: %v", err)
			}
			if b.hits.Load() != 0 {
				t.Errorf("backend saw %d requests, want none", b.hits.Load())
			}
			lines := auditLines(t, &audit)
			if len(lines) != 1 || lines[0].Decision != connectorhost.DecisionRefused || lines[0].BodySHA256 != sha(body) || lines[0].Document != "" {
				t.Errorf("audit = %+v, want one refused entry with body_sha256 %s and no document", lines, sha(body))
			}
		})
	}
}

// A GraphQL server may also read ?query= on a POST, so a query string would
// let the server run a document the host never matched.
func TestNetworkRefusesAQueryStringOnADocumentsRoute(t *testing.T) {
	b := newBackend(t, func(http.ResponseWriter, *http.Request) {})
	var audit bytes.Buffer
	n := netFor(b, &audit, docsRoute(b))
	body := `{"query":"query { viewer { login } }"}`
	raw, err := json.Marshal(map[string]string{"method": "POST", "url": b.URL + "/graphql?query=mutation%7Bx%7D", "body": body})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = n.Do(context.Background(), raw, 1<<10)
	if !errors.Is(err, connectorhost.ErrRouteRefused) {
		t.Fatalf("err = %v, want ErrRouteRefused", err)
	}
	if b.hits.Load() != 0 {
		t.Errorf("backend saw %d requests, want none", b.hits.Load())
	}
	lines := auditLines(t, &audit)
	if len(lines) != 1 || lines[0].Decision != connectorhost.DecisionRefused || lines[0].BodySHA256 != sha(body) || lines[0].Document != "" {
		t.Errorf("audit = %+v, want one refused entry with body_sha256 %s and no document", lines, sha(body))
	}
}

func TestNetworkRefusalNamesAnOversizedKeyBounded(t *testing.T) {
	b := newBackend(t, func(http.ResponseWriter, *http.Request) {})
	var audit bytes.Buffer
	n := netFor(b, &audit, docsRoute(b))
	key := strings.Repeat("k", 4096)
	_, _, err := n.Do(context.Background(), postBody(t, b, `{"`+key+`":1}`), 1<<10)
	if !errors.Is(err, connectorhost.ErrRouteRefused) || len(err.Error()) > 1024 {
		t.Errorf("err = %v (%d bytes), want a bounded ErrRouteRefused", err, len(err.Error()))
	}
}

// An unconstrained route sends any body; its audit line still hashes it.
func TestNetworkAuditsTheBodyHashOfAnUnconstrainedRoute(t *testing.T) {
	for _, body := range []string{`{"anything":1}`, ``} {
		b := newBackend(t, func(http.ResponseWriter, *http.Request) {})
		var audit bytes.Buffer
		n := netFor(b, &audit, connectorhost.Route{Method: "POST", Host: b.host(), Path: "/graphql"})
		if _, _, err := n.Do(context.Background(), postBody(t, b, body), 1<<10); err != nil {
			t.Fatal(err)
		}
		lines := auditLines(t, &audit)
		if len(lines) != 1 || lines[0].BodySHA256 != sha(body) || lines[0].Document != "" {
			t.Errorf("audit = %+v, want body_sha256 %s and no document", lines, sha(body))
		}
	}
}

func TestNetworkAuditsNoBodyHashForAMalformedRequest(t *testing.T) {
	b := newBackend(t, func(http.ResponseWriter, *http.Request) {})
	var audit bytes.Buffer
	n := netFor(b, &audit, docsRoute(b))
	if _, _, err := n.Do(context.Background(), []byte(`{"method":`), 1<<10); !errors.Is(err, connectorhost.ErrRouteRefused) {
		t.Fatalf("err = %v, want ErrRouteRefused", err)
	}
	if lines := auditLines(t, &audit); len(lines) != 1 || lines[0].BodySHA256 != "" {
		t.Errorf("audit = %+v, want no body_sha256", lines)
	}
}

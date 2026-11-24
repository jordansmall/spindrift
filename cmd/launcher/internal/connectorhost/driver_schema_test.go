package connectorhost_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectorprotocol/schematest"
	"spindrift.dev/launcher/internal/driver/driverkit"
)

// conformingStrategy is strategy over a Connector whose every exchange with the
// guest is checked against the protocol schema when the test ends: the params
// core sent and the result the guest answered. Use it for a test whose guest
// answers as the SDK does; a test that makes the guest answer something
// deliberately off-protocol keeps strategy.
func conformingStrategy(t *testing.T, name string, without ...string) driverkit.Strategy {
	t.Helper()
	c, err := load(t, name)
	if err != nil {
		t.Fatal(err)
	}
	exchanges := connectorhost.TapExchanges(c)
	t.Cleanup(func() { requireExchangesConform(t, exchanges()) })
	s, err := c.DriverStrategyWithout(without...)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func requireExchangesConform(t *testing.T, exchanges []connectorhost.Exchange) {
	t.Helper()
	if len(exchanges) == 0 {
		t.Fatal("no exchanges recorded to validate")
	}
	s, err := schematest.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range exchanges {
		var method string
		var checkParams func() error
		if bytes.HasPrefix(e.Request, []byte("{")) {
			var req struct {
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if err := json.Unmarshal(e.Request, &req); err != nil {
				t.Errorf("request %.80q: %v", e.Request, err)
				continue
			}
			method = req.Method
			checkParams = func() error { return s.ValidateParams(method, req.Params) }
		} else {
			// A raw request is "<method>\n<payload>".
			m, payload, _ := strings.Cut(string(e.Request), "\n")
			method = m
			checkParams = func() error { return s.ValidateRawParams(method, payload) }
		}
		if err := checkParams(); err != nil {
			t.Errorf("params core sent: %v", err)
		}
		var resp struct {
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(e.Response, &resp); err != nil {
			t.Errorf("%s response %.80q: %v", method, e.Response, err)
			continue
		}
		if resp.Result == nil {
			continue // a Connector-reported error answers with no result
		}
		if err := s.ValidateResult(method, resp.Result); err != nil {
			t.Errorf("result the guest answered: %v", err)
		}
	}
}

// A batch is carried by decodeRaw when the guest advertises it and by
// decodeBatch otherwise; both exchanges conform. The lines are plain text: the
// fixture answers a host line with a forged host_op event, which the host
// discards and the schema rightly refuses.
func TestDriverBatchExchangesConformToTheSchema(t *testing.T) {
	lines := []string{"a", " b ", "", `"q" \ <&>`, "c"}
	for name, without := range map[string][]string{
		"decodeRaw":   nil,
		"decodeBatch": {connectorhost.DecodeRawMethod},
	} {
		t.Run(name, func(t *testing.T) {
			s := conformingStrategy(t, "driver.wasm", without...)
			got := s.NewBatchDecoder()(lines)
			if len(got) != len(lines) {
				t.Fatalf("decoded %d lines, want %d", len(got), len(lines))
			}
		})
	}
}

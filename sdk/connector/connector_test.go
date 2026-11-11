package connector

import (
	"encoding/json"
	"slices"
	"testing"
)

func resetForTest(t *testing.T) {
	t.Helper()
	saved := capabilities
	t.Cleanup(func() { capabilities = saved })
	capabilities = nil
}

func TestHandshake(t *testing.T) {
	cases := []struct {
		name     string
		register [][]string
		want     []string
	}{
		{"none", nil, nil},
		{"one call", [][]string{{"a", "b"}}, []string{"a", "b"}},
		{"accumulates", [][]string{{"a"}, {"b", "c"}}, []string{"a", "b", "c"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resetForTest(t)
			for _, caps := range c.register {
				Register(caps...)
			}
			raw, err := handshakeJSON()
			if err != nil {
				t.Fatal(err)
			}
			var got handshakeResult
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("not valid JSON: %v", err)
			}
			if got.ProtocolVersion != protocolVersion {
				t.Errorf("protocolVersion = %q, want %q", got.ProtocolVersion, protocolVersion)
			}
			if !slices.Equal(got.Capabilities, c.want) {
				t.Errorf("capabilities = %v, want %v", got.Capabilities, c.want)
			}
		})
	}
}

package driverkit

import (
	"bytes"
	"testing"
)

func TestNewStreamFilterNilIsByteIdenticalPassthrough(t *testing.T) {
	var out bytes.Buffer
	f := New(Strategy{}).NewStreamFilter(&out)
	in := "{\"a\":1}\npartial, no newline"
	for _, chunk := range []string{in[:3], in[3:10], in[10:]} {
		if n, err := f.Write([]byte(chunk)); err != nil || n != len(chunk) {
			t.Fatalf("Write(%q) = %d, %v", chunk, n, err)
		}
	}
	if err := f.Flush(); err != nil {
		t.Fatal(err)
	}
	if out.String() != in {
		t.Errorf("passthrough = %q, want %q", out.String(), in)
	}
}

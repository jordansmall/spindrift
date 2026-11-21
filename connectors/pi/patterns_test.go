package main

import (
	"testing"

	"spindrift.dev/sdk/driver"
)

// The list is pinned literally: its order is the match order. The native
// Driver's copy is held identical by its TestConnectorSourceParity.
func TestTransientPatterns(t *testing.T) {
	want := []driver.Pattern{
		{Substr: "rate_limit_error", Reason: driver.RateLimit},
		{Substr: "429: ", Reason: driver.RateLimit},
		{Substr: "Too Many Requests", Reason: driver.RateLimit},
		{Substr: "overloaded_error", Reason: driver.Overloaded},
		{Substr: "529: ", Reason: driver.Overloaded},
		{Substr: "503: ", Reason: driver.Overloaded},
		{Substr: "Overloaded", Reason: driver.Overloaded},
		{Substr: "Service Unavailable", Reason: driver.Overloaded},
		{Substr: "500: ", Reason: driver.Network},
		{Substr: "502: ", Reason: driver.Network},
		{Substr: "504: ", Reason: driver.Network},
		{Substr: "fetch failed", Reason: driver.Network},
		{Substr: "ECONNRESET", Reason: driver.Network},
		{Substr: "ETIMEDOUT", Reason: driver.Network},
		{Substr: "ENOTFOUND", Reason: driver.Network},
		{Substr: "EAI_AGAIN", Reason: driver.Network},
		{Substr: "socket hang up", Reason: driver.Network},
		{Substr: "other side closed", Reason: driver.Network},
		{Substr: "Connection error", Reason: driver.Network},
	}
	if len(TransientPatterns) != len(want) {
		t.Fatalf("len = %d, want %d", len(TransientPatterns), len(want))
	}
	for i, p := range want {
		if TransientPatterns[i] != p {
			t.Errorf("[%d] = %+v, want %+v", i, TransientPatterns[i], p)
		}
	}
}

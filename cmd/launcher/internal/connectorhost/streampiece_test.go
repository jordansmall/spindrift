package connectorhost

import (
	"math"
	"testing"
)

// A full piece's request must fit under maxRequest, or the host would refuse
// the very writes it splits a stream into. The worst case is a binary piece
// (base64, a third larger) under the widest filter handle.
func TestStreamPieceRequestFitsMaxRequest(t *testing.T) {
	req, err := encodeRequest("driver.streamWrite", map[string]any{
		"filter": uint32(math.MaxUint32),
		"data":   make([]byte, streamPiece),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(req) > maxRequest {
		t.Errorf("a full %d-byte piece encodes to %d bytes, over maxRequest %d", streamPiece, len(req), maxRequest)
	}
}

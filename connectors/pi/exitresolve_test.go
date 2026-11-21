package main

import (
	"strings"
	"testing"
)

const (
	failedTurn = `{"type":"message_end","message":{"role":"assistant","content":[],"stopReason":"error","errorMessage":"400: bad","timestamp":1}}`
	progress   = `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"ok"}],"timestamp":2}}`
)

// paddedFailedTurn returns failedTurn grown to exactly n bytes by padding its
// errorMessage, so it stays a decodable error.
func paddedFailedTurn(n int) string {
	s := strings.Replace(failedTurn, "400: bad", "400: bad"+strings.Repeat("x", n-len(failedTurn)), 1)
	if len(s) != n {
		panic("paddedFailedTurn: failedTurn no longer holds the padded errorMessage")
	}
	return s
}

func TestResolveExit(t *testing.T) {
	for name, tc := range map[string]struct {
		log      string
		exitCode int
		want     int
	}{
		"unrecovered error under exit 0":           {failedTurn + "\n", 0, 1},
		"recovered by later progress":              {failedTurn + "\n" + progress + "\n", 0, 0},
		"empty log":                                {"", 0, 0},
		"nonzero exit passes through":              {failedTurn + "\n", 3, 3},
		"error line of maxLine bytes is skipped":   {paddedFailedTurn(maxLine) + "\n", 0, 0},
		"error line of maxLine-1 bytes is decoded": {paddedFailedTurn(maxLine-1) + "\n", 0, 1},
		// The \r counts toward the native scan's buffer, as it does toward len(line).
		"CRLF error line of maxLine bytes is skipped":           {paddedFailedTurn(maxLine-1) + "\r\n", 0, 0},
		"CRLF error line of maxLine-1 bytes is decoded":         {paddedFailedTurn(maxLine-2) + "\r\n", 0, 1},
		"unterminated error line of maxLine bytes is skipped":   {paddedFailedTurn(maxLine), 0, 0},
		"unterminated error line of maxLine-1 bytes is decoded": {paddedFailedTurn(maxLine - 1), 0, 1},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := ResolveExit([]byte(tc.log), tc.exitCode)
			if err != nil || got != tc.want {
				t.Fatalf("ResolveExit = %d, %v; want %d", got, err, tc.want)
			}
		})
	}
}

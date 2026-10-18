package pi_test

import (
	"fmt"

	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/driver/pi"
)

// newDriver builds the pi Driver from the package's own Strategy, the seam the
// reader tests in this package exercise.
func newDriver() driverkit.Driver {
	return driverkit.New(pi.Strategy)
}

const stubUsage = `{"input":80,"output":30,"cacheRead":40,"cacheWrite":0,"reasoning":10,"totalTokens":150,"cost":{"total":0.000144}}`

// assistantEnd renders an assistant message_end shaped like the pi 1.1.0
// captures: content is the JSON array of blocks, extra is appended verbatim
// (e.g. an errorMessage field).
func assistantEnd(ts int, content, stopReason, extra string) string {
	return fmt.Sprintf(`{"type":"message_end","message":{"role":"assistant","content":%s,"api":"openai-completions","provider":"stub","model":"stub-model","usage":%s,"stopReason":%q,"timestamp":%d,"durationMs":35,"thinkingLevel":"high"%s}}`,
		content, stubUsage, stopReason, ts, extra)
}

func textEnd(ts int, text string) string {
	return assistantEnd(ts, fmt.Sprintf(`[{"type":"text","text":%q}]`, text), "stop", "")
}

func errorEnd(ts int, msg string) string {
	return assistantEnd(ts, `[]`, "error", fmt.Sprintf(`,"errorMessage":%q`, msg))
}

const (
	outcomeText = "SPINDRIFT_OUTCOME issue=42 landing=https://example/pr/1 status=ready note=done"

	rateLimitMsg = `429: {"message":"Rate limit reached","type":"rate_limit_error","code":"rate_limit_exceeded"}`
	badKeyMsg    = `401: {"message":"Incorrect API key","type":"invalid_request_error","code":"invalid_api_key"}`

	retryStart = `{"type":"auto_retry_start","attempt":1,"maxAttempts":3,"delayMs":10,"errorMessage":"429: ..."}`
	agentEnd   = `{"type":"agent_end","messages":[],"willRetry":false}`
	settled    = `{"type":"agent_settled","aborted":false}`
)

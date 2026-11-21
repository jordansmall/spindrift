package main

import "spindrift.dev/sdk/driver"

// TransientPatterns is opencode's complete ordered marker list, checked before
// the host's shared network suffix. The numeric status markers are
// phrase-anchored so a digit run such as a token count or timestamp does not
// match (issue #4423). opencode supplies its own list to keep 429 precedence
// over "overloaded_error"/"Overloaded".
var TransientPatterns = []driver.Pattern{
	{Substr: "rate_limit_error", Reason: driver.RateLimit},
	{Substr: "status 429", Reason: driver.RateLimit},
	{Substr: "status code 429", Reason: driver.RateLimit},
	{Substr: "429 Too Many Requests", Reason: driver.RateLimit},
	{Substr: `"statusCode":429`, Reason: driver.RateLimit},
	{Substr: "overloaded_error", Reason: driver.Overloaded},
	{Substr: "status 529", Reason: driver.Overloaded},
	{Substr: "status code 529", Reason: driver.Overloaded},
	{Substr: `"statusCode":529`, Reason: driver.Overloaded},
	{Substr: "Overloaded", Reason: driver.Overloaded},
}

// TerminalPatterns holds markers for non-retryable harness argv rejections.
// opencode's yargs fail handler answers an unknown flag (effort rides
// --variant) with the `run` usage dump instead of yargs' "Unknown argument"
// line, and answers the sibling argv rejections the same way; a retry fixes
// none of them. "Unknown argument" guards an opencode that stops swallowing
// that message.
var TerminalPatterns = []driver.Pattern{
	{Substr: "opencode run [message..]", Reason: driver.UnsupportedFlag},
	{Substr: "Unknown argument", Reason: driver.UnsupportedFlag},
}

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

// TerminalPatterns holds markers for non-retryable harness argv rejections. opencode
// rejects an unknown flag (the harness passes effort as --variant) through
// yargs .strict() with a custom fail handler that prints the `run` usage dump
// instead of yargs' "Unknown argument" line, then exits 1; "Unknown argument"
// stays as a guard for an opencode that stops swallowing that message. The
// usage banner also fires on yargs' sibling rejections ("Not enough non-option
// arguments", "Invalid values:"), all of which are argv faults a retry cannot
// fix.
var TerminalPatterns = []driver.Pattern{
	{Substr: "opencode run [message..]", Reason: driver.UnsupportedFlag},
	{Substr: "Unknown argument", Reason: driver.UnsupportedFlag},
}

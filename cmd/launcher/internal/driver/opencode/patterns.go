package opencode

import "spindrift.dev/launcher/internal/driver/driverkit"

// TransientPatterns is opencode's complete ordered marker list, checked before
// the shared driverkit.BaseTransientPatterns network suffix. The numeric status
// markers are phrase-anchored so a digit run such as a token count or timestamp
// does not match (issue #4423). opencode supplies its own list to keep 429
// precedence over "overloaded_error"/"Overloaded". Exported only for
// Strategy; treat it as read-only.
var TransientPatterns = []driverkit.Pattern{
	{Substr: "rate_limit_error", Reason: driverkit.RateLimit},
	{Substr: "status 429", Reason: driverkit.RateLimit},
	{Substr: "status code 429", Reason: driverkit.RateLimit},
	{Substr: "429 Too Many Requests", Reason: driverkit.RateLimit},
	{Substr: `"statusCode":429`, Reason: driverkit.RateLimit},
	{Substr: "overloaded_error", Reason: driverkit.Overloaded},
	{Substr: "status 529", Reason: driverkit.Overloaded},
	{Substr: "status code 529", Reason: driverkit.Overloaded},
	{Substr: `"statusCode":529`, Reason: driverkit.Overloaded},
	{Substr: "Overloaded", Reason: driverkit.Overloaded},
}

// TerminalPatterns holds markers for non-retryable harness argv rejections,
// such as an unknown flag (the harness passes effort as --variant).
// opencode's yargs .strict() fail handler prints the `run` usage dump in place
// of yargs' "Unknown argument" line and exits 1; that marker stays as a guard
// for an opencode that stops swallowing the line. The usage banner also covers
// yargs' sibling argv rejections, which no retry can fix.
// Read-only, like TransientPatterns.
var TerminalPatterns = []driverkit.Pattern{
	{Substr: "opencode run [message..]", Reason: driverkit.UnsupportedFlag},
	{Substr: "Unknown argument", Reason: driverkit.UnsupportedFlag},
}

package main

import "spindrift.dev/sdk/driver"

// TransientPatterns is pi's complete ordered marker list, checked before the
// shared base network suffix the host appends. pi writes a failed
// turn's errorMessage as "<status>: <body>", so the status markers carry the
// ": " to keep a bare number such as a token count from matching (issue #4423).
// The set follows the failures pi's own auto-retry treats as retryable. 400
// and 401 stay terminal. Treat it as read-only.
var TransientPatterns = []driver.Pattern{
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

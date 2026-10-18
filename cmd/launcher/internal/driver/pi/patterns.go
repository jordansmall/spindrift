package pi

import "spindrift.dev/launcher/internal/driver/driverkit"

// TransientPatterns is pi's complete ordered marker list, checked before the
// shared driverkit.BaseTransientPatterns network suffix. pi writes a failed
// turn's errorMessage as "<status>: <body>", so the status markers carry the
// ": " to keep a bare number such as a token count from matching (issue #4423).
// The set follows the failures pi's own auto-retry treats as retryable. 400
// and 401 stay terminal. Exported only for Strategy; treat it as read-only.
var TransientPatterns = []driverkit.Pattern{
	{Substr: "rate_limit_error", Reason: driverkit.RateLimit},
	{Substr: "429: ", Reason: driverkit.RateLimit},
	{Substr: "Too Many Requests", Reason: driverkit.RateLimit},
	{Substr: "overloaded_error", Reason: driverkit.Overloaded},
	{Substr: "529: ", Reason: driverkit.Overloaded},
	{Substr: "503: ", Reason: driverkit.Overloaded},
	{Substr: "Overloaded", Reason: driverkit.Overloaded},
	{Substr: "Service Unavailable", Reason: driverkit.Overloaded},
	{Substr: "500: ", Reason: driverkit.Network},
	{Substr: "502: ", Reason: driverkit.Network},
	{Substr: "504: ", Reason: driverkit.Network},
	{Substr: "fetch failed", Reason: driverkit.Network},
	{Substr: "ECONNRESET", Reason: driverkit.Network},
	{Substr: "ETIMEDOUT", Reason: driverkit.Network},
	{Substr: "ENOTFOUND", Reason: driverkit.Network},
	{Substr: "EAI_AGAIN", Reason: driverkit.Network},
	{Substr: "socket hang up", Reason: driverkit.Network},
	{Substr: "other side closed", Reason: driverkit.Network},
	{Substr: "Connection error", Reason: driverkit.Network},
}

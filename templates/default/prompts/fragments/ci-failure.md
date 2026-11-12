# CI FAILURE

The launcher captured this from the failing PR checks. It names each failing
check with its conclusion or state and its summary or status description, then
a capped tail of the failing job's log where the forge exposes one; a notice
marks a tail that was trimmed. Work from that log tail — it is the failure.
A check with no excerpt only points at what failed, so reproduce it locally
by running the check itself. Run no forge CLI or API call to read more of CI.

${CI_FAILURE_SUMMARY}


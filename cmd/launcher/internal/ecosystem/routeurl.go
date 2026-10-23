package ecosystem

import "strconv"

// RouteLocalURL renders a route's own local Forwarder URL from prefix, the
// route's own Prefix field. The proxy listens on one port for every route,
// but each route answers only its own prefix-scoped path (issue #3142), so
// the rewrite target has to carry that prefix too.
func RouteLocalURL(prefix string, port int) string {
	return "http://127.0.0.1:" + strconv.Itoa(port) + "/" + prefix
}

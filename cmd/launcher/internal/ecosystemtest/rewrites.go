package ecosystemtest

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"spindrift.dev/launcher/internal/bindregistry"
	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/registryproxy"
	"spindrift.dev/launcher/internal/registryvocab"
)

// exchange is one request through the proxy and the response it returned.
type exchange struct {
	Request string `json:"request"`
	Status  int    `json:"status"`
	Body    string `json:"body"`
}

// rewrites serves each upstream/<match-host>/ tree from a local server the
// projected route points at, then sends GET /<prefix>/<path> through the
// proxy for every file in it, so the golden holds what a client would receive
// after the row's RewriteRows ran. A case with no upstream/ directory yields an
// empty list.
func (c *replay) rewrites() ([]byte, error) {
	exchanges := []exchange{}
	root := filepath.Join(c.dir, "upstream")
	hosts, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return json.Marshal(exchanges)
	}
	if err != nil {
		return nil, err
	}

	proxied, err := c.proxyRoutes()
	if err != nil {
		return nil, err
	}
	routes := slices.Clone(proxied)
	// Both maps are keyed by registryvocab.HostKey of the match-host, the
	// comparison the proxy itself makes.
	upstreamAddrs := map[string]string{} // the local server's host:port
	hostDirs := map[string]string{}      // the upstream/ subdirectory serving it
	for _, h := range hosts {
		i := slices.IndexFunc(routes, func(r registryproxy.Route) bool {
			return registryvocab.HostKey(r.MatchHost) == registryvocab.HostKey(h.Name())
		})
		if i < 0 {
			return nil, fmt.Errorf("upstream/%s names no route's match-host", h.Name())
		}
		if !h.IsDir() {
			return nil, fmt.Errorf("upstream/%s is not a directory", h.Name())
		}
		srv := httptest.NewServer(http.FileServer(http.Dir(filepath.Join(root, h.Name()))))
		defer srv.Close()
		routes[i].Upstream = srv.URL
		key := registryvocab.HostKey(routes[i].MatchHost)
		upstreamAddrs[key] = strings.TrimPrefix(srv.URL, "http://")
		hostDirs[key] = h.Name()
	}

	proxy, err := registryproxy.New(routes, c.row.RewriteRows)
	if err != nil {
		return nil, err
	}
	for _, r := range routes {
		key := registryvocab.HostKey(r.MatchHost)
		dir, ok := hostDirs[key]
		if !ok {
			continue
		}
		hostRoot := filepath.Join(root, dir)
		localURL, err := url.Parse(ecosystem.RouteLocalURL(r.Prefix, bindregistry.ForwarderPort))
		if err != nil {
			return nil, err
		}
		err = filepath.WalkDir(hostRoot, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			rel, err := filepath.Rel(hostRoot, p)
			if err != nil {
				return err
			}
			// The Forwarder address is whatever Host the client used; pinning it
			// keeps the rewritten URLs stable.
			req := httptest.NewRequest(http.MethodGet, "/"+r.Prefix+"/"+filepath.ToSlash(rel), nil)
			req.Host = localURL.Host
			rec := httptest.NewRecorder()
			proxy.ServeHTTP(rec, req)
			exchanges = append(exchanges, exchange{
				Request: req.Method + " " + req.URL.Path,
				Status:  rec.Code,
				// The upstream's port is random per run.
				Body: strings.ReplaceAll(rec.Body.String(), upstreamAddrs[key], "UPSTREAM"),
			})
			return nil
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	slices.SortFunc(exchanges, func(a, b exchange) int { return strings.Compare(a.Request, b.Request) })
	return json.Marshal(exchanges)
}

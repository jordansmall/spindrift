// Package synthetic is the ecosystemtest kit's test-only row. It is a leaf
// package so tests inside registrydiscover and registrypathset, which the kit
// itself imports, can use the row without an import cycle.
package synthetic

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"path"
	"strings"

	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/registrymanifest"
	"spindrift.dev/launcher/internal/registryvocab"
)

// Row is a test-only row exercising each stage's generic behaviour
// without naming a real ecosystem. It reads `name = <url>` lines from
// .synthetic/registries, binds through an env export, a home config and an
// in-tree rewrite of that file, and rewrites the "download" URL of an
// index.json response.
var Row = ecosystem.Row{
	Name:             "synthetic",
	LockfileNames:    []string{"synthetic.lock"},
	Classification:   "synthetic",
	InTreeConfigPath: ".synthetic/registries",
	ConfigParser:     parseSynthetic,
	RouteDeclaration: validateSyntheticRouteDeclaration,
	CredentialStores: []string{"netrc"},
	EnvExports: func(port int, prefix string, _ func(string) string, _ []registrymanifest.Route) ([]ecosystem.EnvExport, []string) {
		return []ecosystem.EnvExport{{Name: "SYNTHETIC_REGISTRY", Value: ecosystem.RouteLocalURL(prefix, port)}}, nil
	},
	HomeConfig: &ecosystem.HomeConfig{
		HomeEnvVar:          "SYNTHETIC_HOME",
		HomeRelativeDefault: ".synthetic",
		ConfigPath:          "config",
		Render: func(port int, prefix string, _ []registrymanifest.Route) string {
			return "registry = " + ecosystem.RouteLocalURL(prefix, port) + "\n"
		},
	},
	RewriteRows: []registryvocab.RewriteRow{{
		Name:      "synthetic index.json",
		Ecosystem: "synthetic",
		Method:    http.MethodGet,
		Matches: func(routeRelativePath, base string) bool {
			return routeRelativePath == registryvocab.JoinBase(base, "/index.json")
		},
		Rewrite: rewriteSyntheticDownload,
	}},
}

// Twin is Row under another name and config path, for tests that need a second
// row to declare beside the first.
func Twin(name string) ecosystem.Row {
	r := Row
	r.Name = name
	r.InTreeConfigPath = "." + name + "/registries"
	return r
}

func parseSynthetic(content string) (decls []ecosystem.Declaration, namedAny bool, err error) {
	for _, line := range strings.Split(content, "\n") {
		name, raw, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		namedAny = true
		u, perr := url.Parse(strings.TrimSpace(raw))
		if perr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
			continue
		}
		decls = append(decls, ecosystem.Declaration{
			Host:            u.Host,
			UpstreamBaseURL: strings.TrimRight(u.String(), "/"),
			RegistryName:    strings.TrimSpace(name),
		})
	}
	return decls, namedAny, nil
}

func validateSyntheticRouteDeclaration(key string, value any) error {
	if key != "mirror-path" {
		return errors.New("is not a key the synthetic route declaration accepts")
	}
	if s, ok := value.(string); !ok || !strings.HasPrefix(s, "/") {
		return errors.New("must be an absolute path")
	}
	return nil
}

// rewriteSyntheticDownload repoints a "download" URL naming the route's
// match-host at the Forwarder, re-inserting the route prefix; a URL naming any
// other host is left alone.
func rewriteSyntheticDownload(body []byte, rc registryvocab.RewriteContext) registryvocab.RewriteResult {
	unchanged := registryvocab.RewriteResult{Body: body, Outcome: registryvocab.RewriteNone}
	var obj map[string]any
	if json.Unmarshal(body, &obj) != nil {
		return unchanged
	}
	download, _ := obj["download"].(string)
	for _, scheme := range []string{"https://", "http://"} {
		rest, ok := strings.CutPrefix(download, scheme+rc.MatchHost+"/")
		if !ok {
			continue
		}
		to := rc.Forwarder.Scheme + "://" + rc.Forwarder.Host + "/" + rc.Prefix + "/" + rest
		obj["download"] = to
		rewritten, err := json.Marshal(obj)
		if err != nil {
			return unchanged
		}
		return registryvocab.RewriteResult{
			Body:    rewritten,
			Edits:   []registryvocab.RewriteEdit{{From: download, To: to, LearnedPath: path.Dir("/" + rest)}},
			Outcome: registryvocab.RewriteApplied,
		}
	}
	return unchanged
}

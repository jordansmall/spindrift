package ecosystemtest

import (
	"errors"
	"net/url"
	"strings"

	"spindrift.dev/launcher/internal/ecosystem"
)

// Synthetic is a test-only row exercising each stage's generic behaviour
// without naming a real ecosystem. It reads `name = <url>` lines from
// .synthetic/registries. Real rows migrate onto this kit later (issue #5600).
var Synthetic = ecosystem.Row{
	Name:             "synthetic",
	InTreeConfigPath: ".synthetic/registries",
	ConfigParser:     parseSynthetic,
	RouteDeclaration: validateSyntheticRouteDeclaration,
	CredentialStores: []string{"netrc"},
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

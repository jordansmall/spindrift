// Package profileprobe fills a silent registry probe's auth scheme from the
// matching Host profile. It is its own package because registrydiscover sits
// in route resolution's import closure via registrypathset, so ADR 0071's
// advisory boundary keeps the profile table out of it.
package profileprobe

import (
	"spindrift.dev/launcher/internal/hostprofile"
	"spindrift.dev/launcher/internal/registrydiscover"
)

// Fill wraps probe so a response with no usable challenge takes its scheme
// from the matching Host profile. A scheme the registry announced always wins
// (ADR 0071 decision 2), and a public profile, which has no scheme, fills
// nothing.
func Fill(probe registrydiscover.Probe) registrydiscover.Probe {
	return func(upstreamBaseURL string) registrydiscover.ProbeResult {
		res := probe(upstreamBaseURL)
		if res.Scheme != "" {
			return res
		}
		if row, ok := hostprofile.Match(upstreamBaseURL, res.Header); ok && row.AuthScheme != "" {
			res.Scheme, res.Profile = row.AuthScheme, row.Name
		}
		return res
	}
}

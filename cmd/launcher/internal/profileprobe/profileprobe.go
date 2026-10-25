// Package profileprobe fills a silent registry probe's auth scheme from the
// matching Host profile. It is its own package because registrydiscover sits
// in route resolution's import closure via registrypathset, so ADR 0071's
// advisory boundary keeps the profile table out of it.
package profileprobe

import (
	"spindrift.dev/launcher/internal/hostprofile"
	"spindrift.dev/launcher/internal/registrydiscover"
)

// Fill wraps probe so the matching Host profile's advice reaches discovery. A
// response with no usable challenge also takes its scheme from the profile. A
// scheme the registry announced always wins (ADR 0071 decision 2), and a
// public profile, which has no scheme, fills none.
func Fill(probe registrydiscover.Probe) registrydiscover.Probe {
	return func(upstreamBaseURL string) registrydiscover.ProbeResult {
		res := probe(upstreamBaseURL)
		row, ok := hostprofile.Match(upstreamBaseURL, res.Header)
		if !ok {
			return res
		}
		res.Profile = registrydiscover.ProfileAdvice{
			Name:             row.Name,
			Note:             row.Note,
			BasicUsername:    row.BasicUsername,
			CredentialHelper: row.CredentialHelper,
			Siblings:         row.Siblings,
		}
		if res.Scheme == "" && row.AuthScheme != "" {
			res.Scheme, res.SchemeFromProfile = row.AuthScheme, true
		}
		return res
	}
}

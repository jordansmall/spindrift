package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"

	"spindrift.dev/launcher/internal/driver/claude"
)

// dispatchStamp is the deployment facts every Pass log's dispatch_start
// carries (issue #4783). Knobs holds the effective value of every non-secret
// schema knob, with any URL userinfo stripped; secretKnobs never enter
// schemaFlags, and the explicit skip keeps that true if the generator ever
// changes. reviewModel is the dispatch-time REVIEW_MODEL, the same value
// dispatchConfig hands ReviewModelOverride.
func dispatchStamp(reviewModel string) claude.DispatchStart {
	secret := make(map[string]bool, len(secretKnobs))
	for _, s := range secretKnobs {
		secret[s.env] = true
	}
	knobs := make(map[string]string, len(schemaFlags))
	for _, e := range schemaFlags {
		if !secret[e.env] {
			knobs[e.env] = redactUserinfo(getenvSchema(e.env))
		}
	}
	return claude.DispatchStart{
		Revision:      revision,
		RoleModels:    stampRoleModels(reviewModel),
		DriverVersion: docArtifact("DRIVER_VERSION"),
		Knobs:         knobs,
	}
}

// redactUserinfo strips the userinfo of a URL-shaped value entirely rather than
// using Redacted(), which keeps a bare username -- and in https://<token>@host
// that username is the credential. .spindrift/logs/ is uploaded as an Actions
// artifact, so a non-secret knob like CODE_FORGE_REMOTE_URL must not carry it.
func redactUserinfo(v string) string {
	if u, err := url.Parse(v); err == nil && u.User != nil {
		u.User = nil
		return u.String()
	}
	return v
}

// stampRoleModels starts from the baked roster (ROLE_MODELS) and overlays the
// two models that are runtime switches: the main model comes from the
// effective MODEL knob, and reviewModel replaces the reviewer, mirroring
// ReviewModelOverride.
func stampRoleModels(reviewModel string) map[string]string {
	models := map[string]string{}
	if raw := docArtifact("ROLE_MODELS"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &models); err != nil || models == nil {
			if err != nil {
				fmt.Fprintf(os.Stderr, "==> ROLE_MODELS artifact is not valid JSON (%v) -- dispatch_start omits the baked roster\n", err)
			}
			models = map[string]string{}
		}
	}
	if m := getenvSchema("MODEL"); m != "" {
		models["main"] = m
	}
	if reviewModel != "" {
		models["reviewer"] = reviewModel
	}
	return models
}

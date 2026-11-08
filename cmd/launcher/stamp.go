package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"

	"spindrift.dev/launcher/internal/hostop"
	"spindrift.dev/launcher/internal/inputdoc"
)

// dispatchStamp is the deployment facts every Pass log's dispatch_start
// carries (issue #4783). Knobs holds the effective value of every non-secret
// schema knob, with any URL userinfo stripped; inputdoc.SecretKnobs never enter
// inputdoc.SchemaFlags, and the explicit skip keeps that true if the generator ever
// changes. reviewModel is the dispatch-time REVIEW_MODEL, the same value
// dispatchConfig hands ReviewModelOverride.
func dispatchStamp(reviewModel string) hostop.DispatchStart {
	secret := make(map[string]bool, len(inputdoc.SecretKnobs))
	for _, s := range inputdoc.SecretKnobs {
		secret[s.Env] = true
	}
	knobs := make(map[string]string, len(inputdoc.SchemaFlags))
	for _, e := range inputdoc.SchemaFlags {
		if !secret[e.Env] {
			knobs[e.Env] = redactUserinfo(getenvSchema(e.Env))
		}
	}
	return hostop.DispatchStart{
		Revision:      revision,
		RoleModels:    stampRoleModels(reviewModel),
		DriverVersion: docArtifacts().Shared.DriverVersion.String(),
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
	if raw := docArtifacts().Shared.RoleModels.String(); raw != "" {
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

package main

import (
	"context"
	"encoding/base64"
	"errors"
	"sort"
	"strings"
	"unicode/utf8"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Resolve only public identity policy from the exact planned Secret/ConfigMap
// owners. Credential values remain references and never become probe arguments.
// Kubernetes envFrom ordering and explicit-env precedence are preserved.
func (b environmentKubernetesBackend) resolveIdentitySettings(ctx context.Context, plan *deploymentplan.Plan, object, container deploymentplan.Object) (deploymentplan.Object, func(context.Context) error, error) {
	namespace := deploymentplan.String(deploymentplan.Map(object["metadata"])["namespace"])
	type observation struct {
		resource deploymentplan.Resource
		object   deploymentplan.Object
	}
	sources := map[string]observation{}
	settingFailure := func(id, cause string) error {
		return &deploymentplan.Blocker{ID: "deployment.pki.settings", Resource: id, Cause: cause, Next: "restore the planned original Secret/ConfigMap owner and public setting keys through managed bindings; preserve credential references; changes to identity policy need a reviewed PKI migration or original-state candidate qualification, then repeat complete preflight"}
	}

	read := func(kind, name string) (deploymentplan.Object, error) {
		id := kind + "/" + namespace + "/" + name
		if observed, ok := sources[id]; ok {
			return observed.object, nil
		}
		for _, resource := range plan.Resources {
			if resource.ID != id {
				continue
			}
			if resource.UID == "" || resource.Action == "create" || resource.Action == "retire" {
				return nil, settingFailure(id, "original public identity settings owner is unavailable")
			}
			current, err := b.Read(ctx, id)
			if err != nil || current == nil || deploymentplan.String(deploymentplan.Map(current["metadata"])["resourceVersion"]) == "" {
				return nil, settingFailure(id, "original public identity settings cannot be inspected")
			}
			if _, err := resource.Body(current); err != nil {
				return nil, settingFailure(id, "public identity settings drifted after planning")
			}
			sources[id] = observation{resource, current}
			return current, nil
		}
		return nil, settingFailure(id, "public identity settings require a planned original owner")
	}
	decode := func(kind string, source deploymentplan.Object, key string) (string, error) {
		text, ok := deploymentplan.Map(source["data"])[key].(string)
		if !ok {
			return "", errors.New("public identity setting is absent")
		}
		if kind == "Secret" {
			raw, err := base64.StdEncoding.Strict().DecodeString(text)
			if err != nil {
				return "", errors.New("public identity setting encoding is invalid")
			}
			text = string(raw)
		}
		if !utf8.ValidString(text) || len(text) > 64<<10 {
			return "", errors.New("bounded public identity setting required")
		}
		return text, nil
	}
	value := func(kind string, source deploymentplan.Object, key string) (string, error) {
		current, err := decode(kind, source, key)
		if err != nil {
			return "", settingFailure(deploymentplan.ID(source), "required public identity setting key or encoding is unavailable")
		}
		wanted, err := decode(kind, sources[deploymentplan.ID(source)].resource.Desired, key)
		if err != nil || current != wanted {
			return "", settingFailure(deploymentplan.ID(source), "target public identity setting requires separate migration/candidate qualification")
		}
		return wanted, nil
	}
	explicit, err := environmentPKINamedFields(container["env"])
	if err != nil {
		return nil, nil, err
	}
	settings := map[string]deploymentplan.Object{}
	for name, setting := range explicit {
		settings[name] = setting
	}
	inherited := certIssuerObjectList(container["envFrom"])
	for index := len(inherited) - 1; index >= 0; index-- {
		from := deploymentplan.Map(inherited[index])
		kind, ref := "ConfigMap", deploymentplan.Map(from["configMapRef"])
		if from["secretRef"] != nil {
			if ref != nil {
				return nil, nil, errors.New("ambiguous public identity settings source")
			}
			kind, ref = "Secret", deploymentplan.Map(from["secretRef"])
		}
		source, err := read(kind, deploymentplan.String(ref["name"]))
		if err != nil {
			return nil, nil, err
		}
		keys := map[string]bool{}
		for key := range deploymentplan.Map(source["data"]) {
			keys[key] = true
		}
		for key := range deploymentplan.Map(sources[deploymentplan.ID(source)].resource.Desired["data"]) {
			keys[key] = true
		}
		for key := range keys {
			name := deploymentplan.String(from["prefix"]) + key
			if !publicIdentitySetting(name) || settings[name] != nil {
				continue
			}
			text, err := value(kind, source, key)
			if err != nil {
				return nil, nil, err
			}
			settings[name] = deploymentplan.Object{"name": name, "value": text}
		}
	}
	for name, setting := range explicit {
		if !publicIdentitySetting(name) || setting["valueFrom"] == nil {
			settings[name] = setting
			continue
		}
		from := deploymentplan.Map(setting["valueFrom"])
		kind, ref := "ConfigMap", deploymentplan.Map(from["configMapKeyRef"])
		if from["secretKeyRef"] != nil {
			kind, ref = "Secret", deploymentplan.Map(from["secretKeyRef"])
		}
		if len(from) != 1 || ref == nil {
			return nil, nil, errors.New("public identity setting needs an explicit source key")
		}
		source, err := read(kind, deploymentplan.String(ref["name"]))
		if err != nil {
			return nil, nil, err
		}
		text, err := value(kind, source, deploymentplan.String(ref["key"]))
		if err != nil {
			return nil, nil, err
		}
		settings[name] = deploymentplan.Object{"name": name, "value": text}
	}
	resolved := deploymentplan.Clone(container)
	keys := make([]string, 0, len(settings))
	for key := range settings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	resolved["env"] = []any{}
	for _, key := range keys {
		resolved["env"] = append(resolved["env"].([]any), settings[key])
	}
	return resolved, func(ctx context.Context) error {
		for id, observed := range sources {
			current, err := b.Read(ctx, id)
			if err != nil || current == nil || deploymentplan.Map(current["metadata"])["resourceVersion"] != deploymentplan.Map(observed.object["metadata"])["resourceVersion"] {
				return settingFailure(id, "public identity settings changed during inspection")
			}
			if _, err := observed.resource.Body(current); err != nil {
				return settingFailure(id, "public identity settings owner changed during inspection")
			}
		}
		return nil
	}, nil
}

func publicIdentitySetting(name string) bool {
	return name == "VIDEO_CLOUD_AUTH_PRODUCT_PKI_ENABLED" || name == "VIDEO_CLOUD_AUTH_APP_PKI_ENABLED" || strings.HasSuffix(name, "_CRL_MANIFEST") || strings.HasSuffix(name, "_IDENTITY_STATE") || strings.HasSuffix(name, "_ROOT_SHA256") || strings.HasSuffix(name, "_HOST_NAME") || strings.HasSuffix(name, "_HOST_DNS_NAMES")
}

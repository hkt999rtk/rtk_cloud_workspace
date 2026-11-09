package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
)

// A runtime export must resolve to an explicit native writer in this same Pod,
// sharing the exact file and backed by its original durable CRL state.
// The maintenance command never writes the exported PEM itself.
func (r *maintenanceRuntime) runtimePublicProjection(ctx context.Context, pod deploymentplan.Object, out maintenancePublicCRL, mount, volume deploymentplan.Object, known map[string]environmentCreateRegisteredIssuer) (maintenancePublicCRL, error) {
	if volume["emptyDir"] == nil && volume["persistentVolumeClaim"] == nil {
		return out, errors.New("public CRL output is not a native shared volume")
	}
	relative := strings.TrimPrefix(out.Path, deploymentplan.String(mount["mountPath"])+"/")
	spec := deploymentplan.Map(pod["spec"])
	var typed liveDeployment
	adapted, _ := json.Marshal(deploymentplan.Object{"metadata": pod["metadata"], "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": spec}}})
	if json.Unmarshal(adapted, &typed) != nil {
		return out, errors.New("public CRL producer mounts invalid")
	}
	out.Claims = map[string]string{}
	pinVolume := func(v deploymentplan.Object) error {
		claim := deploymentplan.String(deploymentplan.Map(v["persistentVolumeClaim"])["claimName"])
		if claim == "" {
			return errors.New("native public CRL state lacks original PVC")
		}
		id := "PersistentVolumeClaim/" + out.Namespace + "/" + claim
		object, err := r.read(ctx, id)
		uid := deploymentplan.String(deploymentplan.Map(object["metadata"])["uid"])
		if err != nil || uid == "" {
			return errors.New("native public CRL PVC unavailable")
		}
		out.Claims[id] = uid
		return nil
	}
	if volume["persistentVolumeClaim"] != nil {
		if err := pinVolume(volume); err != nil {
			return out, err
		}
	}
	for _, cv := range certIssuerObjectList(spec["containers"]) {
		writer := deploymentplan.Map(cv)
		for _, ev := range certIssuerObjectList(writer["env"]) {
			e := deploymentplan.Map(ev)
			setting, path := deploymentplan.String(e["name"]), deploymentplan.String(e["value"])
			if !strings.HasSuffix(setting, "_PUBLIC_CRL_BUNDLE") || e["valueFrom"] != nil || !filepath.IsAbs(path) || filepath.Clean(path) != path {
				continue
			}
			matches := false
			for _, mv := range certIssuerObjectList(writer["volumeMounts"]) {
				m := deploymentplan.Map(mv)
				matches = matches || m["name"] == mount["name"] && m["readOnly"] != true && m["subPath"] == nil && m["subPathExpr"] == nil && path == deploymentplan.String(m["mountPath"])+"/"+relative
			}
			if !matches {
				continue
			}
			if out.Producer != "" {
				return out, errors.New("public CRL output has ambiguous native writers")
			}
			out.Producer = deploymentplan.String(writer["name"])
			remote := mqttFoundationEnv(writer, "PKI_STATE_BACKEND") == "openbao"
			manifestPath := mqttFoundationEnv(writer, strings.TrimSuffix(setting, "_PUBLIC_CRL_BUNDLE")+"_SERVER_CRL_MANIFEST")
			cm, key, err := mountedPKICRLConfigMap(typed, out.Producer, manifestPath)
			if err != nil {
				return out, err
			}
			source, err := r.read(ctx, "ConfigMap/"+out.Namespace+"/"+cm)
			if err != nil {
				return out, err
			}
			var entries []livePKICRLManifestEntry
			if json.Unmarshal([]byte(deploymentplan.String(deploymentplan.Map(source["data"])[key])), &entries) != nil || len(entries) == 0 {
				return out, errors.New("native public CRL manifest missing")
			}
			issuerIDs, statePaths := map[string]bool{}, map[string]bool{}
			for _, entry := range entries {
				if issuerIDs[entry.Issuer.ID] || statePaths[entry.StatePath] {
					return out, errors.New("native public CRL manifest has duplicate issuer or state purposes")
				}
				issuerIDs[entry.Issuer.ID], statePaths[entry.StatePath] = true, true
				issuer, ok := known[entry.Issuer.ID]
				if !ok || entry.Issuer.Environment != r.cfg.Environment || entry.Issuer.Certificate != issuer.Certificate || entry.Issuer.Fingerprint != deploymentplan.String(issuer.Document["certificate_fingerprint_sha256"]) || !livePKIStateOnStorage(typed, out.Producer, entry.StatePath) {
					return out, errors.New("native public CRL authority or durable state changed")
				}
				out.Issuers = append(out.Issuers, entry.Issuer.ID)
				if remote {
					consumerID, err := r.custodyConsumerID(ctx, out.Namespace, out.Pod, out.Producer, writer, entry.StatePath)
					if err != nil {
						return out, err
					}
					out.CustodyCaches = append(out.CustodyCaches, maintenancePublicCRLCache{
						Consumer: crlMaintenanceConsumer{Namespace: out.Namespace, Pod: out.Pod, PodUID: out.PodUID, Container: out.Producer, StatePath: entry.StatePath, IssuerID: issuer.ID, ConsumerID: consumerID, Custody: true},
						Issuer:   issuer,
					})
					continue
				}
				for _, mv := range certIssuerObjectList(writer["volumeMounts"]) {
					m := deploymentplan.Map(mv)
					if !strings.HasPrefix(entry.StatePath, deploymentplan.String(m["mountPath"])+"/") {
						continue
					}
					if m["subPath"] != nil || m["subPathExpr"] != nil || m["readOnly"] == true {
						return out, errors.New("native public CRL state mount cannot refresh")
					}
					for _, vv := range certIssuerObjectList(spec["volumes"]) {
						v := deploymentplan.Map(vv)
						if v["name"] == m["name"] {
							if err := pinVolume(v); err != nil {
								return out, err
							}
						}
					}
				}
			}
		}
	}
	if out.Producer == "" || len(out.Claims) == 0 && len(out.CustodyCaches) == 0 || out.PodUID == "" {
		return out, errors.New("public CRL shared output lacks a bound native writer")
	}
	// A bound remote producer may be down while its provider transport is
	// being repaired. Discovery records original purpose/authority, not a fake
	// installed bundle. Actual committed cache and reload are checked at convergence.
	if len(out.CustodyCaches) != 0 {
		return out, nil
	}
	raw, err := r.command(ctx, nil, "--namespace", out.Namespace, "exec", "pod/"+out.Pod, "--container", out.Container, "--", "cat", out.Path)
	if err != nil {
		return out, errors.New("native public CRL export unavailable")
	}
	ids, err := maintenanceBundleIssuers(raw, known)
	if err != nil || len(ids) != len(out.Issuers) {
		return out, errors.New("native public CRL export differs from producer manifest")
	}
	for i := range ids {
		if ids[i] != out.Issuers[i] {
			return out, errors.New("native public CRL issuer order differs from producer manifest")
		}
	}
	out.Original = string(raw)
	return out, nil
}

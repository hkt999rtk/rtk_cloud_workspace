package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Kubernetes copies a Job name into a 63-byte label. Keep the existing owner
// and session binding while shortening only the newly generated Job key.
func environmentCreateApplicationCRLJobKey(owner string, batch int) string {
	key := fmt.Sprintf("%s-crl-%d", owner, batch)
	const maximum = 39 // pki-enroll- + '-' + 12-byte session consume 24 bytes.
	if len(key) <= maximum {
		return key
	}
	return strings.TrimRight(key[:maximum-13], "-") + "-" + deploymentplan.Digest(key)[:12]
}

// The caller provides the actual rendered owner and ConfigMap definitions and
// the original registered issuers. No live object or private key is copied.
func renderEnvironmentCreateApplicationCRLInitialization(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, runtime environmentCreateBootstrapRuntime, s environmentCreateServiceCeremony, definitions []deploymentplan.Definition, issuers []environmentCreateRegisteredIssuer) ([]deploymentplan.Definition, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cfg.Values["PKI_STATE_BACKEND"] != "openbao" {
		return nil, nil
	}
	if err := validateEnvironmentCreateServiceCeremony(cfg, p, runtime, s); err != nil {
		return nil, err
	}
	bad := func() error {
		return environmentCreateBootstrapBlocker("application public CRL initialization requires unambiguous rendered manifests and original active or retiring issuers")
	}
	allowed := map[string]environmentCreateRegisteredIssuer{}
	for _, issuer := range issuers {
		if issuer.Environment != cfg.Environment || (issuer.Status != "active" && issuer.Status != "retiring") || !environmentBootstrapOwnerUUID.MatchString(issuer.ID) {
			return nil, bad()
		}
		fp := deploymentplan.String(issuer.Document["certificate_fingerprint_sha256"])
		if !environmentPreflightDigest.MatchString(fp) {
			return nil, bad()
		}
		if _, ok := allowed[issuer.ID]; ok {
			return nil, bad()
		}
		allowed[issuer.ID] = issuer
	}
	namespace := p.Stack + "-video-cloud"
	configs := map[string]deploymentplan.Object{}
	owners := map[string]environmentCreateApplicationOwner{}
	for _, owner := range environmentCreateApplicationOwners() {
		owners[owner.Name] = owner
	}
	// Product registrars own independent KV scopes too. Their permanent keys
	// are already enrolled; initialize only public state from rendered manifests.
	for _, product := range p.ProductIdentities {
		role := strings.TrimPrefix(product.Subject, "service:")
		name := environmentCustodyProductWorkload(role)
		if name == "" {
			return nil, bad()
		}
		if _, exists := owners[name]; exists {
			continue
		}
		state := environmentCreateServiceOwnerIdentityPath(role, "client.json")
		owners[name] = environmentCreateApplicationOwner{Name: name, Container: strings.TrimPrefix(name, "video-cloud-"), Role: role, Claim: product.Claim, StateRoot: environmentCreateServiceState, ClientState: state}
	}
	for _, d := range definitions {
		o := d.Object
		meta := deploymentplan.Map(o["metadata"])
		if o["kind"] != "ConfigMap" || meta["namespace"] != namespace {
			continue
		}
		name := deploymentplan.String(meta["name"])
		data := deploymentplan.Map(o["data"])
		if old, ok := configs[name]; ok && deploymentplan.Digest(old) != deploymentplan.Digest(data) {
			return nil, bad()
		}
		configs[name] = data
	}
	var jobs []deploymentplan.Definition
	seenOwner := map[string]bool{}
	for _, d := range definitions {
		o := d.Object
		meta := deploymentplan.Map(o["metadata"])
		if o["kind"] != "Deployment" || meta["namespace"] != namespace {
			continue
		}
		owner, ok := owners[deploymentplan.String(meta["name"])]
		if !ok {
			if pod := environmentPKIPod(o); pod != nil {
				for _, raw := range certIssuerObjectList(pod["containers"]) {
					for _, f := range certIssuerObjectList(deploymentplan.Map(raw)["env"]) {
						e := deploymentplan.Map(f)
						if strings.HasSuffix(deploymentplan.String(e["name"]), "_CRL_MANIFEST") && deploymentplan.String(e["value"]) != "" {
							return nil, bad()
						}
					}
				}
			}
			continue
		}
		if seenOwner[owner.Name] {
			return nil, bad()
		}
		seenOwner[owner.Name] = true
		pod := environmentPKIPod(o)
		if pod == nil {
			return nil, bad()
		}
		volumes := map[string]string{}
		for _, raw := range certIssuerObjectList(pod["volumes"]) {
			v := deploymentplan.Map(raw)
			if name := deploymentplan.String(deploymentplan.Map(v["configMap"])["name"]); name != "" {
				volumes[deploymentplan.String(v["name"])] = name
			}
		}
		type record struct{ path, id, fp string }
		var records []record
		paths := map[string]record{}
		for _, raw := range certIssuerObjectList(pod["containers"]) {
			c := deploymentplan.Map(raw)
			for _, field := range certIssuerObjectList(c["env"]) {
				e := deploymentplan.Map(field)
				if !strings.HasSuffix(deploymentplan.String(e["name"]), "_CRL_MANIFEST") {
					continue
				}
				path := deploymentplan.String(e["value"])
				if path == "" {
					if e["valueFrom"] != nil {
						return nil, bad()
					}
					continue
				}
				if !filepath.IsAbs(path) || filepath.Clean(path) != path {
					return nil, bad()
				}
				config, key := "", ""
				matches := 0
				for _, mraw := range certIssuerObjectList(c["volumeMounts"]) {
					m := deploymentplan.Map(mraw)
					mount := deploymentplan.String(m["mountPath"])
					name := volumes[deploymentplan.String(m["name"])]
					if name == "" || mount == "" {
						continue
					}
					sub := deploymentplan.String(m["subPath"])
					candidate := ""
					if sub != "" && path == mount {
						candidate = sub
					} else if sub == "" && strings.HasPrefix(path, strings.TrimSuffix(mount, "/")+"/") {
						candidate = strings.TrimPrefix(path, strings.TrimSuffix(mount, "/")+"/")
					}
					if candidate != "" && !strings.Contains(candidate, "/") {
						config, key = name, candidate
						matches++
					}
				}
				if matches != 1 {
					return nil, bad()
				}
				rawManifest := deploymentplan.String(configs[config][key])
				var entries []struct {
					Issuer deploymentplan.Object `json:"issuer"`
					State  string                `json:"state_path"`
				}
				if rawManifest == "" || json.Unmarshal([]byte(rawManifest), &entries) != nil || len(entries) == 0 {
					return nil, bad()
				}
				for _, entry := range entries {
					id := deploymentplan.String(entry.Issuer["issuer_id"])
					issuer, ok := allowed[id]
					fp := deploymentplan.String(entry.Issuer["certificate_fingerprint_sha256"])
					if !ok || entry.Issuer["environment"] != cfg.Environment || entry.Issuer["trust_domain"] != issuer.Domain || entry.Issuer["status"] != issuer.Status || fp != deploymentplan.String(issuer.Document["certificate_fingerprint_sha256"]) || !filepath.IsAbs(entry.State) || filepath.Clean(entry.State) != entry.State {
						return nil, bad()
					}
					rec := record{entry.State, id, fp}
					if old, ok := paths[entry.State]; ok {
						if old != rec {
							return nil, bad()
						}
						continue
					}
					paths[entry.State] = rec
					records = append(records, rec)
				}
			}
		}
		for i := 0; i < len(records); i += 4 {
			end := min(i+4, len(records))
			command := []any{"/app/serviceidentity-bootstrap", "initialize-public-crl"}
			for _, rec := range records[i:end] {
				command = append(command, rec.path, rec.id, rec.fp)
			}
			settings := map[string]string{"PKI_ENVIRONMENT": p.Environment, "PKI_CLIENT_IDENTITY_STATE": owner.ClientState}
			job := environmentCreateServiceEnrollmentJob(p, namespace, environmentCreateApplicationCRLJobKey(owner.Name, i/4), owner.Claim, s, command, settings, false)
			jp := environmentPKIPod(job.Object)
			// Use the selected environment's rendered workload identity; no UID or
			// GID is selected by the deployment script. OpenBao state is memory-backed.
			if security := deploymentplan.Map(pod["securityContext"]); security != nil {
				jp["securityContext"] = deploymentplan.Clone(security)
			} else {
				delete(jp, "securityContext")
			}
			delete(jp, "initContainers")
			sourceContainer := managedNamedField(pod["containers"], owner.Container)
			if sourceContainer == nil {
				return nil, bad()
			}
			targetContainer := managedNamedField(jp["containers"], "enroll")
			if security := deploymentplan.Map(sourceContainer["securityContext"]); security != nil {
				targetContainer["securityContext"] = deploymentplan.Clone(security)
			} else {
				delete(targetContainer, "securityContext")
			}
			for _, raw := range certIssuerObjectList(targetContainer["volumeMounts"]) {
				m := deploymentplan.Map(raw)
				if m["name"] == "service-state" {
					m["mountPath"] = owner.StateRoot
				}
			}
			jp["volumes"] = environmentCreateServiceWithoutNamed(jp["volumes"], "bootstrap", "service-trust")
			c := managedNamedField(jp["containers"], "enroll")
			c["volumeMounts"] = environmentCreateServiceWithoutNamed(c["volumeMounts"], "bootstrap", "service-trust")
			// Public cache projections are temporary and never include private keys.
			// The native command needs a writable directory even when it is outside
			// the owner's private identity mount and the root filesystem is read-only.
			dirs := map[string]bool{}
			for _, rec := range records[i:end] {
				dir := filepath.Dir(rec.path)
				if dir == owner.StateRoot || strings.HasPrefix(dir, strings.TrimSuffix(owner.StateRoot, "/")+"/") || dirs[dir] {
					continue
				}
				name := fmt.Sprintf("public-crl-%d", len(dirs))
				dirs[dir] = true
				jp["volumes"] = mergeEnvironmentNamedFields(jp["volumes"], []any{deploymentplan.Object{"name": name, "emptyDir": deploymentplan.Object{"medium": "Memory"}}})
				c["volumeMounts"] = mergeEnvironmentNamedFields(c["volumeMounts"], []any{environmentCreateServiceMount(name, dir, false)})
			}
			jobs = append(jobs, job)
		}
	}
	return jobs, nil
}

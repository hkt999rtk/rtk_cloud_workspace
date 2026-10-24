package main

import (
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"slices"
	"strings"
)

func deviceTrustForwardReview(key string) string {
	const prefix = "kubernetes/device-root-trust-policy-forward-"
	if !strings.HasPrefix(key, prefix) {
		return ""
	}
	review := strings.TrimPrefix(key, prefix)
	if !environmentPreflightDigest.MatchString(review) {
		return ""
	}
	return review
}

// Accept only the observed broker failure: four retained configuration owners,
// completed Controller, written broker, unwritten API; or a completed broker and written failed API. No uncertain write or
// other failure boundary can select this continuation.
func knownDeviceTrustForwardFailure(plan *deploymentplan.Plan, j deploymentplan.Journal, stack string) bool {
	if plan == nil || len(plan.Resources) != 7 || j.PlanSHA256 != plan.SHA256() || j.Accepted || j.AcceptanceStarted || j.InFlight != "" {
		return false
	}
	prefix := "Deployment/" + stack + "-video-cloud/"
	apiFailure := j.Failed == prefix+"video-cloud-api-pki"
	if !apiFailure && j.Failed != prefix+"mqtt-pki" {
		return false
	}
	completed, owners := 5, 6
	if apiFailure {
		completed, owners = 6, 7
	}
	if len(j.Completed) != completed || len(j.Owners) != owners {
		return false
	}
	expected := map[string]bool{}
	for _, id := range []string{"ConfigMap/pki-create-device-trust", "Secret/factoryenroll-runtime", "Secret/video-cloud-runtime", "Secret/video-cloud-workers-runtime", "Deployment/pki-controller"} {
		parts := strings.SplitN(id, "/", 2)
		expected[parts[0]+"/"+stack+"-video-cloud/"+parts[1]] = true
	}
	if apiFailure {
		expected[prefix+"mqtt-pki"] = true
	}
	done := map[string]bool{}
	for _, id := range j.Completed {
		if !expected[id] || done[id] || j.Owners[id] == "" {
			return false
		}
		done[id] = true
	}
	seen := map[string]bool{}
	for _, v := range plan.Resources {
		if seen[v.ID] || deploymentplan.ID(v.Desired) != v.ID {
			return false
		}
		seen[v.ID] = true
		switch {
		case expected[v.ID]:
			if !done[v.ID] || v.UID != j.Owners[v.ID] || v.Action != "retain" && v.Action != "update" {
				return false
			}
			if v.Phase == "configuration" && v.Action != "retain" {
				return false
			}
		case v.ID == j.Failed:
			if j.Owners[v.ID] == "" {
				return false
			}
			if apiFailure && v.Action == "create" {
				if v.UID != "" {
					return false
				}
			} else if v.Action != "update" || v.UID != j.Owners[v.ID] {
				return false
			}
		case !apiFailure && v.ID == prefix+"video-cloud-api-pki":
			if v.Action != "create" || j.Owners[v.ID] != "" {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func loadDeviceTrustPartialForwards(store secretStore, e *environmentCreateExecution, d *createDeviceTrustRecovery) error {
	bad := func() error {
		return createProviderRecoveryBlock("Device trust retry requires its admitted exact forward plan, original source prefix and native owned broker/API rollout failure")
	}
	for i, entry := range e.Journal.Entries {
		review := deviceTrustForwardReview(entry.Key)
		if review == "" {
			continue
		}
		if i == 0 || e.Journal.Entries[i-1].Key != "openbao-recovery-admission-"+review || entry.State != "pending" || len(entry.Receipt) != 0 {
			return bad()
		}
		a, before, err := loadAdmittedCreateRecovery(store, e, review)
		if err != nil || !isDeviceTrustCheckpoint(before) || len(before.Entries) != i-1 || deploymentplan.Digest(before.Entries) != deploymentplan.Digest(e.Journal.Entries[:i-1]) || a.Plan.Materials["accepted-service-core-device-trust-source"] != d.SourceReview {
			return bad()
		}

		var candidate deploymentplan.Plan
		base := filepath.Join(store.Root, "deployment/create/openbao-recovery", review)
		if readExecutionJSON(filepath.Join(base, "openbao-root-trust-plan.json"), &candidate) != nil || candidate.SHA256() != a.Plan.Materials["accepted-service-core-device-trust-forward-plan"] || candidate.Stack != e.Journal.Stack || candidate.Environment != e.Journal.Environment {
			return bad()
		}
		// Execution binds the same admitted bodies to its actual named stage.
		// Its stage binding differs from the preview's binding; every other field
		// and the parent-recorded intent must agree exactly.
		var plan *deploymentplan.Plan
		var j deploymentplan.Journal
		seen := map[string]bool{}
		for _, lease := range e.Journal.KubernetesLeases {
			sha := strings.SplitN(lease.Holder, ":", 2)[0]
			if !environmentPreflightDigest.MatchString(sha) || seen[sha] {
				continue
			}
			seen[sha] = true
			var observed deploymentplan.Plan
			var state deploymentplan.Journal
			if readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", sha, "plan.json"), &observed) != nil || observed.SHA256() != sha || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", sha, "journal.json"), &state) != nil || !knownDeviceTrustForwardFailure(&observed, state, e.Journal.Stack) {
				continue
			}
			intent, err := environmentCreateKubernetesStageIntent(&observed)
			intentSHA, hashErr := environmentCreatePlanSHA256(intent)
			if err != nil || hashErr != nil || intentSHA != entry.IntentSHA256 {
				continue
			}
			expected := candidate
			expected.Inputs.BindingsSHA256 = observed.BindingsSHA256
			if expected.SHA256() != observed.SHA256() || plan != nil {
				return bad()
			}
			plan, j = &observed, state
		}
		if plan == nil {
			return bad()
		}
		d.ForwardKeys = append(d.ForwardKeys, entry.Key)
		d.ForwardPlan, d.ForwardJournal = plan, j
	}
	return nil
}

// Reconstruct the old admitted intent exactly. Only this newly maintained
// public CA setting may be absent in the historical native recipe.
func restoreOriginalDeviceTrustCA(defs []deploymentplan.Definition, original *deploymentplan.Plan) {
	for _, v := range original.Resources {
		if v.ID == "Deployment/"+original.Stack+"-video-cloud/video-cloud-api-pki" {
			originalManifest := ""
			for _, value := range certIssuerObjectList(environmentPKIPod(v.Desired)["containers"]) {
				c := deploymentplan.Map(value)
				if c["name"] == "app" {
					originalManifest = mqttFoundationEnv(c, "VIDEO_CLOUD_AUTH_ISSUER_TRUST_MANIFEST")
				}
			}
			if originalManifest != "/run/pki-device/bundles.json" {
				continue
			}
			for _, d := range defs {
				if deploymentplan.ID(d.Object) == v.ID {
					pod := environmentPKIPod(d.Object)
					pod["volumes"] = slices.DeleteFunc(certIssuerObjectList(pod["volumes"]), func(v any) bool { return deploymentplan.Map(v)["name"] == "device-trust-manifest" })
					for _, value := range certIssuerObjectList(pod["containers"]) {
						c := deploymentplan.Map(value)
						if c["name"] != "app" {
							continue
						}
						c["volumeMounts"] = slices.DeleteFunc(certIssuerObjectList(c["volumeMounts"]), func(v any) bool { return deploymentplan.Map(v)["name"] == "device-trust-manifest" })
						c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{"VIDEO_CLOUD_AUTH_ISSUER_TRUST_MANIFEST": originalManifest}))
					}
				}
			}
		}
	}
	// The originally unwritten API's admitted template predates the maintained
	// TLS-reader group. Reconstruct that historical intent for comparison only;
	// the new candidate explicitly supplies the already documented reader group.
	for _, v := range original.Resources {
		if v.ID == "Deployment/"+original.Stack+"-video-cloud/video-cloud-api-pki" && environmentPKIPod(v.Desired)["securityContext"] != nil && deploymentplan.Map(environmentPKIPod(v.Desired)["securityContext"])["supplementalGroups"] == nil {
			for _, d := range defs {
				if deploymentplan.ID(d.Object) == v.ID {
					delete(deploymentplan.Map(environmentPKIPod(d.Object)["securityContext"]), "supplementalGroups")
				}
			}
		}
	}
	for _, v := range original.Resources {
		if v.ID != "Deployment/"+original.Stack+"-video-cloud/mqtt-pki" {
			continue
		}
		hadCA := false
		for _, value := range certIssuerObjectList(environmentPKIPod(v.Desired)["containers"]) {
			c := deploymentplan.Map(value)
			if c["name"] == "pkibroker" && mqttFoundationEnv(c, "PKI_BROKER_MANAGEMENT_CA") != "" {
				hadCA = true
			}
		}
		// The original recipe may predate this exact permission-only initializer.
		hadInit := false
		for _, value := range certIssuerObjectList(environmentPKIPod(v.Desired)["initContainers"]) {
			if deploymentplan.Map(value)["name"] == deviceBrokerTrustInitializer {
				hadInit = true
			}
		}
		if !hadInit {
			for _, d := range defs {
				if deploymentplan.ID(d.Object) == v.ID {
					pod := environmentPKIPod(d.Object)
					init := slices.DeleteFunc(certIssuerObjectList(pod["initContainers"]), func(value any) bool { return deploymentplan.Map(value)["name"] == deviceBrokerTrustInitializer })
					if len(init) == 0 && environmentPKIPod(v.Desired)["initContainers"] == nil {
						delete(pod, "initContainers")
					} else {
						pod["initContainers"] = init
					}
				}
			}
		}
		if hadCA {
			return
		}
		for _, d := range defs {
			if deploymentplan.ID(d.Object) != v.ID {
				continue
			}
			for _, value := range certIssuerObjectList(environmentPKIPod(d.Object)["containers"]) {
				c := deploymentplan.Map(value)
				if c["name"] != "pkibroker" {
					continue
				}
				env := certIssuerObjectList(c["env"])
				env = slices.DeleteFunc(env, func(v any) bool { return deploymentplan.Map(v)["name"] == "PKI_BROKER_MANAGEMENT_CA" })
				c["env"] = env
			}
		}
	}
}

const deviceBrokerTrustInitializer = "broker-trust-permissions"

// Prepare only the owner PVC root metadata used by native Root/CRL atomic
// writes. The existing managed Service identity is hash-checked, never changed.
func prepareDeviceBrokerTrustDirectory(defs []deploymentplan.Definition, image string) error {
	if !immutableEnvironmentImage.MatchString(image) {
		return environmentCreateBootstrapBlocker("broker trust permission preparation requires its pinned formal Alpine image")
	}
	for _, d := range defs {
		if d.Object["kind"] != "Deployment" || deploymentplan.Map(d.Object["metadata"])["name"] != "mqtt-pki" {
			continue
		}
		pod := environmentPKIPod(d.Object)
		volume := ""
		for _, value := range certIssuerObjectList(pod["containers"]) {
			c := deploymentplan.Map(value)
			if c["name"] != "pkibroker" {
				continue
			}
			if mqttFoundationEnv(c, "PKI_BROKER_IDENTITY_STATE") != environmentCreateBrokerIdentity {
				return environmentCreateBootstrapBlocker("broker trust permission preparation requires its original managed client state")
			}
			for _, value := range certIssuerObjectList(c["volumeMounts"]) {
				m := deploymentplan.Map(value)
				if m["mountPath"] == "/run/pki-state" && m["readOnly"] != true {
					if deploymentplan.String(m["subPath"]) != "" || deploymentplan.String(m["subPathExpr"]) != "" {
						return environmentCreateBootstrapBlocker("broker trust permission preparation requires the original full PVC root")
					}
					name := deploymentplan.String(m["name"])
					for _, value := range certIssuerObjectList(pod["volumes"]) {
						v := deploymentplan.Map(value)
						if v["name"] == name && deploymentplan.Map(v["persistentVolumeClaim"])["claimName"] == "mqtt-pki-trust" {
							if volume != "" {
								return environmentCreateBootstrapBlocker("broker trust PVC root is ambiguous")
							}
							volume = name
						}
					}
				}
			}
		}
		if volume == "" || deploymentplan.Map(pod["securityContext"])["fsGroup"] != nil {
			return environmentCreateBootstrapBlocker("broker trust requires its original exclusive claim and directory-only preparation without fsGroup")
		}
		init := deploymentplan.Object{"name": deviceBrokerTrustInitializer, "image": image, "command": []any{"/bin/sh", "-ec", coreIdentityPreparation}, "args": []any{"identity-permissions", "/identity-volume/" + strings.TrimPrefix(environmentCreateBrokerIdentity, "/run/pki-state/")}, "volumeMounts": []any{deploymentplan.Object{"name": volume, "mountPath": "/identity-volume"}}, "securityContext": deploymentplan.Object{"runAsNonRoot": false, "runAsUser": float64(0), "runAsGroup": float64(0), "allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": deploymentplan.Object{"drop": []any{"ALL"}, "add": []any{"CHOWN", "DAC_OVERRIDE", "FOWNER"}}}}
		pod["initContainers"] = mergeEnvironmentNamedFields(pod["initContainers"], []any{init})
	}
	return nil
}

// The native incoming admission reader requires a regular public JSON file.
// Bind only the original ConfigMap key as a read-only subPath file; no credential
// is copied. App bundle revision annotations already restart each new version.
func projectIncomingBootstrapManifest(object deploymentplan.Object, domain string) error {
	if domain != "device" && domain != "app" {
		return environmentCreateBootstrapBlocker("incoming bootstrap manifest requires its exact trust domain")
	}
	pod := environmentPKIPod(object)
	volumeName := domain + "-trust-manifest"
	trustName := "pki-create-" + domain + "-trust"
	file := "/run/pki-" + domain + "-manifest/bundles.json"
	found := false
	for _, value := range certIssuerObjectList(pod["containers"]) {
		c := deploymentplan.Map(value)
		if c["name"] != "app" {
			continue
		}
		old := mqttFoundationEnv(c, "VIDEO_CLOUD_AUTH_ISSUER_TRUST_MANIFEST")
		if old != "/run/pki-"+domain+"/bundles.json" && old != file {
			return environmentCreateBootstrapBlocker("incoming manifest projection differs from its maintained original public bundle")
		}
		found = true
		c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{"VIDEO_CLOUD_AUTH_ISSUER_TRUST_MANIFEST": file}))
		c["volumeMounts"] = mergeEnvironmentNamedFields(c["volumeMounts"], []any{deploymentplan.Object{"name": volumeName, "mountPath": file, "subPath": "bundles.json", "readOnly": true}})
	}
	if !found {
		return environmentCreateBootstrapBlocker("incoming manifest projection requires the native original API container")
	}
	pod["volumes"] = mergeEnvironmentNamedFields(pod["volumes"], []any{deploymentplan.Object{"name": volumeName, "configMap": deploymentplan.Object{"name": trustName, "items": []any{deploymentplan.Object{"key": "bundles.json", "path": "bundles.json"}}}}})
	return nil
}

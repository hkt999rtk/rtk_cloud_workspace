package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCurrentCleanupBindingOrdinaryRenderPreservesMountedSession(t *testing.T) {
	cfg, h, release, live := normalTailCleanupBindingFixture(t)
	before := deploymentplan.Digest(live)
	// Observation must use no active execution or normal-tail context.
	if err := h.Execution.Close(); err != nil {
		t.Fatal(err)
	}
	stale, err := observeEnvironmentBindings(cfg.Environment, release, live)
	if err != nil || stale.BootstrapCleanup == nil || stale.BootstrapCleanup.ConfigMapName != "" {
		t.Fatal("historical record fixture", err)
	}
	bindings, err := observeEnvironmentTargetBindings(context.Background(), cfg, release, "upgrade", live)
	if err != nil || deploymentplan.Digest(bindings.BootstrapCleanup) != deploymentplan.Digest(h.Bindings.BootstrapCleanup) {
		t.Fatal("ordinary observation lost mounted current record", err)
	}
	values, renderedBindings, _ := completeEnvironmentRenderFixture(cfg.Environment)
	// The unrelated consumers use the maintained whole-catalog fixture. The
	// Controller's complete native binding and cleanup record are observed above.
	for _, p := range bindings.Pods {
		if p.Component == "pki-controller" {
			for i := range renderedBindings.Pods {
				if renderedBindings.Pods[i].Component == p.Component {
					renderedBindings.Pods[i] = p
				}
			}
		}
	}
	renderedBindings.BootstrapCleanup = bindings.BootstrapCleanup
	renderedBindings.IngressAdmission = &environmentAdmissionBinding{SecretName: "ingress-nginx-admission", CABundle: base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t))}
	for id, uid := range bindings.Owners {
		renderedBindings.Owners[id] = uid
	}
	definitions, err := renderEnvironmentResources(values, renderedBindings, release)
	if err != nil {
		t.Fatal("actual ordinary maintained renderer", err)
	}
	id := "Deployment/" + bindings.Stack + "-video-cloud/pki-controller"
	target := createRecipeObject(t, definitions, id)
	if err := validateEnvironmentPKITransition([]deploymentplan.Definition{{Object: target}}, []deploymentplan.Object{live[0]}); err != nil {
		t.Fatal("ordinary target changed original lifecycle sources", err)
	}
	volumes := environmentPKIPod(target)["volumes"]
	if deploymentplan.Map(managedNamedField(volumes, "bootstrap-cleanup")["configMap"])["name"] != bindings.BootstrapCleanup.ConfigMapName {
		t.Fatal("renderer selected historical record")
	}
	var actualCM deploymentplan.Object
	for _, d := range definitions {
		if deploymentplan.ID(d.Object) == "ConfigMap/"+bindings.Stack+"-video-cloud/"+bindings.BootstrapCleanup.ConfigMapName {
			actualCM = d.Object
		}
	}
	if actualCM == nil || !deploymentplan.Matches(actualCM, live[1]) {
		t.Fatal("renderer changed immutable current binding record")
	}
	if deploymentplan.Digest(live) != before {
		t.Fatal("ordinary observation changed native inputs")
	}
	// The finalized bootstrap Secret is absent in this fixture. Its old expiry
	// and original execution state are not new runtime authority prerequisites.
	if bindings.Owners["Secret/"+bindings.Stack+"-video-cloud/service-bootstrap-"+bindings.BootstrapCleanup.SessionID] != "" {
		t.Fatal("fixture contains an active bootstrap credential")
	}
}

func TestCurrentCleanupBindingRejectsOwnerMountAndLineageDrift(t *testing.T) {
	cfg, _, release, live := normalTailCleanupBindingFixture(t)
	original, err := observeEnvironmentBindings(cfg.Environment, release, live)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"controller-uid", "cm-uid", "namespace-uid", "missing-cm", "duplicate-cm", "mutable-cm", "deleting-cm", "record-session", "record-namespace", "record-environment", "record-ca", "record-unknown-field", "missing-lineage", "foreign-sa", "automount", "foreign-volume", "write-mount", "subpath", "token-audience", "token-path", "token-expiry", "token-mount", "env-source", "secret-uid"} {
		t.Run(mode, func(t *testing.T) {
			var b environmentIdentityBindings
			raw, _ := json.Marshal(original)
			if json.Unmarshal(raw, &b) != nil {
				t.Fatal("bindings fixture")
			}
			current := make([]deploymentplan.Object, len(live))
			for i, o := range live {
				current[i] = deploymentplan.Clone(o)
			}
			controller, cm, namespace := current[0], current[1], current[2]
			pod := environmentPKIPod(controller)
			container := managedNamedField(pod["containers"], "pki-controller")
			switch mode {
			case "controller-uid":
				deploymentplan.Map(controller["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
			case "cm-uid":
				deploymentplan.Map(cm["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
			case "namespace-uid":
				deploymentplan.Map(namespace["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
			case "missing-cm":
				current = append(current[:1], current[2:]...)
			case "duplicate-cm":
				current = append(current, deploymentplan.Clone(cm))
			case "mutable-cm":
				cm["immutable"] = false
			case "deleting-cm":
				deploymentplan.Map(cm["metadata"])["deletionTimestamp"] = "now"
			case "record-session", "record-namespace", "record-environment", "record-unknown-field":
				data := deploymentplan.Map(cm["data"])
				var record map[string]any
				if json.Unmarshal([]byte(deploymentplan.String(data["binding.json"])), &record) != nil {
					t.Fatal("record fixture")
				}
				key, value := "session_id", "99999999-9999-4999-8999-999999999999"
				if mode == "record-namespace" {
					key = "namespace_uid"
				}
				if mode == "record-environment" {
					key, value = "environment", "staging"
				}
				if mode == "record-unknown-field" {
					key, value = "foreign", "field"
				}
				record[key] = value
				body, _ := json.Marshal(record)
				data["binding.json"] = string(body)
			case "record-ca":
				deploymentplan.Map(cm["data"])["ca.crt"] = "foreign"
			case "missing-lineage":
				b.BootstrapCleanup = nil
			case "foreign-sa":
				pod["serviceAccountName"] = "other"
			case "automount":
				pod["automountServiceAccountToken"] = true
			case "foreign-volume":
				deploymentplan.Map(managedNamedField(pod["volumes"], "bootstrap-cleanup")["configMap"])["name"] = "unrelated"
			case "write-mount":
				managedNamedField(container["volumeMounts"], "bootstrap-cleanup")["readOnly"] = false
			case "subpath":
				managedNamedField(container["volumeMounts"], "bootstrap-cleanup")["subPath"] = "binding.json"
			case "token-audience", "token-path", "token-expiry":
				projected := deploymentplan.Map(managedNamedField(pod["volumes"], "bootstrap-cleanup-api")["projected"])
				token := deploymentplan.Map(deploymentplan.Map(certIssuerObjectList(projected["sources"])[0])["serviceAccountToken"])
				if mode == "token-audience" {
					token["audience"] = "other"
				}
				if mode == "token-path" {
					token["path"] = "other"
				}
				if mode == "token-expiry" {
					token["expirationSeconds"] = float64(30)
				}
			case "token-mount":
				managedNamedField(container["volumeMounts"], "bootstrap-cleanup-api")["readOnly"] = false
			case "env-source":
				managedNamedField(container["env"], "PKI_BOOTSTRAP_CLEANUP_BINDING")["valueFrom"] = deploymentplan.Object{"secretKeyRef": deploymentplan.Object{"name": "other", "key": "x"}}
			case "secret-uid":
				b.Owners["Secret/"+b.Stack+"-video-cloud/service-bootstrap-"+hBindingSession(t, cm)] = "99999999-9999-4999-8999-999999999999"
			}
			before := deploymentplan.Digest(current)
			if err := observeEnvironmentCurrentCleanupBinding(&b, current); err == nil {
				t.Fatal("foreign current cleanup accepted", mode)
			}
			if deploymentplan.Digest(current) != before {
				t.Fatal("failed observation mutated current owners")
			}
		})
	}
}
func hBindingSession(t *testing.T, cm deploymentplan.Object) string {
	t.Helper()
	var record environmentBootstrapCleanupBinding
	if decodeEnvironmentJSON([]byte(deploymentplan.String(deploymentplan.Map(cm["data"])["binding.json"])), &record) != nil {
		t.Fatal("record")
	}
	return record.SessionID
}

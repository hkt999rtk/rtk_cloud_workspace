package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func normalTailCleanupBindingFixture(t *testing.T) (deploymentConfig, *createNormalTailHandoff, environmentRelease, []deploymentplan.Object) {
	t.Helper()
	nativeCfg, p, runtime, service := createServiceRecipeFixture(t, "dev")
	service.CleanupBinding.ConfigMapName = "pki-bootstrap-cleanup-binding-" + service.BootstrapSessionID[:12]
	defs, err := renderEnvironmentCreateManagedServiceDefinitions(context.Background(), nativeCfg, p, runtime, service)
	if err != nil {
		t.Fatal(err)
	}
	controller := deploymentplan.Clone(createRecipeObject(t, defs, "Deployment/"+p.Stack+"-video-cloud/pki-controller"))
	cm := deploymentplan.Clone(createRecipeObject(t, defs, "ConfigMap/"+p.Stack+"-video-cloud/"+service.CleanupBinding.ConfigMapName))
	deploymentplan.Map(controller["metadata"])["uid"] = "11111111-1111-4111-8111-111111111111"
	deploymentplan.Map(cm["metadata"])["uid"] = "22222222-2222-4222-8222-222222222222"
	namespace := deploymentplan.Object{"apiVersion": "v1", "kind": "Namespace", "metadata": deploymentplan.Object{"name": p.Stack + "-video-cloud", "uid": service.CleanupBinding.NamespaceUID}}
	_, _, release := completeEnvironmentRenderFixture("dev")
	live := []deploymentplan.Object{controller, cm, namespace}
	captured, err := observeEnvironmentBindings("dev", release, live)
	if err != nil {
		t.Fatal(err)
	}
	captured.BootstrapCleanup = &service.CleanupBinding
	// Preserve the genuine older owner too. Its presence must not select it over
	// the accepted versioned allocation mounted by the original Controller.
	old := service.CleanupBinding
	old.SessionID, old.SecretUID, old.ConfigMapName = "66666666-6666-4666-8666-666666666666", "77777777-7777-4777-8777-777777777777", ""
	oldDefs, err := applyEnvironmentBootstrapCleanup(nativeCfg.Values, deploymentplan.Clone(controller), old)
	if err != nil {
		t.Fatal(err)
	}
	oldCM := deploymentplan.Clone(createRecipeObject(t, oldDefs, "ConfigMap/"+p.Stack+"-video-cloud/pki-bootstrap-cleanup-binding"))
	deploymentplan.Map(oldCM["metadata"])["uid"] = "88888888-8888-4888-8888-888888888888"
	live = append(live, oldCM)
	cfg, store, operator, artifact := createStateFixture(t, "dev")
	e, err := startEnvironmentCreateExecution(context.Background(), cfg, artifact, operator, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	relative := filepath.Join(managedLateRedisDurableBase(e.Journal.PlanSHA256), "bindings.json")
	if _, err := writeEnvironmentCreateBootstrapJSON(store, relative, captured); err != nil {
		t.Fatal(err)
	}
	raw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, relative), true)
	if err != nil {
		t.Fatal(err)
	}
	h := &createNormalTailHandoff{Execution: e, Bindings: captured, Pins: map[string]string{"owner": deploymentplan.Digest(e.owner), "bindings": environmentPreflightBytesSHA256(raw)}}
	return cfg, h, release, live
}

func TestCreateNormalTailCleanupBindingRetainsAcceptedProducerThroughFormalTargetObservation(t *testing.T) {
	cfg, h, release, live := normalTailCleanupBindingFixture(t)
	before := deploymentplan.Digest(live)
	plain, err := observeEnvironmentTargetBindings(context.Background(), cfg, release, "create", live)
	if err != nil || plain.BootstrapCleanup == nil || plain.BootstrapCleanup.ConfigMapName != "" {
		t.Fatal("fixture did not expose original unversioned observation", err)
	}
	ctx := withCreateNormalTailHandoff(context.Background(), h)
	current, err := observeEnvironmentTargetBindings(ctx, cfg, release, "create", live)
	if err != nil || deploymentplan.Digest(current.BootstrapCleanup) != deploymentplan.Digest(h.Bindings.BootstrapCleanup) {
		t.Fatal("completed target lost accepted current cleanup producer", err)
	}
	controller := live[0]
	for _, tc := range []struct {
		name    string
		binding *environmentBootstrapCleanupBinding
		pass    bool
	}{{"historical", plain.BootstrapCleanup, false}, {"completed-handoff", current.BootstrapCleanup, true}} {
		t.Run(tc.name, func(t *testing.T) {
			target := deploymentplan.Clone(controller)
			defs, err := applyEnvironmentBootstrapCleanup(map[string]string{"CLOUD_ENV_NAME": "dev", "CLOUD_STACK_NAME": h.Bindings.Stack}, target, *tc.binding)
			if err != nil {
				t.Fatal(err)
			}
			defs = append(defs, deploymentplan.Definition{Object: target, Phase: "workload"})
			if err := validateEnvironmentPKITransition(defs, []deploymentplan.Object{controller}); (err == nil) != tc.pass {
				t.Fatal("original volume/identity guard outcome differs", err)
			}
			if tc.pass {
				for _, d := range defs {
					if d.Object["kind"] == "Role" {
						rule := deploymentplan.Map(certIssuerObjectList(d.Object["rules"])[0])
						if deploymentplan.Digest(rule["resourceNames"]) != deploymentplan.Digest([]any{"service-bootstrap-" + tc.binding.SessionID}) {
							t.Fatal("Role selected a retired session")
						}
					}
				}
			}
		})
	}
	ordinary, err := observeEnvironmentTargetBindings(ctx, cfg, release, "upgrade", live)
	if err != nil || deploymentplan.Digest(ordinary.BootstrapCleanup) != deploymentplan.Digest(current.BootstrapCleanup) {
		t.Fatal("ordinary adoption lost its current mounted cleanup binding", err)
	}
	if deploymentplan.Digest(live) != before {
		t.Fatal("binding selection mutated native observations")
	}
}

func TestCreateNormalTailCleanupBindingRejectsRecordOwnerAndCurrentMountDrift(t *testing.T) {
	cfg, original, release, baseline := normalTailCleanupBindingFixture(t)
	for _, mode := range []string{"cm-uid", "controller-uid", "namespace-uid", "duplicate-cm", "missing-cm", "mutable-cm", "deleting-cm", "deleting-controller", "record-session", "record-secret", "record-ca", "old-mount", "path", "writable-mount", "identity-env", "handoff-memory", "source-pin", "owner-pin", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			raw, _ := json.Marshal(original.Bindings)
			var source environmentIdentityBindings
			if json.Unmarshal(raw, &source) != nil {
				t.Fatal("fixture encoding")
			}
			h := *original
			h.Bindings = source
			h.Pins = map[string]string{}
			for k, v := range original.Pins {
				h.Pins[k] = v
			}
			live := make([]deploymentplan.Object, len(baseline))
			for i, o := range baseline {
				live[i] = deploymentplan.Clone(o)
			}
			controller, cm, namespace := live[0], live[1], live[2]
			ctx, cancel := context.WithCancel(withCreateNormalTailHandoff(context.Background(), &h))
			defer cancel()
			switch mode {
			case "cm-uid":
				deploymentplan.Map(cm["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
			case "controller-uid":
				deploymentplan.Map(controller["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
			case "namespace-uid":
				deploymentplan.Map(namespace["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
			case "duplicate-cm":
				live = append(live, deploymentplan.Clone(cm))
			case "missing-cm":
				live = append(live[:1], live[2:]...)
			case "mutable-cm":
				cm["immutable"] = false
			case "deleting-cm":
				deploymentplan.Map(cm["metadata"])["deletionTimestamp"] = "now"
			case "deleting-controller":
				deploymentplan.Map(controller["metadata"])["deletionTimestamp"] = "now"
			case "record-session", "record-secret":
				var record map[string]any
				if json.Unmarshal([]byte(deploymentplan.String(deploymentplan.Map(cm["data"])["binding.json"])), &record) != nil {
					t.Fatal("record fixture")
				}
				key := "session_id"
				if mode == "record-secret" {
					key = "secret_uid"
				}
				record[key] = "99999999-9999-4999-8999-999999999999"
				body, _ := json.Marshal(record)
				deploymentplan.Map(cm["data"])["binding.json"] = string(body)
			case "record-ca":
				deploymentplan.Map(cm["data"])["ca.crt"] = "different-public-ca"
			case "old-mount":
				deploymentplan.Map(managedNamedField(environmentPKIPod(controller)["volumes"], "bootstrap-cleanup")["configMap"])["name"] = "pki-bootstrap-cleanup-binding"
			case "path", "writable-mount":
				mount := managedNamedField(managedNamedField(environmentPKIPod(controller)["containers"], "pki-controller")["volumeMounts"], "bootstrap-cleanup")
				if mode == "path" {
					mount["mountPath"] = "/other"
				} else {
					mount["readOnly"] = false
				}
			case "identity-env":
				managedNamedField(managedNamedField(environmentPKIPod(controller)["containers"], "pki-controller")["env"], "PKI_BOOTSTRAP_CLEANUP_BINDING")["value"] = "/other/binding.json"
			case "handoff-memory":
				h.Bindings.BootstrapCleanup.SecretUID = "99999999-9999-4999-8999-999999999999"
			case "source-pin":
				h.Pins["bindings"] = strings.Repeat("f", 64)
			case "owner-pin":
				h.Pins["owner"] = strings.Repeat("f", 64)
			case "cancelled":
				cancel()
			}
			before := deploymentplan.Digest(live)
			if _, err := observeEnvironmentTargetBindings(ctx, cfg, release, "create", live); err == nil {
				t.Fatal("foreign record/owner/source accepted", mode)
			}
			if deploymentplan.Digest(live) != before {
				t.Fatal("failed observation mutated native inputs")
			}
		})
	}
}

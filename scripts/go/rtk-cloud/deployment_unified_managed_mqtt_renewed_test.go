package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const renewedMQTTACKSession = "7e19089d-c723-4cd3-887c-bdbd6dccbd6d"

func setRenewedMQTTACK(defs []deploymentplan.Definition) {
	for _, d := range defs {
		container, _ := managedMQTTWorkerRole(deploymentplan.String(deploymentplan.Map(d.Object["metadata"])["name"]))
		if container == "" {
			continue
		}
		c := mqttFoundationPrimary(d.Object)
		c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{
			"PKI_BOOTSTRAP_SESSION_ID":     renewedMQTTACKSession,
			"PKI_BOOTSTRAP_ISSUER_ID":      "11111111-1111-4111-8111-111111111111",
			"PKI_BOOTSTRAP_CONTROLLER_URL": "https://pki-controller." + deploymentplan.String(deploymentplan.Map(d.Object["metadata"])["namespace"]) + ".svc:18446",
		}))
	}
}

func TestManagedMQTTRenewedACKKeepsActualEnrollmentPlacement(t *testing.T) {
	for _, environment := range []string{"dev", "staging"} {
		t.Run(environment, func(t *testing.T) {
			defs, original := actualManagedMQTTConsumersEnrollmentFixture(t, environment, true)
			if err := bindEnvironmentManagedMQTTConsumers(defs, original); err != nil {
				t.Fatal(err)
			}
			setRenewedMQTTACK(defs)
			before := deploymentplan.Digest(defs)
			checkActualManagedMQTTMounts(t, defs)
			if err := bindEnvironmentManagedMQTTConsumers(defs, original); err != nil || deploymentplan.Digest(defs) != before {
				t.Fatal("renewed ACK changed original placement or public transport", err)
			}
			owners := map[string]string{}
			live := []deploymentplan.Object{}
			for _, d := range defs {
				metadata := deploymentplan.Map(d.Object["metadata"])
				labels := deploymentplan.Map(metadata["labels"])
				if labels == nil {
					labels = deploymentplan.Object{}
					metadata["labels"] = labels
				}
				labels["rtk.realtek.com/stack"], labels["app.kubernetes.io/managed-by"] = "video-cloud-"+environment, "rtk-cloud"
				id := deploymentplan.ID(d.Object)
				owners[id] = deploymentplan.String(deploymentplan.Map(d.Object["metadata"])["uid"])
				native := deploymentplan.Clone(d.Object)
				// Kubernetes never returns the write-only Secret stringData field.
				if native["kind"] == "Secret" && native["stringData"] != nil {
					data := deploymentplan.Map(native["data"])
					if data == nil {
						data = deploymentplan.Object{}
					}
					for key, value := range deploymentplan.Map(native["stringData"]) {
						data[key] = base64.StdEncoding.EncodeToString([]byte(value.(string)))
					}
					native["data"] = data
					delete(native, "stringData")
				}
				live = append(live, native)
			}
			p, err := deploymentplan.Compile(deploymentplan.Inputs{Operation: "scoped", Environment: environment, Stack: "video-cloud-" + environment, ConfigurationSHA256: deploymentplan.Digest("renewed-placement"), ReleaseSHA256: deploymentplan.Digest("original-release"), BindingsSHA256: deploymentplan.Digest(owners)}, defs, live, owners)
			if err != nil {
				t.Fatal("actual donor/enrollment target compile", err)
			}
			current := map[string]deploymentplan.Object{}
			for _, o := range live {
				current[deploymentplan.ID(o)] = o
			}
			if err := p.Verify(t.Context(), &normalTailE2EBackend{objects: current}); err != nil {
				t.Fatal("compiled actual target readback", err)
			}
			for _, r := range p.Resources {
				if r.Action != "retain" {
					t.Fatal("unchanged original resource proposed write", r.ID, r.Action)
				}
			}
		})
	}
}

func TestManagedMQTTRenewedACKStillRejectsForeignPlacement(t *testing.T) {
	for _, mode := range []string{"producer", "affinity", "missing-affinity", "duplicate-producer", "explicit-new-session"} {
		t.Run(mode, func(t *testing.T) {
			defs, original := actualManagedMQTTConsumersEnrollmentFixture(t, "dev", true)
			if err := bindEnvironmentManagedMQTTConsumers(defs, original); err != nil {
				t.Fatal(err)
			}
			setRenewedMQTTACK(defs)
			worker := mqttFoundationTarget(defs, "Deployment", "video-cloud-mqttfoundation")
			producer := mqttFoundationTarget(defs, "Deployment", "pki-product-mqtt-"+strings.ReplaceAll(original, "-", "")[:12])
			switch mode {
			case "producer":
				deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(producer["spec"])["template"])["metadata"])["labels"] = deploymentplan.Object{"app.kubernetes.io/name": "pki-product-mqtt-foreign"}
			case "affinity":
				term := deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(deploymentplan.Map(environmentPKIPod(worker)["affinity"])["podAffinity"])["requiredDuringSchedulingIgnoredDuringExecution"])[0])
				exp := deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(term["labelSelector"])["matchExpressions"])[0])
				exp["values"] = []any{"video-cloud-mqttfoundation", "pki-product-mqtt-" + strings.ReplaceAll(renewedMQTTACKSession, "-", "")[:12]}
			case "missing-affinity":
				delete(environmentPKIPod(worker), "affinity")
			case "duplicate-producer":
				copy := deploymentplan.Clone(producer)
				deploymentplan.Map(copy["metadata"])["name"] = "foreign-enrollment"
				defs = append(defs, deploymentplan.Definition{Object: copy})
			case "explicit-new-session":
				if err := bindEnvironmentManagedMQTTConsumers(defs, renewedMQTTACKSession); err == nil {
					t.Fatal("new ACK session acquired original placement authority")
				}
				return
			}
			if err := validateEnvironmentManagedMQTTConsumers(defs); err == nil {
				t.Fatal("foreign original placement accepted", mode)
			}
		})
	}
}

func TestCurrentMQTTBindingsRenewedACKOrdinaryRenderAndCheck(t *testing.T) {
	env, release, live := currentMQTTBindingsFixture(t)
	defs := []deploymentplan.Definition{}
	for _, o := range live {
		defs = append(defs, deploymentplan.Definition{Object: o})
	}
	setRenewedMQTTACK(defs)
	cfg := deploymentConfig{Environment: "dev", Values: env}
	b, err := observeEnvironmentTargetBindings(t.Context(), cfg, release, "upgrade", live)
	if err != nil {
		t.Fatal("renewed ordinary observer", err)
	}
	raw, _ := json.Marshal(b)
	var decoded environmentIdentityBindings
	if decodeEnvironmentJSON(raw, &decoded) != nil || validateEnvironmentIdentityBindings(decoded, "dev") != nil {
		t.Fatal("renewed ordinary binding serialization")
	}
	_, base, _ := completeEnvironmentRenderFixture("dev")
	seen := map[string]bool{}
	for _, p := range decoded.Pods {
		seen[p.Name] = true
	}
	for _, p := range base.Pods {
		if !seen[p.Name] {
			decoded.Pods = append(decoded.Pods, p)
		}
	}
	decoded.IngressAdmission = &environmentAdmissionBinding{SecretName: "ingress-nginx-admission", CABundle: base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t))}
	restore := installAllCredentialEnvironment(env)
	defer restore()
	target, err := renderEnvironmentResources(env, decoded, release)
	if err != nil {
		t.Fatal("renewed ordinary full renderer", err)
	}
	if err := validateEnvironmentManagedMQTTConsumers(target); err != nil {
		t.Fatal("renewed ordinary target validator", err)
	}
	current := map[string]deploymentplan.Object{}
	for _, o := range live {
		current[deploymentplan.ID(o)] = o
	}
	plan := &deploymentplan.Plan{}
	retained := map[string]bool{}
	for _, d := range target {
		id := deploymentplan.ID(d.Object)
		if current[id] != nil {
			plan.Resources = append(plan.Resources, deploymentplan.Resource{ID: id, Action: "retain", UID: decoded.Owners[id], Desired: d.Object})
			retained[id] = true
		}
	}
	for _, o := range live {
		id := deploymentplan.ID(o)
		if !retained[id] && (o["kind"] == "ConfigMap" || o["kind"] == "PersistentVolumeClaim") {
			plan.Resources = append(plan.Resources, deploymentplan.Resource{ID: id, Action: "retain", UID: decoded.Owners[id], Desired: o})
		}
	}
	if err := checkEnvironmentManagedMQTTConsumers(context.Background(), plan, func(_ context.Context, id string) (deploymentplan.Object, error) { return current[id], nil }); err != nil {
		t.Fatal("renewed ordinary full native profile check", err)
	}
	for _, name := range []string{"video-cloud-mqttfoundation", "video-cloud-shadowworker"} {
		wanted := mqttFoundationTarget(target, "Deployment", name)
		original := current[deploymentplan.ID(wanted)]
		if deploymentplan.Digest(environmentPKIPod(wanted)["affinity"]) != deploymentplan.Digest(environmentPKIPod(original)["affinity"]) {
			t.Fatal("ordinary render changed original placement", name)
		}
	}
}

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func createIngressAdmissionFixture(t *testing.T, environment string) (deploymentConfig, environmentCreateBootstrapPlan, environmentRelease, secretStore, *environmentCreateExecution) {
	t.Helper()
	parent, store, operatorSHA, artifact := createStateFixture(t, environment)
	execution, err := startEnvironmentCreateExecution(context.Background(), parent, artifact, operatorSHA, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { execution.Close() })
	values, _, release := completeEnvironmentRenderFixture(environment)
	cfg := deploymentConfig{Environment: environment, Adapter: "lke", Values: values}
	p := environmentCreateBootstrapPlan{Environment: environment, Stack: release.Stack, IngressAdmissionProfile: environmentCreateIngressAdmissionProfile, Images: map[string]string{}}
	for role, image := range release.Images {
		p.Images[role] = image.Reference
	}
	return cfg, p, release, store, execution
}

func TestCreateIngressAdmissionProducesRealScopedIdentityAndNeverRegeneratesRecordedMaterial(t *testing.T) {
	for _, environment := range []string{"dev", "staging"} {
		t.Run(environment, func(t *testing.T) {
			cfg, p, release, store, execution := createIngressAdmissionFixture(t, environment)
			m, err := prepareEnvironmentCreateIngressAdmissionMaterial(context.Background(), cfg, p, release, store, execution)
			if err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(store.Root, environmentCreateIngressAdmissionMaterialPath(execution.Journal.PlanSHA256))
			original, err := os.ReadFile(file)
			info, statErr := os.Stat(file)
			if err != nil || statErr != nil || info.Mode().Perm() != 0600 || execution.Journal.Entries[0].State != "pending" {
				t.Fatal("new admission identity lacks durable private preceding parent intent", err, statErr)
			}
			definitions, err := renderEnvironmentCreateIngressAdmission(cfg, p, release, m)
			if err != nil {
				t.Fatal(err)
			}
			for _, definition := range definitions {
				if deploymentplan.Map(deploymentplan.Map(definition.Object["metadata"])["labels"])["rtk.realtek.com/stack"] != p.Stack {
					t.Fatal("actual ingress producer hid an original cluster owner from native inventory", deploymentplan.ID(definition.Object))
				}
			}
			if _, err := prepareEnvironmentCreateIngressAdmissionMaterial(context.Background(), cfg, p, release, store, execution); err == nil {
				t.Fatal("recorded original admission identity was replaced by a retry")
			}
			actual, _ := os.ReadFile(file)
			if string(actual) != string(original) {
				t.Fatal("rejected retry changed original admission private material")
			}
			if err := os.Remove(file); err != nil {
				t.Fatal(err)
			}
			if _, err := prepareEnvironmentCreateIngressAdmissionMaterial(context.Background(), cfg, p, release, store, execution); err == nil {
				t.Fatal("missing recorded admission private material was silently regenerated")
			}
			for _, altered := range []string{"scope", "certificate", "key", "expired"} {
				bad, now := m, time.Now().UTC()
				switch altered {
				case "scope":
					bad.Stack = "video-cloud-foreign"
				case "certificate":
					bad.CertificatePEM = bad.CAPEM
				case "key":
					bad.PrivateKeyPEM = "private-canary"
				case "expired":
					now = bad.CreatedAt.Add(environmentCreateIngressAdmissionServerValidity + time.Minute)
				}
				if err := validateEnvironmentCreateIngressAdmissionMaterial(p, execution.Journal.PlanSHA256, bad, now); err == nil || strings.Contains(err.Error(), "private-canary") {
					t.Fatal("foreign, mismatched or expired material passed or leaked private diagnostics", altered, err)
				}
			}
		})
	}
}

func TestCreateIngressAdmissionRequiresOriginalParentAndFrozenPolicyBeforeGeneration(t *testing.T) {
	for _, changed := range []string{"parent", "profile", "image", "environment", "conflicting-material", "cancelled"} {
		t.Run(changed, func(t *testing.T) {
			cfg, p, release, store, execution := createIngressAdmissionFixture(t, "dev")
			ctx := context.Background()
			file := filepath.Join(store.Root, environmentCreateIngressAdmissionMaterialPath(execution.Journal.PlanSHA256))
			switch changed {
			case "parent":
				execution = nil
			case "profile":
				p.IngressAdmissionProfile = "unfrozen-admission-policy"
			case "image":
				p.Images["LKE_INGRESS_IMAGE"] = "foreign.example/controller@sha256:" + strings.Repeat("a", 64)
			case "environment":
				p.Environment = "staging"
			case "conflicting-material":
				if err := store.write(environmentCreateIngressAdmissionMaterialPath(execution.Journal.PlanSHA256), []byte("original-private-material"), false); err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			if _, err := prepareEnvironmentCreateIngressAdmissionMaterial(ctx, cfg, p, release, store, execution); err == nil {
				t.Fatal("unadmitted fresh admission material was generated", changed)
			}
			if changed != "conflicting-material" {
				if _, err := os.Lstat(file); !os.IsNotExist(err) {
					t.Fatal("blocked admission initialization saved private identity")
				}
			}
		})
	}
}

func TestCreateIngressAdmissionReadbackRejectsReplacedOwnersAndTransportDrift(t *testing.T) {
	for _, mode := range []string{"complete", "secret-replaced", "controller-replaced", "mount-drift", "webhook-port", "webhook-path", "missing-stage-proof", "preexisting-owner"} {
		t.Run(mode, func(t *testing.T) {
			cfg, p, release, store, execution := createIngressAdmissionFixture(t, "dev")
			kubeconfig := []byte("private-isolated-selected-kubeconfig")
			if err := store.write("kube/kubeconfig.yaml", kubeconfig, false); err != nil {
				t.Fatal(err)
			}
			directory := t.TempDir()
			stateFile := filepath.Join(directory, "native-api.json")
			state := map[string]deploymentplan.Object{}
			secretID := "Secret/" + p.Stack + "-ingress/" + environmentCreateIngressAdmissionSecret
			if mode == "preexisting-owner" {
				state[secretID] = deploymentplan.Object{"kind": "Secret", "metadata": deploymentplan.Object{"name": environmentCreateIngressAdmissionSecret, "namespace": p.Stack + "-ingress", "uid": "already-existing-owner"}}
			}
			persist := func() {
				raw, _ := json.Marshal(state)
				if err := os.WriteFile(stateFile, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			persist()
			kubectl := filepath.Join(directory, "kubectl")
			writeTestFile(t, kubectl, `#!/usr/bin/env python3
import json, os, sys
args = sys.argv[1:]
i = args.index("get")
namespace = args[args.index("--namespace") + 1] if "--namespace" in args else ""
identity = args[i+1] + "/" + namespace + "/" + args[i+2]
with open(os.environ["RTK_INGRESS_NATIVE_FIXTURE"]) as file:
    object = json.load(file).get(identity)
if object is not None:
    print(json.dumps(object))
`)
			if err := os.Chmod(kubectl, 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("RTK_INGRESS_NATIVE_FIXTURE", stateFile)
			runtime := environmentCreateBootstrapRuntime{Execution: execution, ExecutionSHA256: execution.Journal.PlanSHA256, Backend: environmentKubernetesBackend{Store: store, KubeconfigContent: kubeconfig}}
			applied := 0
			apply := func(_ context.Context, stage string, definitions []deploymentplan.Definition) error {
				applied++
				if len(execution.Journal.Entries) == 0 || execution.Journal.Entries[0].Key != environmentCreateIngressAdmissionEntry || execution.Journal.Entries[0].State != "pending" {
					t.Fatal("Kubernetes mutation preceded durable original admission intent")
				}
				owners := map[string]string{}
				var live []deploymentplan.Object
				for _, object := range state {
					live = append(live, object)
				}
				plan, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: p.Environment, Stack: p.Stack, Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest(cfg), ReleaseSHA256: deploymentplan.Digest(release), BindingsSHA256: deploymentplan.Digest(p)}, definitions, live)
				if err != nil {
					return err
				}
				for _, resource := range plan.Resources {
					if resource.Action == "retain" {
						owners[resource.ID] = resource.UID
						continue
					}
					object := deploymentplan.Clone(resource.Desired)
					uid, err := allocateEnvironmentCreateUUID()
					if err != nil {
						return err
					}
					deploymentplan.Map(object["metadata"])["uid"] = uid
					state[resource.ID], owners[resource.ID] = object, uid
				}
				if mode != "missing-stage-proof" {
					intent, _ := json.Marshal(deploymentplan.Object{"definition_sha256": deploymentplan.Digest(definitions)})
					key := "kubernetes/" + stage
					if err := execution.BeginEntry(key, stage, intent, time.Now().UTC()); err != nil {
						return err
					}
					receipt, _ := json.Marshal(deploymentplan.Object{"plan_sha256": deploymentplan.Digest(definitions), "owners": owners})
					if err := execution.RecordReceipt(key, receipt); err != nil {
						return err
					}
					if err := execution.CompleteEntry(key, time.Now().UTC()); err != nil {
						return err
					}
				}
				if stage == "ingress-controller" {
					controller := state["Deployment/"+p.Stack+"-ingress/ingress-nginx-controller"]
					webhook := state["ValidatingWebhookConfiguration//"+p.Stack+"-ingress-admission"]
					config := deploymentplan.Map(deploymentplan.Map(certIssuerObjectList(webhook["webhooks"])[0])["clientConfig"])
					switch mode {
					case "secret-replaced":
						deploymentplan.Map(state[secretID]["metadata"])["uid"] = "replacement-owner"
					case "controller-replaced":
						deploymentplan.Map(controller["metadata"])["uid"] = "replacement-controller"
					case "mount-drift":
						deploymentplan.Map(certIssuerObjectList(mqttFoundationPrimary(controller)["volumeMounts"])[0])["mountPath"] = "/wrong-owner"
					case "webhook-port":
						deploymentplan.Map(config["service"])["port"] = float64(8443)
					case "webhook-path":
						deploymentplan.Map(config["service"])["path"] = "/wrong-admission"
					}
				}
				persist()
				return nil
			}
			err := executeEnvironmentCreateIngressAdmission(context.Background(), cfg, p, release, store, runtime, apply)
			if (err == nil) != (mode == "complete") {
				t.Fatal("native admission readback admitted changed ownership/transport or rejected original identity", err)
			}
			if mode == "preexisting-owner" {
				if applied != 0 || len(execution.Journal.Entries) != 0 {
					t.Fatal("existing admission identity was adopted or replaced")
				}
				return
			}
			entry := execution.Journal.Entries[0]
			if mode == "complete" && (entry.State != "completed" || len(entry.Receipt) == 0 || strings.Contains(string(entry.Receipt), "PRIVATE KEY")) || mode != "complete" && (entry.State != "pending" || len(entry.Receipt) != 0) {
				t.Fatal("admission receipt fabricated completion or disclosed private material")
			}
		})
	}
}

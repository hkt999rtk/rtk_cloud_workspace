package main

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestRuntimeSecretReferencesAggregateKeepsCryptoAndActiveExpiryStrict(t *testing.T) {
	for _, mode := range []string{"historical", "active", "unknown-inventory", "unknown-consumer-kind", "invalid-unmounted-pair"} {
		t.Run(mode, func(t *testing.T) {
			store := makeIsolatedTestSecretStore(t, "dev")
			if err := store.write("kube/kubeconfig.yaml", []byte("apiVersion: v1\n"), true); err != nil {
				t.Fatal(err)
			}
			expired := rolloutTLSFixture(t, time.Now().Add(-time.Hour), x509.ExtKeyUsageServerAuth)
			cert, err := os.ReadFile(expired.cert)
			if err != nil {
				t.Fatal(err)
			}
			key, err := os.ReadFile(expired.key)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "invalid-unmounted-pair" {
				key = []byte("not a key")
			}
			secrets, _ := json.Marshal(map[string]any{"items": []any{map[string]any{"kind": "Secret", "metadata": map[string]any{"namespace": "video-cloud-dev-video-cloud", "name": "original-expired-export"}, "data": map[string]string{"tls.crt": base64.StdEncoding.EncodeToString(cert), "tls.key": base64.StdEncoding.EncodeToString(key)}}}})
			objects := runtimeReferenceObjects()
			if mode == "active" {
				deploymentplan.Map(objects[1]["spec"])["replicas"] = float64(1)
			}
			consumers, _ := json.Marshal(map[string]any{"items": objects})
			if mode == "unknown-inventory" {
				consumers = []byte(`{"items":null}`)
			}
			if mode == "unknown-consumer-kind" {
				consumers = []byte(`{"items":[{"metadata":{"name":"unknown","namespace":"video-cloud-dev-video-cloud"}}]}`)
			}
			dir := t.TempDir()
			secretPath := filepath.Join(dir, "secrets.json")
			consumersPath := filepath.Join(dir, "consumers.json")
			if os.WriteFile(secretPath, secrets, 0600) != nil || os.WriteFile(consumersPath, consumers, 0600) != nil {
				t.Fatal("isolated fixture unavailable")
			}
			t.Setenv("RTK_REFS_SECRETS", secretPath)
			t.Setenv("RTK_REFS_CONSUMERS", consumersPath)
			deploymentRuntimeTestKubectl(t, `case "$*" in
*"get secrets"*) cat "$RTK_REFS_SECRETS" ;;
*"get pods,deployments"*) cat "$RTK_REFS_CONSUMERS" ;;
*) printf '{"items":[]}' ;;
esac
`)
			err = verifySecretStoreK8SRuntime(store, time.Now())
			message := ""
			if err != nil {
				message = err.Error()
			}
			if mode == "historical" && strings.Contains(message, "not currently valid") {
				t.Fatal("unmounted historical export remained a current credential", err)
			}
			if (mode == "active" || mode == "unknown-inventory" || mode == "unknown-consumer-kind") && !strings.Contains(message, "not currently valid") {
				t.Fatal("active or unknown credential expiry was skipped", err)
			}
			if mode == "invalid-unmounted-pair" && !strings.Contains(message, "invalid or mismatched") {
				t.Fatal("historical identity's keypair validation was skipped", err)
			}
		})
	}
}

func runtimeReferenceObjects() []deploymentplan.Object {
	ns := "video-cloud-dev-video-cloud"
	meta := func(name, uid string) deploymentplan.Object {
		return deploymentplan.Object{"name": name, "namespace": ns, "uid": uid, "generation": float64(2)}
	}
	pod := func(secret string) deploymentplan.Object {
		return deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "server", "image": "pinned-image", "volumeMounts": []any{deploymentplan.Object{"name": "tls", "mountPath": "/run/tls", "readOnly": true}}}}, "volumes": []any{deploymentplan.Object{"name": "tls", "secret": deploymentplan.Object{"secretName": secret}}}}
	}
	owner := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": meta("server", "current-deployment"), "spec": deploymentplan.Object{"replicas": float64(1), "template": deploymentplan.Object{"spec": pod("current-server")}}}
	old := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "ReplicaSet", "metadata": meta("server-old-revision", "old-revision"), "spec": deploymentplan.Object{"replicas": float64(0), "template": deploymentplan.Object{"spec": pod("original-expired-export")}}, "status": deploymentplan.Object{"replicas": float64(0), "observedGeneration": float64(2)}}
	deploymentplan.Map(old["metadata"])["ownerReferences"] = []any{deploymentplan.Object{"controller": true, "kind": "Deployment", "name": "server", "uid": "current-deployment"}}
	return []deploymentplan.Object{owner, old}
}

func TestRuntimeSecretReferencesDistinguishHistoricalRevisionsFromCurrentAuthority(t *testing.T) {
	for _, mode := range []string{"historical", "running-replica", "desired-replica", "ready-replica", "still-owned-pod", "foreign-deployment", "standalone-replica", "unobserved-scale", "current-zero-deployment"} {
		t.Run(mode, func(t *testing.T) {
			objects := runtimeReferenceObjects()
			old := objects[1]
			switch mode {
			case "running-replica":
				deploymentplan.Map(old["status"])["replicas"] = float64(1)
			case "desired-replica":
				deploymentplan.Map(old["spec"])["replicas"] = float64(1)
			case "ready-replica":
				deploymentplan.Map(old["status"])["readyReplicas"] = float64(1)
			case "still-owned-pod":
				objects = append(objects, deploymentplan.Object{"kind": "Pod", "metadata": deploymentplan.Object{"name": "old-pod", "namespace": "video-cloud-dev-video-cloud", "ownerReferences": []any{deploymentplan.Object{"uid": "old-revision"}}}, "spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "server"}}}})
			case "foreign-deployment":
				deploymentplan.Map(objects[0]["metadata"])["uid"] = "replacement-deployment"
			case "standalone-replica":
				delete(deploymentplan.Map(old["metadata"]), "ownerReferences")
			case "unobserved-scale":
				deploymentplan.Map(old["status"])["observedGeneration"] = float64(1)
			case "current-zero-deployment":
				deploymentplan.Map(objects[0]["spec"])["replicas"] = float64(0)
			}
			before := deploymentplan.Digest(objects)
			refs, err := runtimeSecretReferenceIndex("video-cloud-dev", objects)
			if err != nil {
				t.Fatal(err)
			}
			if !refs["Secret/video-cloud-dev-video-cloud/current-server"] {
				t.Fatal("current desired listener lost its validity check")
			}
			wantOld := mode != "historical" && mode != "current-zero-deployment"
			if refs["Secret/video-cloud-dev-video-cloud/original-expired-export"] != wantOld {
				t.Fatal("old revision was incorrectly active or skipped")
			}
			if deploymentplan.Digest(objects) != before {
				t.Fatal("read-only classification changed source objects")
			}
		})
	}
}

func TestRuntimeSecretReferencesCoverActualPodAndIngressInputs(t *testing.T) {
	ns := "video-cloud-dev-video-cloud"
	pod := deploymentplan.Object{
		"containers":          []any{deploymentplan.Object{"name": "main", "env": []any{deploymentplan.Object{"name": "TLS", "valueFrom": deploymentplan.Object{"secretKeyRef": deploymentplan.Object{"name": "env-identity", "key": "tls.crt"}}}}, "envFrom": []any{deploymentplan.Object{"secretRef": deploymentplan.Object{"name": "env-map"}}}}},
		"initContainers":      []any{deploymentplan.Object{"name": "init", "envFrom": []any{deploymentplan.Object{"secretRef": deploymentplan.Object{"name": "init-identity"}}}}},
		"ephemeralContainers": []any{deploymentplan.Object{"name": "diagnostic", "envFrom": []any{deploymentplan.Object{"secretRef": deploymentplan.Object{"name": "ephemeral-identity"}}}}},
		"imagePullSecrets":    []any{deploymentplan.Object{"name": "pull"}},
		"volumes":             []any{deploymentplan.Object{"name": "tls", "secret": deploymentplan.Object{"secretName": "tls-volume"}}, deploymentplan.Object{"name": "projected", "projected": deploymentplan.Object{"sources": []any{deploymentplan.Object{"secret": deploymentplan.Object{"name": "projected-identity"}}}}}, deploymentplan.Object{"name": "csi", "csi": deploymentplan.Object{"nodePublishSecretRef": deploymentplan.Object{"name": "csi-identity"}}}},
	}
	objects := []deploymentplan.Object{{"kind": "Pod", "metadata": deploymentplan.Object{"name": "actual-pod", "namespace": ns}, "spec": pod}, {"kind": "CronJob", "metadata": deploymentplan.Object{"name": "future-job", "namespace": ns}, "spec": deploymentplan.Object{"jobTemplate": deploymentplan.Object{"spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": pod}}}}}, {"kind": "Ingress", "metadata": deploymentplan.Object{"name": "public", "namespace": ns}, "spec": deploymentplan.Object{"tls": []any{deploymentplan.Object{"secretName": "public-tls"}}}}, {"kind": "ServiceAccount", "metadata": deploymentplan.Object{"name": "native", "namespace": ns}, "imagePullSecrets": []any{deploymentplan.Object{"name": "account-pull"}}}}
	refs, err := runtimeSecretReferenceIndex("video-cloud-dev", objects)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"env-identity", "env-map", "init-identity", "ephemeral-identity", "pull", "tls-volume", "projected-identity", "csi-identity", "public-tls", "account-pull"} {
		if !refs["Secret/"+ns+"/"+name] {
			t.Fatal("actual or planned credential input was not indexed", name)
		}
	}
	if len(refs) != 10 {
		t.Fatal("credential values or unrelated fields became references")
	}
	for _, mode := range []string{"missing-namespace", "missing-spec", "malformed-secret", "cross-namespace-csi", "unresolved-secret-provider"} {
		t.Run(mode, func(t *testing.T) {
			object := deploymentplan.Clone(objects[0])
			spec := deploymentplan.Map(object["spec"])
			switch mode {
			case "missing-namespace":
				delete(deploymentplan.Map(object["metadata"]), "namespace")
			case "missing-spec":
				delete(object, "spec")
			case "malformed-secret":
				spec["volumes"] = []any{deploymentplan.Object{"secret": deploymentplan.Object{}}}
			case "cross-namespace-csi":
				spec["volumes"] = []any{deploymentplan.Object{"csi": deploymentplan.Object{"nodePublishSecretRef": deploymentplan.Object{"name": "other", "namespace": "foreign"}}}}
			case "unresolved-secret-provider":
				spec["volumes"] = []any{deploymentplan.Object{"csi": deploymentplan.Object{"volumeAttributes": deploymentplan.Object{"secretProviderClass": "unresolved"}}}}
			}
			if _, err := runtimeSecretReferenceIndex("video-cloud-dev", []deploymentplan.Object{object}); err == nil {
				t.Fatal("unknown credential inventory was accepted")
			}
		})
	}
}

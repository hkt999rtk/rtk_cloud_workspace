package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// installIsolatedFreshStatefulKube creates an empty, private simulated API.
// No controller readiness is inferred from a stored object.  The caller must
// explicitly connect separate verified runtime bridges for supported workloads.
func installIsolatedFreshStatefulKube(t *testing.T, store secretStore, kube []byte, bin string) (map[string]string, string) {
	t.Helper()
	if len(kube) == 0 || !filepath.IsAbs(bin) {
		t.Fatal("isolated kubeconfig and binary directory required")
	}
	frozen, err := newEnvironmentKubernetesBackend(string(kube), store.Environment)
	if err != nil || frozen.Store.Root != store.Root {
		t.Fatal("selected synthetic Kubernetes identity cannot be frozen", err)
	}
	_, dir, calls := unifiedSessionNativeBackend(t, store, map[string]deploymentplan.Object{}, nil)
	expected := filepath.Join(dir, "expected-kubeconfig")
	if err := os.WriteFile(expected, frozen.KubeconfigContent, 0600); err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(dir, "unified-kubectl.py")
	source, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatal(err)
	}
	code := string(source)
	oldKube := "    if f.read() != b'isolated-native-kubeconfig': sys.exit(72)"
	newKube := "    if f.read() != open(os.environ['RTK_FRESH_EXPECTED_KUBECONFIG'], 'rb').read(): sys.exit(72)"
	oldWait := "    elif ('rollout' in a and 'status' in a) or 'wait' in a:\n        if os.path.exists(os.path.join(root, 'fail-ready')): fail('not ready')"
	newWait := "    elif ('rollout' in a and 'status' in a) or 'wait' in a:\n        fail('isolated controller readiness is not implemented')"
	oldGet := "    if a[0] == 'get':\n"
	newGet := "    if a == ['get', '--raw=/readyz']:\n        print('ok')\n    elif a[0] == 'get':\n"
	oldList := `            if kind == 'namespace': items = [o for o in objs if o.get('kind') == 'Namespace']
            elif kind == 'validatingwebhookconfiguration': items = [o for o in objs if o.get('kind') == 'ValidatingWebhookConfiguration']
            elif ns is not None: items = [o for o in objs if o.get('metadata', {}).get('namespace', '') == ns]
            elif '-l' in a:
                label = a[a.index('-l')+1].split('=', 1)
                items = [o for o in objs if not o.get('metadata', {}).get('namespace') and o.get('metadata', {}).get('labels', {}).get(label[0]) == label[1]]
            else: fail('unsupported inventory')`
	newList := `            aliases = {'namespaces':'namespace','pods':'pod','deployments':'deployment','statefulsets':'statefulset','replicasets':'replicaset','services':'service','configmaps':'configmap','secrets':'secret','persistentvolumeclaims':'persistentvolumeclaim','persistentvolumes':'persistentvolume','serviceaccounts':'serviceaccount','leases':'lease','jobs':'job'}
            known = set('namespace deployment statefulset daemonset replicaset pod job cronjob service configmap secret persistentvolume persistentvolumeclaim serviceaccount role rolebinding networkpolicy ingress poddisruptionbudget horizontalpodautoscaler lease clusterrole clusterrolebinding ingressclass validatingwebhookconfiguration'.split())
            requested = {aliases.get(k, k) for k in kind.lower().split(',')}
            if not requested or not requested.issubset(known): fail('unsupported inventory kind')
            items = [o for o in objs if o.get('kind', '').lower() in requested]
            if ns is not None: items = [o for o in items if o.get('metadata', {}).get('namespace', '') == ns]
            elif kind not in ('namespace', 'namespaces', 'persistentvolume', 'persistentvolumes', 'validatingwebhookconfiguration') and '-l' not in a: fail('unsupported inventory scope')
            if '-l' in a:
                selectors = dict(part.split('=', 1) for part in a[a.index('-l')+1].split(','))
                items = [o for o in items if all(o.get('metadata', {}).get('labels', {}).get(k) == v for k, v in selectors.items())]`
	oldUID := "            meta['uid'] = 'fixture-' + uuid.uuid4().hex"
	newUID := "            meta['uid'] = str(uuid.uuid4())"
	oldSave := "        if '--dry-run=server' not in a: save(file, obj)"
	newSave := `        if kind in ('Deployment', 'StatefulSet'):
            if old is None: meta['generation'] = 1
            else:
                generation = old.get('metadata', {}).get('generation')
                if type(generation) != int or generation < 1: fail('invalid generation')
                meta['generation'] = generation + (1 if old.get('spec') != obj.get('spec') else 0)
        if '--dry-run=server' not in a: save(file, obj)`
	if strings.Count(code, oldKube) != 1 || strings.Count(code, oldWait) != 1 || strings.Count(code, oldGet) != 1 || strings.Count(code, oldList) != 1 || strings.Count(code, oldUID) != 1 || strings.Count(code, oldSave) != 1 {
		t.Fatal("existing isolated Kubernetes protocol changed")
	}
	code = strings.Replace(code, oldKube, newKube, 1)
	code = strings.Replace(code, oldWait, newWait, 1)
	code = strings.Replace(code, oldGet, newGet, 1)
	code = strings.Replace(code, oldList, newList, 1)
	code = strings.Replace(code, oldUID, newUID, 1)
	code = strings.Replace(code, oldSave, newSave, 1)
	if err := os.WriteFile(scriptPath, []byte(code), 0600); err != nil {
		t.Fatal(err)
	}
	launcher := `#!/bin/sh
umask 077
"$RTK_UNIFIED_PYTHON" "$RTK_UNIFIED_KUBECTL" "$@"
status=$?
shift 3
if [ "${1-}" = --namespace ]; then shift 2; fi
action=${1-other}
case "$action" in get|create|replace|delete|wait|rollout|auth) ;; *) action=other;; esac
kind=${2-other}
case "$kind" in namespace|Namespace|namespaces|pod|pods|deployment|Deployment|statefulset|StatefulSet|clusterrole|ClusterRole|configmap|ConfigMap|storageclass|StorageClass|validatingwebhookconfiguration|__NAMESPACED__|__CLUSTER__) ;; *) kind=other;; esac
printf '%s %s %s\n' "$action" "$kind" "$status" >> "$RTK_FRESH_SAFE_CALLS"
exit "$status"
`
	launcher = strings.ReplaceAll(launcher, "__NAMESPACED__", environmentNamespacedInventoryKinds)
	launcher = strings.ReplaceAll(launcher, "__CLUSTER__", environmentClusterInventoryKinds)
	if err := os.WriteFile(filepath.Join(bin, "kubectl"), []byte(launcher), 0700); err != nil {
		t.Fatal(err)
	}
	vars := map[string]string{
		"RTK_UNIFIED_PYTHON":            os.Getenv("RTK_UNIFIED_PYTHON"),
		"RTK_UNIFIED_KUBECTL":           scriptPath,
		"RTK_LATE_NATIVE_OBJECTS":       dir,
		"RTK_LATE_NATIVE_CALLS":         calls,
		"RTK_FRESH_EXPECTED_KUBECONFIG": expected,
		"RTK_FRESH_SAFE_CALLS":          filepath.Join(dir, "safe-calls.log"),
	}
	for k, v := range vars {
		if v == "" {
			t.Fatalf("missing private fixture variable %s", k)
		}
	}
	return vars, dir
}

func TestIsolatedFreshStatefulKubeStartsEmptyAndFailsClosed(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "dev")
	bin := t.TempDir()
	ca := base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t))
	kube := []byte("apiVersion: v1\nkind: Config\ncurrent-context: isolated\nclusters:\n- name: isolated\n  cluster:\n    server: https://127.0.0.1:6443\n    certificate-authority-data: " + ca + "\ncontexts:\n- name: isolated\n  context:\n    cluster: isolated\n    user: isolated\nusers:\n- name: isolated\n  user:\n    token: isolated-kubernetes-token\n")
	vars, _ := installIsolatedFreshStatefulKube(t, store, kube, bin)
	frozen, err := newEnvironmentKubernetesBackend(string(kube), store.Environment)
	if err != nil || bytes.Equal(kube, frozen.KubeconfigContent) {
		t.Fatal("fixture must exercise the native kubeconfig normalization", err)
	}
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, frozen.KubeconfigContent, 0600); err != nil {
		t.Fatal(err)
	}
	invoke := func(stdin []byte, args ...string) ([]byte, error) {
		c := exec.Command(filepath.Join(bin, "kubectl"), append([]string{"--kubeconfig", path, "--request-timeout=90s"}, args...)...)
		c.Env = []string{"PATH=/usr/bin:/bin"}
		for k, v := range vars {
			c.Env = append(c.Env, k+"="+v)
		}
		c.Stdin = bytes.NewReader(stdin)
		return c.CombinedOutput()
	}
	got, err := invoke(nil, "get", "Namespace", "example", "--ignore-not-found=true", "-o", "json")
	if err != nil || len(got) != 0 {
		t.Fatal("new inventory must be empty", string(got), err)
	}
	got, err = invoke(nil, "get", "--raw=/readyz")
	if err != nil || strings.TrimSpace(string(got)) != "ok" {
		t.Fatal("isolated API readiness is unavailable", string(got), err)
	}
	obj := []byte(`{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"example","labels":{"rtk.realtek.com/stack":"video-cloud-dev"}}}`)
	raw, err := invoke(obj, "create", "-f", "-", "-o", "json")
	if err != nil {
		t.Fatal("create failed", string(raw), err)
	}
	var created map[string]any
	if json.Unmarshal(raw, &created) != nil || !environmentBootstrapOwnerUUID.MatchString(deploymentplan.String(deploymentplan.Map(created["metadata"])["uid"])) {
		t.Fatal("missing native owner UID")
	}
	got, err = invoke(nil, "get", "Namespace", "example", "--ignore-not-found=true", "-o", "json")
	if err != nil || !bytes.Contains(got, []byte(deploymentplan.String(deploymentplan.Map(created["metadata"])["uid"]))) {
		t.Fatal("created owner missing", string(got), err)
	}
	for _, body := range []string{
		`{"apiVersion":"rbac.authorization.k8s.io/v1","kind":"ClusterRole","metadata":{"name":"in-scope","labels":{"rtk.realtek.com/stack":"video-cloud-dev"}}}`,
		`{"apiVersion":"rbac.authorization.k8s.io/v1","kind":"ClusterRole","metadata":{"name":"other-stack","labels":{"rtk.realtek.com/stack":"other"}}}`,
		`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"in-ns","namespace":"example"}}`,
		`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"other-ns","namespace":"foreign"}}`,
	} {
		if raw, err := invoke([]byte(body), "create", "-f", "-", "-o", "json"); err != nil {
			t.Fatal("cannot create isolated test object", string(raw), err)
		}
	}
	got, err = invoke(nil, "get", environmentClusterInventoryKinds, "-l", "rtk.realtek.com/stack=video-cloud-dev", "-o", "json")
	var clusterList struct {
		Items []deploymentplan.Object `json:"items"`
	}
	if err != nil || json.Unmarshal(got, &clusterList) != nil || len(clusterList.Items) != 1 || clusterList.Items[0]["kind"] != "ClusterRole" || deploymentplan.Map(clusterList.Items[0]["metadata"])["name"] != "in-scope" {
		t.Fatal("cluster inventory must filter type and stack", string(got), err)
	}
	got, err = invoke(nil, "get", environmentNamespacedInventoryKinds, "--namespace", "example", "-o", "json")
	var namespaceList struct {
		Items []deploymentplan.Object `json:"items"`
	}
	if err != nil || json.Unmarshal(got, &namespaceList) != nil || len(namespaceList.Items) != 1 || namespaceList.Items[0]["kind"] != "ConfigMap" || deploymentplan.Map(namespaceList.Items[0]["metadata"])["name"] != "in-ns" {
		t.Fatal("namespace inventory must filter type and namespace", string(got), err)
	}
	got, err = invoke(nil, "get", "persistentvolumes", "-o", "json")
	var volumes struct {
		Items []deploymentplan.Object `json:"items"`
	}
	if err != nil || json.Unmarshal(got, &volumes) != nil || volumes.Items == nil || len(volumes.Items) != 0 {
		t.Fatal("empty isolated PV inventory unavailable", string(got), err)
	}
	volume := []byte(`{"apiVersion":"v1","kind":"PersistentVolume","metadata":{"name":"test-pv"},"spec":{"claimRef":{"name":"test-pvc","namespace":"example"}}}`)
	if raw, err := invoke(volume, "create", "-f", "-", "-o", "json"); err != nil {
		t.Fatal("cannot create isolated PV object", string(raw), err)
	}
	got, err = invoke(nil, "get", "persistentvolumes", "-o", "json")
	if err != nil || json.Unmarshal(got, &volumes) != nil || len(volumes.Items) != 1 || volumes.Items[0]["kind"] != "PersistentVolume" || deploymentplan.Map(volumes.Items[0]["metadata"])["name"] != "test-pv" {
		t.Fatal("PV inventory includes wrong objects", string(got), err)
	}
	for _, kind := range []string{"Deployment", "StatefulSet"} {
		body := deploymentplan.Object{"apiVersion": "apps/v1", "kind": kind, "metadata": deploymentplan.Object{"name": "generation-test", "namespace": "example"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "app", "image": "synthetic"}}}}}}
		raw, _ := json.Marshal(body)
		dry, err := invoke(raw, "create", "-f", "-", "-o", "json", "--dry-run=server")
		var predicted deploymentplan.Object
		if err != nil || json.Unmarshal(dry, &predicted) != nil || deploymentplan.Map(predicted["metadata"])["generation"] != float64(1) {
			t.Fatal("dry-run creation generation differs", kind, err)
		}
		got, err := invoke(nil, "get", kind, "generation-test", "--namespace", "example", "--ignore-not-found=true", "-o", "json")
		if err != nil || len(got) != 0 {
			t.Fatal("dry-run wrote the resource", kind, err)
		}
		createdRaw, err := invoke(raw, "create", "-f", "-", "-o", "json")
		var created deploymentplan.Object
		if err != nil || json.Unmarshal(createdRaw, &created) != nil || deploymentplan.Map(created["metadata"])["generation"] != float64(1) {
			t.Fatal("initial generation missing", kind, err)
		}
		meta := deploymentplan.Map(created["metadata"])
		meta["labels"] = deploymentplan.Object{"isolated": "true"}
		metadataRaw, _ := json.Marshal(created)
		updatedRaw, err := invoke(metadataRaw, "replace", "-f", "-", "-o", "json")
		var updated deploymentplan.Object
		if err != nil || json.Unmarshal(updatedRaw, &updated) != nil || deploymentplan.Map(updated["metadata"])["generation"] != float64(1) {
			t.Fatal("metadata-only update changed generation", kind, err)
		}
		deploymentplan.Map(updated["spec"])["replicas"] = float64(2)
		replaceRaw, _ := json.Marshal(updated)
		changedRaw, err := invoke(replaceRaw, "replace", "-f", "-", "-o", "json")
		var changed deploymentplan.Object
		if err != nil || json.Unmarshal(changedRaw, &changed) != nil || deploymentplan.Map(changed["metadata"])["generation"] != float64(2) {
			t.Fatal("spec update did not advance generation", kind, err)
		}
		deploymentplan.Map(changed["spec"])["replicas"] = float64(3)
		dryRaw, _ := json.Marshal(changed)
		if _, err := invoke(dryRaw, "replace", "-f", "-", "-o", "json", "--dry-run=server"); err != nil {
			t.Fatal("dry-run replace failed", kind, err)
		}
		readback, err := invoke(nil, "get", kind, "generation-test", "--namespace", "example", "--ignore-not-found=true", "-o", "json")
		var retained deploymentplan.Object
		if err != nil || json.Unmarshal(readback, &retained) != nil || deploymentplan.Map(retained["metadata"])["generation"] != float64(2) || deploymentplan.Map(retained["spec"])["replicas"] != float64(2) {
			t.Fatal("dry-run replacement changed stored resource", kind, err)
		}
		deploymentplan.Map(retained["metadata"])["uid"] = "foreign"
		foreign, _ := json.Marshal(retained)
		if _, err := invoke(foreign, "replace", "-f", "-", "-o", "json"); err == nil {
			t.Fatal("foreign UID replacement accepted", kind)
		}
	}
	if _, err = invoke(nil, "get", "unknownkind", "--namespace", "example", "-o", "json"); err == nil {
		t.Fatal("unknown inventory kind was accepted")
	}
	if _, err = invoke(nil, "--namespace", "example", "rollout", "status", "deployment/example", "--timeout=1s"); err == nil {
		t.Fatal("unimplemented rollout was accepted")
	}
	if _, err = invoke(nil, "--namespace", "example", "wait", "--for=condition=complete", "Job/example", "--timeout=1s"); err == nil {
		t.Fatal("unimplemented Job was accepted")
	}
	if err := os.WriteFile(path, []byte("foreign"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = invoke(nil, "get", "Namespace", "example", "--ignore-not-found=true", "-o", "json"); err == nil {
		t.Fatal("foreign Kubernetes identity accepted")
	}
	calls, err := os.ReadFile(vars["RTK_FRESH_SAFE_CALLS"])
	if err != nil || !bytes.Contains(calls, []byte("get Namespace 0")) || bytes.Contains(calls, []byte("example")) || bytes.Contains(calls, []byte("foreign")) {
		t.Fatal("isolated call summary includes unexpected raw arguments", err)
	}
}

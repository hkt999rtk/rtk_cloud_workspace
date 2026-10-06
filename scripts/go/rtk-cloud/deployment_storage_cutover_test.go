package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func storageCutoverFixture(kind, name string) map[string]any {
	pod := map[string]any{"containers": []any{map[string]any{"name": "app", "env": []any{
		map[string]any{"name": "VIDEO_CLOUD_BLOB_BUCKET", "value": "old"}, map[string]any{"name": "VIDEO_CLOUD_BLOB_ENDPOINT", "value": "https://source.example"}, map[string]any{"name": "VIDEO_CLOUD_BLOB_REGION", "value": "us-sea"}, map[string]any{"name": "VIDEO_CLOUD_BLOB_PREFIX", "value": "environments/stack"},
		map[string]any{"name": "AWS_ACCESS_KEY_ID", "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "old-credentials", "key": "access"}}}, map[string]any{"name": "AWS_SECRET_ACCESS_KEY", "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "old-credentials", "key": "secret"}}}, map[string]any{"name": "KEEP", "value": "unchanged"},
	}}}}
	spec := map[string]any{"replicas": float64(1), "template": map[string]any{"spec": pod}}
	if kind == "CronJob" {
		spec = map[string]any{"suspend": true, "jobTemplate": map[string]any{"spec": map[string]any{"template": map[string]any{"spec": pod}}}}
	}
	return map[string]any{"apiVersion": "apps/v1", "kind": kind, "metadata": map[string]any{"name": name, "namespace": "stack-video-cloud", "uid": name + "-uid", "resourceVersion": "1"}, "spec": spec}
}
func storageCutoverMockFile(root, kind, namespace, name string) string {
	return filepath.Join(root, strings.ToLower(kind)+"--"+namespace+"--"+name+".json")
}
func storageCutoverMockWrite(t *testing.T, root string, object map[string]any) {
	t.Helper()
	b, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(storageCutoverMockFile(root, storageCutoverString(object["kind"]), storageCutoverString(storageCutoverGet(object, "/metadata/namespace")), storageCutoverString(storageCutoverGet(object, "/metadata/name"))), b, 0600); err != nil {
		t.Fatal(err)
	}
}
func installStorageCutoverMock(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "kubectl")
	if old := os.Getenv("RTK_CLOUD_KUBECTL"); old != "" {
		t.Setenv("STORAGE_KUBECTL_FALLBACK", old)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexec \"$STORAGE_KUBECTL_TEST_BINARY\" -test.run '^TestStorageKubectlHelperProcess$' -- \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STORAGE_KUBECTL_TEST_BINARY", os.Args[0])
	t.Setenv("STORAGE_KUBECTL_TEST_ROOT", root)
	t.Setenv("RTK_CLOUD_KUBECTL", path)
	t.Setenv("RTK_CLOUD_KUBECTL_RETRY_ATTEMPTS", "1")
	storageCutoverMockWrite(t, root, map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": "kube-system", "uid": "cluster-uid", "resourceVersion": "1"}})
	storageCutoverMockWrite(t, root, map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"namespace": "stack-video-cloud", "name": "old-credentials", "uid": "old-secret-uid", "resourceVersion": "1"}, "data": map[string]any{"access": "b2xk", "secret": "b2xk"}})
	return root
}
func TestStorageKubectlHelperProcess(t *testing.T) {
	root := os.Getenv("STORAGE_KUBECTL_TEST_ROOT")
	if root == "" {
		return
	}
	args := []string{}
	for i, a := range os.Args {
		if a == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	namespace := ""
	command := ""
	position := 0
	for i, a := range args {
		if a == "-n" && i+1 < len(args) {
			namespace = args[i+1]
		}
		if a == "get" || a == "patch" || a == "create" || a == "delete" || a == "rollout" || a == "wait" {
			command = a
			position = i
			break
		}
	}
	log, _ := os.OpenFile(filepath.Join(root, "commands"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	fmt.Fprintln(log, strings.Join(args, " "))
	_ = log.Close()
	fail := func() { fmt.Fprintln(os.Stderr, "mock conditional operation rejected"); os.Exit(1) }
	fallback := func() {
		path := os.Getenv("STORAGE_KUBECTL_FALLBACK")
		if path != "" {
			cmd := exec.Command(path, args...)
			cmd.Stdin = os.Stdin
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if cmd.Run() != nil {
				fail()
			}
		}
		os.Exit(0)
	}
	read := func(kind, name string) map[string]any {
		b, err := os.ReadFile(storageCutoverMockFile(root, kind, namespace, name))
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			fail()
		}
		var v map[string]any
		if json.Unmarshal(b, &v) != nil {
			fail()
		}
		return v
	}
	write := func(v map[string]any) {
		b, _ := json.Marshal(v)
		if os.WriteFile(storageCutoverMockFile(root, storageCutoverString(v["kind"]), storageCutoverString(storageCutoverGet(v, "/metadata/namespace")), storageCutoverString(storageCutoverGet(v, "/metadata/name"))), b, 0600) != nil {
			fail()
		}
		fmt.Println(string(b))
	}
	switch command {
	case "get":
		kind := strings.ToLower(args[position+1])
		name := args[position+2]
		if strings.Contains(kind, ",") {
			items := []map[string]any{}
			files, _ := filepath.Glob(filepath.Join(root, "*.json"))
			for _, file := range files {
				b, _ := os.ReadFile(file)
				var v map[string]any
				_ = json.Unmarshal(b, &v)
				if storageCutoverPodPath(storageCutoverString(v["kind"])) != "" || v["kind"] == "Pod" || v["kind"] == "ReplicaSet" {
					items = append(items, v)
				}
			}
			b, _ := json.Marshal(map[string]any{"items": items})
			fmt.Println(string(b))
			break
		}
		if strings.Contains(strings.Join(args, " "), "jsonpath") || kind == "endpointslices" {
			fallback()
		}
		v := read(kind, name)
		if v != nil {
			b, _ := json.Marshal(v)
			fmt.Println(string(b))
		} else if kind == "secret" && name != "rtk-storage-media" && name != "destination" && name != "ota-object-storage" {
			fallback()
		}
	case "patch":
		v := read(args[position+1], args[position+2])
		if v == nil {
			fail()
		}
		body, _ := io.ReadAll(os.Stdin)
		var ops []map[string]any
		if json.Unmarshal(body, &ops) != nil {
			fail()
		}
		if os.Getenv("STORAGE_TEST_PATCH_CONFLICT") == "1" {
			storageCutoverMap(v["metadata"])["resourceVersion"] = "changed"
		}
		for _, op := range ops {
			path := storageCutoverString(op["path"])
			switch op["op"] {
			case "test":
				if !reflect.DeepEqual(storageCutoverGet(v, path), op["value"]) {
					fail()
				}
			case "add":
				storageCutoverMockSet(v, path, op["value"], false)
			case "remove":
				storageCutoverMockSet(v, path, nil, true)
			default:
				fail()
			}
		}
		storageCutoverMap(v["metadata"])["resourceVersion"] = "2"
		write(v)
	case "create":
		body, _ := io.ReadAll(os.Stdin)
		var v map[string]any
		if json.Unmarshal(body, &v) != nil {
			fail()
		}
		namespace = storageCutoverString(storageCutoverGet(v, "/metadata/namespace"))
		name := storageCutoverString(storageCutoverGet(v, "/metadata/name"))
		if read(storageCutoverString(v["kind"]), name) != nil {
			fail()
		}
		storageCutoverMap(v["metadata"])["uid"] = "created-" + name
		storageCutoverMap(v["metadata"])["resourceVersion"] = "1"
		write(v)
	case "delete":
		raw := ""
		for _, a := range args {
			if strings.HasPrefix(a, "--raw=") {
				raw = strings.TrimPrefix(a, "--raw=")
			}
		}
		parts := strings.Split(raw, "/")
		namespace = parts[len(parts)-3]
		kind := strings.TrimSuffix(parts[len(parts)-2], "s")
		name := parts[len(parts)-1]
		if kind == "networkpolicie" {
			kind = "networkpolicy"
		}
		v := read(kind, name)
		body, _ := io.ReadAll(os.Stdin)
		var options map[string]any
		if json.Unmarshal(body, &options) != nil {
			fail()
		}
		if v == nil || !reflect.DeepEqual(storageCutoverGet(options, "/preconditions/uid"), storageCutoverGet(v, "/metadata/uid")) || !reflect.DeepEqual(storageCutoverGet(options, "/preconditions/resourceVersion"), storageCutoverGet(v, "/metadata/resourceVersion")) {
			fail()
		}
		if os.Remove(storageCutoverMockFile(root, kind, namespace, name)) != nil {
			fail()
		}
	case "rollout", "wait":
	default:
		fallback()
	}
	os.Exit(0)
}
func storageCutoverMockSet(object map[string]any, path string, value any, remove bool) {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	parent := storageCutoverGet(object, "/"+strings.Join(parts[:len(parts)-1], "/"))
	if len(parts) == 1 {
		parent = object
	}
	key := parts[len(parts)-1]
	switch p := parent.(type) {
	case map[string]any:
		if remove {
			delete(p, key)
		} else {
			p[key] = value
		}
	case []any:
		var i int
		_, _ = fmt.Sscanf(key, "%d", &i)
		p[i] = value
	default:
		panic("invalid test patch path")
	}
}
func TestStorageCutoverPlansAllConsumersAndRejectsUnsafeJobs(t *testing.T) {
	installStorageCutoverMock(t)
	objects := []map[string]any{}
	for _, name := range []string{"video-cloud-api", "video-cloud-api-pki", "video-cloud-videostorage", "video-cloud-cleaner", "video-cloud-clipverifier", "legacy-ota"} {
		objects = append(objects, storageCutoverFixture("Deployment", name))
	}
	objects = append(objects, storageCutoverFixture("StatefulSet", "stateful"), storageCutoverFixture("DaemonSet", "daemon"), storageCutoverFixture("CronJob", "scheduled"))
	completed := storageCutoverFixture("Job", "complete")
	completed["status"] = map[string]any{"conditions": []any{map[string]any{"type": "Complete", "status": "True"}}}
	objects = append(objects, completed)
	source := provisionObjectStore{bucket: "old", endpoint: "https://source.example", region: "us-sea", prefix: "environments/stack", prefixSet: true}
	target := deploymentStorageTarget{Bucket: "new", Endpoint: "https://destination.example", Region: "us-sea", Prefix: "environments/stack"}
	plan := func(items []map[string]any) ([]storageCutoverMutation, error) {
		b, _ := json.Marshal(map[string]any{"items": items})
		m, secrets, err := planMediaStorageConsumers(b, "stack", source, target, "new-access", "new-secret")
		if err == nil && len(secrets) != 1 {
			t.Fatalf("source credential snapshot count=%d", len(secrets))
		}
		return m, err
	}
	mutations, err := plan(objects)
	if err != nil {
		t.Fatal(err)
	}
	if len(mutations) != 10 {
		t.Fatalf("expected target Secret and 9 consumers, got %d", len(mutations))
	}
	for _, m := range mutations {
		if m.Kind == "Secret" {
			continue
		}
		for _, value := range m.After {
			env := value.([]any)
			if storageCutoverMap(env[0])["value"] != "new" || storageCutoverMap(env[len(env)-1])["value"] != "unchanged" {
				t.Fatalf("bad env patch for %s", m.Name)
			}
		}
	}
	for _, kind := range []string{"Job", "CronJob"} {
		t.Run(kind, func(t *testing.T) {
			object := storageCutoverFixture(kind, "unsafe")
			if kind == "CronJob" {
				storageCutoverMap(object["spec"])["suspend"] = false
			}
			if _, err := plan([]map[string]any{object}); err == nil {
				t.Fatal("unsafe scheduled writer accepted")
			}
		})
	}
	scaledDown := storageCutoverFixture("Deployment", "scaled-down")
	storageCutoverMap(scaledDown["spec"])["replicas"] = float64(0)
	if mutations, err := plan([]map[string]any{scaledDown}); err != nil || len(mutations) != 2 {
		t.Fatalf("scaled-down consumer was not migrated before future scale-up: %v", err)
	}
	mismatch := storageCutoverFixture("Deployment", "different-prefix")
	storageCutoverMockSet(mismatch, "/spec/template/spec/containers/0/env/3/value", "different", false)
	if _, err := plan([]map[string]any{mismatch}); err == nil {
		t.Fatal("wrong source prefix accepted")
	}
}
func TestStorageCutoverCASAndRollbackPreserveConcurrentChanges(t *testing.T) {
	root := installStorageCutoverMock(t)
	original := storageCutoverFixture("Deployment", "api")
	storageCutoverMockWrite(t, root, original)
	next := storageCutoverClone(storageCutoverGet(original, "/spec/template/spec/containers/0/env")).([]any)
	storageCutoverMap(next[0])["value"] = "new"
	mutation, err := storageCutoverMutationFor(original, map[string]any{"/spec/template/spec/containers/0/env": next})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("STORAGE_TEST_PATCH_CONFLICT", "1")
	if err := storageCutoverCAS(mutation, false); err == nil {
		t.Fatal("server-side resourceVersion race accepted")
	}
	t.Setenv("STORAGE_TEST_PATCH_CONFLICT", "")
	if err := storageCutoverCAS(mutation, false); err != nil {
		t.Fatal(err)
	}
	live, err := storageCutoverRead("Deployment", "stack-video-cloud", "api")
	if err != nil {
		t.Fatal(err)
	}
	if storageCutoverGet(live, "/spec/template/spec/containers/0/env/0/value") != "new" {
		t.Fatal("destination was not applied")
	}
	storageCutoverMap(live["spec"])["unrelated-setting"] = "preserve"
	storageCutoverMockWrite(t, root, live)
	if err := storageCutoverCAS(mutation, true); err != nil {
		t.Fatal(err)
	}
	restored, _ := storageCutoverRead("Deployment", "stack-video-cloud", "api")
	if storageCutoverGet(restored, "/spec/unrelated-setting") != "preserve" || storageCutoverGet(restored, "/spec/template/spec/containers/0/env/0/value") != "old" {
		t.Fatal("rollback lost unrelated data or source setting")
	}
	if err := storageCutoverCAS(mutation, false); err == nil {
		t.Fatal("stale resourceVersion accepted")
	}
}
func TestStorageCutoverPrivateJournalAndConditionalCreatedResourceRollback(t *testing.T) {
	root := installStorageCutoverMock(t)
	store := secretStore{Root: filepath.Join(t.TempDir(), "dev")}
	desired := map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"namespace": "stack-video-cloud", "name": "destination"}, "data": map[string]any{"secret": "never-in-argv"}}
	m, err := storageCutoverDesiredMutation(desired, nil)
	if err != nil {
		t.Fatal(err)
	}
	journal := storageCutoverJournal{Environment: "dev", Purpose: "media", ID: "operation-1", ClusterUID: "cluster-uid", Mutations: []storageCutoverMutation{m}}
	if err := saveStorageCutoverJournal(store, journal, false); err != nil {
		t.Fatal(err)
	}
	if err := applyStorageCutover(store, &journal); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(store.Root, storageCutoverJournalName("media")))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("journal permissions: %v %v", info, err)
	}
	commands, _ := os.ReadFile(filepath.Join(root, "commands"))
	if strings.Contains(string(commands), "never-in-argv") {
		t.Fatal("Secret exposed in argv")
	}
	if err := rollbackStorageJournal(store, &journal); err != nil {
		t.Fatal(err)
	}
	if live, err := storageCutoverRead("Secret", "stack-video-cloud", "destination"); err != nil || live != nil {
		t.Fatal("conditional rollback did not remove created resource")
	}
}
func TestStorageRollbackRejectsChangedMigrationProofBeforeClusterMutation(t *testing.T) {
	makeIsolatedTestSecretStore(t, "dev")
	root := installStorageCutoverMock(t)
	cfg := deploymentConfig{Environment: "dev", RuntimeRoot: t.TempDir()}
	if err := writeStorageState(mustStorageStatePath(t, cfg, storageMigrationReceiptName("media")), map[string]any{"changed": true}); err != nil {
		t.Fatal(err)
	}
	err := validateStorageRollbackData(cfg, storageCutoverJournal{Purpose: "media", MigrationSHA256: "old-proof", Mutations: []storageCutoverMutation{{Attempted: true}}})
	if err == nil || !strings.Contains(err.Error(), "reconciliation") {
		t.Fatalf("changed proof accepted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "commands")); !os.IsNotExist(err) {
		t.Fatal("cluster touched before proof validation")
	}
}

func TestStorageCutoverResolvesIndirectConsumersAndPods(t *testing.T) {
	root := installStorageCutoverMock(t)
	source := provisionObjectStore{bucket: "old", endpoint: "https://source.example", region: "us-sea", prefix: "environments/stack", prefixSet: true}
	target := deploymentStorageTarget{Bucket: "new", Endpoint: "https://destination.example", Region: "us-sea", Prefix: "environments/stack"}
	journal := storageCutoverJournal{}
	plan := func(items []map[string]any, after bool) error {
		body, _ := json.Marshal(map[string]any{"items": items})
		var rolled []storageCutoverJournal
		if after {
			rolled = append(rolled, journal)
		}
		_, _, err := planMediaStorageConsumers(body, "stack", source, target, "next", "secret", rolled...)
		return err
	}
	for _, namespace := range []string{"stack-video-cloud", "outside-stack"} {
		for _, kind := range []string{"Secret", "ConfigMap"} {
			data := "old"
			if kind == "Secret" {
				data = base64.StdEncoding.EncodeToString([]byte(data))
			}
			storageCutoverMockWrite(t, root, map[string]any{"apiVersion": "v1", "kind": kind, "metadata": map[string]any{"name": "storage-settings", "namespace": namespace, "uid": "settings-uid", "resourceVersion": "1"}, "data": map[string]any{"VIDEO_CLOUD_BLOB_BUCKET": data}})
			t.Run(namespace+"/"+kind, func(t *testing.T) {
				consumer := storageCutoverFixture("Deployment", "indirect")
				storageCutoverMap(consumer["metadata"])["namespace"] = namespace
				container := storageCutoverMap(storageCutoverGet(consumer, "/spec/template/spec/containers/0"))
				ref := "secretRef"
				if kind == "ConfigMap" {
					ref = "configMapRef"
				}
				container["envFrom"] = []any{map[string]any{ref: map[string]any{"name": "storage-settings"}}}
				container["env"] = []any{}
				if err := plan([]map[string]any{consumer}, false); err == nil {
					t.Fatal("indirect source escaped inventory")
				}
				container["env"] = []any{map[string]any{"name": "VIDEO_CLOUD_BLOB_BUCKET", "value": "new"}}
				if err := plan([]map[string]any{consumer}, false); err != nil {
					t.Fatalf("explicit env did not override envFrom: %v", err)
				}
				keyRef := "secretKeyRef"
				if kind == "ConfigMap" {
					keyRef = "configMapKeyRef"
				}
				container["env"] = []any{map[string]any{"name": "VIDEO_CLOUD_BLOB_BUCKET", "valueFrom": map[string]any{keyRef: map[string]any{"name": "storage-settings", "key": "VIDEO_CLOUD_BLOB_BUCKET"}}}}
				if err := plan([]map[string]any{consumer}, false); err == nil {
					t.Fatal("indirect valueFrom source escaped inventory")
				}
			})
		}
	}
	storageCutoverMockWrite(t, root, map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "new-settings", "namespace": "stack-video-cloud"}, "data": map[string]any{"BLOB_BUCKET": "new"}})
	container := map[string]any{"envFrom": []any{map[string]any{"configMapRef": map[string]any{"name": "storage-settings"}}, map[string]any{"prefix": "VIDEO_CLOUD_", "configMapRef": map[string]any{"name": "new-settings"}}}}
	values, _, err := storageCutoverEffectiveStorageEnv(container, "stack-video-cloud", map[string]map[string]any{})
	if err != nil || values["VIDEO_CLOUD_BLOB_BUCKET"] != "new" {
		t.Fatalf("envFrom source ordering or prefix failed: %v", err)
	}
	deployment := storageCutoverFixture("Deployment", "api")
	pod := storageCutoverFixture("Deployment", "source-pod")
	pod["kind"], pod["apiVersion"], pod["spec"] = "Pod", "v1", storageCutoverGet(pod, "/spec/template/spec")
	if err := plan([]map[string]any{deployment, pod}, false); err == nil || !strings.Contains(err.Error(), "not controlled") {
		t.Fatalf("bare source Pod accepted: %v", err)
	}
	replicaSet := map[string]any{"kind": "ReplicaSet", "metadata": map[string]any{"uid": "replica-uid", "ownerReferences": []any{map[string]any{"controller": true, "uid": "api-uid"}}}}
	storageCutoverMap(pod["metadata"])["ownerReferences"] = []any{map[string]any{"controller": true, "uid": "replica-uid"}}
	if err := plan([]map[string]any{pod, replicaSet, deployment}, false); err != nil {
		t.Fatalf("verified controller Pod blocked before rollout: %v", err)
	}
	live := storageCutoverMap(storageCutoverClone(deployment))
	storageCutoverMap(storageCutoverGet(live, "/spec/template/spec/containers/0/env/0"))["value"] = target.Bucket
	mutation, err := storageCutoverMutationFor(deployment, map[string]any{"/spec/template/spec/containers/0/env": storageCutoverGet(live, "/spec/template/spec/containers/0/env")})
	if err != nil {
		t.Fatal(err)
	}
	journal.Mutations = []storageCutoverMutation{mutation}
	if err := plan([]map[string]any{pod, replicaSet, live}, true); err == nil || !strings.Contains(err.Error(), "after rollout") {
		t.Fatalf("old Pod accepted after rollout: %v", err)
	}
	pod["status"] = map[string]any{"phase": "Succeeded"}
	if err := plan([]map[string]any{pod, replicaSet, deployment}, false); err != nil {
		t.Fatalf("completed Pod blocked cutover: %v", err)
	}
	mixed := storageCutoverFixture("Deployment", "mixed")
	mixedContainer := storageCutoverMap(storageCutoverGet(mixed, "/spec/template/spec/containers/0"))
	mixedContainer["env"] = append(mixedContainer["env"].([]any), map[string]any{"name": "VIDEO_CLOUD_OTA_BLOB_BUCKET", "value": "other"})
	if err := plan([]map[string]any{mixed}, false); err == nil || !strings.Contains(err.Error(), "another bucket") {
		t.Fatalf("mixed credential consumers accepted: %v", err)
	}
}

func storageCutoverDrainingFixture(t *testing.T) (storageCutoverJournal, []map[string]any, provisionObjectStore, deploymentStorageTarget) {
	t.Helper()
	source := provisionObjectStore{bucket: "old", endpoint: "https://source.example", region: "us-sea", prefix: "environments/stack", prefixSet: true}
	target := deploymentStorageTarget{Bucket: "new", Endpoint: "https://destination.example", Region: "us-sea", Prefix: "environments/stack"}
	deployment := storageCutoverFixture("Deployment", "api")
	pod := storageCutoverFixture("Deployment", "old-pod")
	pod["kind"], pod["apiVersion"], pod["spec"] = "Pod", "v1", storageCutoverGet(pod, "/spec/template/spec")
	pod["status"] = map[string]any{"phase": "Running"}
	storageCutoverMap(pod["metadata"])["deletionTimestamp"] = "2026-10-02T12:00:00Z"
	storageCutoverMap(pod["metadata"])["ownerReferences"] = []any{map[string]any{"controller": true, "uid": "old-replica-uid"}}
	replicaSet := map[string]any{"kind": "ReplicaSet", "metadata": map[string]any{"name": "old-replica", "namespace": "stack-video-cloud", "uid": "old-replica-uid", "ownerReferences": []any{map[string]any{"controller": true, "uid": "api-uid"}}}}
	before := storageCutoverMap(storageCutoverClone(deployment))
	for index, value := range []string{target.Bucket, target.Endpoint, target.Region, target.Prefix} {
		storageCutoverMap(storageCutoverGet(deployment, fmt.Sprintf("/spec/template/spec/containers/0/env/%d", index)))["value"] = value
	}
	m, err := storageCutoverMutationFor(before, map[string]any{"/spec/template/spec/containers/0/env": storageCutoverClone(storageCutoverGet(deployment, "/spec/template/spec/containers/0/env"))})
	if err != nil {
		t.Fatal(err)
	}
	return storageCutoverJournal{Mutations: []storageCutoverMutation{m}}, []map[string]any{deployment, replicaSet, pod}, source, target
}

func TestStorageCutoverWaitsForManagedSourcePodTermination(t *testing.T) {
	for _, test := range []struct {
		name    string
		timeout string
		goneAt  int
		wantErr string
		elapsed time.Duration
	}{
		{name: "graceful drain", timeout: "10s", goneAt: 3, elapsed: 4 * time.Second},
		{name: "deadline", timeout: "3s", wantErr: "timed out", elapsed: 3 * time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("LKE_ROLLOUT_TIMEOUT", test.timeout)
			synctest.Test(t, func(t *testing.T) {
				journal, items, source, target := storageCutoverDrainingFixture(t)
				calls := 0
				start := time.Now()
				err := waitMediaStorageConsumers(journal, "stack", source, target, "next", "secret", func() ([]byte, error) {
					calls++
					if test.goneAt > 0 && calls >= test.goneAt {
						items = items[:2]
					}
					return json.Marshal(map[string]any{"items": items})
				})
				if test.wantErr == "" && err != nil || test.wantErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantErr)) {
					t.Fatalf("unexpected drain result: %v", err)
				}
				if time.Since(start) != test.elapsed || calls != 3 {
					t.Fatalf("drain wait = %s, calls = %d", time.Since(start), calls)
				}
			})
		})
	}
}

func TestStorageCutoverDrainRejectsUnsafeConsumersAndDrift(t *testing.T) {
	for _, test := range []struct {
		name    string
		change  func([]map[string]any)
		wantErr string
	}{
		{name: "standalone", change: func(items []map[string]any) { delete(storageCutoverMap(items[2]["metadata"]), "ownerReferences") }, wantErr: "not controlled"},
		{name: "unknown owner", change: func(items []map[string]any) {
			storageCutoverMap(storageCutoverGet(items[1], "/metadata/ownerReferences/0"))["uid"] = "unselected-uid"
		}, wantErr: "not controlled"},
		{name: "missing owner chain", change: func(items []map[string]any) { storageCutoverMap(items[1]["metadata"])["uid"] = "different-replica-uid" }, wantErr: "not controlled"},
		{name: "nonterminating", change: func(items []map[string]any) { delete(storageCutoverMap(items[2]["metadata"]), "deletionTimestamp") }, wantErr: "not terminating"},
		{name: "invalid deletion timestamp", change: func(items []map[string]any) { storageCutoverMap(items[2]["metadata"])["deletionTimestamp"] = "invalid" }, wantErr: "not terminating"},
		{name: "source endpoint drift", change: func(items []map[string]any) {
			storageCutoverMap(storageCutoverGet(items[2], "/spec/containers/0/env/1"))["value"] = "https://other.example"
		}, wantErr: "binding differs"},
		{name: "source region drift", change: func(items []map[string]any) {
			storageCutoverMap(storageCutoverGet(items[2], "/spec/containers/0/env/2"))["value"] = "other-region"
		}, wantErr: "binding differs"},
		{name: "source prefix drift", change: func(items []map[string]any) {
			storageCutoverMap(storageCutoverGet(items[2], "/spec/containers/0/env/3"))["value"] = "other/prefix"
		}, wantErr: "binding differs"},
		{name: "destination drift", change: func(items []map[string]any) {
			storageCutoverMap(storageCutoverGet(items[0], "/spec/template/spec/containers/0/env/0"))["value"] = "other"
		}, wantErr: "differs while waiting"},
		{name: "controller replaced", change: func(items []map[string]any) { storageCutoverMap(items[0]["metadata"])["uid"] = "new-controller-uid" }, wantErr: "differs while waiting"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("LKE_ROLLOUT_TIMEOUT", "10s")
			synctest.Test(t, func(t *testing.T) {
				journal, items, source, target := storageCutoverDrainingFixture(t)
				test.change(items)
				calls := 0
				err := waitMediaStorageConsumers(journal, "stack", source, target, "next", "secret", func() ([]byte, error) {
					calls++
					return json.Marshal(map[string]any{"items": items})
				})
				if err == nil || !strings.Contains(err.Error(), test.wantErr) || calls != 1 {
					t.Fatalf("unsafe consumer was not rejected immediately: calls=%d err=%v", calls, err)
				}
			})
		})
	}
}

func TestStorageCutoverDrainRejectsChangesBetweenPolls(t *testing.T) {
	for _, change := range []string{"new pod", "changed owner", "new controller", "inventory failure"} {
		t.Run(change, func(t *testing.T) {
			t.Setenv("LKE_ROLLOUT_TIMEOUT", "10s")
			synctest.Test(t, func(t *testing.T) {
				journal, items, source, target := storageCutoverDrainingFixture(t)
				calls := 0
				err := waitMediaStorageConsumers(journal, "stack", source, target, "next", "secret", func() ([]byte, error) {
					calls++
					if calls == 2 {
						switch change {
						case "new pod":
							storageCutoverMap(items[2]["metadata"])["uid"] = "new-pod-uid"
						case "changed owner":
							// Even a new ReplicaSet belonging to the same selected
							// Deployment cannot adopt a previously draining Pod.
							storageCutoverMap(items[1]["metadata"])["uid"] = "new-replica-uid"
							storageCutoverMap(storageCutoverGet(items[2], "/metadata/ownerReferences/0"))["uid"] = "new-replica-uid"
						case "new controller":
							items = append(items, storageCutoverFixture("Deployment", "new-consumer"))
						case "inventory failure":
							return nil, errors.New("inventory unavailable")
						}
					}
					return json.Marshal(map[string]any{"items": items})
				})
				if err == nil || strings.Contains(err.Error(), "timed out") || calls != 2 {
					t.Fatalf("changed consumer was retried: calls=%d err=%v", calls, err)
				}
			})
		})
	}
}

type storageCutoverRecoveryFixture struct {
	cfg                             deploymentConfig
	store                           secretStore
	journal                         storageCutoverJournal
	candidate, sourcePath, mockRoot string
	setDestination                  func(string, *string)
}

// Exercise the actual rollback proof against a local HTTP S3 endpoint, a real
// source file and the same private files used by the cutover entry points.
func newStorageCutoverRecoveryFixture(t *testing.T) storageCutoverRecoveryFixture {
	t.Helper()
	clearDeploymentCredentialEnvironment(t)
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", t.TempDir())
	root := installStorageCutoverMock(t)
	cfg := deploymentConfig{Environment: "dev", RuntimeRoot: t.TempDir(), Storage: deploymentStoragePlan{RuntimeMedia: deploymentStorageTarget{Purpose: "runtime-media", Bucket: "destination", Region: "us-sea", Prefix: "environments/stack"}}}
	objects := map[string]string{"environments/stack/clips/a": "payload"}
	var mu sync.Mutex
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v4/object-storage/buckets" {
			fmt.Fprintf(w, `{"data":[{"label":"destination","region":"us-sea","s3_endpoint":%q}]}`, server.URL)
			return
		}
		if serveMigrationBucketInspection(w, r) {
			return
		}
		if r.URL.Query().Has("tagging") {
			fmt.Fprint(w, `<Tagging><TagSet/></Tagging>`)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Query().Get("list-type") == "2" {
			fmt.Fprint(w, `<ListBucketResult><IsTruncated>false</IsTruncated>`)
			for key, data := range objects {
				if strings.HasPrefix(key, r.URL.Query().Get("prefix")) {
					fmt.Fprintf(w, `<Contents><Key>%s</Key><Size>%d</Size></Contents>`, key, len(data))
				}
			}
			fmt.Fprint(w, `</ListBucketResult>`)
			return
		}
		key := strings.TrimPrefix(r.URL.Path, "/destination/")
		if data, ok := objects[key]; ok {
			fmt.Fprint(w, data)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	t.Setenv("RTK_CLOUD_LINODE_API_ROOT", server.URL+"/v4")
	sourceRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "old", "clips", "a")
	if err := os.MkdirAll(filepath.Dir(sourcePath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourcePath, []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}
	sourceProfile := filepath.Join(t.TempDir(), "source.env")
	if err := os.WriteFile(sourceProfile, []byte("LINODE_OBJ_BUCKET=old\nLINODE_OBJ_ENDPOINT=file://"+sourceRoot+"\nLINODE_OBJ_REGION=us-sea\nLINODE_OBJ_PREFIX=\nLINODE_OBJ_ACCESS_KEY_ID=old\nLINODE_OBJ_SECRET_ACCESS_KEY=old\n"), 0600); err != nil {
		t.Fatal(err)
	}
	candidate := filepath.Join(t.TempDir(), "candidate.env")
	if err := os.WriteFile(candidate, []byte("RTK_STORAGE_CANDIDATE_ENVIRONMENT=dev\nLINODE_TOKEN=token\nLINODE_MEDIA_OBJ_ACCESS_KEY_ID=next\nLINODE_MEDIA_OBJ_SECRET_ACCESS_KEY=next-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	state := deploymentStorageMigrationState{Environment: "dev", Purpose: "media", Source: "old", SourceRegion: "us-sea", SourceEndpoint: "file://" + sourceRoot, SourcePrefixExplicit: true, Destination: "destination", DestinationRegion: "us-sea", DestinationEndpoint: server.URL, Prefix: "environments/stack", Objects: map[string]storageObjectProof{"environments/stack/clips/a": otaObjectProof([]byte("payload"))}, SourceKeys: map[string]string{"clips/a": "environments/stack/clips/a"}, ObjectCount: 1, ByteCount: 7, UpdatedAt: time.Now().UTC().Format(time.RFC3339)}
	if err := writeStorageState(mustStorageStatePath(t, cfg, storageMigrationReceiptName("media")), state); err != nil {
		t.Fatal(err)
	}
	consumer := storageCutoverFixture("Deployment", "api")
	storageCutoverMockSet(consumer, "/spec/template/spec/containers/0/env/1/value", "file://"+sourceRoot, false)
	storageCutoverMockSet(consumer, "/spec/template/spec/containers/0/env/3/value", "", false)
	storageCutoverMockWrite(t, root, consumer)
	current := map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"namespace": "stack-video-cloud", "name": "rtk-storage-media", "uid": "existing-destination-key", "resourceVersion": "1", "labels": map[string]any{"rtk.realtek.com/stack": "stack", "rtk.realtek.com/storage-purpose": "media"}}, "data": map[string]any{"AWS_ACCESS_KEY_ID": "b2xk", "AWS_SECRET_ACCESS_KEY": "b2xk", "UNRELATED": "a2VlcA=="}}
	storageCutoverMockWrite(t, root, current)
	body, _ := json.Marshal(map[string]any{"items": []map[string]any{consumer}})
	source := provisionObjectStore{bucket: "old", endpoint: "file://" + sourceRoot, region: "us-sea", prefixSet: true}
	target := cfg.Storage.RuntimeMedia
	target.Endpoint = server.URL
	mutations, snapshots, err := planMediaStorageConsumers(body, "stack", source, target, "next", "next-secret")
	if err != nil {
		t.Fatal(err)
	}
	store, journal, err := beginStorageCutover(cfg, "media", sourceProfile, candidate, mutations, snapshots)
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{"LINODE_MEDIA_OBJ_ACCESS_KEY_ID": "prior-active", "LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY": "prior-secret"} {
		if err := store.write(filepath.Join("operator", "env", key), []byte(value), true); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(store.KubeconfigPath()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.KubeconfigPath(), []byte("apiVersion: v1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return storageCutoverRecoveryFixture{cfg: cfg, store: store, journal: journal, candidate: candidate, sourcePath: sourcePath, mockRoot: root, setDestination: func(key string, data *string) {
		mu.Lock()
		defer mu.Unlock()
		if data == nil {
			delete(objects, key)
		} else {
			objects[key] = *data
		}
	}}
}

func TestStorageCutoverFailurePromotionRestoresExistingSecretBeforeWorkload(t *testing.T) {
	f := newStorageCutoverRecoveryFixture(t)
	body, err := os.ReadFile(f.candidate)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.candidate, []byte(strings.Replace(string(body), "ENVIRONMENT=dev", "ENVIRONMENT=other", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	err = completeStorageCutover(f.cfg, f.candidate, "media", f.store, &f.journal, map[string]any{}, func() error { return nil })
	if err == nil || !strings.Contains(err.Error(), "workload settings restored") {
		t.Fatalf("failed promotion did not roll back: %v", err)
	}
	consumer, _ := storageCutoverRead("Deployment", "stack-video-cloud", "api")
	secret, _ := storageCutoverRead("Secret", "stack-video-cloud", "rtk-storage-media")
	if storageCutoverGet(consumer, "/spec/template/spec/containers/0/env/0/value") != "old" || storageCutoverGet(secret, "/data/AWS_ACCESS_KEY_ID") != "b2xk" || storageCutoverGet(secret, "/data/UNRELATED") != "a2VlcA==" {
		t.Fatal("failed promotion did not restore workload and preserve old Secret data")
	}
	if _, err := os.Stat(mustStorageStatePath(t, f.cfg, "storage-cutover.json")); !os.IsNotExist(err) {
		t.Fatal("failed promotion retained completion receipt")
	}
	active, _ := f.store.readOperator()
	if active["LINODE_MEDIA_OBJ_ACCESS_KEY_ID"] != "prior-active" {
		t.Fatal("failed promotion changed active credentials")
	}
	log, _ := os.ReadFile(filepath.Join(f.mockRoot, "commands"))
	lastSecret, lastWorkload := strings.LastIndex(string(log), "patch Secret"), strings.LastIndex(string(log), "patch Deployment")
	if lastSecret < 0 || lastWorkload < lastSecret {
		t.Fatal("rollback restored workloads before their old credentials")
	}
	if err := rollbackStorageJournal(f.store, &f.journal); err != nil {
		t.Fatalf("idempotent rollback failed: %v", err)
	}
}

func TestStorageCutoverExplicitRollbackAndDataDriftFence(t *testing.T) {
	for _, change := range []string{"none", "destination-write", "destination-delete", "destination-add", "source-write", "source-delete"} {
		t.Run(change, func(t *testing.T) {
			f := newStorageCutoverRecoveryFixture(t)
			if err := completeStorageCutover(f.cfg, f.candidate, "media", f.store, &f.journal, map[string]any{}, func() error { return nil }); err != nil {
				t.Fatal(err)
			}
			data := "changed"
			switch change {
			case "destination-write":
				f.setDestination("environments/stack/clips/a", &data)
			case "destination-delete":
				f.setDestination("environments/stack/clips/a", nil)
			case "destination-add":
				f.setDestination("environments/stack/clips/new", &data)
			case "source-write":
				if err := os.WriteFile(f.sourcePath, []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			case "source-delete":
				if err := os.Remove(f.sourcePath); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(f.mockRoot, "commands"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			err := rollbackStorageCutover(f.cfg, "media")
			if change != "none" {
				if err == nil || !strings.Contains(err.Error(), "writers fenced") {
					t.Fatalf("storage drift did not block rollback: %v", err)
				}
				log, _ := os.ReadFile(filepath.Join(f.mockRoot, "commands"))
				if strings.Contains(string(log), "patch ") || strings.Contains(string(log), "delete ") {
					t.Fatal("data drift was detected after cluster mutation")
				}
				consumer, _ := storageCutoverRead("Deployment", "stack-video-cloud", "api")
				if storageCutoverGet(consumer, "/spec/template/spec/containers/0/env/0/value") != "destination" {
					t.Fatal("drifted data was hidden behind source workload")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := rollbackStorageCandidate(f.cfg, f.candidate, "media"); err != nil {
				t.Fatal(err)
			}
			active, _ := f.store.readOperator()
			if active["LINODE_MEDIA_OBJ_ACCESS_KEY_ID"] != "prior-active" {
				t.Fatal("explicit rollback did not restore active credential")
			}
			if _, err := os.Stat(mustStorageStatePath(t, f.cfg, "storage-cutover.json")); !os.IsNotExist(err) {
				t.Fatal("rollback left completion receipt")
			}
		})
	}
}

func TestStorageRollbackRejectsSourceSecretOrConcurrentWorkloadDrift(t *testing.T) {
	for _, change := range []string{"source-secret", "workload", "cluster", "created-secret", "created-owner"} {
		t.Run(change, func(t *testing.T) {
			f := newStorageCutoverRecoveryFixture(t)
			if err := applyStorageCutover(f.store, &f.journal); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "source-secret":
				secret, _ := storageCutoverRead("Secret", "stack-video-cloud", "old-credentials")
				storageCutoverMap(secret["data"])["secret"] = "cm90YXRlZA=="
				storageCutoverMockWrite(t, f.mockRoot, secret)
			case "workload":
				consumer, _ := storageCutoverRead("Deployment", "stack-video-cloud", "api")
				storageCutoverMockSet(consumer, "/spec/template/spec/containers/0/env/0/value", "concurrent", false)
				storageCutoverMockWrite(t, f.mockRoot, consumer)
			case "cluster":
				f.journal.ClusterUID = "foreign-cluster"
			case "created-secret", "created-owner":
				desired := map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"namespace": "stack-video-cloud", "name": "destination"}, "data": map[string]any{"value": "original"}}
				mutation, err := storageCutoverDesiredMutation(desired, nil)
				if err != nil {
					t.Fatal(err)
				}
				extra := storageCutoverJournal{Purpose: "extra", ID: f.journal.ID, ClusterUID: f.journal.ClusterUID, Mutations: []storageCutoverMutation{mutation}}
				if err := applyStorageCutover(f.store, &extra); err != nil {
					t.Fatal(err)
				}
				f.journal.Mutations = append(f.journal.Mutations, extra.Mutations...)
				secret, _ := storageCutoverRead("Secret", "stack-video-cloud", "destination")
				if change == "created-secret" {
					storageCutoverMap(secret["data"])["value"] = "changed"
				} else {
					storageCutoverMap(secret["metadata"])["uid"] = "replacement-uid"
				}
				storageCutoverMockWrite(t, f.mockRoot, secret)
			}
			if err := rollbackStorageJournal(f.store, &f.journal); err == nil {
				t.Fatal("concurrent live change was silently overwritten")
			}
			secret, _ := storageCutoverRead("Secret", "stack-video-cloud", "rtk-storage-media")
			if storageCutoverGet(secret, "/data/AWS_ACCESS_KEY_ID") != base64.StdEncoding.EncodeToString([]byte("next")) {
				t.Fatal("rollback changed destination credentials before detecting resource drift")
			}
			if change == "created-secret" || change == "created-owner" {
				if secret, _ := storageCutoverRead("Secret", "stack-video-cloud", "destination"); secret == nil {
					t.Fatal("changed created resource was deleted")
				}
			}
		})
	}
}

func TestStorageCutoverExistingServiceKeepsDefaultsAndRollsBack(t *testing.T) {
	root := installStorageCutoverMock(t)
	live := map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"namespace": "stack-video-cloud", "name": "ota", "uid": "service-uid", "resourceVersion": "1"}, "spec": map[string]any{"clusterIP": "10.2.3.4", "selector": map[string]any{"app": "old", "operator-label": "keep"}, "ports": []any{map[string]any{"port": float64(8080)}}}}
	storageCutoverMockWrite(t, root, live)
	mutations, err := planStorageCutoverManifests([]string{"apiVersion: v1\nkind: Service\nmetadata:\n  namespace: stack-video-cloud\n  name: ota\nspec:\n  selector:\n    app: dedicated\n  ports:\n    - port: 18084\n"})
	if err != nil {
		t.Fatal(err)
	}
	store := secretStore{Root: filepath.Join(t.TempDir(), "dev")}
	journal := storageCutoverJournal{Purpose: "ota", ClusterUID: "cluster-uid", Mutations: mutations}
	if err := applyStorageCutover(store, &journal); err != nil {
		t.Fatal(err)
	}
	if err := verifyStorageCutover(journal); err != nil {
		t.Fatal(err)
	}
	updated, _ := storageCutoverRead("Service", "stack-video-cloud", "ota")
	if storageCutoverGet(updated, "/spec/clusterIP") != "10.2.3.4" || storageCutoverGet(updated, "/spec/selector/operator-label") != "keep" || storageCutoverGet(updated, "/spec/selector/app") != "dedicated" {
		t.Fatal("service cutover lost server defaults or unrelated selectors")
	}
	if err := rollbackStorageJournal(store, &journal); err != nil {
		t.Fatal(err)
	}
	restored, _ := storageCutoverRead("Service", "stack-video-cloud", "ota")
	if !reflect.DeepEqual(restored["spec"], live["spec"]) {
		t.Fatal("service rollback did not restore original spec")
	}
	secret := map[string]any{"kind": "Secret", "immutable": true, "metadata": map[string]any{"name": "immutable", "namespace": "stack-video-cloud"}, "data": map[string]any{"key": "value"}}
	if _, err := storageCutoverDesiredMutation(secret, secret); err == nil {
		t.Fatal("immutable destination Secret accepted")
	}
}

func TestStorageCutoverDedicatedOTASourceBinding(t *testing.T) {
	root := installStorageCutoverMock(t)
	profile := filepath.Join(t.TempDir(), "source.env")
	if err := os.WriteFile(profile, []byte("LINODE_OBJ_BUCKET=old\nLINODE_OBJ_REGION=us-sea\nLINODE_OBJ_ENDPOINT=https://source.example\nLINODE_OBJ_PREFIX=environments/stack\nLINODE_OBJ_ACCESS_KEY_ID=old\nLINODE_OBJ_SECRET_ACCESS_KEY=old\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"none", "bucket", "region", "endpoint", "prefix", "indirect", "absent", "sidecar"} {
		t.Run(change, func(t *testing.T) {
			workload := storageCutoverFixture("Deployment", otaServiceWorkloadName)
			index := map[string]int{"bucket": 0, "endpoint": 1, "region": 2, "prefix": 3}
			if i, ok := index[change]; ok {
				storageCutoverMockSet(workload, fmt.Sprintf("/spec/template/spec/containers/0/env/%d/value", i), "wrong", false)
			}
			if change == "indirect" {
				storageCutoverMap(storageCutoverGet(workload, "/spec/template/spec/containers/0/env/0"))["valueFrom"] = map[string]any{"secretKeyRef": map[string]any{"name": "storage", "key": "bucket"}}
			}
			if change == "absent" {
				storageCutoverMockSet(workload, "/spec/template/spec/containers", []any{}, false)
			}
			if change == "sidecar" {
				spec := storageCutoverMap(storageCutoverGet(workload, "/spec/template/spec"))
				spec["containers"] = append([]any{map[string]any{"name": "sidecar", "env": []any{map[string]any{"name": "OTHER", "value": "ignored"}}}}, spec["containers"].([]any)...)
			}
			storageCutoverMockWrite(t, root, workload)
			err := ensureOTACutoverSource("stack-video-cloud", profile, "environments/new")
			if change == "none" || change == "sidecar" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("unverified dedicated OTA source accepted")
			}
		})
	}
	if err := ensureOTACutoverSource("stack-video-cloud", filepath.Join(t.TempDir(), "missing.env"), ""); err == nil {
		t.Fatal("missing source profile accepted")
	}
}

func TestStorageCutoverOptionalReferencesAndForeignOwners(t *testing.T) {
	root := installStorageCutoverMock(t)
	cfg := map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "partial", "namespace": "stack-video-cloud"}, "data": map[string]any{"UNRELATED": "ignored"}}
	storageCutoverMockWrite(t, root, cfg)
	for _, test := range []struct {
		name      string
		container map[string]any
		wantErr   bool
	}{
		{"optional envFrom", map[string]any{"envFrom": []any{map[string]any{"configMapRef": map[string]any{"name": "missing", "optional": true}}}}, false},
		{"required envFrom", map[string]any{"envFrom": []any{map[string]any{"configMapRef": map[string]any{"name": "missing"}}}}, true},
		{"optional key", map[string]any{"env": []any{map[string]any{"name": "VIDEO_CLOUD_BLOB_BUCKET", "valueFrom": map[string]any{"configMapKeyRef": map[string]any{"name": "partial", "key": "missing", "optional": true}}}}}, false},
		{"required key", map[string]any{"env": []any{map[string]any{"name": "VIDEO_CLOUD_BLOB_BUCKET", "valueFrom": map[string]any{"configMapKeyRef": map[string]any{"name": "partial", "key": "missing"}}}}}, true},
		{"unsupported indirect field", map[string]any{"env": []any{map[string]any{"name": "VIDEO_CLOUD_BLOB_BUCKET", "valueFrom": map[string]any{"fieldRef": map[string]any{"fieldPath": "metadata.name"}}}}}, true},
		{"missing reference name", map[string]any{"envFrom": []any{map[string]any{"configMapRef": map[string]any{}}}}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := storageCutoverEffectiveStorageEnv(test.container, "stack-video-cloud", map[string]map[string]any{})
			if (err != nil) != test.wantErr {
				t.Fatalf("environment reference handling: %v", err)
			}
		})
	}
	pod := storageCutoverFixture("Deployment", "foreign-pod")
	pod["kind"], pod["spec"] = "Pod", storageCutoverGet(pod, "/spec/template/spec")
	storageCutoverMap(pod["metadata"])["ownerReferences"] = []any{map[string]any{"controller": true, "uid": "foreign-controller"}}
	body, _ := json.Marshal(map[string]any{"items": []map[string]any{storageCutoverFixture("Deployment", "api"), pod}})
	_, _, err := planMediaStorageConsumers(body, "stack", provisionObjectStore{bucket: "old", region: "us-sea", endpoint: "https://source.example", prefix: "environments/stack", prefixSet: true}, deploymentStorageTarget{Bucket: "new", Region: "us-sea", Endpoint: "https://destination.example", Prefix: "environments/stack"}, "next", "secret")
	if err == nil || !strings.Contains(err.Error(), "not controlled") {
		t.Fatalf("foreign Pod owner accepted: %v", err)
	}
}

func TestStorageCutoverFailedVerificationPreservesDestinationOnNewWrites(t *testing.T) {
	f := newStorageCutoverRecoveryFixture(t)
	err := completeStorageCutover(f.cfg, f.candidate, "media", f.store, &f.journal, map[string]any{}, func() error {
		value := "new write during rollout"
		f.setDestination("environments/stack/clips/new", &value)
		return errors.New("endpoint unavailable")
	})
	if err == nil || !strings.Contains(err.Error(), "rollback needs attention") || !strings.Contains(err.Error(), "writers fenced") {
		t.Fatalf("failed rollout rolled back across new data: %v", err)
	}
	consumer, _ := storageCutoverRead("Deployment", "stack-video-cloud", "api")
	if storageCutoverGet(consumer, "/spec/template/spec/containers/0/env/0/value") != "destination" {
		t.Fatal("new data became inaccessible after failed rollout")
	}
	raw, err := f.store.read(storageCutoverJournalName("media"))
	if err != nil {
		t.Fatal(err)
	}
	var saved storageCutoverJournal
	if json.Unmarshal([]byte(raw), &saved) != nil || saved.Status == "rolled-back" {
		t.Fatal("blocked recovery lost its applied journal")
	}
	if _, err := os.Stat(mustStorageStatePath(t, f.cfg, "storage-cutover.json")); !os.IsNotExist(err) {
		t.Fatal("failed verification left completion receipt")
	}
}

func TestStorageRollbackRejectsInvalidJournalAndProfile(t *testing.T) {
	f := newStorageCutoverRecoveryFixture(t)
	if err := rollbackStorageCutover(f.cfg, "artifacts"); err == nil {
		t.Fatal("artifact rollback used runtime path")
	}
	wrong := f.journal
	wrong.Environment = "staging"
	if err := saveStorageCutoverJournal(f.store, wrong, true); err != nil {
		t.Fatal(err)
	}
	if err := rollbackStorageCutover(f.cfg, "media"); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("foreign journal accepted: %v", err)
	}
	proof, err := os.ReadFile(mustStorageStatePath(t, f.cfg, storageMigrationReceiptName("media")))
	if err != nil {
		t.Fatal(err)
	}
	wrong = f.journal
	wrong.MigrationSHA256 = fmt.Sprintf("%x", sha256.Sum256(proof))
	wrong.DestinationFile = filepath.Join(t.TempDir(), "missing.env")
	wrong.Mutations = []storageCutoverMutation{{Attempted: true}}
	if err := validateStorageRollbackData(f.cfg, wrong); err == nil {
		t.Fatal("missing preserved candidate accepted")
	}
}

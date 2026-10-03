package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func reinitializationOTATree() []map[string]any {
	ota := storageCutoverFixture("Deployment", otaServiceWorkloadName)
	storageCutoverMap(storageCutoverGet(ota, "/spec/template/spec/containers/0"))["name"] = "otaservice"
	replica := storageCutoverMap(storageCutoverClone(ota))
	replica["kind"] = "ReplicaSet"
	replica["metadata"] = map[string]any{"name": "ota-replica", "namespace": "stack-video-cloud", "uid": "ota-replica-uid", "ownerReferences": []any{map[string]any{"controller": true, "uid": otaServiceWorkloadName + "-uid"}}}
	pod := map[string]any{"kind": "Pod", "metadata": map[string]any{"name": "ota-pod", "namespace": "stack-video-cloud", "uid": "ota-pod-uid", "ownerReferences": []any{map[string]any{"controller": true, "uid": "ota-replica-uid"}}}, "spec": storageCutoverClone(storageCutoverGet(ota, "/spec/template/spec")), "status": map[string]any{"phase": "Running"}}
	return []map[string]any{ota, replica, pod}
}

func TestReinitializeDedicatedMediaRejectsSharedDestinationSecretBeforeMutation(t *testing.T) {
	f := newReinitializeFixture(t)
	f.cfg.Storage.OTAMode = "dedicated"
	ota := reinitializationOTATree()[0]
	storageCutoverMap(storageCutoverGet(ota, "/spec/template/spec/containers/0/env/4/valueFrom/secretKeyRef"))["name"] = "rtk-storage-media"
	storageCutoverMockWrite(t, f.mockRoot, ota)
	err := f.checker.reinitializeStorage(f.cfg, "media", f.source, f.candidate, "old", false)
	if err == nil || !strings.Contains(err.Error(), "excluded OTA consumer references a destination Secret") {
		t.Fatalf("shared destination Secret accepted: %v", err)
	}
	commands, _ := os.ReadFile(filepath.Join(f.mockRoot, "commands"))
	if strings.Contains(string(commands), " patch ") || strings.Contains(string(commands), "create ") {
		t.Fatal("shared destination Secret was detected after Kubernetes mutation")
	}
	if _, err := f.store.read(storageCutoverJournalName("media")); !os.IsNotExist(err) {
		t.Fatal("unsafe plan created a journal")
	}
}

func TestReinitializeDedicatedMediaPreservesCompleteOTATree(t *testing.T) {
	f := newReinitializeFixture(t)
	f.cfg.Storage.OTAMode = "dedicated"
	objects := reinitializationOTATree()
	for _, object := range objects {
		storageCutoverMockWrite(t, f.mockRoot, object)
	}
	if err := f.store.write("operator/env/LINODE_OTA_OBJ_ACCESS_KEY_ID", []byte("unqualified-ota-key"), true); err != nil {
		t.Fatal(err)
	}
	if err := f.checker.reinitializeStorage(f.cfg, "media", f.source, f.candidate, "old", false); err != nil {
		t.Fatal(err)
	}
	for _, before := range objects {
		after, err := storageCutoverRead(storageCutoverString(before["kind"]), "stack-video-cloud", storageCutoverString(storageCutoverGet(before, "/metadata/name")))
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("media activation changed excluded OTA definition: %v", err)
		}
	}
	api, err := storageCutoverRead("Deployment", "stack-video-cloud", "api")
	if err != nil || storageCutoverGet(api, "/spec/template/spec/containers/0/env/0/value") != f.cfg.Storage.RuntimeMedia.Bucket {
		t.Fatal("media consumer was not moved")
	}
	active, err := f.store.readOperator()
	if err != nil || active["LINODE_OTA_OBJ_ACCESS_KEY_ID"] != "unqualified-ota-key" {
		t.Fatal("media activation changed OTA credentials")
	}
	if _, err := os.Stat(filepath.Join(f.cfg.RuntimeRoot, "state/storage-cutover-ota.json")); !os.IsNotExist(err) {
		t.Fatal("media activation wrote an OTA receipt")
	}
}

func TestReinitializeMediaIsolationRejectsDriftAndForeignOTA(t *testing.T) {
	for _, change := range []string{"replace deployment", "remove deployment", "change bucket", "change credential reference", "orphan pod", "new pod", "foreign OTA", "foreign OTA legacy alias", "unknown OTA binding", "status only"} {
		t.Run(change, func(t *testing.T) {
			objects := reinitializationOTATree()
			g := &storageReinitializationMediaIsolation{stack: "stack", sourceBucket: "old"}
			body, _ := json.Marshal(map[string]any{"items": objects})
			if _, err := g.filter(body); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "replace deployment":
				storageCutoverMap(objects[0]["metadata"])["uid"] = "replacement"
			case "remove deployment":
				objects = objects[1:]
			case "change bucket":
				storageCutoverMap(storageCutoverGet(objects[0], "/spec/template/spec/containers/0/env/0"))["value"] = "changed"
			case "change credential reference":
				storageCutoverMap(storageCutoverGet(objects[0], "/spec/template/spec/containers/0/env/4/valueFrom/secretKeyRef"))["name"] = "changed"
			case "orphan pod":
				delete(storageCutoverMap(objects[2]["metadata"]), "ownerReferences")
			case "new pod":
				pod := storageCutoverMap(storageCutoverClone(objects[2]))
				storageCutoverMap(pod["metadata"])["uid"] = "new-pod"
				objects = append(objects, pod)
			case "foreign OTA", "foreign OTA legacy alias", "unknown OTA binding":
				foreign := storageCutoverFixture("Deployment", "unknown-ota")
				if change != "unknown OTA binding" {
					storageCutoverMap(storageCutoverGet(foreign, "/spec/template/spec/containers/0"))["name"] = "otaservice"
					if change == "foreign OTA legacy alias" {
						storageCutoverMap(storageCutoverGet(foreign, "/spec/template/spec/containers/0/env/0"))["name"] = "LINODE_OBJ_BUCKET"
					}
				} else {
					storageCutoverMap(storageCutoverGet(foreign, "/spec/template/spec/containers/0/env/0"))["name"] = "VIDEO_CLOUD_OTA_BLOB_BUCKET"
				}
				objects = append(objects, foreign)
			case "status only":
				objects[0]["status"] = map[string]any{"readyReplicas": float64(0)}
			}
			body, _ = json.Marshal(map[string]any{"items": objects})
			_, err := g.filter(body)
			if (err == nil) != (change == "status only") {
				t.Fatalf("change %q accepted=%v error=%v", change, err == nil, err)
			}
		})
	}
}

func TestReinitializeMediaIsolationProtectsReferencedDestinationSecrets(t *testing.T) {
	for _, reference := range []any{
		map[string]any{"secretKeyRef": map[string]any{"name": "rtk-storage-media"}},
		map[string]any{"secretRef": map[string]any{"name": "rtk-storage-media"}},
		map[string]any{"secret": map[string]any{"secretName": "rtk-storage-media"}},
		map[string]any{"projected": map[string]any{"sources": []any{map[string]any{"secret": map[string]any{"name": "rtk-storage-media"}}}}},
		map[string]any{"nodePublishSecretRef": map[string]any{"name": "rtk-storage-media"}},
		map[string]any{"imagePullSecrets": []any{map[string]any{"name": "rtk-storage-media"}}},
	} {
		g := &storageReinitializationMediaIsolation{excluded: []map[string]any{{"metadata": map[string]any{"namespace": "stack-video-cloud"}, "spec": reference}}}
		if err := g.protectDestinationSecrets([]storageCutoverMutation{{Kind: "Secret", Namespace: "stack-video-cloud", Name: "rtk-storage-media"}}); err == nil {
			t.Fatal("excluded consumer's destination Secret could be overwritten")
		}
		if err := g.protectDestinationSecrets([]storageCutoverMutation{{Kind: "Secret", Namespace: "other-namespace", Name: "rtk-storage-media"}}); err != nil {
			t.Fatal("unrelated namespace Secret rejected")
		}
	}
}

func TestReinitializeMediaIsolationRetainsInactiveForeignHistory(t *testing.T) {
	for _, kind := range []string{"ReplicaSet", "Pod", "Job"} {
		object := storageCutoverFixture(kind, "old-history")
		pod := storageCutoverMap(storageCutoverGet(object, "/spec/template/spec"))
		pod["containers"] = []any{map[string]any{"name": "otaservice", "envFrom": []any{map[string]any{"secretRef": map[string]any{"name": "retired-secret"}}}}}
		if kind == "Pod" {
			object["spec"] = pod
			object["status"] = map[string]any{"phase": "Succeeded"}
		} else if kind == "Job" {
			object["status"] = map[string]any{"conditions": []any{map[string]any{"type": "Complete", "status": "True"}}}
		}
		body, _ := json.Marshal(map[string]any{"items": []map[string]any{object}})
		g := &storageReinitializationMediaIsolation{stack: "stack", sourceBucket: "old"}
		if _, err := g.filter(body); err != nil {
			t.Fatalf("inactive %s triggered irrelevant Secret lookup: %v", kind, err)
		}
	}
}

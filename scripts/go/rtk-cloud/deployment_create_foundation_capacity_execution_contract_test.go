package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestFoundationCapacityExecutionContractRetainsThreeOriginalNativeBindings(t *testing.T) {
	for _, mode := range []string{"unlimited", "finite-exact", "insufficient-capacity", "missing-claim", "template-drift", "duplicate-volume", "foreign-driver", "delete-policy", "foreign-claim-owner", "unbound-claim", "foreign-volume-owner", "unbound-volume", "too-few-allocations", "provider-denied"} {
		t.Run(mode, func(t *testing.T) {
			cfg, store, _, _ := createStateFixture(t, "dev")
			spec := deploymentplan.Object{"storageClassName": "linode-block-storage-retain", "accessModes": []any{"ReadWriteOnce"}, "resources": deploymentplan.Object{"requests": deploymentplan.Object{"storage": "10Gi"}}}
			definition := deploymentplan.Definition{Phase: "workload", Object: deploymentplan.Object{"apiVersion": "apps/v1", "kind": "StatefulSet", "metadata": deploymentplan.Object{"name": "openbao", "namespace": "video-cloud-dev-secrets"}, "spec": deploymentplan.Object{"replicas": float64(3), "volumeClaimTemplates": []any{deploymentplan.Object{"metadata": deploymentplan.Object{"name": "data"}, "spec": spec}}}}}
			child, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev", Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest(cfg), ReleaseSHA256: deploymentplan.Digest("isolated original release"), BindingsSHA256: environmentCreateStageBindingsSHA("foundation", []deploymentplan.Definition{definition})}, []deploymentplan.Definition{definition}, nil)
			if err != nil {
				t.Fatal(err)
			}
			objects := map[string]deploymentplan.Object{}
			var firstClaim, firstVolume string
			for ordinal := 0; ordinal < 3; ordinal++ {
				name, volume := fmt.Sprintf("data-openbao-%d", ordinal), fmt.Sprintf("original-pv-%d", ordinal)
				uid := fmt.Sprintf("original-claim-%d", ordinal)
				claim := deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": deploymentplan.Object{"name": name, "namespace": "video-cloud-dev-secrets", "uid": uid}, "spec": deploymentplan.Clone(spec), "status": deploymentplan.Object{"phase": "Bound"}}
				deploymentplan.Map(claim["spec"])["volumeName"] = volume
				pv := deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolume", "metadata": deploymentplan.Object{"name": volume, "uid": fmt.Sprintf("original-volume-%d", ordinal)}, "spec": deploymentplan.Object{"persistentVolumeReclaimPolicy": "Retain", "claimRef": deploymentplan.Object{"name": name, "namespace": "video-cloud-dev-secrets", "uid": uid}, "csi": deploymentplan.Object{"driver": "linodebs.csi.linode.com", "volumeHandle": fmt.Sprintf("%d-original-data", ordinal+100)}}, "status": deploymentplan.Object{"phase": "Bound"}}
				objects[deploymentplan.ID(claim)], objects[deploymentplan.ID(pv)] = claim, pv
				if ordinal == 0 {
					firstClaim, firstVolume = deploymentplan.ID(claim), deploymentplan.ID(pv)
				}
			}
			allocations, limit := 14, "unlimited"
			switch mode {
			case "finite-exact":
				limit = "20" // Nine native resources plus eleven still to allocate.
			case "insufficient-capacity":
				limit = "19"
			case "missing-claim":
				delete(objects, firstClaim)
			case "template-drift":
				deploymentplan.Map(objects[firstClaim]["spec"])["storageClassName"] = "other"
			case "duplicate-volume":
				deploymentplan.Map(objects["PersistentVolumeClaim/video-cloud-dev-secrets/data-openbao-1"]["spec"])["volumeName"] = "original-pv-0"
			case "foreign-driver":
				deploymentplan.Map(deploymentplan.Map(objects[firstVolume]["spec"])["csi"])["driver"] = "other.csi.invalid"
			case "delete-policy":
				deploymentplan.Map(objects[firstVolume]["spec"])["persistentVolumeReclaimPolicy"] = "Delete"
			case "foreign-claim-owner":
				deploymentplan.Map(deploymentplan.Map(objects[firstVolume]["spec"])["claimRef"])["uid"] = "replacement-claim"
			case "unbound-claim":
				deploymentplan.Map(objects[firstClaim]["status"])["phase"] = "Pending"
			case "foreign-volume-owner":
				delete(deploymentplan.Map(objects[firstVolume]["metadata"]), "uid")
			case "unbound-volume":
				deploymentplan.Map(objects[firstVolume]["status"])["phase"] = "Released"
			case "too-few-allocations":
				allocations = 2
			}
			path, err := deploymentLKEStatePath("dev", "account.env")
			if err != nil {
				t.Fatal(err)
			}
			relative, err := filepath.Rel(store.Root, path)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.write(relative, []byte("LKE_ACTIVE_SERVICE_LIMIT="+limit+"\n"), false); err != nil {
				t.Fatal(err)
			}
			b, _, _ := lateNativeReadContractBackend(t, store, objects)
			_, cleanup, err := b.privateKubeconfig()
			if err != nil {
				t.Fatal(err)
			}
			cleanup()
			reads := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer isolated-native-provider" {
					t.Error("capacity check changed native authorization or mutated provider")
					w.WriteHeader(403)
					return
				}
				switch r.URL.Path {
				case "/linode/instances", "/volumes", "/nodebalancers":
				default:
					t.Error("unselected capacity endpoint", r.URL.Path)
					w.WriteHeader(400)
					return
				}
				reads++
				if mode == "provider-denied" {
					w.WriteHeader(403)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"id": 1, "label": "native-one"}, map[string]any{"id": 2, "label": "native-two"}, map[string]any{"id": 3, "label": "native-three"}}, "page": 1, "pages": 1, "results": 3})
			}))
			t.Cleanup(server.Close)
			t.Setenv("RTK_CLOUD_LINODE_API_ROOT", server.URL)
			before := createBootstrapStateSnapshot(t, store.Root)
			err = verifyFoundationVolumeCapacity(t.Context(), cfg, b, child, allocations, map[string]string{"LINODE_TOKEN": "isolated-native-provider"})
			if (err == nil) != (mode == "unlimited" || mode == "finite-exact") {
				t.Fatal("unexpected original allocation capacity decision", err)
			}
			if err == nil && reads != 3 {
				t.Fatal("capacity omitted native resource classes", reads)
			}
			if !reflect.DeepEqual(before, createBootstrapStateSnapshot(t, store.Root)) {
				t.Fatal("read-only capacity check changed private allocation state")
			}
		})
	}
}

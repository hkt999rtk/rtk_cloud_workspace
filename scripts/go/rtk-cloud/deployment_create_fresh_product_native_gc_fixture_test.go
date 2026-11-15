package main

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Model only the API server's UID/RV-guarded Deployment deletion and its
// controller-owned ReplicaSet/Pod garbage collection. Production admission,
// retirement selection and completion receipts remain in the ordinary engine.
func joinedProducerRetirementBoundary(t *testing.T, f *productProtocolFixture, args []string, input []byte) ([]byte, int, bool) {
	t.Helper()
	i := slices.Index(args, "delete")
	if i < 0 || i+1 >= len(args) || !strings.HasPrefix(args[i+1], "--raw=/apis/apps/v1/namespaces/") {
		return nil, 0, false
	}
	fail := func() ([]byte, int, bool) { return nil, 1, true }
	if len(args[i:]) != 4 || args[i+2] != "-f" || args[i+3] != "-" {
		return fail()
	}
	uri, err := url.Parse(strings.TrimPrefix(args[i+1], "--raw="))
	if err != nil {
		return fail()
	}
	parts := strings.Split(strings.TrimPrefix(uri.Path, "/apis/apps/v1/namespaces/"), "/")
	if len(parts) != 3 || parts[0] != f.e.Journal.Stack+"-video-cloud" || parts[1] != "deployments" || parts[2] == "" || parts[2] == "." || parts[2] == ".." || strings.ContainsAny(parts[2], `/\`) {
		return fail()
	}
	root := os.Getenv("RTK_LATE_NATIVE_OBJECTS")
	if root == "" {
		return fail()
	}
	file := filepath.Join(root, "Deployment_"+parts[0]+"_"+parts[2]+".json")
	var current, options deploymentplan.Object
	if readExecutionJSON(file, &current) != nil || json.Unmarshal(input, &options) != nil || current["kind"] != "Deployment" || options["kind"] != "DeleteOptions" || options["apiVersion"] != "v1" || options["propagationPolicy"] != "Foreground" {
		return fail()
	}
	meta, pre := deploymentplan.Map(current["metadata"]), deploymentplan.Map(options["preconditions"])
	uid, version := deploymentplan.String(meta["uid"]), deploymentplan.String(meta["resourceVersion"])
	if uid == "" || version == "" || pre["uid"] != uid || pre["resourceVersion"] != version {
		return fail()
	}
	dry := certIssuerObjectList(options["dryRun"])
	queryDry := uri.Query().Get("dryRun")
	if len(dry) > 0 || queryDry != "" {
		if len(dry) != 1 || dry[0] != "All" || queryDry != "All" {
			return fail()
		}
		return mustProviderRecoveryJSON(current), 0, true
	}
	// First collect only same-namespace dependents of these exact owners.
	// A malformed observation cannot leave half a simulated deletion behind.
	owned := map[string]bool{"Deployment/" + uid: true}
	type objectFile struct{ path, id string }
	var remove []objectFile
	for _, kind := range []string{"ReplicaSet", "Pod"} {
		files, err := filepath.Glob(filepath.Join(root, kind+"_"+parts[0]+"_*.json"))
		if err != nil {
			return fail()
		}
		for _, path := range files {
			var object deploymentplan.Object
			if readExecutionJSON(path, &object) != nil {
				return fail()
			}
			metadata := deploymentplan.Map(object["metadata"])
			if object["kind"] != kind || metadata["namespace"] != parts[0] {
				return fail()
			}
			for _, value := range certIssuerObjectList(metadata["ownerReferences"]) {
				ref := deploymentplan.Map(value)
				if ref["controller"] == true && owned[deploymentplan.String(ref["kind"])+"/"+deploymentplan.String(ref["uid"])] {
					childUID := deploymentplan.String(metadata["uid"])
					if childUID == "" {
						return fail()
					}
					owned[kind+"/"+childUID] = true
					remove = append(remove, objectFile{path, deploymentplan.ID(object)})
					break
				}
			}
		}
	}
	remove = append(remove, objectFile{file, deploymentplan.ID(current)})
	for _, object := range remove {
		if os.Remove(object.path) != nil {
			return fail()
		}
		delete(f.b.objects, object.id)
	}
	return []byte("{}"), 0, true
}

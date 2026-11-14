package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// The original foundation and the in-place producer share the same isolated
// parent, Kubernetes API and owner. The producer has already completed its
// native child and receipt before the original foundation is re-observed.
func foundationInPlaceSuccessorFixture(t *testing.T) unifiedOpenBaoParentFixture {
	t.Helper()
	image := environmentReleaseImage{Reference: "ghcr.io/example/api@sha256:" + strings.Repeat("a", 64)}
	return newUnifiedOpenBaoParentFixture(t, image, true)
}

func TestOriginalFoundationAcceptsCompletedInPlaceSuccessor(t *testing.T) {
	f := foundationInPlaceSuccessorFixture(t)
	pod, set, err := originalCreateOpenBaoFoundationIdentity(t.Context(), f.cfg, f.store, f.execution, f.native, "kubernetes/original-openbao")
	if err != nil || pod != f.owner.PodUID || set != f.owner.StatefulSetUID {
		t.Fatalf("accepted in-place successor lost original foundation: %v", err)
	}
}

func TestOriginalFoundationRejectsChangedInPlaceSuccessor(t *testing.T) {
	f := foundationInPlaceSuccessorFixture(t)
	for _, mode := range []string{"pending-receipt", "foreign-receipt", "pod-uid", "provider-image", "initialization"} {
		t.Run(mode, func(t *testing.T) {
			journal := mustProviderRecoveryJSON(f.execution.Journal)
			t.Cleanup(func() {
				if err := json.Unmarshal(journal, &f.execution.Journal); err != nil {
					t.Fatal(err)
				}
				if err := f.execution.save(); err != nil {
					t.Fatal(err)
				}
			})
			switch mode {
			case "pending-receipt", "foreign-receipt":
				found := false
				for i := range f.execution.Journal.Entries {
					entry := &f.execution.Journal.Entries[i]
					if entry.Phase != "openbao-host-inplace" {
						continue
					}
					found = true
					if mode == "pending-receipt" {
						entry.State, entry.Receipt, entry.CompletedAt = "pending", nil, time.Time{}
					} else {
						entry.Receipt = mustProviderRecoveryJSON(openBaoInPlaceReceipt{QualificationSHA256: strings.Repeat("f", 64)})
					}
				}
				if !found || f.execution.save() != nil {
					t.Fatal("original in-place receipt not available")
				}
			case "pod-uid", "provider-image":
				id := "Pod/" + f.execution.Journal.Stack + "-secrets/openbao-0"
				path := filepath.Join(f.nativeDir, strings.ReplaceAll(id, "/", "_")+".json")
				original, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.WriteFile(path, original, 0600) })
				var object deploymentplan.Object
				if err := json.Unmarshal(original, &object); err != nil {
					t.Fatal(err)
				}
				if mode == "pod-uid" {
					deploymentplan.Map(object["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
				} else {
					managedNamedField(deploymentplan.Map(object["spec"])["containers"], "openbao")["image"] = "foreign-image"
				}
				if err := os.WriteFile(path, mustProviderRecoveryJSON(object), 0600); err != nil {
					t.Fatal(err)
				}
			case "initialization":
				path := filepath.Join(f.store.Root, "openbao", "init.json")
				original, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.WriteFile(path, original, 0600) })
				if err := os.WriteFile(path, []byte(`{"root_token":"foreign","unseal_keys_b64":["isolated-unseal-key"]}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := originalCreateOpenBaoFoundationIdentity(t.Context(), f.cfg, f.store, f.execution, f.native, "kubernetes/original-openbao"); err == nil {
				t.Fatal("changed original foundation evidence was accepted")
			}
		})
	}
}

func TestInPlaceHistoryChecksReplacementPodTarget(t *testing.T) {
	f := foundationInPlaceSuccessorFixture(t)
	receipt, found, err := completedOpenBaoInPlaceReceipt(t.Context(), f.store, f.execution)
	if err != nil || !found {
		t.Fatal("accepted original receipt unavailable", err)
	}
	history, err := verifyOpenBaoInPlaceHistory(t.Context(), f.cfg, f.store, f.execution, receipt)
	if err != nil {
		t.Fatal(err)
	}
	namespace := f.execution.Journal.Stack + "-secrets"
	set, err := f.native.Read(t.Context(), "StatefulSet/"+namespace+"/openbao")
	if err != nil {
		t.Fatal(err)
	}
	pod, err := f.native.Read(t.Context(), "Pod/"+namespace+"/openbao-0")
	if err != nil || pod == nil {
		t.Fatal("original synthetic Pod unavailable", err)
	}
	// A newly created Pod uses both images from the accepted template.
	deploymentplan.Map(pod["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
	podSpec := deploymentplan.Map(pod["spec"])
	managedNamedField(podSpec["initContainers"], "openbao-pki-install")["image"] = history.image.Reference
	if managedNamedField(podSpec["volumes"], "data") == nil {
		t.Fatal("replacement fixture lacks the original StatefulSet claim volume")
	}
	if !openBaoInPlaceReplacementMatches(history, set, pod) {
		t.Fatal("replacement Pod did not retain the accepted owner and image")
	}
	for _, mode := range []string{"uid", "provider-image", "host-image-id", "init-image", "foreign-pvc", "not-ready"} {
		t.Run(mode, func(t *testing.T) {
			changed := deploymentplan.Clone(pod)
			switch mode {
			case "uid":
				deploymentplan.Map(deploymentplan.Map(changed["metadata"])["ownerReferences"].([]any)[0])["uid"] = "foreign"
			case "provider-image":
				managedNamedField(deploymentplan.Map(changed["spec"])["containers"], "openbao")["image"] = "foreign"
			case "host-image-id":
				managedNamedField(deploymentplan.Map(changed["status"])["containerStatuses"], "openbao-pki")["imageID"] = "foreign"
			case "init-image":
				managedNamedField(deploymentplan.Map(changed["spec"])["initContainers"], "openbao-pki-install")["image"] = "foreign"
			case "foreign-pvc":
				deploymentplan.Map(managedNamedField(deploymentplan.Map(changed["spec"])["volumes"], "data")["persistentVolumeClaim"])["claimName"] = "foreign"
			case "not-ready":
				deploymentplan.Map(changed["status"])["phase"] = "Pending"
			}
			if openBaoInPlaceReplacementMatches(history, set, changed) {
				t.Fatal("replacement accepted changed identity or image")
			}
		})
	}
}

package main

import (
	"context"
	"errors"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
	"time"
)

// StatefulSet adds one Pod volume for each volumeClaimTemplate.
func addOpenBaoFixtureClaimVolumes(set, pod deploymentplan.Object) {
	spec := deploymentplan.Map(pod["spec"])
	volumes := certIssuerObjectList(spec["volumes"])
	for _, value := range certIssuerObjectList(deploymentplan.Map(set["spec"])["volumeClaimTemplates"]) {
		name := deploymentplan.String(deploymentplan.Map(deploymentplan.Map(value)["metadata"])["name"])
		volumes = append(volumes, deploymentplan.Object{"name": name, "persistentVolumeClaim": deploymentplan.Object{"claimName": name + "-openbao-0"}})
	}
	spec["volumes"] = volumes
}

func TestOpenBaoInPlaceCandidateKeepsProviderAndResumesTemplateFirst(t *testing.T) {
	_, _, inventory := existingFileOpenBaoFixture("dev", true)
	set := deploymentplan.Clone(inventory[0])
	sm := deploymentplan.Map(set["metadata"])
	template := environmentPKIPod(set)
	pod := deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": deploymentplan.Object{"name": "openbao-0", "namespace": sm["namespace"], "uid": "original-pod", "resourceVersion": "3", "ownerReferences": []any{deploymentplan.Object{"controller": true, "kind": "StatefulSet", "name": "openbao", "uid": sm["uid"]}}}, "spec": deploymentplan.Clone(template)}
	addOpenBaoFixtureClaimVolumes(set, pod)
	next := "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-openbao-pki@sha256:" + strings.Repeat("f", 64)
	base := deploymentplan.Clone(set)
	tpatch, ppatch, err := compileOpenBaoInPlaceImagePatches(base, set, pod, next)
	if err != nil || len(tpatch) != 8 || len(ppatch) != 5 {
		t.Fatal("expected narrowly guarded two-stage patch", err)
	}
	if tpatch[6].Path != "/spec/template/spec/initContainers/0/image" || tpatch[7].Path != "/spec/template/spec/containers/1/image" || ppatch[4].Path != "/spec/containers/1/image" {
		t.Fatal("unexpected update scope")
	}
	target := deploymentplan.Clone(base)
	managedNamedField(environmentPKIPod(target)["containers"], "openbao-pki")["image"] = next
	managedNamedField(environmentPKIPod(target)["initContainers"], "openbao-pki-install")["image"] = next
	tpatch, ppatch, err = compileOpenBaoInPlaceImagePatches(base, target, pod, next)
	if err != nil || len(tpatch) != 0 || len(ppatch) != 5 {
		t.Fatal("cannot resume after template update", err)
	}
	managedNamedField(deploymentplan.Map(pod["spec"])["containers"], "openbao-pki")["image"] = next
	tpatch, ppatch, err = compileOpenBaoInPlaceImagePatches(base, target, pod, next)
	if err != nil || len(tpatch) != 0 || len(ppatch) != 0 {
		t.Fatal("cannot observe finished in-place update", err)
	}
	for _, mode := range []string{"foreign-pod", "foreign-provider", "foreign-host", "foreign-pvc", "pod-first"} {
		t.Run(mode, func(t *testing.T) {
			changedSet, changedPod := deploymentplan.Clone(target), deploymentplan.Clone(pod)
			switch mode {
			case "foreign-pod":
				deploymentplan.Map(deploymentplan.Map(changedPod["metadata"])["ownerReferences"].([]any)[0])["uid"] = "foreign"
			case "foreign-provider":
				managedNamedField(deploymentplan.Map(changedPod["spec"])["containers"], "openbao")["image"] = "foreign"
			case "foreign-host":
				managedNamedField(deploymentplan.Map(changedPod["spec"])["containers"], "openbao-pki")["image"] = "foreign"
			case "foreign-pvc":
				deploymentplan.Map(managedNamedField(deploymentplan.Map(changedPod["spec"])["volumes"], "data")["persistentVolumeClaim"])["claimName"] = "foreign"
			case "pod-first":
				changedSet = deploymentplan.Clone(base)
			}
			if _, _, err := compileOpenBaoInPlaceImagePatches(base, changedSet, changedPod, next); err == nil {
				t.Fatal("accepted", mode)
			}
		})
	}
}

func TestOpenBaoInPlaceObservationRequiresOriginalProviderAndNewHost(t *testing.T) {
	_, _, inventory := existingFileOpenBaoFixture("dev", true)
	original := deploymentplan.Clone(inventory[0])
	set := deploymentplan.Clone(original)
	target := "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-openbao-pki@sha256:" + strings.Repeat("f", 64)
	managedNamedField(environmentPKIPod(set)["containers"], "openbao-pki")["image"] = target
	managedNamedField(environmentPKIPod(set)["initContainers"], "openbao-pki-install")["image"] = target
	meta := deploymentplan.Map(set["metadata"])
	pod := deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": deploymentplan.Object{"name": "openbao-0", "namespace": meta["namespace"], "uid": "original-pod", "resourceVersion": "3", "ownerReferences": []any{deploymentplan.Object{"controller": true, "kind": "StatefulSet", "name": "openbao", "uid": meta["uid"]}}}, "spec": deploymentplan.Clone(environmentPKIPod(original)), "status": deploymentplan.Object{"conditions": []any{deploymentplan.Object{"type": "Ready", "status": "True"}}, "containerStatuses": []any{deploymentplan.Object{"name": "openbao", "ready": true, "containerID": "provider-id"}, deploymentplan.Object{"name": "openbao-pki", "ready": true, "containerID": "new-host-id", "imageID": target}}, "initContainerStatuses": []any{deploymentplan.Object{"name": "openbao-pki-install", "state": deploymentplan.Object{"terminated": deploymentplan.Object{"exitCode": float64(0)}}}}}}
	addOpenBaoFixtureClaimVolumes(original, pod)
	managedNamedField(deploymentplan.Map(pod["spec"])["containers"], "openbao-pki")["image"] = target
	q := openBaoInPlaceQualification{TargetImage: target, PodUID: "original-pod", ProviderContainerID: "provider-id", OldHostContainerID: "old-host-id"}
	owner, err := validateOpenBaoInPlaceObservation(q, original, set, pod, "new-host-id")
	if err != nil || owner.PodUID != q.PodUID || owner.ContainerID != "new-host-id" || owner.Backend != "pvc" {
		t.Fatal("valid observation rejected", err)
	}
	for _, mode := range []string{"pod", "provider", "host", "digest", "init", "not-ready", "remote"} {
		t.Run(mode, func(t *testing.T) {
			p := deploymentplan.Clone(pod)
			s := deploymentplan.Map(p["status"])
			switch mode {
			case "pod":
				deploymentplan.Map(p["metadata"])["uid"] = "replacement"
			case "provider":
				managedNamedField(s["containerStatuses"], "openbao")["containerID"] = "restarted"
			case "host":
				managedNamedField(s["containerStatuses"], "openbao-pki")["containerID"] = "old-host-id"
			case "digest":
				managedNamedField(s["containerStatuses"], "openbao-pki")["imageID"] = "wrong"
			case "init":
				deploymentplan.Map(deploymentplan.Map(managedNamedField(s["initContainerStatuses"], "openbao-pki-install")["state"])["terminated"])["exitCode"] = float64(1)
			case "not-ready":
				deploymentplan.Map(certIssuerObjectList(s["conditions"])[0])["status"] = "False"
			case "remote":
				h := managedNamedField(deploymentplan.Map(p["spec"])["containers"], "openbao-pki")
				h["env"] = append(certIssuerObjectList(h["env"]), deploymentplan.Object{"name": "PKI_STATE_BACKEND", "value": "openbao"})
			}
			if _, err := validateOpenBaoInPlaceObservation(q, original, set, p, "new-host-id"); err == nil {
				t.Fatal("accepted", mode)
			}
		})
	}
}

func TestOpenBaoInPlaceReceiptDoesNotCompleteOnFailedVerification(t *testing.T) {
	_, e, _, _ := freshStageFixture(t)
	q := openBaoInPlaceQualification{Environment: e.Journal.Environment, ParentSHA: e.Journal.PlanSHA256}
	key := "openbao-host-inplace-" + deploymentplan.Digest(q)
	now := time.Now().UTC()
	if err := e.BeginEntry(key, "openbao-host-inplace", openBaoInPlaceIntent(q), now); err != nil {
		t.Fatal(err)
	}
	receipt := openBaoInPlaceReceipt{QualificationSHA256: deploymentplan.Digest(q), HostContainerID: "new-host"}
	fail := func(context.Context) error { return errors.New("failed native owner verification") }
	pass := func(context.Context) error { return nil }
	if err := completeOpenBaoInPlaceReceipt(t.Context(), e, key, receipt, fail, pass, now.Add(time.Second)); err == nil {
		t.Fatal("failed verification accepted")
	}
	entry := createOpenBaoACKCompletionEntry(e.Journal, key)
	if entry == nil || entry.State != "pending" || len(entry.Receipt) != 0 {
		t.Fatal("failed verification wrote receipt")
	}
	if err := completeOpenBaoInPlaceReceipt(t.Context(), e, key, receipt, pass, pass, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if entry = createOpenBaoACKCompletionEntry(e.Journal, key); entry == nil || entry.State != "completed" {
		t.Fatal("verified receipt not completed")
	}
}

func TestOpenBaoInPlaceOldRevisionRequiresExactAcceptedOwner(t *testing.T) {
	for _, mode := range []string{"exact", "pod-uid", "set-uid", "host-id", "host-image", "not-ready", "provider-drift", "init-drift", "no-proof"} {
		t.Run(mode, func(t *testing.T) {
			p, defs, live := openBaoPodQualificationFixture(t, "dev")
			set, pod := live[0], live[1]
			addOpenBaoFixtureClaimVolumes(set, pod)
			host := managedNamedField(environmentPKIPod(p.Resources[0].Desired)["containers"], "openbao-pki")
			ref := deploymentplan.String(host["image"])
			old := "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-openbao-pki@sha256:" + strings.Repeat("b", 64)
			original := deploymentplan.Clone(set)
			managedNamedField(environmentPKIPod(original)["containers"], "openbao-pki")["image"] = old
			managedNamedField(environmentPKIPod(original)["initContainers"], "openbao-pki-install")["image"] = old
			managedNamedField(deploymentplan.Map(pod["spec"])["initContainers"], "openbao-pki-install")["image"] = old
			status := deploymentplan.Map(pod["status"])
			status["containerStatuses"] = []any{deploymentplan.Object{"name": "openbao", "ready": true, "containerID": "provider-id"}, deploymentplan.Object{"name": "openbao-pki", "ready": true, "containerID": "same-host", "imageID": ref}}
			status["initContainerStatuses"] = []any{deploymentplan.Object{"name": "openbao-pki-install", "state": deploymentplan.Object{"terminated": deploymentplan.Object{"exitCode": float64(0)}}}}
			q := openBaoInPlaceQualification{TargetImage: ref, PodUID: "original-pod", ProviderContainerID: "provider-id", OldHostContainerID: "old-host"}
			approval := openBaoInPlaceOwner{Namespace: deploymentplan.String(deploymentplan.Map(set["metadata"])["namespace"]), PodName: "openbao-0", PodUID: "original-pod", ContainerID: "same-host", StatefulSetUID: "original-controller", ReleaseImage: environmentReleaseImage{Reference: ref}, qualification: q, original: original}
			switch mode {
			case "pod-uid":
				approval.PodUID = "foreign"
			case "set-uid":
				approval.StatefulSetUID = "foreign"
			case "host-id":
				approval.ContainerID = "foreign"
			case "host-image":
				approval.ReleaseImage.Reference = "foreign"
			case "not-ready":
				managedNamedField(deploymentplan.Map(pod["status"])["containerStatuses"], "openbao-pki")["ready"] = false
			case "provider-drift":
				managedNamedField(deploymentplan.Map(pod["spec"])["containers"], "openbao")["image"] = "foreign"
			case "init-drift":
				managedNamedField(deploymentplan.Map(pod["spec"])["initContainers"], "openbao-pki-install")["image"] = "foreign"
			}
			var err error
			if mode == "no-proof" {
				err = qualifyEnvironmentOnDeleteOpenBaoPlan(p, defs, live)
			} else {
				err = qualifyEnvironmentOnDeleteOpenBaoPlan(p, defs, live, approval)
			}
			if err != nil {
				t.Fatal(err)
			}
			want := "update"
			if mode == "exact" {
				want = "retain"
			}
			if p.Resources[0].Action != want {
				t.Fatal("wrong OnDelete decision", mode, p.Resources[0].Action)
			}
		})
	}
}

package main

import (
	"os"
	"path/filepath"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// These dev packages were qualified and installed through scoped operations.
// Their accepted journals can authorize retaining the original slot, without
// classifying the package as a candidate application or database owner.
func environmentRetainedDevImageSlot(release environmentRelease, key string, image environmentReleaseImage) (id, container string) {
	if release.Environment != "dev" || release.Stack != "video-cloud-dev" ||
		!strings.HasPrefix(image.Reference, "ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:") ||
		!immutableEnvironmentImage.MatchString(image.Reference) || image.SourceRevision != "" || image.OTARegistration != nil {
		return "", ""
	}
	switch key {
	case "LKE_PKI_CONTROLLER_IMAGE":
		return "Deployment/" + release.Stack + "-video-cloud/pki-controller", "pki-controller"
	case "LKE_PKI_MANAGEMENT_IMAGE":
		return "Deployment/" + release.Stack + "-account-manager/account-manager", "pkimanagement"
	}
	return "", ""
}

func isEnvironmentRetainedDevImage(release environmentRelease, key string, image environmentReleaseImage) bool {
	id, _ := environmentRetainedDevImageSlot(release, key, image)
	return id != "" && environmentPreflightDigest.MatchString(image.RetainedPlanSHA256)
}

func environmentReleaseSlotImage(object deploymentplan.Object, container string) string {
	fields, err := environmentPKINamedFields(environmentPKIPod(object)["containers"])
	if err != nil {
		return ""
	}
	return deploymentplan.String(fields[container]["image"])
}

func retainedDevImageBlocker(role string) error {
	return environmentReleaseFailure(nil, "deployment.release.source", role, "dev image lacks accepted scoped evidence for the unchanged original container", "retain the accepted scoped plan and journal, or select an image with formal CI and gitlink provenance")
}

func acceptedEnvironmentRetainedDevImage(store secretStore, sha, id, uid, slot, ref string) bool {
	if store.Environment != "dev" || !environmentPreflightDigest.MatchString(sha) || !environmentBootstrapOwnerUUID.MatchString(uid) {
		return false
	}
	dir := filepath.Join(store.Root, "deployment", "rollback", sha)
	if validateEnvironmentPreflightDirectories(store, dir) != nil {
		return false
	}
	var candidate deploymentplan.Plan
	if readEnvironmentPreflightJSONWithCanonical(filepath.Join(dir, "plan.json"), &candidate, true) != nil ||
		candidate.SHA256() != sha || candidate.Operation != "scoped" || candidate.Environment != "dev" || candidate.Stack != "video-cloud-dev" {
		return false
	}
	for _, r := range candidate.Resources {
		old := environmentReleaseSlotImage(r.Baseline, slot)
		if r.ID == id && r.UID == uid && r.Action == "update" && deploymentplan.ID(r.Desired) == id &&
			deploymentplan.ID(r.Baseline) == id && deploymentplan.Map(r.Baseline["metadata"])["uid"] == uid &&
			environmentReleaseSlotImage(r.Desired, slot) == ref && old != ref && immutableEnvironmentImage.MatchString(old) {
			j, found, err := candidate.ReadExecution(store.Root)
			return err == nil && found && j.Accepted && j.Owners[id] == uid
		}
	}
	return false
}

func bindEnvironmentRetainedDevImages(store secretStore, release environmentRelease, live []deploymentplan.Object) (environmentRelease, error) {
	var rollback []os.DirEntry
	for _, role := range []string{"LKE_PKI_CONTROLLER_IMAGE", "LKE_PKI_MANAGEMENT_IMAGE"} {
		image := release.Images[role]
		id, slot := environmentRetainedDevImageSlot(release, role, image)
		if id == "" {
			continue
		}
		uid := ""
		for _, object := range live {
			if deploymentplan.ID(object) == id {
				meta := deploymentplan.Map(object["metadata"])
				if uid != "" || meta["deletionTimestamp"] != nil || environmentReleaseSlotImage(object, slot) != image.Reference {
					return release, retainedDevImageBlocker(role)
				}
				uid = deploymentplan.String(meta["uid"])
			}
		}
		if !environmentBootstrapOwnerUUID.MatchString(uid) {
			return release, retainedDevImageBlocker(role)
		}
		if rollback == nil {
			dir := filepath.Join(store.Root, "deployment", "rollback")
			if validateEnvironmentPreflightDirectories(store, dir) != nil {
				return release, retainedDevImageBlocker(role)
			}
			var err error
			if rollback, err = os.ReadDir(dir); err != nil {
				return release, retainedDevImageBlocker(role)
			}
			images := make(map[string]environmentReleaseImage, len(release.Images))
			for key, value := range release.Images {
				images[key] = value
			}
			release.Images = images
		}
		image.RetainedPlanSHA256 = ""
		for _, entry := range rollback {
			if !entry.IsDir() || !environmentPreflightDigest.MatchString(entry.Name()) {
				continue
			}
			var j deploymentplan.Journal
			if readEnvironmentPreflightJSONWithCanonical(filepath.Join(store.Root, "deployment", "rollback", entry.Name(), "journal.json"), &j, true) == nil && j.Accepted && j.Owners[id] == uid &&
				acceptedEnvironmentRetainedDevImage(store, entry.Name(), id, uid, slot, image.Reference) {
				image.RetainedPlanSHA256 = entry.Name()
				break
			}
		}
		if image.RetainedPlanSHA256 == "" {
			return release, retainedDevImageBlocker(role)
		}
		release.Images[role] = image
	}
	return release, nil
}

func verifyEnvironmentRetainedDevImages(store secretStore, release environmentRelease, plan *deploymentplan.Plan, live map[string]deploymentplan.Object) error {
	for role, image := range release.Images {
		if image.RetainedPlanSHA256 == "" {
			continue
		}
		if !isEnvironmentRetainedDevImage(release, role, image) || plan.Operation != "upgrade" || plan.Environment != release.Environment || plan.Stack != release.Stack {
			return retainedDevImageBlocker(role)
		}
		id, slot := environmentRetainedDevImageSlot(release, role, image)
		current := live[id]
		metadata := deploymentplan.Map(current["metadata"])
		uid := deploymentplan.String(metadata["uid"])
		if metadata["deletionTimestamp"] != nil || environmentReleaseSlotImage(current, slot) != image.Reference ||
			!acceptedEnvironmentRetainedDevImage(store, image.RetainedPlanSHA256, id, uid, slot, image.Reference) {
			return retainedDevImageBlocker(role)
		}
		found := false
		for _, resource := range plan.Resources {
			if resource.Action == "retire" {
				continue
			}
			pod := environmentPKIPod(resource.Desired)
			if resource.Desired["kind"] == "Pod" {
				pod = deploymentplan.Map(resource.Desired["spec"])
			} else if resource.Desired["kind"] == "CronJob" {
				job := deploymentplan.Map(deploymentplan.Map(resource.Desired["spec"])["jobTemplate"])
				pod = environmentPKIPod(job)
			}
			for _, group := range []string{"containers", "initContainers", "ephemeralContainers"} {
				for _, raw := range certIssuerObjectList(pod[group]) {
					c := deploymentplan.Map(raw)
					if c["image"] != image.Reference {
						continue
					}
					if found || group != "containers" || resource.ID != id || c["name"] != slot ||
						resource.UID != uid || (resource.Action != "retain" && resource.Action != "update") ||
						environmentReleaseSlotImage(resource.Baseline, slot) != image.Reference {
						return retainedDevImageBlocker(role)
					}
					found = true
				}
			}
		}
		if !found {
			return retainedDevImageBlocker(role)
		}
	}
	return nil
}

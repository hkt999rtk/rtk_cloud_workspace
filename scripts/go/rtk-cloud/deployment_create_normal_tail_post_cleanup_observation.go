package main

import (
	"context"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Only the authenticated accepted correction adds this observation role. The
// original Program release remains immutable and other API consumers retain it.
func observeCreateNormalTailPostCleanupBindings(ctx context.Context, cfg deploymentConfig, e *environmentCreateExecution, release environmentRelease, live []deploymentplan.Object) (environmentIdentityBindings, error) {
	r, err := readCreateNormalTailPostCleanupCorrection(cfg, e.Store, e)
	if err != nil {
		return environmentIdentityBindings{}, err
	}
	effective, err := postCleanupControllerObservationRelease(release, r, live)
	if err != nil {
		return environmentIdentityBindings{}, err
	}
	return observeEnvironmentTargetBindings(ctx, cfg, effective, "create", live)
}

func postCleanupControllerObservationRelease(original environmentRelease, r *createNormalTailPostCleanupCorrection, live []deploymentplan.Object) (environmentRelease, error) {
	if _, exists := original.Images["LKE_PKI_CONTROLLER_IMAGE"]; exists {
		return original, postCleanupCorrectionBlock("original release unexpectedly contains corrective controller authority")
	}
	if r == nil || r.Source.ControllerImage == "" || !r.Journals["apply"].Accepted {
		return original, nil
	}
	p, j := r.Plans["apply"], r.Journals["apply"]
	if p == nil {
		return original, postCleanupCorrectionBlock("accepted controller correction lacks its exact plan")
	}
	id := "Deployment/" + original.Stack + "-video-cloud/pki-controller"
	uid := j.Owners[id]
	if uid == "" {
		return original, postCleanupCorrectionBlock("accepted controller correction lacks original owner")
	}
	matched := 0
	for _, object := range live {
		if deploymentplan.ID(object) != id {
			continue
		}
		matched++
		if deploymentplan.Map(object["metadata"])["uid"] != uid || deploymentplan.Map(object["metadata"])["deletionTimestamp"] != nil {
			return original, postCleanupCorrectionBlock("corrected controller owner changed")
		}
		containers := certIssuerObjectList(environmentPKIPod(object)["containers"])
		if len(containers) != 1 || deploymentplan.Map(containers[0])["name"] != "pki-controller" || deploymentplan.Map(containers[0])["image"] != r.Source.ControllerImage {
			return original, postCleanupCorrectionBlock("corrected controller image or executable changed")
		}
	}
	if matched != 1 {
		return original, postCleanupCorrectionBlock("corrected controller is absent or ambiguous")
	}
	effective := original
	effective.Images = make(map[string]environmentReleaseImage, len(original.Images)+1)
	for key, value := range original.Images {
		effective.Images[key] = value
	}
	effective.Images["LKE_PKI_CONTROLLER_IMAGE"] = environmentReleaseImage{Reference: r.Source.ControllerImage}
	return effective, nil
}

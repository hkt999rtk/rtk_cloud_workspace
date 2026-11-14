package main

import (
	"errors"
	"fmt"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
)

type openBaoImagePatch struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value"`
}

// compileOpenBaoInPlaceImagePatches admits only the original template or its
// exact two-image successor, and only the original Pod or its single-sidecar
// successor. Every mutation tests the original UID, resourceVersion and image.
func compileOpenBaoInPlaceImagePatches(original, controller, pod deploymentplan.Object, image string) ([]openBaoImagePatch, []openBaoImagePatch, error) {
	bad := func() ([]openBaoImagePatch, []openBaoImagePatch, error) {
		return nil, nil, errors.New("original OpenBao in-place owner, template or image changed")
	}
	if original == nil || controller == nil || pod == nil || !immutableEnvironmentImage.MatchString(image) || !strings.HasPrefix(image, "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-openbao-pki@sha256:") {
		return bad()
	}
	om, cm, pm := deploymentplan.Map(original["metadata"]), deploymentplan.Map(controller["metadata"]), deploymentplan.Map(pod["metadata"])
	if deploymentplan.ID(original) != deploymentplan.ID(controller) || original["kind"] != "StatefulSet" || om["name"] != "openbao" || om["uid"] == nil || om["uid"] != cm["uid"] || deploymentplan.String(cm["resourceVersion"]) == "" || deploymentplan.String(pm["namespace"]) != deploymentplan.String(cm["namespace"]) || pm["name"] != "openbao-0" || envObjectReplicas(original) != 1 || deploymentplan.Map(deploymentplan.Map(original["spec"])["updateStrategy"])["type"] != "OnDelete" || validateOpenBaoPodOwner(controller, pod) != nil {
		return bad()
	}
	source := environmentPKIPod(original)
	srcContainers, srcInit := certIssuerObjectList(source["containers"]), certIssuerObjectList(source["initContainers"])
	find := func(list []any, name string) (int, deploymentplan.Object) {
		index := -1
		var found deploymentplan.Object
		for i, raw := range list {
			c := deploymentplan.Map(raw)
			if c["name"] == name {
				if index != -1 {
					return -1, nil
				}
				index, found = i, c
			}
		}
		return index, found
	}
	hostIndex, host := find(srcContainers, "openbao-pki")
	initIndex, init := find(srcInit, "openbao-pki-install")
	_, provider := find(srcContainers, "openbao")
	if len(srcContainers) != 2 || len(srcInit) != 1 || host == nil || init == nil || provider == nil || source["shareProcessNamespace"] != true {
		return bad()
	}
	old := deploymentplan.String(host["image"])
	if !immutableEnvironmentImage.MatchString(old) || old != init["image"] || old == image {
		return bad()
	}
	target := deploymentplan.Clone(original)
	desired := environmentPKIPod(target)
	managedNamedField(desired["containers"], "openbao-pki")["image"] = image
	managedNamedField(desired["initContainers"], "openbao-pki-install")["image"] = image
	before, after := deploymentplan.Matches(original, controller), deploymentplan.Matches(target, controller)
	if before == after {
		return bad()
	}
	podSpec := deploymentplan.Clone(deploymentplan.Map(pod["spec"]))
	if podSpec == nil {
		return bad()
	}
	podHostIndex, podHost := find(certIssuerObjectList(podSpec["containers"]), "openbao-pki")
	if podHost == nil || podHostIndex != hostIndex {
		return bad()
	}
	actual := deploymentplan.String(podHost["image"])
	if actual != old && actual != image || actual == image && before {
		return bad()
	}
	podHost["image"] = old
	originalPod := deploymentplan.Clone(pod)
	originalPod["spec"] = podSpec
	if !managedOpenBaoPodMatches(original, originalPod) {
		return bad()
	}
	var template, running []openBaoImagePatch
	if before {
		initPath := fmt.Sprintf("/spec/template/spec/initContainers/%d", initIndex)
		hostPath := fmt.Sprintf("/spec/template/spec/containers/%d", hostIndex)
		template = []openBaoImagePatch{{"test", "/metadata/uid", cm["uid"]}, {"test", "/metadata/resourceVersion", cm["resourceVersion"]}, {"test", initPath + "/name", "openbao-pki-install"}, {"test", hostPath + "/name", "openbao-pki"}, {"test", initPath + "/image", old}, {"test", hostPath + "/image", old}, {"replace", initPath + "/image", image}, {"replace", hostPath + "/image", image}}
	}
	if actual == old {
		path := fmt.Sprintf("/spec/containers/%d", hostIndex)
		running = []openBaoImagePatch{{"test", "/metadata/uid", pm["uid"]}, {"test", "/metadata/resourceVersion", pm["resourceVersion"]}, {"test", path + "/name", "openbao-pki"}, {"test", path + "/image", old}, {"replace", path + "/image", image}}
	}
	return template, running, nil
}

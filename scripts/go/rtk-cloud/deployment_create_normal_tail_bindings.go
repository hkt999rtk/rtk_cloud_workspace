package main

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// A completed handoff retains the accepted cleanup allocation. Observing the
// older unsuffixed record cannot select a retired bootstrap session again.
func applyCreateNormalTailCleanupBinding(ctx context.Context, cfg deploymentConfig, h *createNormalTailHandoff, bindings *environmentIdentityBindings, live []deploymentplan.Object) error {
	bad := func() error {
		return environmentPKITransitionBlocker("Deployment/"+cfg.Values["CLOUD_STACK_NAME"]+"-video-cloud/pki-controller", "completed handoff cleanup record, original owners or current Controller reference differs")
	}
	if h == nil || h.Execution == nil || bindings == nil || h.Execution.Store.Environment != cfg.Environment || h.Execution.Check(ctx) != nil || h.Pins["owner"] != deploymentplan.Digest(h.Execution.owner) || bindings.Environment != cfg.Environment || bindings.Stack != h.Execution.Journal.Stack || h.Bindings.Environment != bindings.Environment || h.Bindings.Stack != bindings.Stack {
		return bad()
	}
	// This is the already authenticated handoff output, not a search through
	// historical sessions or a replay of their expired authorization windows.
	raw, err := readBoundedEnvironmentFile(filepath.Join(h.Execution.Store.Root, managedLateRedisDurableBase(h.Execution.Journal.PlanSHA256), "bindings.json"), true)
	var captured environmentIdentityBindings
	if err != nil || h.Pins["bindings"] != environmentPreflightBytesSHA256(raw) || decodeEnvironmentJSON(raw, &captured) != nil || deploymentplan.Digest(captured) != deploymentplan.Digest(h.Bindings) || captured.BootstrapCleanup == nil || validateEnvironmentBootstrapCleanupBinding(captured.BootstrapCleanup, cfg.Environment) != nil {
		return bad()
	}
	expected := *captured.BootstrapCleanup
	owners := captured.Owners
	if current, currentOwners, err := readCreateNormalTailProductCleanupBinding(cfg, h, captured); err != nil {
		return err
	} else if current != nil {
		expected, owners = *current, currentOwners
	}
	name := firstNonEmpty(expected.ConfigMapName, "pki-bootstrap-cleanup-binding")
	namespace := bindings.Stack + "-video-cloud"
	cmID, controllerID := "ConfigMap/"+namespace+"/"+name, "Deployment/"+namespace+"/pki-controller"
	var cm, controller deploymentplan.Object
	for _, object := range live {
		switch deploymentplan.ID(object) {
		case cmID:
			if cm != nil {
				return bad()
			}
			cm = object
		case controllerID:
			if controller != nil {
				return bad()
			}
			controller = object
		}
	}
	for id, object := range map[string]deploymentplan.Object{cmID: cm, controllerID: controller} {
		uid := owners[id]
		if object == nil || !environmentBootstrapOwnerUUID.MatchString(uid) || bindings.Owners[id] != uid || deploymentplan.Map(object["metadata"])["uid"] != uid || deploymentplan.Map(object["metadata"])["deletionTimestamp"] != nil {
			return bad()
		}
	}
	if cm["immutable"] != true || bindings.Owners["Namespace//"+namespace] != expected.NamespaceUID || captured.Owners["Namespace//"+namespace] != expected.NamespaceUID {
		return bad()
	}
	var actual environmentBootstrapCleanupBinding
	data := deploymentplan.Map(cm["data"])
	if decodeEnvironmentJSON([]byte(deploymentplan.String(data["binding.json"])), &actual) != nil {
		return bad()
	}
	actual.KubernetesCA = base64.StdEncoding.EncodeToString([]byte(deploymentplan.String(data["ca.crt"])))
	actual.ConfigMapName = expected.ConfigMapName
	if deploymentplan.Digest(actual) != deploymentplan.Digest(expected) {
		return bad()
	}
	pod := environmentPKIPod(controller)
	volumes, volumeErr := environmentPKINamedFields(pod["volumes"])
	containers, containerErr := environmentPKIPodContainers(pod)
	container := containers["containers/pki-controller"]
	mounts, mountErr := environmentPKINamedFields(container["volumeMounts"])
	if volumeErr != nil || containerErr != nil || mountErr != nil || pod["serviceAccountName"] != "pki-controller" || pod["automountServiceAccountToken"] != false || deploymentplan.Map(volumes["bootstrap-cleanup"]["configMap"])["name"] != name || mounts["bootstrap-cleanup"]["mountPath"] != "/run/bootstrap-cleanup" || mounts["bootstrap-cleanup"]["readOnly"] != true || mounts["bootstrap-cleanup"]["subPath"] != nil || mounts["bootstrap-cleanup"]["subPathExpr"] != nil {
		return bad()
	}
	settings, settingErr := environmentPKINamedFields(container["env"])
	if settingErr != nil {
		return bad()
	}
	for key, value := range map[string]string{"PKI_BOOTSTRAP_CLEANUP_BINDING": "/run/bootstrap-cleanup/binding.json", "PKI_BOOTSTRAP_KUBERNETES_CA_FILE": "/run/bootstrap-cleanup/ca.crt", "PKI_BOOTSTRAP_KUBERNETES_TOKEN_FILE": "/run/bootstrap-cleanup-api/token"} {
		if deploymentplan.String(settings[key]["value"]) != value || settings[key]["valueFrom"] != nil {
			return bad()
		}
	}
	if err := h.Execution.Check(ctx); err != nil {
		return err
	}
	bindings.BootstrapCleanup = &actual
	return nil
}

// A genuine product session replaces the bootstrap cleanup allocation under the
// same original Controller. Select only its accepted controls, never a live
// ConfigMap name or a future installed-output artifact.
func readCreateNormalTailProductCleanupBinding(cfg deploymentConfig, h *createNormalTailHandoff, captured environmentIdentityBindings) (*environmentBootstrapCleanupBinding, map[string]string, error) {
	e := h.Execution
	bad := func() (*environmentBootstrapCleanupBinding, map[string]string, error) {
		return nil, nil, environmentPKITransitionBlocker("Deployment/"+e.Journal.Stack+"-video-cloud/pki-controller", "product cleanup lacks its original admitted session and accepted controls")
	}
	if _, err := os.Lstat(createNormalTailProgramPath(e)); os.IsNotExist(err) {
		return nil, nil, nil
	} else if err != nil {
		return bad()
	}
	program, err := readCreateNormalTailProgram(e.Store, e)
	if err != nil || program.HandoffSHA256 != deploymentplan.Digest(h.Pins) || program.Products == nil {
		return bad()
	}
	l := program.Products
	stage := "product-identities-" + productIdentityScope(l)[:12] + "-session-controls"
	start, end := -1, -1
	for i, entry := range e.Journal.Entries {
		if entry.Key == "normal-tail/program" {
			if start >= 0 {
				return bad()
			}
			start = i + 1
		}
		if entry.Key == "kubernetes/"+stage {
			if end >= 0 || entry.State != "completed" {
				return bad()
			}
			end = i + 1
		}
	}
	if end < 0 {
		return nil, nil, nil
	}
	if start < 0 || end <= start {
		return bad()
	}
	// Historical cumulative policies belong to this literal completed prefix,
	// rather than later enrollment/finalization records in the current journal.
	view := *e
	view.Journal = e.Journal
	view.Journal.Entries = e.Journal.Entries[:end]
	prefix := view.Journal.Entries[start:]
	n, _, err := validateCreateNormalTailPublicTLSStages(e.Store, &view, program.PublicTLS, prefix)
	if err != nil || n > len(prefix) {
		return bad()
	}
	consumed, _, err := validateCreateNormalTailProductStagesWithConfig(cfg, e.Store, &view, l, prefix[n:])
	if err != nil {
		return nil, nil, err
	}
	if consumed != len(prefix)-n {
		return bad()
	}
	var session environmentProductIdentitySession
	if readProductStageJSON(e.Store, l, "product-session.json", &session) != nil {
		return bad()
	}
	expected := session.Ceremony.CleanupBinding
	if renewed, er := readProductSessionRenewal(e.Store, e, l); er != nil {
		return nil, nil, er
	} else if renewed != nil {
		stage = productSessionRenewalStage(l, renewed.Source, "controls")
		expected = renewed.Installed.Ceremony.CleanupBinding
	}
	p, j, err := acceptedCreateStage(e.Store, e, "kubernetes/"+stage)
	namespace := e.Journal.Stack + "-video-cloud"
	controllerID := "Deployment/" + namespace + "/pki-controller"
	cmID := "ConfigMap/" + namespace + "/" + expected.ConfigMapName
	if err != nil || p == nil || expected.ConfigMapName == "" || validateEnvironmentBootstrapCleanupBinding(&expected, cfg.Environment) != nil || captured.BootstrapCleanup == nil || expected.NamespaceUID != captured.BootstrapCleanup.NamespaceUID || expected.KubernetesCA != captured.BootstrapCleanup.KubernetesCA || j.Owners[controllerID] != captured.Owners[controllerID] || l.SourceOwners[controllerID] != captured.Owners[controllerID] || !environmentBootstrapOwnerUUID.MatchString(j.Owners[cmID]) {
		return bad()
	}
	owners := make(map[string]string, len(captured.Owners)+1)
	for id, uid := range captured.Owners {
		owners[id] = uid
	}
	owners[cmID] = j.Owners[cmID]
	return &expected, owners, nil
}

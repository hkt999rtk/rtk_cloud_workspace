package main

import (
	"context"
	"path/filepath"
	"slices"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func openBaoStartupForwardStage(original *deploymentplan.Plan) string {
	return "transport-server-startup-forward-" + original.SHA256()[:12]
}

// The original bootstrap failed after issuing the server identity. A new Job
// may only prepare its public startup snapshot from that retained identity;
// it must never run the signing or identity enrollment command again.
func compileOpenBaoStartupForward(original *deploymentplan.Plan, journal deploymentplan.Journal, live []deploymentplan.Object) (*deploymentplan.Plan, error) {
	bad := func() (*deploymentplan.Plan, error) {
		return nil, createProviderRecoveryBlock("OpenBao startup recovery requires the exact failed original enrollment Job")
	}
	if original == nil || journal.PlanSHA256 != original.SHA256() || journal.Accepted || journal.InFlight != "" || len(journal.Completed) != 0 || len(journal.Owners) != 1 {
		return bad()
	}
	prefix := "Job/" + original.Stack + "-secrets/pki-enroll-openbao-server-"
	if !strings.HasPrefix(journal.Failed, prefix) || !environmentBootstrapOwnerUUID.MatchString(journal.Owners[journal.Failed]) {
		return bad()
	}
	var definitions []deploymentplan.Definition
	found := false
	for _, resource := range original.Resources {
		object := deploymentplan.Clone(resource.Desired)
		if resource.ID == journal.Failed {
			found = true
			pod := environmentPKIPod(object)
			containers := certIssuerObjectList(pod["containers"])
			if len(containers) != 1 {
				return bad()
			}
			container := deploymentplan.Map(containers[0])
			command := certIssuerObjectList(container["command"])
			if len(command) != 2 || command[0] != "/app/openbaopkihost" || command[1] != "bootstrap" || mqttFoundationEnv(container, "OPENBAO_PKI_STARTUP_TRUST") != "" {
				return bad()
			}
			state := mqttFoundationEnv(container, "OPENBAO_PKI_HOST_IDENTITY_STATE")
			if !filepath.IsAbs(state) || filepath.Clean(state) != state {
				return bad()
			}
			metadata := deploymentplan.Map(object["metadata"])
			name := deploymentplan.String(metadata["name"]) + "-startup-" + original.SHA256()[:8]
			if len(name) > 63 {
				return bad()
			}
			metadata["name"] = name
			container["command"] = []any{"/app/openbaopkihost", "prepare-startup"}
			container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(map[string]string{"OPENBAO_PKI_STARTUP_TRUST": filepath.Join(filepath.Dir(state), "startup-trust.json")}))
		}
		definitions = append(definitions, deploymentplan.Definition{Object: object, Phase: resource.Phase, Dependencies: slices.Clone(resource.Dependencies)})
	}
	if !found {
		return bad()
	}
	inputs := original.Inputs
	inputs.BindingsSHA256 = environmentCreateStageBindingsSHA(openBaoStartupForwardStage(original), definitions)
	return deploymentplan.Compile(inputs, definitions, live)
}

func qualifyOpenBaoStartupForward(ctx context.Context, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend) (*deploymentplan.Plan, *deploymentplan.Plan, error) {
	original, err := readEnvironmentCreateStageInput(store, e, "transport-server-enrollment")
	if err != nil || original == nil {
		return original, nil, err
	}
	entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/transport-server-enrollment")
	if entry == nil {
		return nil, nil, createProviderRecoveryBlock("original server enrollment parent is unavailable")
	}
	var journal deploymentplan.Journal
	var exists bool
	if entry.State == "pending" {
		journal, exists, err = readEnvironmentCreateStageProgress(store, e, "transport-server-enrollment", original)
	} else {
		var receipt struct {
			Forward  string `json:"actual_qualified_forward_stage"`
			Original string `json:"original_failed_plan_sha256"`
		}
		if (entry.State != "allocated" && entry.State != "completed") || decodeEnvironmentJSON(entry.Receipt, &receipt) != nil || receipt.Original != original.SHA256() || receipt.Forward != "kubernetes/"+openBaoStartupForwardStage(original) {
			return nil, nil, createProviderRecoveryBlock("original server enrollment forward receipt changed")
		}
		if _, _, err := acceptedCreateStage(store, e, receipt.Forward); err != nil {
			return nil, nil, err
		}
		journal, exists, err = original.ReadExecution(store.Root)
	}
	if err != nil || !exists {
		return nil, nil, createProviderRecoveryBlock("original server enrollment child journal is unavailable")
	}
	failed, err := b.Read(ctx, journal.Failed)
	if err != nil || failed == nil || deploymentplan.Map(failed["metadata"])["uid"] != journal.Owners[journal.Failed] {
		return nil, nil, createProviderRecoveryBlock("original failed OpenBao enrollment Job owner changed")
	}
	bound := false
	for _, resource := range original.Resources {
		if resource.ID == journal.Failed && deploymentplan.Matches(resource.Desired, failed) {
			bound = true
		}
	}
	if !bound {
		return nil, nil, createProviderRecoveryBlock("original failed OpenBao enrollment Job body changed")
	}
	conditions := certIssuerObjectList(deploymentplan.Map(failed["status"])["conditions"])
	terminal := false
	for _, raw := range conditions {
		condition := deploymentplan.Map(raw)
		terminal = terminal || condition["type"] == "Failed" && condition["status"] == "True"
	}
	if !terminal {
		return nil, nil, createProviderRecoveryBlock("original OpenBao enrollment Job is not terminally failed")
	}
	live, err := b.Inventory(ctx, original.Stack)
	if err != nil {
		return nil, nil, err
	}
	forward, err := compileOpenBaoStartupForward(original, journal, live)
	if err != nil {
		return nil, nil, err
	}
	previous, err := readEnvironmentCreateStageInput(store, e, openBaoStartupForwardStage(original))
	if err != nil {
		return nil, nil, err
	}
	if previous != nil {
		if previous.Inputs != forward.Inputs {
			return nil, nil, createProviderRecoveryBlock("saved OpenBao startup continuation differs from the original Job")
		}
		if entry.State == "completed" {
			_, err = verifyAcceptedStageAfterRecordedHistory(ctx, store, e, b, "kubernetes/"+openBaoStartupForwardStage(original))
			return original, previous, err
		}
		return original, previous, previous.AdmitResumable(ctx, b, store.Root)
	}
	return original, forward, forward.Admit(ctx, b)
}

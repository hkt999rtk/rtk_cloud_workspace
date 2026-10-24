package main

import (
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Resolve only native public outputs into the frozen consumer catalog. The
// initial target publishes registration ACKs; the final target removes those
// temporary settings after the genuine session finalization.
func resolveCreateNormalTailProductDefinitions(cfg deploymentConfig, l *createNormalTailProductLifecycle, installed *environmentProductIdentitySession) ([]deploymentplan.Definition, error) {
	return resolveCreateNormalTailProductTarget(cfg, l, installed, true)
}

func resolveCreateNormalTailProductFinalDefinitions(cfg deploymentConfig, l *createNormalTailProductLifecycle, installed *environmentProductIdentitySession) ([]deploymentplan.Definition, error) {
	return resolveCreateNormalTailProductTarget(cfg, l, installed, false)
}

func resolveCreateNormalTailProductTarget(cfg deploymentConfig, l *createNormalTailProductLifecycle, installed *environmentProductIdentitySession, bootstrap bool) ([]deploymentplan.Definition, error) {
	bad := func() ([]deploymentplan.Definition, error) {
		return nil, environmentCreateBootstrapBlocker("resolved product target differs from its frozen program and genuine native outputs")
	}
	if l == nil || installed == nil || installed.Schema != "rtk-cloud.product-identities/v1" || installed.ParentSHA256 != l.ParentSHA256 || installed.PlanSHA256 != productIdentityScope(l) || installed.OwnerSHA256 != l.OwnerSHA256 || len(installed.Outputs) != len(l.Outputs) || len(l.SourceDefinitions) == 0 || !environmentBootstrapOwnerUUID.MatchString(installed.Ceremony.BootstrapSessionID) || installed.Ceremony.Root.ID != l.Root.ID || installed.Ceremony.Root.RootFingerprint != l.Root.RootFingerprint || installed.Ceremony.Root.Certificate != l.Root.Certificate || !environmentBootstrapOwnerUUID.MatchString(installed.Ceremony.Intermediate.ID) || installed.Ceremony.Intermediate.RootFingerprint != l.Root.RootFingerprint || !l.RequiresSuccessor && installed.Ceremony.Intermediate.ID != l.Intermediate.ID {
		return bad()
	}
	outputs := map[string]environmentCreateProductIdentity{}
	owners := map[string]string{}
	for _, output := range l.Outputs {
		if outputs[output.ID].ID != "" {
			return bad()
		}
		outputs[output.ID] = output
	}
	for _, receipt := range installed.Outputs {
		output, ok := outputs[receipt.ResourceID]
		if !ok || receipt.Subject != output.Subject || !environmentBootstrapOwnerUUID.MatchString(receipt.SecretUID) || !environmentBootstrapOwnerUUID.MatchString(receipt.ClaimUID) || !environmentPreflightDigest.MatchString(receipt.CertificateSHA256) || owners[receipt.ResourceID] != "" {
			return bad()
		}
		owners[receipt.ResourceID] = receipt.SecretUID
	}
	// Enrollment is closed before normal target observation. Only the five
	// consumer registration ACK settings remain temporary in the initial target.
	controls, err := productIdentityControlDefinitions(cfg, l, installed.Ceremony, false)
	if err != nil {
		return nil, err
	}
	var trust []deploymentplan.Definition
	if l.RequiresSuccessor {
		trust, err = productIdentityTrustDefinitions(l, installed.Ceremony.Intermediate)
		if err != nil {
			return nil, err
		}
	}
	replacements := map[string]deploymentplan.Definition{}
	for _, definition := range append(controls, trust...) {
		id := deploymentplan.ID(definition.Object)
		if id == "" || replacements[id].Object != nil {
			return bad()
		}
		replacements[id] = definition
	}
	var resolved []deploymentplan.Definition
	seen := map[string]bool{}
	consumers := map[string]int{}
	for _, source := range l.SourceDefinitions {
		id := deploymentplan.ID(source.Object)
		if seen[id] {
			return bad()
		}
		seen[id] = true
		definition := source
		definition.Object = deploymentplan.Clone(source.Object)
		definition.Dependencies = append([]string(nil), source.Dependencies...)
		if replacement, ok := replacements[id]; ok {
			definition.Object = deploymentplan.Clone(replacement.Object)
			definition.Retain = replacement.Retain
			delete(replacements, id)
		}
		if err := resolveCreateNormalTailProductACK(definition.Object, outputs, consumers, installed.Ceremony, bootstrap); err != nil {
			return nil, err
		}
		resolved = append(resolved, definition)
	}
	// Generated cleanup RBAC and its exact immutable binding are part of the
	// declared producer's output; they are not reconstructed from live bodies.
	for _, definition := range append(controls, trust...) {
		id := deploymentplan.ID(definition.Object)
		if _, ok := replacements[id]; ok {
			resolved = append(resolved, definition)
			delete(replacements, id)
		}
	}
	for id := range outputs {
		if consumers[id] != 1 || owners[id] == "" {
			return bad()
		}
	}
	placementSession := installed.Ceremony.BootstrapSessionID
	if installed.EnrollmentSessionID != "" {
		placementSession = installed.EnrollmentSessionID
	}
	if err := bindEnvironmentManagedMQTTConsumers(resolved, placementSession); err != nil {
		return nil, err
	}
	return resolved, nil
}

func resolveCreateNormalTailProductACK(object deploymentplan.Object, outputs map[string]environmentCreateProductIdentity, consumers map[string]int, service environmentCreateServiceCeremony, bootstrap bool) error {
	pod := environmentPKIPod(object)
	if pod == nil {
		return nil
	}
	meta := deploymentplan.Map(object["metadata"])
	volumes := map[string]string{}
	for _, raw := range certIssuerObjectList(pod["volumes"]) {
		v := deploymentplan.Map(raw)
		name := deploymentplan.String(deploymentplan.Map(v["secret"])["secretName"])
		id := "Secret/" + deploymentplan.String(meta["namespace"]) + "/" + name
		if _, ok := outputs[id]; ok {
			volume := deploymentplan.String(v["name"])
			if volume == "" || volumes[volume] != "" {
				return environmentCreateBootstrapBlocker("product identity mount is ambiguous")
			}
			volumes[volume] = id
		}
	}
	for _, raw := range certIssuerObjectList(pod["containers"]) {
		container := deploymentplan.Map(raw)
		identity := ""
		for _, rawMount := range certIssuerObjectList(container["volumeMounts"]) {
			id := volumes[deploymentplan.String(deploymentplan.Map(rawMount)["name"])]
			if id != "" {
				if identity != "" && identity != id {
					return environmentCreateBootstrapBlocker("one product consumer cannot acknowledge another identity")
				}
				identity = id
			}
		}
		if identity == "" {
			continue
		}
		consumers[identity]++
		if consumers[identity] != 1 {
			return environmentCreateBootstrapBlocker("product identity has multiple registration consumers")
		}
		container["env"] = environmentCreateServiceWithoutEnv(container["env"], "PKI_BOOTSTRAP_CONTROLLER_URL", "PKI_BOOTSTRAP_ISSUER_ID", "PKI_BOOTSTRAP_SESSION_ID")
		if bootstrap {
			parts := strings.Split(identity, "/")
			container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_CONTROLLER_URL": "https://pki-controller." + parts[1] + ".svc:18446", "PKI_BOOTSTRAP_ISSUER_ID": service.Intermediate.ID, "PKI_BOOTSTRAP_SESSION_ID": service.BootstrapSessionID}))
		}
	}
	return nil
}

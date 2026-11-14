package main

import (
	"context"
	"errors"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Registry-backed provider TLS verifies revocation at each connection/sweep;
// it has no file-cache ACK. Keep this evidence distinct from cached consumers.
type maintenanceRegistryTransportProof struct {
	Consumer, Namespace, PodUID, Container, Scope string
	RootIssuer, RootSHA256, RootCRLSHA256         string
	HelperImage, NativeReadbackSHA256             string
	CheckedAt                                     time.Time
}

func (r *maintenanceRuntime) verifyRegistryTransportConsumer(ctx context.Context, inventory crlMaintenanceInventory, issuers map[string]environmentCreateRegisteredIssuer, records map[string]createRootCRLRecord, target environmentCreateRegisteredIssuer, consumer string) (bool, error) {
	if target.Domain != "openbao_tls" {
		return false, nil
	}
	for _, c := range inventory.Consumers {
		if c.ConsumerID == consumer && issuers[c.IssuerID].Domain == "openbao_tls" {
			return false, nil // Declared caches retain their exact installation/ACK contract.
		}
	}
	for _, c := range inventory.Consumers {
		if !c.Custody || c.ConsumerID != consumer || issuers[c.IssuerID].Domain == "openbao_tls" {
			continue
		}
		pod, err := r.read(ctx, "Pod/"+c.Namespace+"/"+c.Pod)
		if err != nil || deploymentplan.Map(pod["metadata"])["uid"] != c.PodUID {
			return false, errors.New("registry transport consumer original Pod changed")
		}
		source := managedNamedField(deploymentplan.Map(pod["spec"])["containers"], c.Container)
		pin := mqttFoundationEnv(source, "PKI_STATE_OPENBAO_SERVER_PKI_ROOT_SHA256")
		if mqttFoundationEnv(source, "PKI_STATE_BACKEND") != "openbao" || !environmentPreflightDigest.MatchString(pin) || !maintenanceConsumerRunning(pod, c.Container) {
			return false, errors.New("registry transport consumer lacks its running original managed OpenBao profile")
		}
		var root environmentCreateRegisteredIssuer
		tlsRecords := map[string]string{}
		for id, issuer := range issuers {
			if issuer.Domain != "openbao_tls" {
				continue
			}
			tlsRecords[id] = records[id].Digest
			if issuer.Kind == "root" && issuer.RootFingerprint == pin {
				if root.ID != "" {
					return false, errors.New("registry transport consumer Root is ambiguous")
				}
				root = issuer
			}
		}
		if root.ID == "" || target.Kind == "root" && target.ID != root.ID || target.Kind != "root" && target.Document["parent_issuer_id"] != root.ID {
			return false, errors.New("registry transport consumer Root differs from its selected native issuer")
		}
		key := deploymentplan.Digest(deploymentplan.Object{"consumer": consumer, "pod_uid": c.PodUID, "container": c.Container, "spec": pod["spec"], "records": tlsRecords, "image": r.image})
		if _, ok := r.registryTransports[key]; ok {
			return true, nil
		}
		// Read an existing public record through this original logical owner's
		// scoped native reader. Requiring the managed Root explicitly disables
		// bootstrap trust for this probe without editing any workload setting.
		state, err := r.custodyPublicCRLWithManagedRoot(ctx, pod, c, issuers[c.IssuerID], nil, pin)
		if err != nil {
			return false, err
		}
		after, err := r.snapshot(ctx, root)
		if err != nil || after.Prior.Digest != records[root.ID].Digest {
			return false, errors.New("OpenBao TLS Root CRL changed during native managed transport verification")
		}
		if r.registryTransports == nil {
			r.registryTransports = map[string]maintenanceRegistryTransportProof{}
		}
		r.registryTransports[key] = maintenanceRegistryTransportProof{Consumer: consumer, Namespace: c.Namespace, PodUID: c.PodUID, Container: c.Container,
			Scope: mqttFoundationEnv(source, "PKI_STATE_RECORD_PREFIX"), RootIssuer: root.ID, RootSHA256: pin, RootCRLSHA256: after.Prior.Digest,
			HelperImage: r.image, NativeReadbackSHA256: deploymentplan.Digest(state), CheckedAt: time.Now().UTC()}
		return true, nil
	}
	return false, nil
}

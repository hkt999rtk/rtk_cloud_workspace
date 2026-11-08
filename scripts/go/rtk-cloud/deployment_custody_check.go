package main

import (
	"context"
	"encoding/json"
	"errors"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func (c *compiledEnvironmentDeployment) newCheckRuntime(ctx context.Context) *deploymentCheckRuntime {
	return newEnvironmentCheckRuntime(ctx, c.Configuration, c.Store, &c.Backend, c.Release.Images["LKE_PKI_CONTROLLER_IMAGE"].Reference)
}

// Checkers consume selected settings/images and original generation facts. This
// initializes only a reader, never provider configuration, keys or a writer Lease.
func newEnvironmentCheckRuntime(ctx context.Context, cfg deploymentConfig, store secretStore, backend *environmentKubernetesBackend, image string) *deploymentCheckRuntime {
	r := newDeploymentCheckRuntime(ctx, cfg.Environment)
	if backend != nil {
		r = newFrozenDeploymentCheckRuntime(ctx, cfg.Environment, *backend)
	}
	if cfg.Values["PKI_STATE_BACKEND"] != "openbao" {
		return r
	}
	bad := func() *deploymentCheckRuntime {
		r.custodyError = errors.New("selected environment custody inspection inputs are unavailable")
		return r
	}
	if store.Environment != cfg.Environment || store.Root == "" {
		return bad()
	}
	var selected environmentKubernetesBackend
	if backend != nil {
		selected = *backend
	} else {
		content, err := readBoundedEnvironmentFile(store.KubeconfigPath(), true)
		if err != nil {
			return bad()
		}
		selected, err = newEnvironmentKubernetesBackend(string(content), cfg.Environment)
		if err != nil {
			return bad()
		}
		selected.Store = store
	}
	if selected.Store.Root != store.Root || selected.Store.Environment != cfg.Environment {
		return bad()
	}
	if image == "" {
		operator, err := store.readOperator()
		if err != nil {
			return bad()
		}
		image = operator["LKE_PKI_CONTROLLER_IMAGE"]
	}
	if !immutableEnvironmentImage.MatchString(image) {
		return bad()
	}
	selected.KubeconfigContent = append([]byte(nil), selected.KubeconfigContent...)
	// The ordinary checker and independent helper share one selected credential
	// snapshot, including a health invocation that started from a canonical path.
	r = newFrozenDeploymentCheckRuntime(ctx, cfg.Environment, selected)
	r.custodyReader = &maintenanceRuntime{cfg: cfg, store: store, backend: selected, image: image}
	return r
}

// Find this exact original workload without asking its failed container to boot.
// No image-version equality or unrelated workload health is required here.
func (r *maintenanceRuntime) checkConsumerCRL(ctx context.Context, namespace, target, container string, entry livePKICRLManifestEntry) (environmentCustodyCRLState, string, error) {
	var empty environmentCustodyCRLState
	bad := func() (environmentCustodyCRLState, string, error) {
		return empty, "", errors.New("CRL check lacks its original unique workload, custody scope or issuer")
	}
	id := "Deployment/" + namespace + "/" + target
	owner, err := r.read(ctx, id)
	uid := deploymentplan.String(deploymentplan.Map(owner["metadata"])["uid"])
	if err != nil || uid == "" {
		return bad()
	}
	raw, err := r.command(ctx, nil, "get", "pods", "--namespace", namespace, "-o", "json")
	var pods struct {
		Items []deploymentplan.Object `json:"items"`
	}
	if err != nil || json.Unmarshal(raw, &pods) != nil {
		return bad()
	}
	var selected *crlMaintenanceConsumer
	for _, pod := range pods.Items {
		meta := deploymentplan.Map(pod["metadata"])
		if meta["namespace"] != namespace || meta["deletionTimestamp"] != nil {
			continue
		}
		source := managedNamedField(deploymentplan.Map(pod["spec"])["containers"], container)
		if mqttFoundationEnv(source, "PKI_STATE_BACKEND") != "openbao" {
			continue
		}
		c := crlMaintenanceConsumer{Namespace: namespace, Pod: deploymentplan.String(meta["name"]), PodUID: deploymentplan.String(meta["uid"]), Container: container, StatePath: entry.StatePath, IssuerID: entry.Issuer.ID, Custody: true}
		parentID, parent, err := r.consumerDeployment(ctx, c)
		if err != nil || parentID != id || deploymentplan.Map(parent["metadata"])["uid"] != uid {
			continue
		}
		if selected != nil {
			return bad()
		}
		c.ConsumerID, err = r.custodyConsumerID(ctx, namespace, c.Pod, container, source, entry.StatePath)
		if err != nil {
			return empty, "", err
		}
		selected = &c
	}
	if selected == nil {
		return bad()
	}
	issuer := environmentCreateRegisteredIssuer{ID: entry.Issuer.ID, Environment: entry.Issuer.Environment, Domain: entry.Issuer.Domain, Status: entry.Issuer.Status, Certificate: entry.Issuer.Certificate, RootFingerprint: entry.Issuer.Fingerprint, Document: map[string]any{"certificate_fingerprint_sha256": entry.Issuer.Fingerprint}}
	state, err := r.installedCRL(ctx, *selected, issuer)
	return state, selected.ConsumerID, err
}

func (r *deploymentCheckRuntime) committedCRLState(namespace, environment, target, container string, entry livePKICRLManifestEntry) ([]byte, string, error) {
	if r == nil || r.environment != environment || r.custodyReader == nil {
		return nil, "", errors.New("remote CRL check requires its selected environment custody context")
	}
	if r.custodyError != nil {
		return nil, "", r.custodyError
	}
	type evidence struct {
		State    environmentCustodyCRLState `json:"state"`
		Consumer string                     `json:"consumer"`
	}
	// The invocation's frozen target and original workload are checked by the
	// native reader. Reuse that public observation for now/window validation;
	// each caller still independently validates its requested expiration cutoff.
	key, _ := json.Marshal([]string{"custody-crl", namespace, target, container, entry.StatePath, entry.Issuer.ID, entry.Issuer.Fingerprint})
	raw, err := r.cached(string(key), func() ([]byte, error) {
		state, consumer, err := r.custodyReader.checkConsumerCRL(r.ctx, namespace, target, container, entry)
		if err != nil {
			return nil, err
		}
		return json.Marshal(evidence{State: state, Consumer: consumer})
	})
	if err != nil {
		return nil, "", err
	}
	var result evidence
	if decodeEnvironmentJSON(raw, &result) != nil || !environmentCreateConsumerID.MatchString(result.Consumer) {
		return nil, "", errors.New("committed CRL inspection returned invalid public evidence")
	}
	state, err := json.Marshal(result.State)
	return state, result.Consumer, err
}

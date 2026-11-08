package main

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const environmentCustodyTrustName = "pki-custody-openbao-trust"
const environmentCustodyTrustPath = "/run/pki-custody-openbao"

// Public, generation-bound counterpart of native pkitrust.CustodyTransportTrust.
// This snapshot contains no signing key, token or workload identity material.
type environmentCustodyTransportTrust struct {
	Schema            string `json:"schema"`
	Environment       string `json:"environment"`
	Generation        string `json:"generation"`
	ServerName        string `json:"server_name"`
	BootstrapPEM      string `json:"bootstrap_pem,omitempty"`
	ManagedRootID     string `json:"managed_root_id,omitempty"`
	ManagedRootSHA256 string `json:"managed_root_sha256,omitempty"`
	ManagedPEM        string `json:"managed_pem,omitempty"`
	ManagedOnly       bool   `json:"managed_only"`
}

// This producer belongs to the admitted create executor, never preflight. A
// resumed foundation republishes its latest original handoff instead of
// reintroducing a bootstrap anchor after managed-only admission was recorded.
func publishEnvironmentCreateCustodyTrust(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, managed *environmentCreateRegisteredIssuer, managedOnly bool, apply environmentCreateBootstrapApply) error {
	if cfg.Values["PKI_STATE_BACKEND"] != "openbao" {
		return nil
	}
	e := runtime.Execution
	if e == nil || e.Store.Environment != cfg.Environment || apply == nil {
		return environmentCreateBootstrapBlocker("custody trust publication requires the original admitted create executor")
	}
	if err := e.Check(ctx); err != nil {
		return err
	}
	// Forward recovery may have a different execution plan. Workload custody
	// remains bound to the original admitted bootstrap generation.
	generation := runtime.ExecutionSHA256
	if !environmentPreflightDigest.MatchString(generation) {
		return environmentCreateBootstrapBlocker("custody trust requires the original bootstrap generation reference")
	}
	originalCAPath, err := e.Store.safePath(filepath.Join("deployment", "create", "material", generation, "openbao-transport", "ca.crt"))
	if err != nil || runtime.OpenBao.CACertPath != originalCAPath {
		return environmentCreateBootstrapBlocker("custody trust must use the selected generation's original OpenBao foundation authority")
	}
	ca, err := readBoundedEnvironmentFile(originalCAPath, true)
	if err != nil {
		return environmentCreateBootstrapBlocker("original OpenBao foundation authority is unavailable for custody trust publication")
	}
	relative := filepath.Join("deployment", "create", generation, "custody", "transport.json")
	path, err := e.Store.safePath(relative)
	if err != nil {
		return err
	}
	_, statErr := os.Lstat(path)
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	if statErr == nil {
		previous, err := readBoundedEnvironmentInputLimit(path, true, false, 256<<10)
		if err != nil {
			return err
		}
		var old environmentCustodyTransportTrust
		if json.Unmarshal(previous, &old) != nil || old.Schema != "rtk.custody.transport/v1" || old.Environment != cfg.Environment || old.Generation != generation || old.ServerName != "openbao."+cfg.Values["CLOUD_STACK_NAME"]+"-secrets.svc" || (old.BootstrapPEM != "" && old.BootstrapPEM != string(ca)) || (!old.ManagedOnly && old.BootstrapPEM == "") {
			return environmentCreateBootstrapBlocker("recorded custody trust does not belong to the original environment and foundation authority")
		}
		if old.ManagedPEM != "" {
			if managed != nil && (managed.ID != old.ManagedRootID || managed.RootFingerprint != old.ManagedRootSHA256 || managed.Certificate != old.ManagedPEM) {
				return environmentCreateBootstrapBlocker("creation custody handoff cannot substitute a different managed authority")
			}
			if managed == nil {
				status := "ready"
				if old.ManagedOnly {
					status = "active"
				}
				managed = &environmentCreateRegisteredIssuer{ID: old.ManagedRootID, Environment: cfg.Environment, Domain: "openbao_tls", Kind: "root", Status: status, Certificate: old.ManagedPEM, ChainPEM: old.ManagedPEM, RootFingerprint: old.ManagedRootSHA256, BundleVersion: environmentPreflightBytesSHA256([]byte(old.ManagedPEM))}
			}
		} else if old.ManagedOnly || old.ManagedRootID != "" || old.ManagedRootSHA256 != "" {
			return environmentCreateBootstrapBlocker("recorded custody managed authority is incomplete")
		}
		managedOnly = managedOnly || old.ManagedOnly
	}
	definitions, err := renderEnvironmentCustodyTrust(cfg, generation, string(ca), managed, managedOnly)
	if err != nil {
		return err
	}
	raw := []byte(deploymentplan.String(deploymentplan.Map(definitions[0].Object["data"])["transport.json"]))
	if err := e.Check(ctx); err != nil {
		return err
	}
	if err := e.Store.write(relative, raw, true); err != nil {
		return err
	}
	return apply(ctx, "custody-transport-trust-"+environmentPreflightBytesSHA256(raw)[:12], definitions)
}

// Render bootstrap -> overlapping anchors -> managed-only without generating
// either authority. READY roots may enter the pool, but native admission still
// requires ACTIVE; the bootstrap anchor cannot be withdrawn before activation.
func renderEnvironmentCustodyTrust(cfg deploymentConfig, generation, bootstrapPEM string, managed *environmentCreateRegisteredIssuer, managedOnly bool) ([]deploymentplan.Definition, error) {
	stack := cfg.Values["CLOUD_STACK_NAME"]
	if !secretEnvironmentPattern.MatchString(cfg.Environment) || stack != "video-cloud-"+cfg.Environment || !environmentPreflightDigest.MatchString(generation) {
		return nil, environmentCreateBootstrapBlocker("custody transport requires its exact environment, stack and original create generation")
	}
	rootCertificate := func(value string) (*x509.Certificate, error) {
		block, rest := pem.Decode([]byte(value))
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 || strings.TrimSpace(string(rest)) != "" {
			return nil, environmentCreateBootstrapBlocker("custody transport authority must contain one public Root certificate only")
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil || !cert.IsCA || cert.CheckSignatureFrom(cert) != nil {
			return nil, environmentCreateBootstrapBlocker("custody transport authority is not its original self-signed Root")
		}
		return cert, nil
	}
	bootstrap, err := rootCertificate(bootstrapPEM)
	if err != nil {
		return nil, err
	}
	snapshot := environmentCustodyTransportTrust{Schema: "rtk.custody.transport/v1", Environment: cfg.Environment, Generation: generation, ServerName: "openbao." + stack + "-secrets.svc", ManagedOnly: managedOnly}
	if !managedOnly {
		snapshot.BootstrapPEM = bootstrapPEM
	}
	data := deploymentplan.Object{}
	if managed != nil {
		cert, er := rootCertificate(managed.Certificate)
		if er != nil {
			return nil, er
		}
		if managed.Environment != cfg.Environment || managed.Domain != "openbao_tls" || managed.Kind != "root" || !environmentBootstrapOwnerUUID.MatchString(managed.ID) || (managed.Status != "ready" && managed.Status != "active") || (managedOnly && managed.Status != "active") || environmentPreflightBytesSHA256(cert.Raw) != managed.RootFingerprint || environmentPreflightBytesSHA256([]byte(managed.ChainPEM)) != managed.BundleVersion || managed.ChainPEM != managed.Certificate || cert.Equal(bootstrap) {
			return nil, environmentCreateBootstrapBlocker("custody handoff differs from its exact registered managed OpenBao Root or activation phase")
		}
		snapshot.ManagedRootID, snapshot.ManagedRootSHA256, snapshot.ManagedPEM = managed.ID, managed.RootFingerprint, managed.Certificate
		data["managed-root.pem"] = managed.Certificate
	} else if managedOnly {
		return nil, environmentCreateBootstrapBlocker("custody cannot withdraw bootstrap transport before its managed authority exists")
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	if len(raw) > 256<<10 {
		return nil, environmentCreateBootstrapBlocker("custody transport snapshot exceeds the native public snapshot limit")
	}
	data["transport.json"] = string(raw)
	var definitions []deploymentplan.Definition
	for _, namespace := range []string{stack + "-video-cloud", stack + "-account-manager"} {
		definitions = append(definitions, deploymentplan.Definition{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": environmentCustodyTrustName, "namespace": namespace, "labels": deploymentplan.Object{"rtk.realtek.com/stack": stack}, "annotations": deploymentplan.Object{"rtk.realtek.com/create-generation": generation}}, "data": deploymentplan.Clone(data)}})
	}
	return definitions, nil
}

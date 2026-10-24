package main

import (
	"context"
	"crypto/x509"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// History and execution share the exact maintained recipes. This function has
// no Kubernetes, registry, credential or issuer mutation boundary.
func productStageDefinitions(ctx context.Context, cfg deploymentConfig, l *createNormalTailProductLifecycle, s environmentCreateServiceCeremony, stage string, stores ...secretStore) ([]deploymentplan.Definition, error) {
	if l == nil || !environmentPreflightDigest.MatchString(productIdentityScope(l)) || l.Bootstrap.Environment != cfg.Environment || l.Bootstrap.Stack != cfg.Values["CLOUD_STACK_NAME"] || s.Root.ID != l.Root.ID || s.Root.RootFingerprint != l.Root.RootFingerprint {
		return nil, environmentCreateBootstrapBlocker("product stage recipe differs from its qualified original source")
	}
	var p environmentCreateBootstrapPlan
	if decodeEnvironmentJSON(mustProviderRecoveryJSON(l.Bootstrap), &p) != nil {
		return nil, environmentCreateBootstrapBlocker("qualified product recipe cannot be restored")
	}
	for i := range p.Domains {
		if p.Domains[i].Domain == "service" {
			p.Domains[i] = l.Policy
		}
	}
	cfg.Values = appendMap(cfg.Values, map[string]string{"LKE_STORAGE_CLASS": l.StorageClass})
	cert, err := parseFirstCertificatePEM([]byte(l.TransportCAPEM))
	if err != nil {
		return nil, environmentCreateBootstrapBlocker("product stage lost its original management transport authority")
	}
	runtime := environmentCreateBootstrapRuntime{ProductIdentityScope: productIdentityScope(l), TransportTrustConfigMap: "pki-create-transport-trust", TransportCAPEM: l.TransportCAPEM, TransportRootSHA256: environmentPreflightBytesSHA256(cert.Raw), TransportSecrets: map[string]string{"pki-controller": "pki-create-pki-controller-transport", "certissuer": "pki-create-certissuer-transport", "account-manager": "pki-create-account-manager-transport"}}
	switch stage {
	case "enrollment-closed":
		return productEnrollmentClosedDefinitions(cfg, l, s)
	case "successor-trust":
		return productIdentityTrustStageDefinitions(l, s.Intermediate)
	case "successor-crls":
		if productRegistrationCRLSource(l) != nil {
			if len(stores) != 1 {
				return nil, accountRegistrationCRLBlock("new lifecycle recipe public source")
			}
			if err := bindProductRegistrationCRLs(stores[0], l, s.Intermediate); err != nil {
				return nil, err
			}
		}
		return productIdentityTrustStageDefinitions(l, s.Intermediate)
	case "session-controls":
		return productIdentityControlDefinitions(cfg, l, s, true)
	case "service-custody-reader":
		return []deploymentplan.Definition{renderEnvironmentCreateCustodyReader(cfg, p, s)}, nil
	case "service-session":
		var subjects []string
		for _, p := range l.Outputs {
			subjects = append(subjects, p.Subject)
		}
		defs, err := renderEnvironmentCreatePKIPhase(ctx, cfg, p, "service-bootstrap", "--openbao-ca-configmap", "pki-openbao-transport-ca", "--service-bootstrap-subject", "service:deployment-bootstrap", "--service-bootstrap-subjects", strings.Join(subjects, ","), "--service-bootstrap-issuer-id", s.Intermediate.ID, "--service-bootstrap-issuer-mount", s.Intermediate.Reference, "--service-bootstrap-root-sha256", s.Root.RootFingerprint, "--service-bootstrap-deployment-id", "create-"+l.ParentSHA256[:32], "--service-bootstrap-session-id", s.BootstrapSessionID, "--service-bootstrap-state-claim", s.BootstrapClaim)
		if err != nil {
			return nil, err
		}
		account := "pki-product-bootstrap-" + productIdentityScope(l)[:12]
		for _, d := range defs {
			if d.Object["kind"] == "ServiceAccount" {
				deploymentplan.Map(d.Object["metadata"])["name"] = account
			}
			if d.Object["kind"] == "Job" {
				pod := environmentPKIPod(d.Object)
				pod["serviceAccountName"] = account
				container := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
				container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(map[string]string{"OPENBAO_KUBERNETES_ROLE": account, "PKI_BOOTSTRAP_DEPLOYMENT_ID": "product-" + productIdentityScope(l)[:48]}))
			}
		}
		if l.OpenBaoTrustProfile != "" {
			if l.OpenBaoTrustProfile != productIdentityManagedOpenBaoTrustProfile {
				return nil, environmentCreateBootstrapBlocker("product recipe has an unknown OpenBao trust profile")
			}
			return bindProductIdentityOpenBaoTrust(l, defs)
		}
		return defs, nil
	}
	for _, product := range l.Outputs {
		role := strings.TrimPrefix(product.Subject, "service:")
		if stage == "enroll-"+role {
			owner, err := productIdentityEnrollmentOwner(cfg, p, runtime, s, product)
			if err != nil {
				return nil, err
			}
			return []deploymentplan.Definition{environmentCreateServiceClaimDefinition(cfg, p.Stack+"-video-cloud", product.Claim), owner}, nil
		}
	}
	return nil, environmentCreateBootstrapBlocker("product stage has no declared maintained recipe")
}

func productEnrollmentClosedDefinitions(cfg deploymentConfig, l *createNormalTailProductLifecycle, s environmentCreateServiceCeremony) ([]deploymentplan.Definition, error) {
	controls, err := productIdentityControlDefinitions(cfg, l, s, false)
	if err != nil {
		return nil, err
	}
	for _, d := range controls {
		if deploymentplan.ID(d.Object) == "Deployment/"+cfg.Values["CLOUD_STACK_NAME"]+"-video-cloud/certissuer" {
			return []deploymentplan.Definition{d}, nil
		}
	}
	return nil, environmentCreateBootstrapBlocker("product provisioning close lacks its original maintained issuer")
}

func productCertificateMatches(raw []byte, product environmentCreateProductIdentity, s environmentCreateServiceCeremony) (*x509.Certificate, error) {
	leaf, err := parseFirstCertificatePEM(raw)
	ica, ie := parseFirstCertificatePEM([]byte(s.Intermediate.Certificate))
	if err != nil || ie != nil || leaf.Subject.CommonName != product.Subject || leaf.IsCA || leaf.CheckSignatureFrom(ica) != nil {
		return nil, environmentCreateBootstrapBlocker("product certificate differs from its native subject and issuer")
	}
	return leaf, nil
}

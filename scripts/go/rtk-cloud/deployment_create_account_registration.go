package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"slices"
	"strconv"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// The protected adapter supplies a native export of this Account Manager's
// existing server state and the controller's current signed intermediate CRL.
// Both listeners belong to the same recorded workload owner. No new key, leaf,
// authority or revocation acknowledgment is created by this renderer.
func renderEnvironmentCreateAccountRegistration(cfg deploymentConfig, p environmentCreateBootstrapPlan, s environmentCreateServiceCeremony, certificatePEM, keyPEM, intermediateCRLPEM string, definitions []deploymentplan.Definition) ([]deploymentplan.Definition, error) {
	if cfg.Values["ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED"] != "true" {
		return definitions, nil
	}
	port, err := strconv.Atoi(cfg.Values["ACCOUNT_MANAGER_SERVICE_REGISTRATION_PORT"])
	if err != nil || port <= 0 || port > 65535 || port == 18443 || p.Environment != cfg.Environment || p.Stack != cfg.Values["CLOUD_STACK_NAME"] || s.Root.Status != "active" || s.Intermediate.Status != "active" {
		return nil, environmentCreateBootstrapBlocker("maintained registration listener requires its exact active Account Manager and a separate valid configured port")
	}
	pair, err := tls.X509KeyPair([]byte(certificatePEM), []byte(keyPEM))
	if err != nil || len(pair.Certificate) != 3 {
		return nil, environmentCreateBootstrapBlocker("original native Account Manager server export is unavailable")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	name := "account-manager." + p.Stack + "-account-manager.svc.cluster.local"
	intermediate, intermediateErr := parseFirstCertificatePEM([]byte(s.Intermediate.Certificate))
	root, rootErr := parseFirstCertificatePEM([]byte(s.Root.Certificate))
	if err != nil || intermediateErr != nil || rootErr != nil || leaf.Subject.CommonName != name || leaf.VerifyHostname(name) != nil || !bytes.Equal(pair.Certificate[1], intermediate.Raw) || !bytes.Equal(pair.Certificate[2], root.Raw) || leaf.CheckSignatureFrom(intermediate) != nil || environmentPreflightBytesSHA256(root.Raw) != s.Root.RootFingerprint {
		return nil, environmentCreateBootstrapBlocker("registration listener export differs from original native Account Manager and Service lineage")
	}
	now := time.Now().UTC()
	validateCRL := func(raw string, authority *x509.Certificate) error {
		block, rest := pem.Decode([]byte(raw))
		if block == nil || block.Type != "X509 CRL" || len(bytes.TrimSpace(rest)) != 0 {
			return environmentCreateBootstrapBlocker("actual original Service CRL PEM required")
		}
		crl, err := x509.ParseRevocationList(block.Bytes)
		if err != nil || crl.Number == nil || crl.Number.Sign() <= 0 || !bytes.Equal(crl.RawIssuer, authority.RawSubject) || crl.CheckSignatureFrom(authority) != nil || now.Before(crl.ThisUpdate) || !now.Before(crl.NextUpdate) {
			return environmentCreateBootstrapBlocker("registration listener requires current natively signed original Service CRLs")
		}
		return nil
	}
	if err := validateCRL(s.RootCRLPEM, root); err != nil {
		return nil, err
	}
	if err := validateCRL(intermediateCRLPEM, intermediate); err != nil {
		return nil, err
	}
	secretName, mountPath := "account-manager-service-registration-tls", "/etc/rtk-account-manager/service-registration"
	secret := environmentCreateSecret(p.Stack+"-account-manager", secretName, map[string]string{"tls.crt": certificatePEM, "tls.key": keyPEM, "client-ca.crt": s.Root.Certificate, "client.crl": strings.TrimSpace(s.RootCRLPEM) + "\n" + strings.TrimSpace(intermediateCRLPEM) + "\n"})
	out := make([]deploymentplan.Definition, 0, len(definitions)+1)
	found := map[string]bool{}
	for _, definition := range definitions {
		d := definition
		d.Object = deploymentplan.Clone(d.Object)
		id := deploymentplan.ID(d.Object)
		if id == "Secret/"+p.Stack+"-account-manager/"+secretName {
			continue
		}
		if id == "Deployment/"+p.Stack+"-account-manager/account-manager" {
			found["deployment"] = true
			dependency := "Secret/" + p.Stack + "-account-manager/" + secretName
			d.Dependencies = slices.Clone(d.Dependencies)
			if !slices.Contains(d.Dependencies, dependency) {
				d.Dependencies = append(d.Dependencies, dependency)
			}
			pod := environmentPKIPod(d.Object)
			main := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
			if main["name"] != "app" {
				return nil, environmentCreateBootstrapBlocker("original Account Manager primary registration owner is absent")
			}
			main["env"] = mergeEnvironmentNamedFields(main["env"], environmentCreateEnv(map[string]string{"ACCOUNT_MANAGER_SERVICE_REGISTRATION_PORT": strconv.Itoa(port), "ACCOUNT_MANAGER_SERVICE_REGISTRATION_SERVER_CERT": mountPath + "/tls.crt", "ACCOUNT_MANAGER_SERVICE_REGISTRATION_SERVER_KEY": mountPath + "/tls.key", "ACCOUNT_MANAGER_SERVICE_REGISTRATION_CLIENT_CA": mountPath + "/client-ca.crt", "ACCOUNT_MANAGER_SERVICE_REGISTRATION_CLIENT_CRL": mountPath + "/client.crl"}))
			main["ports"] = mergeEnvironmentNamedFields(main["ports"], []any{deploymentplan.Object{"name": "service-reg", "containerPort": float64(port)}})
			main["volumeMounts"] = mergeEnvironmentNamedFields(main["volumeMounts"], []any{environmentCreateServiceMount("account-manager-service-registration-tls", mountPath, true)})
			pod["volumes"] = mergeEnvironmentNamedFields(pod["volumes"], []any{environmentCreateServiceVolume("account-manager-service-registration-tls", "secret", secretName)})
		}
		if id == "Service/"+p.Stack+"-account-manager/account-manager" {
			found["service"] = true
			spec := deploymentplan.Map(d.Object["spec"])
			spec["ports"] = mergeEnvironmentNamedFields(spec["ports"], []any{deploymentplan.Object{"name": "service-reg", "port": float64(8443), "targetPort": "service-reg"}})
		}
		out = append(out, d)
	}
	if !found["deployment"] || !found["service"] {
		return nil, environmentCreateBootstrapBlocker("maintained Account Manager registration needs both original desired Deployment and private Service")
	}
	return append(out, secret), nil
}

// Registration changes only the Account Manager namespace. Its existing
// cross-namespace dependencies remain explicit read-only prerequisites from
// the same authenticated core recipes; none is dropped or observed as intent.
func environmentCreateAccountRegistrationStage(p environmentCreateBootstrapPlan, definitions []deploymentplan.Definition) ([]deploymentplan.Definition, error) {
	bad := func() ([]deploymentplan.Definition, error) {
		return nil, environmentCreateBootstrapBlocker("account registration requires its exact declared dependency closure")
	}
	all := map[string]deploymentplan.Definition{}
	selected := map[string]bool{}
	for _, definition := range definitions {
		id := deploymentplan.ID(definition.Object)
		if id == "//" || all[id].Object != nil {
			return bad()
		}
		all[id] = definition
		if deploymentplan.Map(definition.Object["metadata"])["namespace"] == p.Stack+"-account-manager" {
			selected[id] = true
		}
	}
	if !selected["Deployment/"+p.Stack+"-account-manager/account-manager"] || !selected["Service/"+p.Stack+"-account-manager/account-manager"] || !selected["Secret/"+p.Stack+"-account-manager/account-manager-service-registration-tls"] {
		return bad()
	}
	visiting, visited := map[string]bool{}, map[string]bool{}
	var visit func(string) bool
	visit = func(id string) bool {
		definition, ok := all[id]
		if !ok || definition.Retire || visiting[id] {
			return false
		}
		if visited[id] {
			return true
		}
		visiting[id] = true
		for _, dependency := range definition.Dependencies {
			if !visit(dependency) {
				return false
			}
		}
		visiting[id], visited[id] = false, true
		return true
	}
	for id := range selected {
		if !visit(id) {
			return bad()
		}
	}
	out := make([]deploymentplan.Definition, 0, len(visited))
	for _, definition := range definitions {
		id := deploymentplan.ID(definition.Object)
		if !visited[id] {
			continue
		}
		definition.Object = deploymentplan.Clone(definition.Object)
		definition.Dependencies = slices.Clone(definition.Dependencies)
		if !selected[id] {
			definition.Retain = true
		}
		out = append(out, definition)
	}
	return out, nil
}

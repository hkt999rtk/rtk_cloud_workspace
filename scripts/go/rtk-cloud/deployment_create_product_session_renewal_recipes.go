package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"path/filepath"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Derive only the native credential command's session/deployment/certificate
// output. Its original key and signed CSR are protected by a read-only init.
func productSessionRenewalJobDefinitions(cfg deploymentConfig, l *createNormalTailProductLifecycle, s productSessionRenewalSource, originalJob deploymentplan.Object) ([]deploymentplan.Definition, error) {
	if err := validateProductSessionRenewalDirectoryProfile(s); err != nil {
		return nil, err
	}
	if l == nil || s.ScopeSHA256 != productIdentityScope(l) || s.ParentSHA256 != l.ParentSHA256 || originalJob["kind"] != "Job" || !environmentPreflightDigest.MatchString(s.KeySHA256) || !environmentPreflightDigest.MatchString(s.CSRSHA256) {
		return nil, productSessionRenewalBlock("original renewal Job")
	}
	sid, did, path := productSessionRenewalIdentity(s.ParentSHA256, s.ScopeSHA256, s.OriginalSession.Session)
	if sid != s.SessionID || did != s.DeploymentID || path != s.CertificatePath {
		return nil, productSessionRenewalBlock("deterministic renewal identity")
	}
	o := deploymentplan.Projection(originalJob)
	m := deploymentplan.Map(o["metadata"])
	delete(m, "uid")
	m["name"] = "pki-product-renew-" + deploymentplan.Digest(s)[:16]
	pod := environmentPKIPod(o)
	cs := certIssuerObjectList(pod["containers"])
	if len(cs) != 1 || pod["serviceAccountName"] != "pki-product-bootstrap-"+s.ScopeSHA256[:12] {
		return nil, productSessionRenewalBlock("native renewal caller")
	}
	c := deploymentplan.Map(cs[0])
	if c["image"] != l.Bootstrap.Image || mqttFoundationEnv(c, "PKI_BOOTSTRAP_KEY_PATH") != "/run/pki-bootstrap/bootstrap.key" || mqttFoundationEnv(c, "PKI_BOOTSTRAP_CSR_PATH") != "/run/pki-bootstrap/bootstrap.csr" || mqttFoundationEnv(c, "PKI_BOOTSTRAP_SESSION_ID") != s.OriginalSession.Session {
		return nil, productSessionRenewalBlock("native renewal image and paths")
	}
	volume, err := environmentPKINamedFields(pod["volumes"])
	if err != nil || deploymentplan.Map(volume["bootstrap-state"]["persistentVolumeClaim"])["claimName"] != "pki-product-bootstrap-"+s.ScopeSHA256[:12] {
		return nil, productSessionRenewalBlock("original renewal claim")
	}
	delete(deploymentplan.Map(pod["securityContext"]), "fsGroup")
	c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_SESSION_ID": s.SessionID, "PKI_BOOTSTRAP_DEPLOYMENT_ID": s.DeploymentID, "PKI_BOOTSTRAP_CERT_PATH": s.CertificatePath, "PKI_BOOTSTRAP_EXPIRES_AT": ""}))
	// Native production accepts an omitted deadline and issues its supported 8h
	// lifetime. Test fixtures may select their actual short expiry separately.
	c["env"] = environmentCreateServiceWithoutEnv(c["env"], "PKI_BOOTSTRAP_EXPIRES_AT")
	guard := deploymentplan.Object{"name": "original-custody-proof", "image": l.Bootstrap.Image, "command": []any{"/bin/sh", "-ec", productSessionForwardKeyGuard(&environmentProductSessionForward{KeySHA256: s.KeySHA256, CSRSHA256: s.CSRSHA256})}, "securityContext": deploymentplan.Object{"runAsNonRoot": true, "runAsUser": float64(10001), "runAsGroup": float64(10001), "allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": deploymentplan.Object{"drop": []any{"ALL"}}}, "volumeMounts": []any{deploymentplan.Object{"name": "bootstrap-state", "mountPath": "/run/pki-bootstrap", "readOnly": true}}}
	prepare := deploymentplan.Clone(guard)
	prepare["name"] = "renewal-certificate-directory"
	prepare["volumeMounts"] = []any{deploymentplan.Object{"name": "bootstrap-state", "mountPath": "/run/pki-bootstrap"}}
	directory := filepath.Dir(s.CertificatePath)
	prepare["command"] = []any{"/bin/sh", "-ec", "set -eu; test ! -L /run/pki-bootstrap; test -d /run/pki-bootstrap; test ! -L " + directory + "; if test -e " + directory + "; then test -d " + directory + "; test \"$(stat -c '%a %u %g' " + directory + ")\" = '700 10001 10001'; else mkdir -m 0700 " + directory + "; fi"}
	if s.Generation == 2 || s.CertificateDirectoryProfile == productSessionRenewalSGIDDirectoryProfile {
		command := prepare["command"].([]any)
		command[2] = strings.Replace(command[2].(string), `test "$(stat -c '%a %u %g' `+directory+`)" = '700 10001 10001'`, `case "$(stat -c '%a %u %g' `+directory+`)" in '700 10001 10001'|'2700 10001 10001') ;; *) exit 1;; esac`, 1)
	}
	pod["initContainers"] = []any{guard, prepare}
	// Required same-node placement permits sharing this one RWO claim with the
	// exact original accepted custody reader. There is no second private claim.
	pod["affinity"] = deploymentplan.Object{"podAffinity": deploymentplan.Object{"requiredDuringSchedulingIgnoredDuringExecution": []any{deploymentplan.Object{"topologyKey": "kubernetes.io/hostname", "namespaces": []any{l.Bootstrap.Stack + "-video-cloud"}, "labelSelector": deploymentplan.Object{"matchLabels": deploymentplan.Object{"app.kubernetes.io/name": "pki-product-custody-" + s.ScopeSHA256[:12]}}}}}}
	defs, err := bindProductIdentityOpenBaoTrust(l, []deploymentplan.Definition{{Phase: "bootstrap", Object: o}})
	if err != nil {
		return nil, err
	}
	return bindProductSessionForwardReadableToken(defs)
}
func productSessionRenewalCustodyDefinitions(cfg deploymentConfig, l *createNormalTailProductLifecycle, s productSessionRenewalSource, old environmentCreateServiceCeremony) ([]deploymentplan.Definition, error) {
	if old.CustodyReader != "pki-product-custody-"+productIdentityScope(l)[:12] || !environmentBootstrapOwnerUUID.MatchString(s.CustodyUID) {
		return nil, productSessionRenewalBlock("original custody name")
	}
	next := old
	next.BootstrapSessionID = s.SessionID
	d := renderEnvironmentCreateCustodyReader(cfg, l.Bootstrap, next)
	deploymentplan.Map(d.Object["metadata"])["uid"] = s.CustodyUID
	spec := deploymentplan.Map(d.Object["spec"])
	spec["strategy"] = deploymentplan.Object{"type": "Recreate"}
	c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(d.Object)["containers"])[0])
	c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_CERT_PATH": s.CertificatePath}))
	return []deploymentplan.Definition{d}, nil
}
func productSessionRenewalCredentialDefinitions(l *createNormalTailProductLifecycle, s productSessionRenewalSource, material map[string]string) ([]deploymentplan.Definition, error) {
	if len(material) != 3 || material["tls.crt"] == "" || material["tls.key"] == "" || material["ca.crt"] != l.Root.Certificate || environmentPreflightBytesSHA256([]byte(material["tls.key"])) != s.KeySHA256 {
		return nil, productSessionRenewalBlock("new credential material")
	}
	pair, err := tls.X509KeyPair([]byte(material["tls.crt"]), []byte(material["tls.key"]))
	if err != nil || (len(pair.Certificate) != 3 && len(pair.Certificate) != 4) {
		return nil, productSessionRenewalBlock("new credential keypair")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	root, rootErr := parseFirstCertificatePEM([]byte(l.Root.Certificate))
	if err != nil || rootErr != nil || leaf.Subject.CommonName != "service:deployment-bootstrap" || environmentPreflightBytesSHA256(pair.Certificate[len(pair.Certificate)-1]) != l.Root.RootFingerprint || !leaf.NotAfter.After(s.OriginalSession.Expires) || environmentPreflightBytesSHA256(leaf.Raw) == s.OriginalFingerprint {
		return nil, productSessionRenewalBlock("new credential public lineage")
	}
	issuer, err := x509.ParseCertificate(pair.Certificate[1])
	if err != nil || leaf.CheckSignatureFrom(issuer) != nil || issuer.CheckSignatureFrom(root) != nil || len(pair.Certificate) == 4 && !bytes.Equal(pair.Certificate[1], pair.Certificate[2]) {
		return nil, productSessionRenewalBlock("new exact credential chain")
	}
	d := environmentCreateSecret(l.Bootstrap.Stack+"-video-cloud", "service-bootstrap-"+s.SessionID, material)
	// Use the persisted Secret representation, exactly as Compile does. Both
	// chronological recipe checks and dependency proofs consume this definition.
	data := deploymentplan.Object{}
	for key, value := range material {
		data[key] = base64.StdEncoding.EncodeToString([]byte(value))
	}
	delete(d.Object, "stringData")
	d.Object["data"] = data
	d.Object["immutable"] = true
	deploymentplan.Map(d.Object["metadata"])["labels"] = deploymentplan.Object{"rtk.realtek.com/stack": l.Bootstrap.Stack, "rtk.cloud/bootstrap-session": s.SessionID}
	return []deploymentplan.Definition{d}, nil
}
func productSessionRenewalControlDefinitions(cfg deploymentConfig, l *createNormalTailProductLifecycle, r *productSessionRenewal) ([]deploymentplan.Definition, error) {
	if r == nil || r.Source.ScopeSHA256 != productIdentityScope(l) || r.Installed.Ceremony.BootstrapSessionID != r.Source.SessionID {
		return nil, productSessionRenewalBlock("renewal controls")
	}
	// This is the reviewed target's exact Controller, including its MQTT roster,
	// not the older enrollment source or an adopted current native body.
	var out []deploymentplan.Definition
	seen := map[string]bool{}
	for _, source := range r.Source.ControlSources {
		d := source
		d.Object = deploymentplan.Clone(source.Object)
		d.Retain = false
		d.Dependencies = nil
		id := deploymentplan.ID(d.Object)
		if seen[id] {
			return nil, productSessionRenewalBlock("control source uniqueness")
		}
		seen[id] = true
		switch id {
		case "Deployment/" + l.Bootstrap.Stack + "-video-cloud/pki-controller":
			extra, err := applyEnvironmentBootstrapCleanup(deploymentEndpointValues(cfg), d.Object, r.Installed.Ceremony.CleanupBinding)
			if err != nil {
				return nil, err
			}
			out = append(out, d)
			for i := range extra {
				// The maintained renderer shares cluster metadata between its
				// two RBAC objects; owner pins must never alias one another.
				extra[i].Object = deploymentplan.Clone(extra[i].Object)
				id := deploymentplan.ID(extra[i].Object)
				if uid := r.Source.ControlOwners[id]; uid != "" {
					deploymentplan.Map(extra[i].Object["metadata"])["uid"] = uid
				}
			}
			out = append(out, extra...)
		case "Deployment/" + l.Bootstrap.Stack + "-video-cloud/certissuer":
			pod := environmentPKIPod(d.Object)
			found := false
			for _, v := range certIssuerObjectList(pod["containers"]) {
				c := deploymentplan.Map(v)
				if c["name"] != "certissuer" {
					continue
				}
				found = true
				c["env"] = environmentCreateServiceWithoutEnv(c["env"], "CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_CALLER", "CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_SESSION_ID", "CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_SUBJECTS")
				c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{"CERT_ISSUER_OPENBAO_PKI_MOUNT": r.Installed.Ceremony.Intermediate.Reference, "CERT_ISSUER_SERVICE_CLIENT_PROVISIONER_CN_PATTERN": "^$"}))
			}
			if !found {
				return nil, productSessionRenewalBlock("closed enrollment owner")
			}
			out = append(out, d)
		default:
			return nil, productSessionRenewalBlock("undeclared control source")
		}
	}
	if len(seen) != 2 {
		return nil, productSessionRenewalBlock("both exact control sources")
	}
	return out, nil
}

// Authenticate the fixed stage sequence and expose only genuine final accepted
// resource owners. This is deliberately pure: no current body is authority.
func productSessionRenewalDependencyResources(cfg deploymentConfig, store secretStore, e *environmentCreateExecution, l *createNormalTailProductLifecycle, r *productSessionRenewal) ([]deploymentplan.Resource, error) {
	if r == nil || r.SourceSHA256 != deploymentplan.Digest(r.Source) || validateProductSessionRenewalSource(store, e, l, r.Source) != nil {
		return nil, productSessionRenewalBlock("dependency source")
	}
	original, err := productSessionRenewalSourceInstalled(store, e, l, r.Source)
	if err != nil {
		return nil, err
	}
	job, err := productSessionRenewalSourceJob(store, e, l, r.Source)
	if err != nil {
		return nil, productSessionRenewalBlock("original Job source")
	}
	credential, _, err := acceptedCreateStage(store, e, "kubernetes/"+productSessionRenewalStage(l, r.Source, "credentials"))
	if err != nil || len(credential.Resources) != 1 {
		return nil, productSessionRenewalBlock("accepted new credential")
	}
	raw := deploymentplan.Map(credential.Resources[0].Desired["data"])
	material := map[string]string{}
	for _, k := range []string{"tls.crt", "tls.key", "ca.crt"} {
		v, er := base64.StdEncoding.DecodeString(deploymentplan.String(raw[k]))
		if er != nil {
			return nil, productSessionRenewalBlock("credential encoding")
		}
		material[k] = string(v)
	}
	pair, er := tls.X509KeyPair([]byte(material["tls.crt"]), []byte(material["tls.key"]))
	observed, observeErr := readProductSessionRenewalObserved(store, e, l, r.Source)
	if er != nil || observeErr != nil || observed == nil || !managedLateBootstrapChainMatches(pair.Certificate, r.Original.Ceremony) || environmentPreflightBytesSHA256(pair.Certificate[0]) != observed.CertificateSHA256 {
		return nil, productSessionRenewalBlock("accepted renewed native credential lineage")
	}
	recipes := map[string][]deploymentplan.Definition{}
	recipes["job"], err = productSessionRenewalJobDefinitions(cfg, l, r.Source, job)
	if err != nil {
		return nil, err
	}
	recipes["custody"], err = productSessionRenewalCustodyDefinitions(cfg, l, r.Source, original.Ceremony)
	if err != nil {
		return nil, err
	}
	recipes["credentials"], err = productSessionRenewalCredentialDefinitions(l, r.Source, material)
	if err != nil {
		return nil, err
	}
	recipes["controls"], err = productSessionRenewalControlDefinitions(cfg, l, r)
	if err != nil {
		return nil, err
	}
	index := map[string]int{}
	var out []deploymentplan.Resource
	for _, suffix := range []string{"job", "custody", "credentials", "controls"} {
		stage := productSessionRenewalStage(l, r.Source, suffix)
		p, j, er := acceptedCreateStage(store, e, "kubernetes/"+stage)
		if er != nil || p.BindingsSHA256 != environmentCreateStageBindingsSHA(stage, recipes[suffix]) || p.CreateParentSHA256 != e.Journal.PlanSHA256 || len(p.Resources) != len(recipes[suffix]) || r.Stages[suffix].PlanSHA256 != p.SHA256() || r.Stages[suffix].JournalSHA256 != deploymentplan.Digest(j) {
			return nil, productSessionRenewalBlock("accepted " + suffix + " proof")
		}
		verified, vj, er := readCreateNormalTailStage(store, e, stage, p)
		if er != nil || verified == nil || !vj.Accepted || deploymentplan.Digest(vj) != deploymentplan.Digest(j) {
			return nil, productSessionRenewalBlock("original " + suffix + " child")
		}
		expected := map[string]deploymentplan.Object{}
		for _, d := range recipes[suffix] {
			expected[deploymentplan.ID(d.Object)] = deploymentplan.Projection(d.Object)
		}
		for _, resource := range p.Resources {
			if resource.Action == "retire" || !environmentBootstrapOwnerUUID.MatchString(j.Owners[resource.ID]) || !deploymentplan.Matches(expected[resource.ID], resource.Desired) {
				return nil, productSessionRenewalBlock("exact " + suffix + " resource")
			}
			expectedUID := productSessionRenewalExpectedOwner(l, r.Source, resource.ID)
			if expectedUID != "" && (resource.UID != expectedUID || j.Owners[resource.ID] != expectedUID) {
				return nil, productSessionRenewalBlock("original accepted renewal owner")
			}
			if suffix == "credentials" && j.Owners[resource.ID] != r.Installed.Ceremony.CredentialUIDs[r.Installed.Ceremony.BootstrapCredentialNamespace] {
				return nil, productSessionRenewalBlock("new credential owner differs from installed proof")
			}
			if expectedUID == "" && (resource.Action != "create" || resource.UID != "" || resource.Baseline != nil) {
				return nil, productSessionRenewalBlock("renewal may not adopt an unrecorded native resource")
			}
			resource.Action = "retain"
			resource.UID = j.Owners[resource.ID]
			resource.Baseline = deploymentplan.Clone(resource.Desired)
			deploymentplan.Map(resource.Baseline["metadata"])["uid"] = resource.UID
			if at, ok := index[resource.ID]; ok {
				out[at] = resource
			} else {
				index[resource.ID] = len(out)
				out = append(out, resource)
			}
		}
	}
	return productSessionRenewalDependencyUnion(cfg, store, e, l, r, out)
}
func productSessionRenewalStageRecipes(store secretStore, e *environmentCreateExecution, l *createNormalTailProductLifecycle, s productSessionRenewalSource) (map[string][]deploymentplan.Definition, error) {
	cfg := deploymentConfig{Environment: l.Bootstrap.Environment, Values: map[string]string{"CLOUD_STACK_NAME": l.Bootstrap.Stack}}
	job, err := productSessionRenewalSourceJob(store, e, l, s)
	if err != nil {
		return nil, productSessionRenewalBlock("original recipe")
	}
	old, err := productSessionRenewalSourceInstalled(store, e, l, s)
	if err != nil {
		return nil, err
	}
	out := map[string][]deploymentplan.Definition{}
	out["job"], err = productSessionRenewalJobDefinitions(cfg, l, s, job)
	if err != nil {
		return nil, err
	}
	out["custody"], err = productSessionRenewalCustodyDefinitions(cfg, l, s, old.Ceremony)
	if err != nil {
		return nil, err
	}
	stage := productSessionRenewalStage(l, s, "credentials")
	p, err := readCreateNormalTailPreparationPlan(store, e, stage)
	if err != nil {
		return nil, err
	}
	if p != nil {
		if len(p.Resources) != 1 {
			return nil, productSessionRenewalBlock("credential recipe")
		}
		material := map[string]string{}
		for _, k := range []string{"tls.crt", "tls.key", "ca.crt"} {
			v, er := base64.StdEncoding.DecodeString(deploymentplan.String(deploymentplan.Map(p.Resources[0].Desired["data"])[k]))
			if er != nil {
				return nil, er
			}
			material[k] = string(v)
		}
		out["credentials"], err = productSessionRenewalCredentialDefinitions(l, s, material)
		if err != nil {
			return nil, err
		}
	}
	o, err := readProductSessionRenewalObserved(store, e, l, s)
	if err != nil {
		return nil, err
	}
	if o == nil {
		return out, nil
	}
	out["controls"], err = productSessionRenewalControlDefinitions(cfg, l, &productSessionRenewal{Source: s, Installed: o.Installed})
	return out, err
}

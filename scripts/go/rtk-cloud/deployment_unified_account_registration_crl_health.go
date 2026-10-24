package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type accountRegistrationListenerRead func(context.Context, deploymentplan.Object, deploymentplan.Object) (deploymentplan.Object, error)

func accountRegistrationPublicCRLDeclared(account deploymentplan.Object) bool {
	pod := environmentPKIPod(account)
	return mqttFoundationEnv(managedNamedField(pod["containers"], "app"), "ACCOUNT_MANAGER_SERVICE_REGISTRATION_CLIENT_CRL") == accountRegistrationCRLBundlePath || mqttFoundationEnv(managedNamedField(pod["containers"], "pkimanagement"), accountRegistrationCRLExportEnv) != ""
}

var errAccountRegistrationCRLSnapshotAdvanced = errors.New("verified Account Manager public CRL snapshots advanced between reads")

func readAccountRegistrationPublicListener(ctx context.Context, cfg deploymentConfig, store secretStore, expected, listener deploymentplan.Object, now time.Time, read func(context.Context, string) (deploymentplan.Object, error), command func(context.Context, ...string) ([]byte, error)) (deploymentplan.Object, error) {
	for attempt := 0; attempt < 3; attempt++ {
		current, err := readAccountRegistrationPublicListenerSnapshot(ctx, cfg, store, expected, listener, now, read, command)
		if !errors.Is(err, errAccountRegistrationCRLSnapshotAdvanced) {
			return current, err
		}
		if attempt == 2 {
			return nil, err
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, errAccountRegistrationCRLSnapshotAdvanced
}

// Only the explicitly selected public exporter may replace the CRL input used
// for validation. The original listener certificate, key and CA stay literal.
func readAccountRegistrationPublicListenerSnapshot(ctx context.Context, cfg deploymentConfig, store secretStore, expected, listener deploymentplan.Object, now time.Time, read func(context.Context, string) (deploymentplan.Object, error), command func(context.Context, ...string) ([]byte, error)) (deploymentplan.Object, error) {
	fail := func() (deploymentplan.Object, error) {
		return nil, fmt.Errorf("managed Account Manager public CRL export lacks its exact selected profile, current owned Pod or verified issuer coverage")
	}
	operator, err := store.readOperator()
	if err != nil {
		return fail()
	}
	image, err := accountRegistrationCRLRefreshInputs(cfg.Environment, operator)
	if err != nil || image == "" {
		return fail()
	}
	id := "Deployment/" + cfg.Values["CLOUD_STACK_NAME"] + "-account-manager/account-manager"
	if deploymentplan.ID(expected) != id {
		return fail()
	}
	actual, err := read(ctx, id)
	if err != nil || actual == nil {
		return fail()
	}
	meta := deploymentplan.Map(actual["metadata"])
	uid := deploymentplan.String(meta["uid"])
	if uid == "" || meta["deletionTimestamp"] != nil || !deploymentplan.Matches(deploymentplan.Projection(expected), actual) {
		return fail()
	}
	if old := deploymentplan.String(deploymentplan.Map(expected["metadata"])["uid"]); old == "" || old != uid {
		return fail()
	}
	wanted, err := environmentAccountRegistrationCRLRefreshDefinition(cfg, actual, image)
	if err != nil || !accountRegistrationPublicCRLDeclared(actual) || deploymentplan.Digest(deploymentplan.Projection(wanted.Object)) != deploymentplan.Digest(deploymentplan.Projection(actual)) {
		return fail()
	}
	ns := cfg.Values["CLOUD_STACK_NAME"] + "-account-manager"
	raw, err := command(ctx, "-n", ns, "get", "pods", "-l", "app.kubernetes.io/name=account-manager", "-o", "json")
	var pods struct {
		Items []deploymentplan.Object `json:"items"`
	}
	if err != nil || json.Unmarshal(raw, &pods) != nil || len(pods.Items) != 1 {
		return fail()
	}
	pod := pods.Items[0]
	pm := deploymentplan.Map(pod["metadata"])
	ps := deploymentplan.Map(pod["status"])
	if pm["namespace"] != ns || pm["deletionTimestamp"] != nil || ps["phase"] != "Running" || deploymentplan.String(pm["uid"]) == "" {
		return fail()
	}
	ready := false
	for _, v := range certIssuerObjectList(ps["conditions"]) {
		c := deploymentplan.Map(v)
		ready = ready || c["type"] == "Ready" && c["status"] == "True"
	}
	if !ready {
		return fail()
	}
	refs := certIssuerObjectList(pm["ownerReferences"])
	if len(refs) != 1 {
		return fail()
	}
	ref := deploymentplan.Map(refs[0])
	if ref["apiVersion"] != "apps/v1" || ref["kind"] != "ReplicaSet" || ref["controller"] != true {
		return fail()
	}
	rsID := "ReplicaSet/" + ns + "/" + deploymentplan.String(ref["name"])
	rs, err := read(ctx, rsID)
	if err != nil || deploymentplan.Map(rs["metadata"])["uid"] != ref["uid"] {
		return fail()
	}
	owners := certIssuerObjectList(deploymentplan.Map(rs["metadata"])["ownerReferences"])
	if len(owners) != 1 {
		return fail()
	}
	owner := deploymentplan.Map(owners[0])
	if owner["apiVersion"] != "apps/v1" || owner["kind"] != "Deployment" || owner["controller"] != true || owner["uid"] != uid || owner["name"] != "account-manager" {
		return fail()
	}
	declared := environmentPKIPod(actual)
	if !accountRegistrationPublicPodMatches(declared, pod) {
		return fail()
	}
	public, err := command(ctx, "-n", ns, "exec", "pod/"+deploymentplan.String(pm["name"]), "-c", "pkimanagement", "--", "cat", accountRegistrationCRLBundlePath)
	if err != nil || len(public) == 0 || len(public) > 1<<20 {
		return fail()
	}
	// The manifest is the same mounted, scoped public issuer source checked by
	// existing managed-CRL health. Every exported list must be current and signed
	// by exactly one declared issuer, and every declared issuer must be present.
	var typed liveDeployment
	if json.Unmarshal(mustProviderRecoveryJSON(actual), &typed) != nil {
		return fail()
	}
	manager := managedNamedField(declared["containers"], "pkimanagement")
	path := mqttFoundationEnv(manager, "PKI_MANAGEMENT_ACCOUNT_SERVICE_CLIENT_SERVER_CRL_MANIFEST")
	cm, key, err := mountedPKICRLConfigMap(typed, "pkimanagement", path)
	if err != nil {
		return fail()
	}
	source, err := read(ctx, "ConfigMap/"+ns+"/"+cm)
	if err != nil || source == nil {
		return fail()
	}
	manifest := deploymentplan.String(deploymentplan.Map(source["data"])[key])
	if validateMountedPKICRLManifest(manifest, cfg.Environment, "service", typed, "pkimanagement") != nil {
		return fail()
	}
	var entries []livePKICRLManifestEntry
	if json.Unmarshal([]byte(manifest), &entries) != nil {
		return fail()
	}
	lists, err := pemCRLs(public)
	if err != nil || len(lists) != len(entries) {
		return fail()
	}
	trusted, err := kubernetesSecretBytes(listener, "client-ca.crt")
	if err != nil {
		return fail()
	}
	roots, err := certificateRoots(trusted, now)
	if err != nil {
		return fail()
	}
	intermediates := x509.NewCertPool()
	for _, entry := range entries {
		cert, e := parseFirstCertificatePEM([]byte(entry.Issuer.Certificate))
		if e != nil {
			return fail()
		}
		intermediates.AddCert(cert)
	}
	for _, entry := range entries {
		cert, _ := parseFirstCertificatePEM([]byte(entry.Issuer.Certificate))
		if _, e := cert.Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); e != nil {
			return fail()
		}
	}
	used := map[int]bool{}
	for _, list := range lists {
		if list.Number == nil || list.Number.Sign() <= 0 || now.Before(list.ThisUpdate) || !now.Before(list.NextUpdate) || list.ThisUpdate.Before(now.Add(-7*24*time.Hour)) || list.NextUpdate.After(list.ThisUpdate.Add(7*24*time.Hour)) {
			return fail()
		}
		matches := 0
		selected := -1
		for i, entry := range entries {
			cert, e := parseFirstCertificatePEM([]byte(entry.Issuer.Certificate))
			if e == nil && bytes.Equal(list.RawIssuer, cert.RawSubject) && list.CheckSignatureFrom(cert) == nil && !list.NextUpdate.After(cert.NotAfter) && len(cert.SubjectKeyId) > 0 && bytes.Equal(list.AuthorityKeyId, cert.SubjectKeyId) {
				matches++
				selected = i
			}
		}
		if matches != 1 || used[selected] {
			return fail()
		}
		// Compare the public export with the same verified consumer record
		// used by ordinary mounted-CRL/registry/ACK health. A still-current
		// older signed list is not the current export source.
		entry := entries[selected]
		stateRaw, e := command(ctx, "-n", ns, "exec", "pod/"+deploymentplan.String(pm["name"]), "-c", "pkimanagement", "--", "cat", entry.StatePath)
		var state struct {
			Fingerprint string `json:"issuer_fingerprint"`
			Terminal    string `json:"terminal_issuer_id"`
			CRL         struct {
				Issuer     string    `json:"issuer_id"`
				Digest     string    `json:"crl_sha256"`
				Number     string    `json:"crl_number"`
				PEM        string    `json:"crl_pem"`
				ThisUpdate time.Time `json:"this_update"`
				NextUpdate time.Time `json:"next_update"`
			} `json:"crl"`
		}
		if e != nil || len(stateRaw) > 1<<20 || json.Unmarshal(stateRaw, &state) != nil || state.Terminal != "" || state.Fingerprint != entry.Issuer.Fingerprint || state.CRL.Issuer != entry.Issuer.ID {
			return fail()
		}
		installed, e := pemCRLs([]byte(state.CRL.PEM))
		if e != nil || len(installed) != 1 {
			return fail()
		}
		current := installed[0]
		sum := sha256.Sum256(current.Raw)
		signer, _ := parseFirstCertificatePEM([]byte(entry.Issuer.Certificate))
		if current.Number == nil || current.Number.Sign() <= 0 || current.CheckSignatureFrom(signer) != nil || !bytes.Equal(current.RawIssuer, signer.RawSubject) || !bytes.Equal(current.AuthorityKeyId, signer.SubjectKeyId) || now.Before(current.ThisUpdate) || !now.Before(current.NextUpdate) || current.NextUpdate.After(signer.NotAfter) || current.ThisUpdate.Before(now.Add(-7*24*time.Hour)) || current.NextUpdate.After(current.ThisUpdate.Add(7*24*time.Hour)) || state.CRL.Digest != hex.EncodeToString(sum[:]) || state.CRL.Number != current.Number.String() || !state.CRL.ThisUpdate.Equal(current.ThisUpdate) || !state.CRL.NextUpdate.Equal(current.NextUpdate) {
			return fail()
		}
		if !bytes.Equal(current.Raw, list.Raw) {
			return nil, errAccountRegistrationCRLSnapshotAdvanced
		}

		used[selected] = true
	}
	again, err := read(ctx, "Pod/"+ns+"/"+deploymentplan.String(pm["name"]))
	if err != nil || deploymentplan.Map(again["metadata"])["uid"] != pm["uid"] || deploymentplan.Map(again["metadata"])["deletionTimestamp"] != nil {
		return fail()
	}
	current, err := read(ctx, id)
	if err != nil || deploymentplan.Map(current["metadata"])["uid"] != uid || !deploymentplan.Matches(deploymentplan.Projection(actual), current) {
		return fail()
	}
	out := deploymentplan.Clone(listener)
	data := deploymentplan.Map(out["data"])
	if data == nil {
		return fail()
	}
	data["client.crl"] = base64.StdEncoding.EncodeToString(public)
	return out, nil
}

// Keep the legacy static profile entirely on its existing path. A selected
// exporter and its operator selection must agree; neither is a fallback.
func verifyAccountRegistrationPublicCRLRuntime(store secretStore, now time.Time, deployments liveDeploymentList) error {
	selected, err := store.read("operator/env/ACCOUNT_MANAGER_SERVICE_REGISTRATION_CRL_REFRESH_PROFILE")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("Account Manager CRL profile selection is unreadable")
	}
	declared := false
	for _, d := range deployments.Items {
		if d.Metadata.Name != "account-manager" {
			continue
		}
		for _, c := range d.Spec.Template.Spec.Containers {
			for _, e := range c.Env {
				declared = declared || (c.Name == "app" && e.Name == "ACCOUNT_MANAGER_SERVICE_REGISTRATION_CLIENT_CRL" && e.Value == accountRegistrationCRLBundlePath) || (c.Name == "pkimanagement" && e.Name == accountRegistrationCRLExportEnv && e.Value != "")
			}
		}
	}
	if selected == "" && !declared {
		return nil
	}
	if selected != accountRegistrationCRLRefreshProfile || !declared {
		return fmt.Errorf("Account Manager public CRL export and selected profile differ")
	}
	operator, err := store.readOperator()
	if err != nil {
		return fmt.Errorf("Account Manager CRL profile operator selection is unavailable")
	}
	image, err := accountRegistrationCRLRefreshInputs(store.Environment, operator)
	if err != nil {
		return err
	}
	if image == "" {
		return nil
	}
	ctx := context.Background()
	if store.checkRuntime != nil {
		ctx = store.checkRuntime.ctx
	}
	command := func(_ context.Context, args ...string) ([]byte, error) {
		return secretCheckKubectl([]*deploymentCheckRuntime{store.checkRuntime}, false, append([]string{"--kubeconfig", store.KubeconfigPath()}, args...)...)
	}
	read := func(ctx context.Context, id string) (deploymentplan.Object, error) {
		parts := strings.SplitN(id, "/", 3)
		if len(parts) != 3 {
			return nil, fmt.Errorf("invalid public CRL resource identity")
		}
		raw, err := command(ctx, "-n", parts[1], "get", strings.ToLower(parts[0]), parts[2], "-o", "json")
		if err != nil {
			return nil, err
		}
		var o deploymentplan.Object
		if json.Unmarshal(raw, &o) != nil {
			return nil, fmt.Errorf("invalid public CRL resource metadata")
		}
		return o, nil
	}
	stack := "video-cloud-" + store.Environment
	cfg := deploymentConfig{Environment: store.Environment, Values: map[string]string{"CLOUD_STACK_NAME": stack}}
	account, err := read(ctx, "Deployment/"+stack+"-account-manager/account-manager")
	if err != nil {
		return fmt.Errorf("selected Account Manager public CRL owner unavailable")
	}
	listener, err := read(ctx, "Secret/"+stack+"-account-manager/"+serviceRegistrationTLSSecretName)
	if err != nil {
		return fmt.Errorf("original Account Manager registration listener unavailable")
	}
	current, err := readAccountRegistrationPublicListener(ctx, cfg, store, account, listener, now, read, command)
	if err != nil {
		return err
	}
	return validateRegistrationServerMaterialAt(current, lkeRegistrationServerDNS(cfg.Values), now)
}

// Native admission may add only its exact service-account projection. Pod-kind
// matching retains the maintained empty EnvVar/readOnly default normalization;
// arrays still require the same names, count and ordering as the owned template.
func accountRegistrationPublicPodMatches(declared deploymentplan.Object, pod deploymentplan.Object) bool {
	wanted := deploymentplan.Clone(declared)
	observed := deploymentplan.Clone(deploymentplan.Map(pod["spec"]))
	if len(certIssuerObjectList(observed["ephemeralContainers"])) != 0 {
		return false
	}
	regular := certIssuerObjectList(observed["containers"])
	initial := certIssuerObjectList(observed["initContainers"])
	for _, v := range certIssuerObjectList(observed["volumes"]) {
		if strings.HasPrefix(deploymentplan.String(deploymentplan.Map(v)["name"]), "kube-api-access-") && wanted["automountServiceAccountToken"] == false {
			return false
		}
	}
	combined := append(append([]any{}, regular...), initial...)
	observed["containers"] = combined
	if !logIngesterPVCWriterAdmissionProjection(observed) {
		return false
	}
	observed["containers"] = combined[:len(regular)]
	observed["initContainers"] = combined[len(regular):]
	for _, field := range []string{"containers", "initContainers"} {
		a, b := certIssuerObjectList(wanted[field]), certIssuerObjectList(observed[field])
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			x, y := deploymentplan.Map(a[i]), deploymentplan.Map(b[i])
			// An admission plugin may not add an executable or an undeclared source.
			for _, key := range []string{"envFrom", "command", "args"} {
				if deploymentplan.Digest(x[key]) != deploymentplan.Digest(y[key]) {
					return false
				}
			}
		}
	}
	return deploymentplan.Matches(deploymentplan.Object{"kind": "Pod", "spec": wanted}, deploymentplan.Object{"kind": "Pod", "spec": observed})
}

func verifyAccountRegistrationPublicCRLForBackend(ctx context.Context, cfg deploymentConfig, store secretStore, b environmentKubernetesBackend, expected deploymentplan.Object, now time.Time) error {
	ns := cfg.Values["CLOUD_STACK_NAME"] + "-account-manager"
	listener, err := b.Read(ctx, "Secret/"+ns+"/"+serviceRegistrationTLSSecretName)
	if err != nil || listener == nil {
		return fmt.Errorf("original Account Manager registration listener unavailable")
	}
	current, err := readAccountRegistrationPublicListener(ctx, cfg, store, expected, listener, now, b.Read, func(ctx context.Context, args ...string) ([]byte, error) { return b.command(ctx, nil, args...) })
	if err != nil {
		return err
	}
	return validateRegistrationServerMaterialAt(current, lkeRegistrationServerDNS(cfg.Values), now)
}

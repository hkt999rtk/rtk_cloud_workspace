package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// A serving Secret is the completed producer's output. Observe its original
// enrollment/export/retirement and committed KV identity before skipping that
// producer; a new temporary session must not reenroll the permanent server or
// resurrect its retired exporter.
func observeCompletedEnvironmentServerPublication(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, runtime environmentCreateBootstrapRuntime, service environmentCreateServiceCeremony, owner environmentCreateApplicationOwner) (bool, error) {
	e := runtime.Execution
	if e == nil || cfg.Values["PKI_STATE_BACKEND"] != "openbao" {
		return false, nil
	}
	finalKey := "kubernetes/server-secret-" + owner.Name
	final := createOpenBaoACKCompletionEntry(e.Journal, finalKey)
	if final == nil || final.State != "completed" {
		return false, nil
	}
	bad := func(boundary string) (bool, error) {
		return false, environmentCreateBootstrapBlocker("completed API server publication differs: owner=" + owner.Name + " boundary=" + boundary)
	}
	ns := p.Stack + "-video-cloud"
	serverName := owner.Name + "." + ns + ".svc"
	serverPath := filepath.Join(filepath.Dir(owner.ClientState), "server.json")
	if e.Store != runtime.Backend.Store || e.Journal.PlanSHA256 != runtime.ExecutionSHA256 || e.Journal.Environment != cfg.Environment || p.Environment != cfg.Environment || p.Stack != e.Journal.Stack || runtime.Custody == nil || runtime.Custody.Generation != runtime.ExecutionSHA256 || runtime.Custody.ServiceRootSHA256 != service.Root.RootFingerprint || !environmentCreateExportOwnerMatches(p, ns, owner.Name, owner.Role, owner.Claim, owner.StateRoot, owner.ClientState, serverPath, serverName) || e.Check(ctx) != nil {
		return bad("original-scope")
	}
	accepted := func(key string) (*deploymentplan.Plan, deploymentplan.Journal, error) {
		plan, j, err := managedTransportPostAcceptedStage(e.Store, e, key)
		if err != nil {
			return nil, j, err
		}
		checked, exists, err := readEnvironmentCreateStageProgress(e.Store, e, strings.TrimPrefix(key, "kubernetes/"), plan)
		if err != nil || !exists || deploymentplan.Digest(checked) != deploymentplan.Digest(j) {
			return nil, j, environmentCreateBootstrapBlocker("completed publication lacks its original native intent and lease: " + key)
		}
		return plan, j, nil
	}
	resource := func(plan *deploymentplan.Plan, id string) *deploymentplan.Resource {
		for i := range plan.Resources {
			if plan.Resources[i].ID == id {
				return &plan.Resources[i]
			}
		}
		return nil
	}
	enrollment, _, err := accepted("kubernetes/server-enrollment-" + owner.Name)
	if err != nil {
		return bad("enrollment")
	}
	var job deploymentplan.Object
	for _, r := range enrollment.Resources {
		if r.Desired["kind"] == "Job" {
			if job != nil {
				return bad("enrollment-owner")
			}
			job = r.Desired
		}
	}
	containers := certIssuerObjectList(environmentPKIPod(job)["containers"])
	if len(containers) != 1 {
		return bad("enrollment-recipe")
	}
	c := deploymentplan.Map(containers[0])
	request := mqttFoundationEnv(c, "PKI_SERVER_BOOTSTRAP_REQUEST_ID")
	originalSession, ok := strings.CutSuffix(request, ":"+owner.Name+":server")
	if !ok || !environmentBootstrapOwnerUUID.MatchString(originalSession) || mqttFoundationEnv(c, "PKI_ENVIRONMENT") != cfg.Environment || mqttFoundationEnv(c, "PKI_SERVER_DOMAIN") != "service" || mqttFoundationEnv(c, "PKI_SERVER_NAME") != serverName || mqttFoundationEnv(c, "PKI_SERVER_DNS_NAMES") != serverName || mqttFoundationEnv(c, "PKI_SERVER_ROOT_SHA256") != service.Root.RootFingerprint || mqttFoundationEnv(c, "PKI_SERVER_IDENTITY_STATE") != serverPath || mqttFoundationEnv(c, "PKI_SERVER_BOOTSTRAP_OWNER_CLIENT_STATE") != owner.ClientState || mqttFoundationEnv(c, "PKI_SERVER_BOOTSTRAP_OWNER_CLIENT_SUBJECT") != "service:"+owner.Role || mqttFoundationEnv(c, "PKI_SERVER_BOOTSTRAP_OWNER_CLIENT_ROOT_SHA256") != service.Root.RootFingerprint {
		return bad("original-server-request")
	}
	readerName := "pki-export-" + owner.Name + "-" + runtime.ExecutionSHA256[:12]
	readerID := "Deployment/" + ns + "/" + readerName
	custody, cj, err := accepted("kubernetes/server-custody-" + owner.Name)
	if err != nil {
		return bad("custody")
	}
	reader := resource(custody, readerID)
	if reader == nil {
		return bad("reader")
	}
	var proof struct {
		Certificate string                            `json:"certificate_sha256"`
		Reader      string                            `json:"reader_uid"`
		Pod         string                            `json:"pod_uid"`
		ReplicaSet  string                            `json:"replicaset_uid"`
		Server      string                            `json:"server_name"`
		Client      *environmentCustodyStateReference `json:"client_state"`
		State       *environmentCustodyStateReference `json:"server_state"`
	}
	exported := createOpenBaoACKCompletionEntry(e.Journal, "server-export-"+owner.Name)
	if exported == nil || exported.State != "completed" || exported.Phase != "bootstrap" || exported.CompletedAt.IsZero() || decodeEnvironmentJSON(exported.Receipt, &proof) != nil || proof.Server != serverName || proof.Reader != cj.Owners[readerID] || !environmentBootstrapOwnerUUID.MatchString(proof.Reader) || !environmentBootstrapOwnerUUID.MatchString(proof.Pod) || !environmentBootstrapOwnerUUID.MatchString(proof.ReplicaSet) || !environmentPreflightDigest.MatchString(proof.Certificate) {
		return bad("export-receipt")
	}
	if validateEnvironmentCustodyStateReference(proof.Client, cfg, runtime.Custody, ns, owner.Claim, owner.ClientState, service.Root.RootFingerprint) != nil || validateEnvironmentCustodyStateReference(proof.State, cfg, runtime.Custody, ns, owner.Claim, serverPath, service.Root.RootFingerprint) != nil {
		return bad("kv-references")
	}
	intent := deploymentplan.Object{"owner": owner.Name, "namespace": ns, "reader_uid": proof.Reader, "pod_uid": proof.Pod, "replicaset_uid": proof.ReplicaSet, "server_name": serverName, "root_sha256": service.Root.RootFingerprint, "client_state": proof.Client, "server_state": proof.State}
	if exported.IntentSHA256 != activeTransportJournalDigest(intent) {
		return bad("export-intent")
	}
	retireKey := "retire-" + readerName
	retire := createOpenBaoACKCompletionEntry(e.Journal, retireKey)
	retireIntent := deploymentplan.Object{"resource_id": readerID, "uid": proof.Reader}
	expectedRetired := deploymentplan.Object{"resource_id": readerID, "uid": proof.Reader, "absent": true}
	if retire == nil || retire.State != "completed" || retire.Phase != "bootstrap" || retire.CompletedAt.IsZero() || retire.IntentSHA256 != activeTransportJournalDigest(retireIntent) || activeTransportJournalDigest(jsonRawObject(retire.Receipt)) != activeTransportJournalDigest(expectedRetired) {
		return bad("retirement-receipt")
	}
	retirement, _, err := accepted("kubernetes/" + retireKey)
	if err != nil || len(retirement.Resources) != 1 {
		return bad("retirement-child")
	}
	retired := retirement.Resources[0]
	if retired.ID != readerID || retired.Action != "retire" || retired.UID != proof.Reader || retired.Baseline == nil || !deploymentplan.Matches(reader.Desired, retired.Baseline) || retire.CompletedAt.Before(exported.CompletedAt) || final.CompletedAt.Before(retire.CompletedAt) {
		return bad("retirement-owner")
	}
	secretPlan, sj, err := accepted(finalKey)
	if err != nil || len(secretPlan.Resources) != 1 {
		return bad("serving-secret")
	}
	secret := secretPlan.Resources[0]
	secretID := "Secret/" + ns + "/" + owner.Name + "-tls"
	if secret.ID != secretID || secret.Desired["type"] != "Opaque" {
		return bad("secret-recipe")
	}
	verifyCurrent := func() error {
		if err := e.Check(ctx); err != nil {
			return err
		}
		absent, err := runtime.Backend.Read(ctx, readerID)
		if err != nil || absent != nil {
			return environmentCreateBootstrapBlocker("completed server exporter is no longer retired")
		}
		live, err := runtime.Backend.Read(ctx, secretID)
		if err != nil || live == nil || deploymentplan.Map(live["metadata"])["uid"] != sj.Owners[secretID] || deploymentplan.Map(live["metadata"])["deletionTimestamp"] != nil || !deploymentplan.Matches(secret.Desired, live) {
			return environmentCreateBootstrapBlocker("completed serving Secret differs from its original owner or material")
		}
		return nil
	}
	if err := verifyCurrent(); err != nil {
		return false, err
	}
	data := deploymentplan.Map(secret.Desired["data"])
	cert, ce := base64.StdEncoding.Strict().DecodeString(deploymentplan.String(data["tls.crt"]))
	key, ke := base64.StdEncoding.Strict().DecodeString(deploymentplan.String(data["tls.key"]))
	ca, ae := base64.StdEncoding.Strict().DecodeString(deploymentplan.String(data["ca.crt"]))
	pair, pe := tls.X509KeyPair(cert, key)
	chain, chainErr := managedApplicationPublicChain(cert)
	root, re := parseFirstCertificatePEM([]byte(service.Root.Certificate))
	issuer, ie := parseFirstCertificatePEM([]byte(service.Intermediate.Certificate))
	if len(data) != 3 || ce != nil || ke != nil || ae != nil || pe != nil || len(pair.Certificate) != 3 || chainErr != nil || len(chain) != 3 || re != nil || ie != nil || string(ca) != service.Root.Certificate || environmentPreflightBytesSHA256(cert) != proof.Certificate || environmentPreflightBytesSHA256(root.Raw) != service.Root.RootFingerprint || !bytes.Equal(chain[1].Raw, issuer.Raw) || !bytes.Equal(chain[2].Raw, root.Raw) || issuer.CheckSignatureFrom(root) != nil || chain[0].CheckSignatureFrom(issuer) != nil || chain[0].VerifyHostname(serverName) != nil || !slices.Equal(chain[0].DNSNames, []string{serverName}) || !chain[0].NotAfter.After(time.Now()) || chain[0].NotBefore.After(time.Now()) {
		return bad("registered-certificate")
	}
	source := environmentPKIPod(reader.Desired)
	for _, ref := range []*environmentCustodyStateReference{proof.Client, proof.State} {
		domain, subject := "", "service:"+owner.Role
		var names []string
		if ref.StatePath == serverPath {
			domain, subject = "service", serverName
			names = []string{serverName}
		}
		state, err := inspectEnvironmentCustodyIdentity(ctx, cfg, runtime.Backend, runtime.Custody, ref, source, p.Image, domain, subject, names)
		if err != nil {
			return false, err
		}
		if state.Pending || ref.StatePath == serverPath && (state.Fingerprint != environmentPreflightBytesSHA256(chain[0].Raw) || state.PublicKeySHA256 != environmentPreflightBytesSHA256(chain[0].RawSubjectPublicKeyInfo)) {
			return bad("committed-kv-identity")
		}
	}
	if err := verifyCurrent(); err != nil {
		return false, err
	}
	return true, nil
}

// A retired exporter is evidence, not an owner to recreate. Re-export the same
// committed KV record through the authenticated permanent management container.
func readCompletedEnvironmentAccountServer(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, runtime environmentCreateBootstrapRuntime, service environmentCreateServiceCeremony) (environmentCreateOwnedServerMaterial, error) {
	var empty environmentCreateOwnedServerMaterial
	bad := func(boundary string) (environmentCreateOwnedServerMaterial, error) {
		return empty, environmentCreateBootstrapBlocker("completed account serving export differs from its original owner; boundary=" + boundary)
	}
	e := runtime.Execution
	remote := cfg.Values["PKI_STATE_BACKEND"] == "openbao"
	ns := p.Stack + "-account-manager"
	name := "account-manager." + ns + ".svc.cluster.local"
	clientPath, serverPath := environmentCreateServiceState+"/private/client.json", environmentCreateServiceState+"/private/server.json"
	if e == nil || e.Store != runtime.Backend.Store || remote && (runtime.Custody == nil || runtime.Custody.Generation != e.Journal.PlanSHA256 || runtime.Custody.ServiceRootSHA256 != service.Root.RootFingerprint) || e.Check(ctx) != nil {
		return bad("original-generation")
	}
	accepted := func(stage string) (*deploymentplan.Plan, deploymentplan.Journal, error) {
		plan, j, err := managedTransportPostAcceptedStage(e.Store, e, "kubernetes/"+stage)
		if err != nil {
			return nil, j, err
		}
		checked, exists, err := readEnvironmentCreateStageProgress(e.Store, e, stage, plan)
		if err != nil || !exists || deploymentplan.Digest(checked) != deploymentplan.Digest(j) {
			return nil, j, environmentCreateBootstrapBlocker("completed account export child intent or native lease changed")
		}
		return plan, j, nil
	}
	readerName := "pki-export-account-manager-" + e.Journal.PlanSHA256[:12]
	readerID := "Deployment/" + ns + "/" + readerName
	custody, cj, err := accepted("server-custody-account-manager")
	if err != nil {
		return bad("accepted-exporter")
	}
	originalReader := environmentCreateOwnedServerReader(p, service, ns, readerName, "account-manager", "account-manager-service-state", environmentCreateServiceState, clientPath, serverPath, name, remote)
	expected, err := projectEnvironmentCustodyFrozenRecipe(cfg, p, e.Journal.PlanSHA256, service.Root.RootFingerprint, []deploymentplan.Definition{originalReader}, custody)
	if err != nil || len(expected) != len(custody.Resources) {
		return bad("accepted-exporter-recipe")
	}
	// Compile applies the ordinary managed labels and native allocation rules.
	// Reuse only this child's authenticated original inventory, never current
	// live bodies, when reproducing its exact projected recipe and graph.
	var inventory []deploymentplan.Object
	for _, r := range custody.Resources {
		if r.Baseline != nil {
			o := deploymentplan.Clone(r.Baseline)
			deploymentplan.Map(o["metadata"])["uid"] = r.UID
			inventory = append(inventory, o)
		}
	}
	rebuilt, err := deploymentplan.Compile(custody.Inputs, expected, inventory)
	if err != nil || rebuilt.SHA256() != custody.SHA256() {
		return bad("accepted-exporter-recipe")
	}
	var reader deploymentplan.Resource
	for _, r := range custody.Resources {
		if r.ID == readerID {
			if reader.ID != "" {
				return bad("ambiguous-exporter-recipe")
			}
			reader = r
		}
	}
	if reader.ID != readerID {
		return bad("accepted-exporter")
	}
	var proof struct {
		Certificate string                            `json:"certificate_sha256"`
		Claim       string                            `json:"claim_uid"`
		Reader      string                            `json:"reader_uid"`
		Pod         string                            `json:"pod_uid"`
		ReplicaSet  string                            `json:"replicaset_uid"`
		Server      string                            `json:"server_name"`
		Client      *environmentCustodyStateReference `json:"client_state"`
		State       *environmentCustodyStateReference `json:"server_state"`
	}
	exported := createOpenBaoACKCompletionEntry(e.Journal, "server-export-account-manager")
	if exported == nil || exported.State != "completed" || exported.Phase != "bootstrap" || exported.CompletedAt.IsZero() || decodeEnvironmentJSON(exported.Receipt, &proof) != nil || proof.Reader != cj.Owners[readerID] || !environmentBootstrapOwnerUUID.MatchString(proof.Reader) || !environmentBootstrapOwnerUUID.MatchString(proof.Pod) || !environmentBootstrapOwnerUUID.MatchString(proof.ReplicaSet) || proof.Server != name || !environmentPreflightDigest.MatchString(proof.Certificate) {
		return bad("export-receipt")
	}
	intent := deploymentplan.Object{"owner": "account-manager", "namespace": ns, "reader_uid": proof.Reader, "pod_uid": proof.Pod, "replicaset_uid": proof.ReplicaSet, "server_name": name, "root_sha256": service.Root.RootFingerprint}
	if remote {
		if proof.Claim != "" || validateEnvironmentCustodyStateReference(proof.Client, cfg, runtime.Custody, ns, "account-manager-service-state", clientPath, service.Root.RootFingerprint) != nil || validateEnvironmentCustodyStateReference(proof.State, cfg, runtime.Custody, ns, "account-manager-service-state", serverPath, service.Root.RootFingerprint) != nil {
			return bad("original-kv-references")
		}
		intent["client_state"], intent["server_state"] = proof.Client, proof.State
	} else {
		if !environmentBootstrapOwnerUUID.MatchString(proof.Claim) || proof.Client != nil || proof.State != nil {
			return bad("original-claim-reference")
		}
		intent["claim_uid"] = proof.Claim
	}
	if exported.IntentSHA256 != activeTransportJournalDigest(intent) {
		return bad("export-intent")
	}
	retireKey := "retire-" + readerName
	retired := createOpenBaoACKCompletionEntry(e.Journal, retireKey)
	retireIntent := deploymentplan.Object{"resource_id": readerID, "uid": proof.Reader}
	retireReceipt := deploymentplan.Object{"resource_id": readerID, "uid": proof.Reader, "absent": true}
	if retired == nil || retired.State != "completed" || retired.Phase != "bootstrap" || retired.CompletedAt.IsZero() || retired.StartedAt.Before(exported.CompletedAt) || retired.IntentSHA256 != activeTransportJournalDigest(retireIntent) || activeTransportJournalDigest(jsonRawObject(retired.Receipt)) != activeTransportJournalDigest(retireReceipt) {
		return bad("retirement-receipt")
	}
	retirement, _, err := accepted(retireKey)
	if err != nil || len(retirement.Resources) != 1 {
		return bad("retirement-child")
	}
	rr := retirement.Resources[0]
	if rr.ID != readerID || rr.Action != "retire" || rr.UID != proof.Reader || rr.Baseline == nil || !deploymentplan.Matches(reader.Desired, rr.Baseline) {
		return bad("retired-original-owner")
	}
	core, _, err := acceptedCurrentCoreStage(e.Store, e)
	if err != nil {
		return empty, err
	}
	var definitions []deploymentplan.Definition
	for _, r := range core.Resources {
		definitions = append(definitions, deploymentplan.Definition{Object: deploymentplan.Clone(r.Desired), Phase: r.Phase, Dependencies: slices.Clone(r.Dependencies)})
	}
	definitions, err = currentEnvironmentCreateAccountRegistrationCoreWithBackend(ctx, cfg, runtime, definitions, runtime.Backend)
	if err != nil {
		return empty, err
	}
	id := "Deployment/" + ns + "/account-manager"
	var owner deploymentplan.Object
	for _, d := range definitions {
		if deploymentplan.ID(d.Object) == id {
			owner = d.Object
		}
	}
	if owner == nil {
		return bad("permanent-owner")
	}
	ownerUID := deploymentplan.String(deploymentplan.Map(owner["metadata"])["uid"])
	var manager deploymentplan.Object
	for _, raw := range certIssuerObjectList(environmentPKIPod(owner)["containers"]) {
		c := deploymentplan.Map(raw)
		if c["name"] == "pkimanagement" {
			if manager != nil {
				return bad("ambiguous-management-container")
			}
			manager = c
		}
	}
	if manager == nil || !immutableEnvironmentImage.MatchString(deploymentplan.String(manager["image"])) || deploymentplan.Digest(manager["command"]) != deploymentplan.Digest([]any{"/app/pkimanagement"}) {
		return bad("native-export-container")
	}
	settings := map[string]string{"PKI_MANAGEMENT_IDENTITY_STATE": clientPath, "PKI_MANAGEMENT_IDENTITY_ROOT_SHA256": service.Root.RootFingerprint, "PKI_MANAGEMENT_ACCOUNT_HOST_IDENTITY_STATE": serverPath, "PKI_MANAGEMENT_ACCOUNT_HOST_NAME": name, "PKI_MANAGEMENT_ACCOUNT_HOST_ROOT_SHA256": service.Root.RootFingerprint}
	if remote {
		settings["PKI_STATE_BACKEND"], settings["PKI_STATE_KV_MOUNT"], settings["PKI_STATE_RECORD_PREFIX"] = "openbao", proof.State.Mount, cfg.Environment+"/"+proof.State.Generation+"/"+proof.State.Owner
		if environmentPKIPod(owner)["serviceAccountName"] != proof.State.ServiceAccount {
			return bad("permanent-kv-service-account")
		}
	} else if !managedLateCompletedClaimMount(environmentPKIPod(owner), manager, "service-state", "account-manager-service-state", environmentCreateServiceState) {
		return bad("permanent-claim-profile")
	}
	for key, value := range settings {
		if mqttFoundationEnv(manager, key) != value {
			return bad("permanent-state-profile")
		}
	}
	pod, err := selectEnvironmentCreateExportPod(ctx, runtime.Backend, owner, ownerUID)
	if err != nil {
		return empty, err
	}
	verify := func() error {
		if err := e.Check(ctx); err != nil {
			return err
		}
		old, err := runtime.Backend.Read(ctx, readerID)
		if err != nil || old != nil {
			return environmentCreateBootstrapBlocker("completed account exporter is no longer retired")
		}
		if !remote {
			claim, err := runtime.Backend.Read(ctx, "PersistentVolumeClaim/"+ns+"/account-manager-service-state")
			if err != nil || claim == nil || deploymentplan.Map(claim["metadata"])["uid"] != proof.Claim || deploymentplan.Map(claim["metadata"])["deletionTimestamp"] != nil {
				return environmentCreateBootstrapBlocker("completed account original claim changed")
			}
		}
		actual, err := runtime.Backend.Read(ctx, id)
		if err != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != ownerUID || deploymentplan.Map(actual["metadata"])["deletionTimestamp"] != nil || !deploymentplan.Matches(owner, actual) {
			return environmentCreateBootstrapBlocker("permanent account export owner changed")
		}
		return pod.Verify(ctx, runtime.Backend, owner, ownerUID)
	}
	if err := verify(); err != nil {
		return empty, err
	}
	args := []string{"--namespace", ns, "exec", "pod/" + pod.Name, "--container", "pkimanagement", "--", "env", "PKI_ENVIRONMENT=" + cfg.Environment, "PKI_SERVER_NAME=" + name, "PKI_SERVER_BOOTSTRAP_OWNER_CLIENT_STATE=" + clientPath, "PKI_SERVER_BOOTSTRAP_OWNER_CLIENT_SUBJECT=service:account-manager", "PKI_SERVER_BOOTSTRAP_OWNER_CLIENT_ROOT_SHA256=" + service.Root.RootFingerprint, "/app/serviceidentity-bootstrap", "export-server", serverPath, "service", "service:account-manager", service.Root.RootFingerprint, name}
	raw, err := runtime.Backend.command(ctx, nil, args...)
	var material environmentCreateOwnedServerMaterial
	if err != nil || len(raw) > 256<<10 || decodeEnvironmentJSON(raw, &material) != nil || material.Environment != cfg.Environment || material.Root != service.Root.RootFingerprint || material.Name != name || environmentPreflightBytesSHA256([]byte(material.Certificate)) != proof.Certificate {
		return bad("same-registered-certificate")
	}
	pair, err := tls.X509KeyPair([]byte(material.Certificate), []byte(material.Key))
	chain, ce := managedApplicationPublicChain([]byte(material.Certificate))
	root, re := parseFirstCertificatePEM([]byte(service.Root.Certificate))
	issuer, ie := parseFirstCertificatePEM([]byte(service.Intermediate.Certificate))
	if err != nil || len(pair.Certificate) != 3 || ce != nil || len(chain) != 3 || re != nil || ie != nil || !bytes.Equal(chain[1].Raw, issuer.Raw) || !bytes.Equal(chain[2].Raw, root.Raw) || chain[0].CheckSignatureFrom(issuer) != nil || issuer.CheckSignatureFrom(root) != nil || chain[0].VerifyHostname(name) != nil || environmentPreflightBytesSHA256(root.Raw) != service.Root.RootFingerprint || chain[0].NotBefore.After(time.Now()) || !chain[0].NotAfter.After(time.Now()) {
		return bad("same-key-and-chain")
	}
	if err := verify(); err != nil {
		return empty, err
	}
	material.registrationCore = definitions
	return material, nil
}

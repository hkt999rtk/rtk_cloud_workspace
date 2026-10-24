package main

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const managedLateSeedForwardSchema = "rtk-cloud.create-managed-late-seed-trust-forward/v1"

type createManagedLateSeedForward struct {
	Schema, OriginalReview, SourceSHA, SourceOwnerSHA, OriginalArtifactSHA string
	FailedPlanSHA, FailedJournalSHA                                        string
	OriginalSource                                                         environmentCreateJournal
	FailedPlan                                                             *deploymentplan.Plan
	FailedJournal                                                          deploymentplan.Journal
	FailedJobUID, FailedPodUID                                             string
	TrustOwner                                                             deploymentplan.Resource
	Candidate                                                              *deploymentplan.Plan
	NativeEvidence                                                         map[string]string
	Review                                                                 string `json:",omitempty"`
}

func managedLateSeedForwardBlock(stage string) error {
	return environmentCreateBootstrapBlocker("same-SID seed trust forward requires its original terminal no-mint source: " + stage)
}

// A failed one-shot signing Job is historical evidence, never a retry target.
func validateManagedLateSeedForwardFailedChild(f *createManagedLateSeedForward) error {
	if f == nil || f.Schema != managedLateSeedForwardSchema || !environmentPreflightDigest.MatchString(f.OriginalReview) || !environmentPreflightDigest.MatchString(f.SourceSHA) || !environmentPreflightDigest.MatchString(f.SourceOwnerSHA) || !environmentPreflightDigest.MatchString(f.OriginalArtifactSHA) || f.FailedPlan == nil || len(f.FailedPlan.Resources) != 1 || f.FailedPlan.SHA256() != f.FailedPlanSHA || deploymentplan.Digest(f.FailedJournal) != f.FailedJournalSHA || !environmentBootstrapOwnerUUID.MatchString(f.FailedJobUID) || f.FailedPodUID != "" && !environmentBootstrapOwnerUUID.MatchString(f.FailedPodUID) {
		return managedLateSeedForwardBlock("failed-child-digests")
	}
	p, j := f.FailedPlan, f.FailedJournal
	r := p.Resources[0]
	if p.Version != deploymentplan.Version || p.Operation != "scoped" || p.Environment != f.OriginalSource.Environment || p.Stack != f.OriginalSource.Stack || !environmentPreflightDigest.MatchString(f.OriginalSource.PlanSHA256) || r.ID != deploymentplan.ID(r.Desired) || r.Desired["kind"] != "Job" || r.Phase != "bootstrap" || r.Action != "create" || r.UID != "" || r.Baseline != nil || len(r.Dependencies) != 0 || j.PlanSHA256 != p.SHA256() || j.Failed != r.ID || j.InFlight != "" || j.Accepted || j.AcceptanceStarted || len(j.Completed) != 0 || len(j.Owners) != 1 || j.Owners[r.ID] != f.FailedJobUID {
		return managedLateSeedForwardBlock("one-terminal-created-job")
	}
	return nil
}

func managedLateSeedForwardDefinitions(f *createManagedLateSeedForward) ([]deploymentplan.Definition, error) {
	if err := validateManagedLateSeedForwardFailedChild(f); err != nil {
		return nil, err
	}
	old := f.FailedPlan.Resources[0]
	trust := f.TrustOwner
	ns := f.FailedPlan.Stack + "-video-cloud"
	if trust.ID != "ConfigMap/"+ns+"/pki-create-openbao-tls-consumer-trust" || deploymentplan.ID(trust.Desired) != trust.ID || !environmentBootstrapOwnerUUID.MatchString(trust.UID) || trust.Desired["kind"] != "ConfigMap" || deploymentplan.Map(trust.Desired["metadata"])["deletionTimestamp"] != nil || !managedLateSeedForwardRoots(deploymentplan.String(deploymentplan.Map(trust.Desired["data"])["roots.pem"]), "", time.Time{}) {
		return nil, managedLateSeedForwardBlock("original-managed-transport-trust")
	}
	object := deploymentplan.Clone(old.Desired)
	metadata := deploymentplan.Map(object["metadata"])
	name := deploymentplan.String(metadata["name"])
	if !regexp.MustCompile(`^pki-bootstrap-renew-[a-f0-9]{12}$`).MatchString(name) || metadata["uid"] != nil || metadata["namespace"] != ns {
		return nil, managedLateSeedForwardBlock("original-seed-name")
	}
	pod := environmentPKIPod(object)
	volumes, err := environmentPKINamedFields(pod["volumes"])
	if err != nil || volumes["openbao-ca"] == nil || deploymentplan.Map(volumes["openbao-ca"]["configMap"])["name"] != "pki-openbao-transport-ca" || len(volumes["openbao-ca"]) != 2 {
		return nil, managedLateSeedForwardBlock("original-ca-volume")
	}
	containers := certIssuerObjectList(pod["containers"])
	if len(containers) != 1 || len(certIssuerObjectList(pod["initContainers"])) != 1 {
		return nil, managedLateSeedForwardBlock("original-native-executor")
	}
	c := deploymentplan.Map(containers[0])
	mounts, err := environmentPKINamedFields(c["volumeMounts"])
	if err != nil || mounts["openbao-ca"] == nil || mounts["openbao-ca"]["mountPath"] != "/run/openbao-ca" || mounts["openbao-ca"]["readOnly"] != true || mqttFoundationEnv(c, "OPENBAO_CACERT") != "/run/openbao-ca/ca.crt" || !immutableEnvironmentImage.MatchString(deploymentplan.String(c["image"])) || !environmentBootstrapOwnerUUID.MatchString(mqttFoundationEnv(c, "PKI_BOOTSTRAP_SESSION_ID")) || !regexp.MustCompile(`^/run/pki-bootstrap/renew-[a-f0-9]{12}/bootstrap\.crt$`).MatchString(mqttFoundationEnv(c, "PKI_BOOTSTRAP_CERT_PATH")) || mqttFoundationEnv(c, "PKI_BOOTSTRAP_KEY_PATH") != "/run/pki-bootstrap/bootstrap.key" || mqttFoundationEnv(c, "PKI_BOOTSTRAP_CSR_PATH") != "/run/pki-bootstrap/bootstrap.csr" {
		return nil, managedLateSeedForwardBlock("unchanged-native-custody-paths")
	}
	metadata["name"] = name + "-f" + f.SourceSHA[:8]
	projection := deploymentplan.Map(volumes["openbao-ca"]["configMap"])
	projection["name"] = "pki-create-openbao-tls-consumer-trust"
	projection["items"] = []any{deploymentplan.Object{"key": "roots.pem", "path": "ca.crt"}}
	return []deploymentplan.Definition{{Phase: old.Phase, Object: object}}, nil
}

func validateManagedLateSeedForwardCandidate(f *createManagedLateSeedForward, candidate *deploymentplan.Plan) error {
	defs, err := managedLateSeedForwardDefinitions(f)
	if err != nil {
		return err
	}
	inputs := f.FailedPlan.Inputs
	inputs.BindingsSHA256 = environmentCreateStageBindingsSHA("managed-late-seed-trust-forward", defs)
	expected, err := deploymentplan.Compile(inputs, defs, nil)
	if err != nil || candidate == nil || candidate.SHA256() != expected.SHA256() || len(candidate.Resources) != 1 || candidate.Resources[0].Action != "create" || candidate.Resources[0].UID != "" || candidate.Resources[0].Baseline != nil {
		return managedLateSeedForwardBlock("one-new-source-bound-ca-only-job")
	}
	return nil
}

// Public trust must consist solely of self-signed CA certificates. The live
// caller also binds the exact original managed Root and current validity.
func managedLateSeedForwardRoots(value, requiredRoot string, now time.Time) bool {
	found, count := requiredRoot == "", 0
	seen := map[string]bool{}
	for strings.TrimSpace(value) != "" {
		if !strings.HasPrefix(strings.TrimSpace(value), "-----BEGIN CERTIFICATE-----") {
			return false
		}
		block, rest := pem.Decode([]byte(value))
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			return false
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil || !cert.IsCA || !cert.BasicConstraintsValid || cert.KeyUsage&x509.KeyUsageCertSign == 0 || cert.CheckSignatureFrom(cert) != nil || !now.IsZero() && (cert.NotBefore.After(now) || !cert.NotAfter.After(now)) {
			return false
		}
		digest := environmentPreflightBytesSHA256(cert.Raw)
		if seen[digest] {
			return false
		}
		seen[digest], count = true, count+1
		found = found || digest == requiredRoot
		value = string(rest)
	}
	return found && count > 0
}

type managedLateSeedForwardNoMintCounts struct {
	Sessions    int `json:"sessions"`
	Deployments int `json:"deployments"`
	Issuances   int `json:"issuances"`
}

func managedLateSeedForwardNoMintQuery(environment, session, deployment string) (string, error) {
	if environment != "dev" || !environmentBootstrapOwnerUUID.MatchString(session) || !regexp.MustCompile(`^create-[a-f0-9]{20}-r[a-f0-9]{12}$`).MatchString(deployment) {
		return "", managedLateSeedForwardBlock("native-no-mint-identifiers")
	}
	return "SELECT json_build_object('sessions',(SELECT count(*) FROM pki_deployment_bootstrap_sessions WHERE environment='" + environment + "' AND session_id='" + session + "'),'deployments',(SELECT count(*) FROM pki_deployment_bootstrap_sessions WHERE environment='" + environment + "' AND deployment_id='" + deployment + "'),'issuances',(SELECT count(*) FROM pki_service_client_issuances WHERE environment='" + environment + "' AND request_id='deployment-bootstrap:" + deployment + "'))", nil
}

// Native readbacks are deliberately separate from the producer. This function
// never deletes the old Job, alters credentials, signals a process or mints.
func verifyManagedLateSeedForwardNoMint(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, r *createServiceSessionRecovery, f *createManagedLateSeedForward) (map[string]string, error) {
	bad := func(stage string) (map[string]string, error) { return nil, managedLateSeedForwardBlock(stage) }
	if _, err := managedLateSeedForwardDefinitions(f); err != nil {
		return nil, err
	}
	if f.Review != "" && !environmentBootstrapOwnerUUID.MatchString(f.FailedPodUID) {
		return bad("admitted-terminal-pod-uid-missing")
	}
	if e == nil || r == nil || r.ManagedLateSession == nil || r.OpenBaoRoot == nil || b.Store.Root != store.Root || e.Store.Root != store.Root || e.Check(ctx) != nil || !managedLateSeedForwardNoMintSourceValid(store, e, r, f) {
		return bad("original-execution-source")
	}
	late := r.ManagedLateSession
	// Only the independently authenticated completed fresh admission may follow
	// the failed source. Authenticate original history on its bounded view while
	// every native read and execution-fence check uses the actual live journal.
	historical := *e
	if deploymentplan.Digest(historical.Journal) != f.SourceSHA {
		historical.Journal.Entries = historical.Journal.Entries[:len(historical.Journal.Entries)-1]
	}
	if deploymentplan.Digest(historical.Journal) != f.SourceSHA {
		return bad("exact-failed-source-prefix")
	}
	source, admitted, err := managedLateSessionRuntimeProgress(store, &historical, late)
	if err != nil || late.AcceptedCore == nil || deploymentplan.Digest(source.Journal) != deploymentplan.Digest(f.OriginalSource) || late.SourceOwnerSHA != f.SourceOwnerSHA || late.Review != f.OriginalReview || deploymentplan.Digest(admitted) != f.OriginalArtifactSHA || !managedLateSamePlan(late.SeedCandidate, f.FailedPlan) || late.Renewal == nil || r.RenewedCertPath != late.NewCertificatePath {
		return bad("original-admitted-failed-seed")
	}
	trustFound := false
	for _, owner := range late.AcceptedCore.Resources {
		if owner.ID == f.TrustOwner.ID {
			trustFound = late.AcceptedCoreJournal.Owners[owner.ID] == f.TrustOwner.UID && deploymentplan.Digest(owner.Desired) == deploymentplan.Digest(f.TrustOwner.Desired)
		}
	}
	trust, err := b.Read(ctx, f.TrustOwner.ID)
	if !trustFound || err != nil || trust == nil || deploymentplan.Map(trust["metadata"])["uid"] != f.TrustOwner.UID || deploymentplan.Map(trust["metadata"])["deletionTimestamp"] != nil || !deploymentplan.Matches(f.TrustOwner.Desired, trust) || !managedLateSeedForwardRoots(deploymentplan.String(deploymentplan.Map(trust["data"])["roots.pem"]), r.OpenBaoRoot.RootFingerprint, time.Now().UTC()) {
		return bad("unchanged-original-managed-ca-owner")
	}
	job := f.FailedPlan.Resources[0]
	actual, err := b.Read(ctx, job.ID)
	if err != nil || actual == nil {
		return bad("terminal-job-readback")
	}
	ns := f.FailedPlan.Stack + "-video-cloud"
	name := deploymentplan.String(deploymentplan.Map(job.Desired["metadata"])["name"])
	raw, err := b.command(ctx, nil, "--namespace", ns, "get", "pods", "--selector", "job-name="+name, "-o", "json")
	var list struct {
		Items []deploymentplan.Object `json:"items"`
	}
	if err != nil || json.Unmarshal(raw, &list) != nil || len(list.Items) != 1 {
		return bad("sole-terminal-owned-pod")
	}
	pod := list.Items[0]
	observedPodUID := deploymentplan.String(deploymentplan.Map(pod["metadata"])["uid"])
	if !environmentBootstrapOwnerUUID.MatchString(observedPodUID) || f.FailedPodUID != "" && f.FailedPodUID != observedPodUID {
		return bad("original-failed-pod-uid")
	}
	// Qualification may discover this sole immutable terminal Pod. An admitted
	// descriptor and every subsequent read require the same populated UUID.
	observed := *f
	observed.FailedPodUID = observedPodUID
	container := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(job.Desired)["containers"])[0])
	logs, err := b.command(ctx, nil, "--namespace", ns, "logs", "pod/"+deploymentplan.String(deploymentplan.Map(pod["metadata"])["name"]), "--container", deploymentplan.String(container["name"]))
	if err != nil || validateManagedLateSeedForwardTerminal(&observed, actual, pod, logs) != nil {
		return bad("terminal-tls-failure-no-running-process")
	}
	lease, err := b.Read(ctx, "Lease/"+ns+"/rtk-deployment-execution")
	if err != nil || lease != nil {
		return bad("original-scoped-lease-not-cleaned")
	}
	runtime := environmentCreateBootstrapRuntime{Execution: e, Backend: b, ServiceSessionRecovery: r}
	if err := verifyManagedSeedOriginalCustody(ctx, runtime, r, late.Renewal); err != nil {
		return bad("original-key-csr-old-certificate")
	}
	path := late.NewCertificatePath
	if !regexp.MustCompile(`^/run/pki-bootstrap/renew-[a-f0-9]{12}/bootstrap\.crt$`).MatchString(path) || mqttFoundationEnv(container, "PKI_BOOTSTRAP_CERT_PATH") != path || mqttFoundationEnv(container, "PKI_BOOTSTRAP_SESSION_ID") != late.NewSessionID {
		return bad("same-prospective-certificate-path")
	}
	privateGuard, err := managedLateSeedForwardCustodyCommand(job.Desired, late)
	if err != nil {
		return nil, err
	}
	reader, err := b.Read(ctx, late.Custody.ID)
	if err != nil || reader == nil || deploymentplan.Map(reader["metadata"])["uid"] != late.Custody.UID || deploymentplan.Map(reader["metadata"])["deletionTimestamp"] != nil || !deploymentplan.Matches(late.Custody.Desired, reader) {
		return bad("original-custody-reader-owner")
	}
	permissions, err := b.command(ctx, nil, "--namespace", r.Ceremony.BootstrapCredentialNamespace, "exec", "deployment/"+r.Ceremony.CustodyReader, "--container", "custody", "--", "/bin/sh", "-ec", privateGuard)
	if err != nil {
		return bad("custody-regular-files-and-prospective-certificate-absence")
	}
	if err := validateManagedLateSeedForwardCustodyPermissions(permissions); err != nil {
		return nil, err
	}
	query, err := managedLateSeedForwardNoMintQuery(cfg.Environment, late.NewSessionID, mqttFoundationEnv(container, "PKI_BOOTSTRAP_DEPLOYMENT_ID"))
	if err != nil {
		return nil, err
	}
	native, err := readEnvironmentCreateRegistryJSON(ctx, cfg, runtime, query)
	if err != nil || !managedLateSeedForwardNoMintCountsValid(native) {
		return bad("native-session-deployment-request-not-absent")
	}
	if e.Check(ctx) != nil {
		return bad("source-changed-after-readback")
	}
	f.FailedPodUID = observedPodUID
	return map[string]string{"terminal-job": deploymentplan.Digest(actual), "terminal-pod": deploymentplan.Digest(pod), "terminal-tls-log": environmentPreflightBytesSHA256(logs), "original-managed-trust": deploymentplan.Digest(f.TrustOwner), "original-key": r.OriginalKeySHA, "original-csr": r.OriginalCSRSHA, "original-certificate": late.Renewal.OriginalCertificateSHA256, "original-custody-permissions": environmentPreflightBytesSHA256(permissions), "original-custody-owner": deploymentplan.Digest(late.Custody), "prospective-certificate-absent": deploymentplan.Digest(path), "native-no-mint": environmentPreflightBytesSHA256(native), "scoped-lease-absent": deploymentplan.Digest(true)}, nil
}

// The original signer uses fsGroup 10001 on the same PVC. Kubernetes can add
// group rw to the retained PUBLIC certificate; the latest permission-only init
// restores the key/CSR to 0600 and the volume root to owner-only 0700/2700.
// The reader itself requires no fsGroup change, path fallback or native chmod.
func managedLateSeedForwardCustodyCommand(seed deploymentplan.Object, late *createManagedLateSessionRecovery) (string, error) {
	bad := func(stage string) (string, error) { return "", managedLateSeedForwardBlock(stage) }
	if late == nil || late.Renewal == nil || late.Custody.ID != "Deployment/"+late.SourceCeremony.BootstrapCredentialNamespace+"/"+late.SourceCeremony.CustodyReader || !environmentBootstrapOwnerUUID.MatchString(late.Custody.UID) || deploymentplan.ID(late.Custody.Desired) != late.Custody.ID || seed["kind"] != "Job" {
		return bad("original-custody-claim-binding")
	}
	security := deploymentplan.Map(environmentPKIPod(seed)["securityContext"])
	if security["runAsUser"] != float64(10001) || security["runAsGroup"] != float64(10001) || security["fsGroup"] != float64(10001) {
		return bad("original-signer-fsgroup-provenance")
	}
	for _, workload := range []deploymentplan.Object{seed, late.Custody.Desired} {
		pod := environmentPKIPod(workload)
		containers := certIssuerObjectList(pod["containers"])
		if len(containers) != 1 {
			return bad("original-custody-claim-binding")
		}
		volumes, vErr := environmentPKINamedFields(pod["volumes"])
		mounts, mErr := environmentPKINamedFields(deploymentplan.Map(containers[0])["volumeMounts"])
		if vErr != nil || mErr != nil {
			return bad("original-custody-claim-binding")
		}
		found := 0
		for name, mount := range mounts {
			if mount["mountPath"] != "/run/pki-bootstrap" {
				continue
			}
			volume := volumes[name]
			pvc := deploymentplan.Map(volume["persistentVolumeClaim"])
			if late.SourceCeremony.BootstrapClaim == "" || pvc["claimName"] != late.SourceCeremony.BootstrapClaim || len(volume) != 2 || mount["subPath"] != nil || mount["subPathExpr"] != nil || mount["readOnly"] != nil && mount["readOnly"] != false {
				return bad("original-custody-claim-binding")
			}
			found++
		}
		if found != 1 {
			return bad("original-custody-claim-binding")
		}
	}
	oldPath, newPath := late.Renewal.OriginalCertificatePath, late.NewCertificatePath
	if !regexp.MustCompile(`^/run/pki-bootstrap/(bootstrap\.crt|renew-[a-f0-9]{12}/bootstrap\.crt)$`).MatchString(oldPath) || !regexp.MustCompile(`^/run/pki-bootstrap/renew-[a-f0-9]{12}/bootstrap\.crt$`).MatchString(newPath) || oldPath == newPath {
		return bad("original-certificate-path")
	}
	script := "test -d /run/pki-bootstrap && test ! -L /run/pki-bootstrap && test -d " + filepath.Dir(oldPath) + " && test ! -L " + filepath.Dir(oldPath) + " && test -d " + filepath.Dir(newPath) + " && test ! -L " + filepath.Dir(newPath) + " && test ! -e " + newPath + " && test ! -L " + newPath
	for _, path := range []string{"/run/pki-bootstrap/bootstrap.key", "/run/pki-bootstrap/bootstrap.csr", oldPath} {
		script += " && test -f " + path + " && test ! -L " + path
	}
	script += " && stat -c '%a %u %g' /run/pki-bootstrap /run/pki-bootstrap/bootstrap.key /run/pki-bootstrap/bootstrap.csr " + oldPath
	return script, nil
}

func validateManagedLateSeedForwardCustodyPermissions(raw []byte) error {
	rows := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(rows) != 4 {
		return managedLateSeedForwardBlock("original-custody-permission-evidence")
	}
	if rows[0] != "700 10001 10001" && rows[0] != "2700 10001 10001" {
		return managedLateSeedForwardBlock("original-private-root-permissions")
	}
	if rows[1] != "600 10001 10001" || rows[2] != "600 10001 10001" {
		return managedLateSeedForwardBlock("original-private-key-or-csr-permissions")
	}
	if rows[3] != "600 10001 10001" && rows[3] != "660 10001 10001" {
		return managedLateSeedForwardBlock("original-public-certificate-permissions")
	}
	return nil
}

func validateManagedLateSeedForwardTerminal(f *createManagedLateSeedForward, job, pod deploymentplan.Object, logs []byte) error {
	if validateManagedLateSeedForwardFailedChild(f) != nil {
		return managedLateSeedForwardBlock("exact-terminal-tls-failure")
	}
	return validateBootstrapTerminalTLSJob(f.FailedPlan.Resources[0].Desired, f.FailedJobUID, f.FailedPodUID, f.FailedPlan.Stack+"-video-cloud", job, pod, logs)
}

// Shared native no-mint terminal proof; the caller separately authenticates its
// own immutable parent/child journal. No historical approval is reused here.
func validateBootstrapTerminalTLSJob(desired deploymentplan.Object, jobUID, podUID, namespace string, job, pod deploymentplan.Object, logs []byte, exactFailure ...string) error {
	bad := func() error { return managedLateSeedForwardBlock("exact-terminal-tls-failure") }
	if desired["kind"] != "Job" || !environmentBootstrapOwnerUUID.MatchString(jobUID) || !environmentBootstrapOwnerUUID.MatchString(podUID) || job == nil || pod == nil || job["kind"] != "Job" || pod["kind"] != "Pod" || deploymentplan.Map(job["metadata"])["uid"] != jobUID || deploymentplan.Map(job["metadata"])["deletionTimestamp"] != nil || !deploymentplan.Matches(desired, job) || deploymentplan.Map(pod["metadata"])["uid"] != podUID || deploymentplan.Map(pod["metadata"])["deletionTimestamp"] != nil || deploymentplan.Map(pod["metadata"])["namespace"] != namespace {
		return bad()
	}

	status := deploymentplan.Map(job["status"])
	failed := false
	for _, item := range certIssuerObjectList(status["conditions"]) {
		c := deploymentplan.Map(item)
		if c["status"] != "True" || c["reason"] != "BackoffLimitExceeded" || c["type"] != "Failed" && c["type"] != "FailureTarget" {
			return bad()
		}
		failed = failed || c["type"] == "Failed"
	}
	if !failed || status["failed"] != float64(1) || status["active"] != nil && status["active"] != float64(0) || status["succeeded"] != nil && status["succeeded"] != float64(0) {
		return bad()
	}
	owners := certIssuerObjectList(deploymentplan.Map(pod["metadata"])["ownerReferences"])
	if len(owners) != 1 || deploymentplan.Map(owners[0])["kind"] != "Job" || deploymentplan.Map(owners[0])["uid"] != jobUID || deploymentplan.Map(owners[0])["name"] != deploymentplan.Map(job["metadata"])["name"] || deploymentplan.Map(owners[0])["controller"] != true {
		return bad()
	}
	expected := environmentPKIPod(desired)
	actual := deploymentplan.Map(pod["spec"])
	if !deploymentplan.Matches(deploymentplan.Object{"kind": "Pod", "spec": expected}, deploymentplan.Object{"kind": "Pod", "spec": actual}) || len(certIssuerObjectList(actual["containers"])) != 1 || len(certIssuerObjectList(actual["initContainers"])) != len(certIssuerObjectList(expected["initContainers"])) {
		return bad()
	}
	pstatus := deploymentplan.Map(pod["status"])
	if pstatus["phase"] != "Failed" {
		return bad()
	}
	for key, required := range map[string]int{"initContainerStatuses": len(certIssuerObjectList(expected["initContainers"])), "containerStatuses": 1} {
		states := certIssuerObjectList(pstatus[key])
		if len(states) != required {
			return bad()
		}
		for i, item := range states {
			s := deploymentplan.Map(item)
			terminated := deploymentplan.Map(deploymentplan.Map(s["state"])["terminated"])
			code := float64(0)
			containers := certIssuerObjectList(expected["initContainers"])
			if key == "containerStatuses" {
				code, containers = 1, certIssuerObjectList(expected["containers"])
			}
			if s["name"] != deploymentplan.Map(containers[i])["name"] || s["restartCount"] != float64(0) || len(deploymentplan.Map(s["state"])) != 1 || terminated["exitCode"] != code || code == 1 && terminated["reason"] != "Error" {
				return bad()
			}
		}
	}
	// The complete native fixed-format TLS login failure is checked below;
	// unrelated authorization/signing outcomes cannot authorize this forward.
	container := deploymentplan.Map(certIssuerObjectList(expected["containers"])[0])
	url := mqttFoundationEnv(container, "OPENBAO_ADDR") + "/v1/auth/kubernetes/login"
	want := "bootstrap OpenBao client: openbao Kubernetes login failed: Post \"" + url + "\": tls: failed to verify certificate: x509: certificate signed by unknown authority"
	if len(exactFailure) > 1 {
		return bad()
	}
	if len(exactFailure) == 1 {
		want = exactFailure[0]
	}
	if len(logs) > 4096 || strings.TrimSpace(string(logs)) != want {
		return bad()
	}
	return nil
}

func managedLateSeedForwardNoMintCountsValid(raw []byte) bool {
	var counts managedLateSeedForwardNoMintCounts
	var fields map[string]json.RawMessage
	return decodeEnvironmentJSON(raw, &counts) == nil && json.Unmarshal(raw, &fields) == nil && len(fields) == 3 && fields["sessions"] != nil && fields["deployments"] != nil && fields["issuances"] != nil && string(fields["sessions"]) != "null" && string(fields["deployments"]) != "null" && string(fields["issuances"]) != "null" && counts == (managedLateSeedForwardNoMintCounts{})
}

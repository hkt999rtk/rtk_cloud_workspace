package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const productSessionForwardSchema = "rtk-cloud.product-session-trust-forward/v1"

type environmentProductSessionForward struct {
	Schema, ProgramSHA256, Scope                               string
	OriginalPlanSHA256, OriginalJournalSHA256                  string
	OriginalJobUID, OriginalPodUID, SessionIntentSHA256        string
	ClaimUID, ServiceAccountUID, CustodyUID, CustodyPlanSHA256 string
	KeySHA256, CSRSHA256                                       string
	Trust                                                      deploymentplan.Resource
	TerminalJob, TerminalPod                                   deploymentplan.Object
	Proof                                                      map[string]string
}
type environmentProductSessionForwardReceipt struct {
	OriginalPlanSHA256          string `json:"original_plan_sha256"`
	OriginalFailedJournalSHA256 string `json:"original_failed_journal_sha256"`
	ActualForwardStage          string `json:"actual_forward_stage"`
	ActualForwardPlanSHA256     string `json:"actual_forward_plan_sha256"`
	SourceSHA256                string `json:"source_sha256"`
}

func productSessionForwardBlock(stage string) error {
	return environmentCreateBootstrapBlocker("original product session trust forward: " + stage)
}
func productSessionForwardStage(l *createNormalTailProductLifecycle, suffix string) string {
	return "product-identities-" + productIdentityScope(l)[:12] + "-service-session-trust-" + suffix
}
func productSessionForwardPath(l *createNormalTailProductLifecycle) string {
	return filepath.Join("deployment/create", l.ParentSHA256, "bootstrap/product-identities", productIdentityScope(l), "service/session-forward.json")
}

// Read the original request and its real failed scoped execution. It is never
// turned into an accepted journal, nor recompiled against already-created state.
func readProductSessionForwardOriginal(store secretStore, e *environmentCreateExecution, l *createNormalTailProductLifecycle) (*deploymentplan.Plan, deploymentplan.Journal, environmentCreateServiceCeremony, error) {
	var j deploymentplan.Journal
	s := environmentCreateServiceCeremony{}
	bad := func() (*deploymentplan.Plan, deploymentplan.Journal, environmentCreateServiceCeremony, error) {
		return nil, j, s, productSessionForwardBlock("original failed child or session intent changed")
	}
	if e == nil || l == nil || e.Store != store || l.ParentSHA256 != e.Journal.PlanSHA256 {
		return bad()
	}
	stage := "product-identities-" + productIdentityScope(l)[:12] + "-service-session"
	p, err := readCreateNormalTailPreparationPlan(store, e, stage)
	entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+stage)
	if err != nil || p == nil || entry == nil || entry.Phase != stage {
		return bad()
	}
	original := *entry
	original.State = "pending"
	original.Receipt = nil
	original.CompletedAt = time.Time{}
	view := *e
	view.Journal = e.Journal
	view.Journal.Entries = slices.Clone(e.Journal.Entries)
	for i := range view.Journal.Entries {
		if view.Journal.Entries[i].Key == entry.Key {
			view.Journal.Entries[i] = original
		}
	}
	actual, j, err := originalCreateChildAtIntent(store, &view, original)
	if err != nil || actual.SHA256() != p.SHA256() || validateProductSessionFailedJournal(p, j) != nil {
		return bad()
	}
	var intent struct {
		Session    string   `json:"session_id"`
		Claim      string   `json:"claim"`
		Issuer     string   `json:"issuer_id"`
		Root       string   `json:"root_sha256"`
		Subjects   []string `json:"subjects"`
		Namespaces []string `json:"credential_namespaces"`
	}
	path := filepath.Join(filepath.Dir(productSessionForwardPath(l)), "session-intent.json")
	if createOpenBaoACKCompletionRead(store, path, &intent) != nil || !environmentBootstrapOwnerUUID.MatchString(intent.Session) || intent.Claim != "pki-product-bootstrap-"+productIdentityScope(l)[:12] || intent.Root != l.Root.RootFingerprint || !slices.Equal(intent.Namespaces, []string{l.Bootstrap.Stack + "-video-cloud"}) {
		return bad()
	}
	job := p.Resources[len(p.Resources)-1].Desired
	c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(job)["containers"])[0])
	var subjects []string
	for _, o := range l.Outputs {
		subjects = append(subjects, o.Subject)
	}
	slices.Sort(subjects)
	if !slices.Equal(intent.Subjects, subjects) || mqttFoundationEnv(c, "PKI_BOOTSTRAP_SESSION_ID") != intent.Session || mqttFoundationEnv(c, "PKI_BOOTSTRAP_ISSUER_ID") != intent.Issuer || mqttFoundationEnv(c, "PKI_BOOTSTRAP_ROOT_SHA256") != intent.Root || mqttFoundationEnv(c, "PKI_BOOTSTRAP_DEPLOYMENT_ID") != "product-"+productIdentityScope(l)[:48] {
		return bad()
	}
	s.Root = l.Root
	s.Intermediate = l.Intermediate
	s.Intermediate.ID = intent.Issuer
	s.BootstrapSessionID = intent.Session
	s.BootstrapClaim = intent.Claim
	s.BootstrapCredentialNamespace = intent.Namespaces[0]
	s.CustodyReader = "pki-product-custody-" + productIdentityScope(l)[:12]
	return p, j, s, nil
}
func validateProductSessionFailedJournal(p *deploymentplan.Plan, j deploymentplan.Journal) error {
	if p == nil || p.Operation != "scoped" || len(p.Resources) != 3 || j.PlanSHA256 != p.SHA256() || j.Accepted || j.AcceptanceStarted || j.InFlight != "" || len(j.Completed) != 2 || len(j.Owners) != 3 {
		return productSessionForwardBlock("known failed original execution required")
	}
	kinds := []string{"PersistentVolumeClaim", "ServiceAccount", "Job"}
	for i, r := range p.Resources {
		if r.Desired["kind"] != kinds[i] || r.ID != deploymentplan.ID(r.Desired) || r.Action != "create" || r.UID != "" || r.Baseline != nil || !environmentBootstrapOwnerUUID.MatchString(j.Owners[r.ID]) || i < 2 && j.Completed[i] != r.ID || i == 2 && j.Failed != r.ID {
			return productSessionForwardBlock("original create prefix and UID proof")
		}
	}
	return nil
}

// A credential-free reader mounts the original claim read-only. The native
// bootstrap executable is the only process allowed to consume the private key.
func productSessionForwardCustodyDefinitions(l *createNormalTailProductLifecycle, s environmentCreateServiceCeremony, uid string, retire bool) ([]deploymentplan.Definition, error) {
	if l == nil || s.BootstrapClaim != "pki-product-bootstrap-"+productIdentityScope(l)[:12] || s.BootstrapCredentialNamespace != l.Bootstrap.Stack+"-video-cloud" || !immutableEnvironmentImage.MatchString(l.Bootstrap.Image) || retire && !environmentBootstrapOwnerUUID.MatchString(uid) {
		return nil, productSessionForwardBlock("read-only custody scope")
	}
	s.CustodyReader = "pki-product-custody-" + productIdentityScope(l)[:12] + "-trust"
	d := renderEnvironmentCreateCustodyReader(deploymentConfig{Environment: l.Bootstrap.Environment}, l.Bootstrap, s)
	pod := environmentPKIPod(d.Object)
	c := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
	delete(c, "env")
	for _, v := range certIssuerObjectList(c["volumeMounts"]) {
		deploymentplan.Map(v)["readOnly"] = true
	}
	deploymentplan.Map(certIssuerObjectList(pod["volumes"])[0])["persistentVolumeClaim"] = deploymentplan.Object{"claimName": s.BootstrapClaim, "readOnly": true}
	if uid != "" {
		deploymentplan.Map(d.Object["metadata"])["uid"] = uid
	}
	if retire {
		d.Phase = "retirement"
		d.Retire = true
	}
	return []deploymentplan.Definition{d}, nil
}
func productSessionForwardDefinitions(l *createNormalTailProductLifecycle, p *deploymentplan.Plan, f *environmentProductSessionForward) ([]deploymentplan.Definition, error) {
	if p == nil || len(p.Resources) != 3 || f == nil || f.OriginalPlanSHA256 != p.SHA256() || !environmentPreflightDigest.MatchString(f.KeySHA256) || !environmentPreflightDigest.MatchString(f.CSRSHA256) {
		return nil, productSessionForwardBlock("original job recipe")
	}
	r := p.Resources[2]
	o := deploymentplan.Clone(r.Desired)
	m := deploymentplan.Map(o["metadata"])
	name := deploymentplan.String(m["name"])
	if !strings.HasPrefix(name, "pki-service-bootstrap-") || len(name) != 34 {
		return nil, productSessionForwardBlock("original job name")
	}
	m["name"] = name + "-f" + p.SHA256()[:8]
	pod := environmentPKIPod(o)
	// This claim is already initialized and privately owned. Reapplying a
	// pod fsGroup would let kubelet chmod retained keys before any init guard.
	delete(deploymentplan.Map(pod["securityContext"]), "fsGroup")
	original := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
	guard := deploymentplan.Object{"name": "original-custody-proof", "image": original["image"], "command": []any{"/bin/sh", "-ec", productSessionForwardKeyGuard(f)}, "securityContext": deploymentplan.Object{"runAsNonRoot": true, "runAsUser": float64(10001), "runAsGroup": float64(10001), "allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": deploymentplan.Object{"drop": []any{"ALL"}}}, "volumeMounts": []any{deploymentplan.Object{"name": "bootstrap-state", "mountPath": "/run/pki-bootstrap", "readOnly": true}}}
	init := certIssuerObjectList(pod["initContainers"])
	pod["initContainers"] = append(init, guard)
	return bindProductIdentityOpenBaoTrust(l, []deploymentplan.Definition{{Phase: r.Phase, Object: o}})
}
func readProductSessionForward(store secretStore, e *environmentCreateExecution, l *createNormalTailProductLifecycle) (*environmentProductSessionForward, error) {
	if e == nil || l == nil || e.Store != store {
		return nil, productSessionForwardBlock("original source unavailable")
	}
	path, err := store.safePath(productSessionForwardPath(l))
	if err != nil {
		return nil, err
	}
	if _, err = os.Lstat(path); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	raw, err := readBoundedEnvironmentFile(path, true)
	var f environmentProductSessionForward
	if err != nil || decodeEnvironmentJSON(raw, &f) != nil {
		return nil, productSessionForwardBlock("saved source unavailable")
	}
	p, j, s, err := readProductSessionForwardOriginal(store, e, l)
	if err != nil {
		return nil, err
	}
	program, err := readCreateNormalTailProgram(store, e)
	intentRaw, ie := readBoundedEnvironmentFile(filepath.Join(store.Root, filepath.Dir(productSessionForwardPath(l)), "session-intent.json"), true)
	if err != nil || ie != nil || f.Schema != productSessionForwardSchema || f.ProgramSHA256 != deploymentplan.Digest(program) || f.Scope != productIdentityScope(l) || f.OriginalPlanSHA256 != p.SHA256() || f.OriginalJournalSHA256 != deploymentplan.Digest(j) || f.OriginalJobUID != j.Owners[p.Resources[2].ID] || f.ClaimUID != j.Owners[p.Resources[0].ID] || f.ServiceAccountUID != j.Owners[p.Resources[1].ID] || f.SessionIntentSHA256 != environmentPreflightBytesSHA256(intentRaw) || !environmentBootstrapOwnerUUID.MatchString(f.OriginalPodUID) || !environmentBootstrapOwnerUUID.MatchString(f.CustodyUID) || !environmentPreflightDigest.MatchString(f.KeySHA256) || !environmentPreflightDigest.MatchString(f.CSRSHA256) {
		return nil, productSessionForwardBlock("saved source pins")
	}
	defs, err := productSessionForwardCustodyDefinitions(l, s, "", false)
	if err != nil {
		return nil, err
	}
	cp, cj, err := acceptedCreateStage(store, e, "kubernetes/"+productSessionForwardStage(l, "custody"))
	if err != nil || cp.SHA256() != f.CustodyPlanSHA256 || len(cp.Resources) != 1 || cj.Owners[cp.Resources[0].ID] != f.CustodyUID || cp.BindingsSHA256 != environmentCreateStageBindingsSHA(productSessionForwardStage(l, "custody"), defs) {
		return nil, productSessionForwardBlock("genuine read-only custody child")
	}
	if f.Trust.ID != "ConfigMap/"+s.BootstrapCredentialNamespace+"/pki-create-openbao-tls-consumer-trust" || f.Trust.UID != l.SourceOwners[f.Trust.ID] || validateProductIdentityOpenBaoTrust(l, f.Trust.Desired, time.Time{}) != nil || f.Proof["managed-trust"] != deploymentplan.Digest(f.Trust.Desired) {
		return nil, productSessionForwardBlock("managed trust source")
	}

	for _, k := range []string{"terminal-job", "terminal-pod", "terminal-tls-log", "native-no-mint", "custody-permissions", "old-trust", "managed-trust"} {
		if !environmentPreflightDigest.MatchString(f.Proof[k]) {
			return nil, productSessionForwardBlock("native proof digest")
		}
	}
	wantLog := productSessionForwardTLSLog(p.Resources[2].Desired)
	if f.Proof["terminal-job"] != deploymentplan.Digest(f.TerminalJob) || f.Proof["terminal-pod"] != deploymentplan.Digest(f.TerminalPod) || f.Proof["terminal-tls-log"] != environmentPreflightBytesSHA256([]byte(wantLog)) || validateBootstrapTerminalTLSJob(p.Resources[2].Desired, f.OriginalJobUID, f.OriginalPodUID, p.Stack+"-video-cloud", f.TerminalJob, f.TerminalPod, []byte(wantLog)) != nil {
		return nil, productSessionForwardBlock("terminal original TLS proof")
	}
	if _, err := productSessionForwardDefinitions(l, p, &f); err != nil {
		return nil, err
	}
	return &f, nil
}
func productSessionForwardKeyGuard(f *environmentProductSessionForward) string {
	return fmt.Sprintf(`set -eu
cd /run/pki-bootstrap
test ! -L /run/pki-bootstrap; test -d /run/pki-bootstrap
for f in bootstrap.key bootstrap.csr; do
 test ! -L "$f"; test -f "$f"
 test "$(stat -c '%%a %%u %%g' "$f")" = '600 10001 10001'
done
printf '%%s  bootstrap.key\n%%s  bootstrap.csr\n' '%s' '%s' | sha256sum -c - >/dev/null
`, f.KeySHA256, f.CSRSHA256)
}

func productSessionForwardTLSLog(job deploymentplan.Object) string {
	c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(job)["containers"])[0])
	return "bootstrap OpenBao client: openbao Kubernetes login failed: Post \"" + mqttFoundationEnv(c, "OPENBAO_ADDR") + "/v1/auth/kubernetes/login\": tls: failed to verify certificate: x509: certificate signed by unknown authority"
}

// Only hashes, mode/owner metadata and absence reach the operator process.
const productSessionForwardCustodyCommand = `set -eu
cd /run/pki-bootstrap
test ! -L .; test -d .
for f in bootstrap.key bootstrap.csr; do test ! -L "$f"; test -f "$f"; done
test ! -e bootstrap.crt; test ! -L bootstrap.crt
stat -c '%a %u %g' . bootstrap.key bootstrap.csr
sha256sum bootstrap.key bootstrap.csr
`

func parseProductSessionForwardCustody(raw []byte) (string, string, error) {
	rows := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(rows) != 5 || (rows[0] != "700 10001 10001" && rows[0] != "2700 10001 10001") || rows[1] != "600 10001 10001" || rows[2] != "600 10001 10001" {
		return "", "", productSessionForwardBlock("original key/CSR permissions or certificate absence")
	}
	a, b := strings.Fields(rows[3]), strings.Fields(rows[4])
	if len(a) != 2 || len(b) != 2 || a[1] != "bootstrap.key" || b[1] != "bootstrap.csr" || !environmentPreflightDigest.MatchString(a[0]) || !environmentPreflightDigest.MatchString(b[0]) {
		return "", "", productSessionForwardBlock("original key/CSR hash proof")
	}
	return a[0], b[0], nil
}
func productSessionForwardNoMintQuery(cfg deploymentConfig, l *createNormalTailProductLifecycle, s environmentCreateServiceCeremony) (string, error) {
	if cfg.Environment != l.Bootstrap.Environment || !secretEnvironmentPattern.MatchString(cfg.Environment) || !environmentBootstrapOwnerUUID.MatchString(s.BootstrapSessionID) {
		return "", productSessionForwardBlock("native no-mint identifiers")
	}
	deployment := "product-" + productIdentityScope(l)[:48]
	return "SELECT json_build_object('sessions',(SELECT count(*) FROM pki_deployment_bootstrap_sessions WHERE environment='" + cfg.Environment + "' AND session_id='" + s.BootstrapSessionID + "'),'deployments',(SELECT count(*) FROM pki_deployment_bootstrap_sessions WHERE environment='" + cfg.Environment + "' AND deployment_id='" + deployment + "'),'issuances',(SELECT count(*) FROM pki_service_client_issuances WHERE environment='" + cfg.Environment + "' AND request_id='deployment-bootstrap:" + deployment + "'))", nil
}

// apply is the ordinary original-parent scoped executor. No native write or
// receipt is fabricated here; a new child has its own immutable plan/UIDs.
func executeProductSessionTrustForward(ctx context.Context, cfg deploymentConfig, store secretStore, runtime environmentCreateBootstrapRuntime, l *createNormalTailProductLifecycle, apply environmentCreateBootstrapApply) (bool, error) {
	e := runtime.Execution
	if e == nil || l == nil || apply == nil {
		return false, productSessionForwardBlock("original runtime unavailable")
	}
	stage := "product-identities-" + productIdentityScope(l)[:12] + "-service-session"
	entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+stage)
	if entry == nil {
		return false, nil
	}
	p, err := readCreateNormalTailPreparationPlan(store, e, stage)
	if err != nil {
		return true, err
	}
	if p == nil {
		return false, nil
	}
	j, exists, err := p.ReadExecution(store.Root)
	if err != nil {
		return true, err
	}
	if !exists || j.Failed == "" {
		return false, nil
	}
	p, j, s, err := readProductSessionForwardOriginal(store, e, l)
	if err != nil {
		return true, err
	}
	f, err := readProductSessionForward(store, e, l)
	if err != nil {
		return true, err
	}
	if f == nil {
		defs, err := productSessionForwardCustodyDefinitions(l, s, "", false)
		if err != nil {
			return true, err
		}
		if err = apply(ctx, "service-session-trust-custody", defs); err != nil {
			return true, err
		}
		f, err = observeProductSessionForward(ctx, cfg, store, runtime, l, p, j, s)
		if err != nil {
			return true, err
		}
		if _, err = writeProductArtifact(store, productSessionForwardPath(l), f); err != nil {
			return true, err
		}
	}
	retireStage := productSessionForwardStage(l, "custody-retire")
	// Read the private proof once more before releasing its read-only mount.
	// An already recorded retirement has its own exact UID-bound outcome.
	if createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+retireStage) == nil {
		fresh, err := observeProductSessionForward(ctx, cfg, store, runtime, l, p, j, s)
		if err != nil {
			return true, err
		}
		if fresh.KeySHA256 != f.KeySHA256 || fresh.CSRSHA256 != f.CSRSHA256 || fresh.OriginalPodUID != f.OriginalPodUID || fresh.ClaimUID != f.ClaimUID || fresh.ServiceAccountUID != f.ServiceAccountUID || deploymentplan.Digest(fresh.Trust) != deploymentplan.Digest(f.Trust) {
			return true, productSessionForwardBlock("source changed before custody retirement")
		}
	}
	retire, err := productSessionForwardCustodyDefinitions(l, s, f.CustodyUID, true)
	if err != nil {
		return true, err
	}
	retired := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+retireStage)
	if retired != nil && retired.State == "completed" {
		rp, rj, err := acceptedCreateStage(store, e, retired.Key)
		if err != nil || len(rp.Resources) != 1 || rp.Resources[0].UID != f.CustodyUID || rj.Owners[rp.Resources[0].ID] != f.CustodyUID {
			return true, productSessionForwardBlock("original custody retirement changed")
		}
		live, err := runtime.Backend.Read(ctx, rp.Resources[0].ID)
		if err != nil || live != nil {
			return true, productSessionForwardBlock("retired custody reader remains")
		}
	} else if err = apply(ctx, "service-session-trust-custody-retire", retire); err != nil {
		return true, err
	}
	forwardStage, fp, _, err := executeProductSessionAttempts(ctx, cfg, store, runtime, l, p, f, s, apply)
	if err != nil {
		return true, err
	}

	receipt := environmentProductSessionForwardReceipt{p.SHA256(), deploymentplan.Digest(j), forwardStage, fp.SHA256(), deploymentplan.Digest(f)}
	entry = createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+stage)
	if entry.State == "completed" {
		if activeTransportJournalDigest(jsonRawObject(entry.Receipt)) != activeTransportJournalDigest(jsonRawObject(mustProviderRecoveryJSON(receipt))) {
			return true, productSessionForwardBlock("logical completion changed")
		}
		return true, e.Check(ctx)
	}
	if err = e.RecordReceipt(entry.Key, mustProviderRecoveryJSON(receipt)); err != nil {
		return true, err
	}
	return true, e.CompleteEntry(entry.Key, time.Now().UTC())
}

func observeProductSessionForward(ctx context.Context, cfg deploymentConfig, store secretStore, runtime environmentCreateBootstrapRuntime, l *createNormalTailProductLifecycle, p *deploymentplan.Plan, j deploymentplan.Journal, s environmentCreateServiceCeremony) (*environmentProductSessionForward, error) {
	e, b := runtime.Execution, runtime.Backend
	if e == nil || e.Check(ctx) != nil {
		return nil, productSessionForwardBlock("original fence")
	}
	program, err := readCreateNormalTailProgram(store, e)
	if err != nil {
		return nil, err
	}
	f := &environmentProductSessionForward{Schema: productSessionForwardSchema, ProgramSHA256: deploymentplan.Digest(program), Scope: productIdentityScope(l), OriginalPlanSHA256: p.SHA256(), OriginalJournalSHA256: deploymentplan.Digest(j), OriginalJobUID: j.Owners[p.Resources[2].ID], ClaimUID: j.Owners[p.Resources[0].ID], ServiceAccountUID: j.Owners[p.Resources[1].ID], Proof: map[string]string{}}
	for _, r := range p.Resources[:2] {
		actual, err := b.Read(ctx, r.ID)
		if err != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != j.Owners[r.ID] || deploymentplan.Map(actual["metadata"])["deletionTimestamp"] != nil || !deploymentplan.Matches(r.Desired, actual) {
			return nil, productSessionForwardBlock("original claim or service account changed")
		}
	}
	job, err := b.Read(ctx, p.Resources[2].ID)
	if err != nil {
		return nil, err
	}
	raw, err := b.command(ctx, nil, "--namespace", s.BootstrapCredentialNamespace, "get", "pods", "--selector", "job-name="+deploymentplan.String(deploymentplan.Map(p.Resources[2].Desired["metadata"])["name"]), "-o", "json")
	var pods struct {
		Items []deploymentplan.Object `json:"items"`
	}
	if err != nil || json.Unmarshal(raw, &pods) != nil || len(pods.Items) != 1 {
		return nil, productSessionForwardBlock("one original terminal pod")
	}
	pod := pods.Items[0]
	f.OriginalPodUID = deploymentplan.String(deploymentplan.Map(pod["metadata"])["uid"])
	c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(p.Resources[2].Desired)["containers"])[0])
	logs, err := b.command(ctx, nil, "--namespace", s.BootstrapCredentialNamespace, "logs", "pod/"+deploymentplan.String(deploymentplan.Map(pod["metadata"])["name"]), "--container", deploymentplan.String(c["name"]))
	if err != nil || validateBootstrapTerminalTLSJob(p.Resources[2].Desired, f.OriginalJobUID, f.OriginalPodUID, s.BootstrapCredentialNamespace, job, pod, logs) != nil {
		return nil, productSessionForwardBlock("exact inactive terminal TLS failure")
	}
	lease, err := b.Read(ctx, "Lease/"+s.BootstrapCredentialNamespace+"/rtk-deployment-execution")
	if err != nil || lease != nil {
		return nil, productSessionForwardBlock("native scoped lease still active")
	}
	for _, d := range l.SourceDefinitions {
		if deploymentplan.ID(d.Object) == "ConfigMap/"+s.BootstrapCredentialNamespace+"/pki-create-openbao-tls-consumer-trust" {
			f.Trust = deploymentplan.Resource{ID: deploymentplan.ID(d.Object), Desired: deploymentplan.Clone(d.Object), UID: l.SourceOwners[deploymentplan.ID(d.Object)]}
		}
	}
	trust, err := b.Read(ctx, f.Trust.ID)
	if err != nil || trust == nil || deploymentplan.Map(trust["metadata"])["uid"] != f.Trust.UID || !deploymentplan.Matches(f.Trust.Desired, trust) || validateProductIdentityOpenBaoTrust(l, trust, time.Now().UTC()) != nil {
		return nil, productSessionForwardBlock("current managed OpenBao authority")
	}
	f.Trust.Desired = deploymentplan.Clone(trust)
	old, err := b.Read(ctx, "ConfigMap/"+s.BootstrapCredentialNamespace+"/pki-openbao-transport-ca")
	if err != nil || old == nil || deploymentplan.Map(old["metadata"])["uid"] != l.SourceOwners[deploymentplan.ID(old)] || deploymentplan.Map(old["metadata"])["deletionTimestamp"] != nil || deploymentplan.Map(old["data"])["ca.crt"] == deploymentplan.Map(trust["data"])["roots.pem"] {
		return nil, productSessionForwardBlock("wrong original trust projection proof")
	}
	cp, cj, err := acceptedCreateStage(store, e, "kubernetes/"+productSessionForwardStage(l, "custody"))
	if err != nil || len(cp.Resources) != 1 {
		return nil, productSessionForwardBlock("accepted read-only custody required")
	}
	owner, err := b.Read(ctx, cp.Resources[0].ID)
	f.CustodyUID = cj.Owners[cp.Resources[0].ID]
	f.CustodyPlanSHA256 = cp.SHA256()
	if err != nil || owner == nil || deploymentplan.Map(owner["metadata"])["uid"] != f.CustodyUID || !deploymentplan.Matches(cp.Resources[0].Desired, owner) {
		return nil, productSessionForwardBlock("read-only custody owner")
	}
	proof, err := b.command(ctx, nil, "--namespace", s.BootstrapCredentialNamespace, "exec", "deployment/"+deploymentplan.String(deploymentplan.Map(cp.Resources[0].Desired["metadata"])["name"]), "--container", "custody", "--", "/bin/sh", "-ec", productSessionForwardCustodyCommand)
	if err != nil {
		return nil, productSessionForwardBlock("read-only original key/CSR proof")
	}
	f.KeySHA256, f.CSRSHA256, err = parseProductSessionForwardCustody(proof)
	if err != nil {
		return nil, err
	}
	query, err := productSessionForwardNoMintQuery(cfg, l, s)
	if err != nil {
		return nil, err
	}
	counts, err := readEnvironmentCreateRegistryJSON(ctx, cfg, runtime, query)
	if err != nil || !managedLateSeedForwardNoMintCountsValid(counts) {
		return nil, productSessionForwardBlock("original session/request already minted or uncertain")
	}
	intent, err := readBoundedEnvironmentFile(filepath.Join(store.Root, filepath.Dir(productSessionForwardPath(l)), "session-intent.json"), true)
	if err != nil {
		return nil, err
	}
	f.SessionIntentSHA256 = environmentPreflightBytesSHA256(intent)
	f.TerminalJob, f.TerminalPod = job, pod
	f.Proof = map[string]string{"terminal-job": deploymentplan.Digest(job), "terminal-pod": deploymentplan.Digest(pod), "terminal-tls-log": environmentPreflightBytesSHA256([]byte(strings.TrimSpace(string(logs)))), "native-no-mint": environmentPreflightBytesSHA256(counts), "custody-permissions": environmentPreflightBytesSHA256(proof), "old-trust": deploymentplan.Digest(old), "managed-trust": deploymentplan.Digest(trust)}
	if err = e.Check(ctx); err != nil {
		return nil, err
	}
	return f, nil
}
